package adroute

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// quickFixture creates one saved, open model entry pointing at baseURL.
func quickFixture(t *testing.T, baseURL string) (*Store, *HTTPApp, string) {
	t.Helper()
	s := newTestStore(t)
	gid := mustGroup(t, s, "work")
	mid := mustModel(t, s, ModelInput{GroupID: gid, Name: "coding", Model: "vendor/model", BaseURL: baseURL, APIKey: "upstream-secret"})
	return s, mustHTTPApp(HTTPOptions{Store: s}), mid
}

func runQuickTest(t *testing.T, app *HTTPApp, mid string) (*QuickTestResult, *httptest.ResponseRecorder) {
	t.Helper()
	w := adminReq(t, app, http.MethodPost, "/api/models/"+mid+"/quick-test", nil)
	var result QuickTestResult
	if w.Code == 200 {
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatalf("quick test json: %v body=%s", err, w.Body.String())
		}
	}
	return &result, w
}

func TestQuickTestSSEReportsTextRateAndSingleRequest(t *testing.T) {
	var calls int32
	var received map[string]any
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		_ = json.NewDecoder(r.Body).Decode(&received)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"status\":\"in_progress\",\"output\":[]}}\n\n")
		fmt.Fprint(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"Hello\"}\n\n")
		fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"Hello from the model.\"}]}],\"usage\":{\"input_tokens\":12,\"output_tokens\":40}}}\n\n")
	}))
	defer up.Close()
	_, app, mid := quickFixture(t, up.URL+"/v1")
	result, w := runQuickTest(t, app, mid)
	if w.Code != 200 {
		t.Fatalf("quick test status=%d body=%s", w.Code, w.Body.String())
	}
	if result.Outcome != "ok" || !result.TextReturned || !result.UsageReported || result.OutputTokens == nil || *result.OutputTokens != 40 {
		t.Fatalf("result %#v", result)
	}
	if result.TokensPerSecond == nil || *result.TokensPerSecond <= 0 || math.IsNaN(*result.TokensPerSecond) || math.IsInf(*result.TokensPerSecond, 0) {
		t.Fatalf("rate %#v", result.TokensPerSecond)
	}
	if result.DurationMs < 0 {
		t.Fatalf("duration %d", result.DurationMs)
	}
	// The reported rate must match output_tokens / total seconds of this call.
	want := 40 / (float64(result.DurationMs) / 1000)
	if math.Abs(*result.TokensPerSecond-want) > want*0.5+1 {
		t.Fatalf("rate %v want ~%v (duration %dms)", *result.TokensPerSecond, want, result.DurationMs)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("quick test must send exactly one request, sent %d", got)
	}
	if received["model"] != "vendor/model" {
		t.Fatalf("upstream received model %#v", received["model"])
	}
	if got, _ := received["max_output_tokens"].(float64); int(got) != QuickTestMaxOutputTokens {
		t.Fatalf("max_output_tokens %#v", received["max_output_tokens"])
	}
	if received["stream"] != true {
		t.Fatalf("stream %#v", received["stream"])
	}
	if strings.Contains(w.Body.String(), "upstream-secret") {
		t.Fatalf("secret leaked in response: %s", w.Body.String())
	}
}

func TestQuickTestJSONResponse(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"object":"response","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"OK."}]}],"usage":{"input_tokens":5,"output_tokens":12}}`)
	}))
	defer up.Close()
	_, app, mid := quickFixture(t, up.URL+"/v1")
	result, w := runQuickTest(t, app, mid)
	if w.Code != 200 || result.Outcome != "ok" || result.OutputTokens == nil || *result.OutputTokens != 12 {
		t.Fatalf("json quick test %d %#v %s", w.Code, result, w.Body.String())
	}
	if result.TokensPerSecond == nil || *result.TokensPerSecond <= 0 {
		t.Fatalf("json rate %#v", result.TokensPerSecond)
	}
}

func TestQuickTestWithoutUsageDoesNotInventTokens(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"Some text.\"}]}]}}\n\n")
	}))
	defer up.Close()
	_, app, mid := quickFixture(t, up.URL+"/v1")
	result, w := runQuickTest(t, app, mid)
	if w.Code != 200 || result.Outcome != "no-usage" || !result.TextReturned {
		t.Fatalf("no usage result %d %#v", w.Code, result)
	}
	if result.OutputTokens != nil || result.TokensPerSecond != nil || result.UsageReported {
		t.Fatalf("invented usage: %#v", result)
	}
	if strings.Contains(w.Body.String(), "NaN") || strings.Contains(w.Body.String(), "Infinity") {
		t.Fatalf("bad numbers in payload: %s", w.Body.String())
	}
}

func TestQuickTestRejectsNonTextAndErrorOutcomes(t *testing.T) {
	cases := map[string]struct {
		contentType string
		body        string
		status      int
		wantHTTP    int
		wantOutcome string
	}{
		"empty 200":           {"text/event-stream", "", 200, 502, ""},
		"only done":           {"text/event-stream", "data: [DONE]\n\n", 200, 502, ""},
		"reasoning only":      {"text/event-stream", "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"reasoning\",\"summary\":[]}],\"usage\":{\"output_tokens\":30}}}\n\n", 200, 200, "no-text"},
		"tool call only":      {"text/event-stream", "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"function_call\",\"name\":\"x\",\"arguments\":\"{}\"}],\"usage\":{\"output_tokens\":30}}}\n\n", 200, 200, "no-text"},
		"failed event":        {"text/event-stream", "data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\"}}\n\n", 200, 502, ""},
		"error event":         {"text/event-stream", "data: {\"type\":\"error\",\"error\":{\"message\":\"boom\"}}\n\n", 200, 502, ""},
		"invalid json body":   {"application/json", `{"unexpected":true}`, 200, 502, ""},
		"http error":          {"application/json", `{"error":{"message":"bad upstream-secret"}}`, 500, 502, ""},
		"empty json response": {"application/json", ``, 200, 502, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer up.Close()
			_, app, mid := quickFixture(t, up.URL+"/v1")
			result, w := runQuickTest(t, app, mid)
			if w.Code != tc.wantHTTP {
				t.Fatalf("status=%d body=%s want %d", w.Code, w.Body.String(), tc.wantHTTP)
			}
			if tc.wantOutcome != "" && result.Outcome != tc.wantOutcome {
				t.Fatalf("outcome=%q want %q", result.Outcome, tc.wantOutcome)
			}
			if tc.wantHTTP != 200 && (result.Outcome == "ok" || result.TextReturned) {
				t.Fatalf("failed call reported as text available: %#v", result)
			}
			if strings.Contains(w.Body.String(), "upstream-secret") {
				t.Fatalf("secret leaked: %s", w.Body.String())
			}
		})
	}
}

// SSE frames may be split at arbitrary byte boundaries, including inside a JSON
// payload or a CRLF pair. The parser must still assemble complete events.
func TestQuickTestHandlesArbitrarySSEChunkBoundaries(t *testing.T) {
	stream := "event: response.output_text.delta\r\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"Hel\"}\r\n\r\n" +
		"data: {\"type\":\"response.output_text.delta\",\r\n" +
		"data: \"delta\":\"lo\"}\r\n\r\n" +
		"event: response.completed\r\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"Hello\"}]}],\"usage\":{\"output_tokens\":8}}}\r\n\r\n"
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for i := 0; i < len(stream); i += 3 {
			end := i + 3
			if end > len(stream) {
				end = len(stream)
			}
			_, _ = io.WriteString(w, stream[i:end])
			flusher.Flush()
		}
	}))
	defer up.Close()
	_, app, mid := quickFixture(t, up.URL+"/v1")
	result, w := runQuickTest(t, app, mid)
	if w.Code != 200 || result.Outcome != "ok" || result.OutputTokens == nil || *result.OutputTokens != 8 {
		t.Fatalf("chunked stream %d %#v %s", w.Code, result, w.Body.String())
	}
}

func TestQuickTestIncompleteDueToOutputLimitIsExplained(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"Partial answer\"}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.incomplete\",\"response\":{\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":\"max_output_tokens\"},\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"Partial answer\"}]}],\"usage\":{\"output_tokens\":192}}}\n\n")
	}))
	defer up.Close()
	_, app, mid := quickFixture(t, up.URL+"/v1")
	result, w := runQuickTest(t, app, mid)
	if w.Code != 200 {
		t.Fatalf("incomplete status=%d body=%s", w.Code, w.Body.String())
	}
	if result.Outcome != "truncated" || !result.Truncated || !result.TextReturned || result.IncompleteReason != "max_output_tokens" {
		t.Fatalf("incomplete result %#v", result)
	}
	if result.Outcome == "ok" {
		t.Fatal("truncated response must not be reported as a full success")
	}
}

func TestQuickTestTransportTimeoutAndCancellationReachUpstream(t *testing.T) {
	t.Run("deadline", func(t *testing.T) {
		disconnected := make(chan struct{})
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			close(disconnected)
		}))
		defer up.Close()
		s, _, mid := quickFixture(t, up.URL+"/v1")
		u := NewUpstreams(up.Client(), 0)
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		route, _ := s.ModelRoute(mid)
		if _, err := u.QuickTest(ctx, route); ErrorStatus(err) != 504 {
			t.Fatalf("deadline err=%v", err)
		}
		select {
		case <-disconnected:
		case <-time.After(2 * time.Second):
			t.Fatal("upstream was not disconnected on deadline")
		}
	})
	t.Run("cancel", func(t *testing.T) {
		disconnected := make(chan struct{})
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			close(disconnected)
		}))
		defer up.Close()
		s, _, mid := quickFixture(t, up.URL+"/v1")
		u := NewUpstreams(up.Client(), 0)
		ctx, cancel := context.WithCancel(context.Background())
		go func() { time.Sleep(80 * time.Millisecond); cancel() }()
		route, _ := s.ModelRoute(mid)
		if _, err := u.QuickTest(ctx, route); ErrorStatus(err) != 499 {
			t.Fatalf("cancel err=%v", err)
		}
		select {
		case <-disconnected:
		case <-time.After(2 * time.Second):
			t.Fatal("upstream was not disconnected on cancel")
		}
	})
}

// The HTTP endpoint must derive its deadline from the request context, so a
// client disconnect actually aborts the upstream generation.
func TestQuickTestHTTPClientDisconnectAbortsUpstream(t *testing.T) {
	disconnected := make(chan struct{})
	var once sync.Once
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		once.Do(func() { close(disconnected) })
	}))
	defer up.Close()
	s, app, mid := quickFixture(t, up.URL+"/v1")
	app.opts.Client = up.Client()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	app.opts.Port = port
	server := httptest.NewUnstartedServer(app)
	server.Listener = listener
	server.Start()
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/api/models/"+mid+"/quick-test", nil)
	req.Header.Set("Authorization", "Bearer "+s.TokenPair().AdminToken)
	go func() { time.Sleep(120 * time.Millisecond); cancel() }()
	if _, err := server.Client().Do(req); err == nil {
		t.Fatal("expected the client request to be cancelled")
	}
	select {
	case <-disconnected:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream generation was not cancelled")
	}
}

func TestQuickTestDoesNotMutateOpenStateDefaultOrOtherModels(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"OK\"}]}],\"usage\":{\"output_tokens\":5}}}\n\n")
	}))
	defer up.Close()
	s := newTestStore(t)
	gid := mustGroup(t, s, "work")
	closed := mustModel(t, s, ModelInput{GroupID: gid, Name: "closed", Model: "m", BaseURL: up.URL + "/v1", APIKey: "synthetic"})
	open := mustModel(t, s, ModelInput{GroupID: gid, Name: "open", Model: "m2", BaseURL: up.URL + "/v1", APIKey: "synthetic"})
	if err := s.SetDefault(open); err != nil {
		t.Fatal(err)
	}
	if err := s.SetModelEnabled(closed, false); err != nil {
		t.Fatal(err)
	}
	app := mustHTTPApp(HTTPOptions{Store: s})
	before := s.Snapshot()
	if result, w := runQuickTest(t, app, closed); w.Code != 200 || result.Outcome != "ok" {
		t.Fatalf("closed model quick test %d %#v", w.Code, result)
	}
	after := s.Snapshot()
	if !after.Models[0].Disabled {
		t.Fatal("quick test reopened a closed model")
	}
	if after.DefaultModelID != open {
		t.Fatal("quick test changed the default model")
	}
	if after.Revision != before.Revision || len(after.Models) != len(before.Models) {
		t.Fatal("quick test mutated configuration")
	}
	if _, err := s.Resolve("adroute/work/closed"); ErrorStatus(err) != 403 {
		t.Fatalf("closed route still reachable after test: %v", err)
	}
}

func TestQuickTestRejectsInvalidUsageWithoutNaN(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"object":"response","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"hi"}]}],"usage":{"output_tokens":-3}}`)
	}))
	defer up.Close()
	_, app, mid := quickFixture(t, up.URL+"/v1")
	result, w := runQuickTest(t, app, mid)
	if w.Code != 200 || result.Outcome != "no-usage" || result.OutputTokens != nil || result.TokensPerSecond != nil {
		t.Fatalf("negative usage %d %#v", w.Code, result)
	}
	if strings.Contains(w.Body.String(), "NaN") || strings.Contains(w.Body.String(), "Infinity") {
		t.Fatalf("bad numeric output: %s", w.Body.String())
	}
}

// The advanced tool-structure probe is separate from the quick text test: it
// verifies the shape of one tool call without executing anything.
func TestToolStructureTestRequiresAWellFormedProbeCall(t *testing.T) {
	call := `{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","call_id":"c1","name":"adroute_probe","arguments":"{\"marker\":\"adroute-test\"}"}}`
	done := `{"type":"response.completed","response":{"status":"completed","output":[{"type":"function_call","call_id":"c1","name":"adroute_probe","arguments":"{\"marker\":\"adroute-test\"}"}]}}`
	for _, tc := range []struct {
		name   string
		stream string
		wantOK bool
	}{
		{"probe call", "data: " + call + "\n\ndata: " + done + "\n\n", true},
		{"text only", "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"hi\"}]}]}}\n\n", false},
		{"failed", "data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\"}}\n\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var received map[string]any
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewDecoder(r.Body).Decode(&received)
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, tc.stream)
			}))
			defer up.Close()
			u := NewUpstreams(up.Client(), 0)
			result, err := u.ToolStructureTest(context.Background(), Route{ID: "m", BaseURL: up.URL + "/v1", APIKey: "synthetic", Model: "vendor/model"})
			if tc.wantOK {
				if err != nil || result["ok"] != true || result["testKind"] != "tool-structure" || result["toolExecuted"] != false {
					t.Fatalf("tool structure result %#v err=%v", result, err)
				}
				if received["model"] != "vendor/model" {
					t.Fatalf("model not pinned: %#v", received["model"])
				}
				if _, ok := received["tools"]; !ok {
					t.Fatalf("tool probe request missing tools: %#v", received)
				}
			} else if err == nil {
				t.Fatalf("expected tool structure failure, got %#v", result)
			}
		})
	}
}
