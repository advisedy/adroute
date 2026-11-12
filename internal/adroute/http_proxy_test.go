package adroute

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/andybalholm/brotli"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"
)

const testUpstreamURL = "http://127.0.0.1:54321/v1"

// proxyFixture creates one saved model entry in group "work" with its own
// upstream credential and marks it as the web default.
func proxyFixture(t *testing.T, options HTTPOptions) (*Store, *HTTPApp, string, string) {
	t.Helper()
	s, e := OpenStore(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	gid, e := s.CreateGroup(GroupInput{Name: "work"})
	if e != nil {
		t.Fatal(e)
	}
	mid, e := s.CreateModel(ModelInput{GroupID: gid, Name: "label", Model: "same-model", BaseURL: testUpstreamURL, APIKey: "upstream-secret"})
	if e != nil {
		t.Fatal(e)
	}
	if e = s.SetDefault(mid); e != nil {
		t.Fatal(e)
	}
	options.Store = s
	if options.Port == 0 {
		options.Port = 3001
	}
	app := mustHTTPApp(options)
	return s, app, gid, mid
}

func setProxiedBaseURL(t *testing.T, s *Store, gid, mid, raw string) {
	t.Helper()
	if e := s.UpdateModel(mid, ModelInput{GroupID: gid, Name: "label", Model: "same-model", BaseURL: raw, APIKey: "upstream-secret"}); e != nil {
		t.Fatal(e)
	}
}

func proxyCall(t *testing.T, a *HTTPApp, model string, body map[string]any, headers http.Header) *httptest.ResponseRecorder {
	t.Helper()
	if model != "" {
		body["model"] = model
	}
	b, _ := json.Marshal(body)
	r := httptest.NewRequest("POST", "http://127.0.0.1:3001/v1/responses", bytes.NewReader(b))
	r.Host = "127.0.0.1:3001"
	r.Header.Set("Authorization", "Bearer "+a.opts.Store.TokenPair().ProxyToken)
	for k, v := range headers {
		for _, x := range v {
			r.Header.Add(k, x)
		}
	}
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	return w
}

func TestProxyNativeStreamFirstByteHeaderAllowlistAndBodyPreservation(t *testing.T) {
	first := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	var received map[string]any
	var got http.Header
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		_ = json.NewDecoder(r.Body).Decode(&received)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Request-Id", "synthetic-id")
		w.Header().Set("Set-Cookie", "no-forward=yes")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"first\"}\n\n")
		w.(http.Flusher).Flush()
		close(first)
		<-release
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
	}))
	defer up.Close()
	s, app, gid, mid := proxyFixture(t, HTTPOptions{Client: up.Client()})
	setProxiedBaseURL(t, s, gid, mid, up.URL+"/v1")
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	app.opts.Port = port
	server := httptest.NewUnstartedServer(app)
	server.Listener = listener
	server.Start()
	defer server.Close()
	payload := map[string]any{"model": "adroute/default", "input": []any{map[string]any{"role": "user", "content": "private prompt"}}, "stream": true, "store": false, "reasoning": map[string]any{"effort": "high"}, "tools": []any{map[string]any{"type": "function", "name": "ignored-client-tool"}}, "extra": map[string]any{"retain": true}}
	req, _ := http.NewRequest("POST", server.URL+"/v1/responses", bytes.NewReader(mustJSONBytes(payload)))
	req.Header.Set("Authorization", "Bearer "+s.TokenPair().ProxyToken)
	req.Header.Set("Cookie", "client-secret-cookie")
	req.Header.Set("OpenAI-Organization", "must-not-forward")
	req.Header.Set("Session-Id", "session-allow")
	req.Header.Set("X-Codex-Turn-State", "turn-allow")
	resp, e := server.Client().Do(req)
	if e != nil {
		t.Fatal(e)
	}
	select {
	case <-first:
	case <-time.After(time.Second):
		t.Fatal("upstream did not start")
	}
	reader := bufio.NewReader(resp.Body)
	line, e := reader.ReadString('\n')
	if e != nil || !strings.Contains(line, "first") {
		t.Fatalf("first chunk delayed: %q %v", line, e)
	}
	if resp.StatusCode != 200 || resp.Header.Get("X-Request-Id") != "synthetic-id" || resp.Header.Get("Set-Cookie") != "" {
		t.Fatalf("status/headers %d %v", resp.StatusCode, resp.Header)
	}
	if received["model"] != "same-model" || received["extra"].(map[string]any)["retain"] != true || received["store"] != false || received["tools"] == nil {
		t.Fatalf("request parameters changed: %#v", received)
	}
	if got.Get("Authorization") != "Bearer upstream-secret" || got.Get("Session-Id") != "session-allow" || got.Get("X-Codex-Turn-State") != "turn-allow" {
		t.Fatalf("allowed headers missing %v", got)
	}
	for _, h := range []string{"Cookie", "OpenAI-Organization", "X-Forwarded-For"} {
		if got.Get(h) != "" {
			t.Fatalf("client header forwarded: %s", h)
		}
	}
	releaseOnce.Do(func() { close(release) })
	tail, _ := io.ReadAll(reader)
	_ = resp.Body.Close()
	if !strings.Contains(string(tail), "response.completed") {
		t.Fatalf("tail=%s", tail)
	}
	if whole := line + string(tail); whole != "data: {\"type\":\"response.output_text.delta\",\"delta\":\"first\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n" {
		t.Fatalf("native stream was not byte-identical: %q", whole)
	}
}

func TestProxyMissingModelUsesDefaultButExplicitInvalidValuesFail(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["model"] != "same-model" {
			t.Errorf("forwarded model = %#v", body["model"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
	}))
	defer up.Close()
	s, app, gid, mid := proxyFixture(t, HTTPOptions{Client: up.Client()})
	setProxiedBaseURL(t, s, gid, mid, up.URL+"/v1")
	for _, tc := range []struct {
		name string
		raw  string
		want int
	}{
		{"absent uses default", `{"input":"synthetic"}`, 200},
		{"explicit default route", `{"model":"adroute/default","input":"synthetic"}`, 200},
		{"null is invalid", `{"model":null}`, 400},
		{"number is invalid", `{"model":7}`, 400},
		{"empty is invalid", `{"model":""}`, 400},
		{"raw upstream id is not a route", `{"model":"same-model","input":"synthetic"}`, 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "http://127.0.0.1:3001/v1/responses", strings.NewReader(tc.raw))
			r.Host = "127.0.0.1:3001"
			r.Header.Set("Authorization", "Bearer "+s.TokenPair().ProxyToken)
			w := httptest.NewRecorder()
			app.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status=%d body=%s, want %d", w.Code, w.Body.String(), tc.want)
			}
		})
	}
	empty, e := OpenStore(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	emptyApp := mustHTTPApp(HTTPOptions{Store: empty, Port: 3001})
	r := httptest.NewRequest("POST", "http://127.0.0.1:3001/v1/responses", strings.NewReader(`{"input":"synthetic"}`))
	r.Host = "127.0.0.1:3001"
	r.Header.Set("Authorization", "Bearer "+empty.TokenPair().ProxyToken)
	w := httptest.NewRecorder()
	emptyApp.ServeHTTP(w, r)
	if w.Code != 503 {
		t.Fatalf("absent model without default status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestResolveStateRejectsAllUnicodeControlCharacters(t *testing.T) {
	s, _, _, _ := proxyFixture(t, HTTPOptions{})
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if unicode.IsControl(r) {
			model := "valid" + string(r) + "model"
			_, err := ResolveState(s.Snapshot(), model)
			var appErr *AppError
			if !errors.As(err, &appErr) || appErr.Code != "INVALID_MODEL" {
				t.Errorf("ResolveState(%q) err=%v; want INVALID_MODEL", model, err)
			}
		}
	}
}
func mustJSONBytes(v any) []byte { b, _ := json.Marshal(v); return b }

func TestProxySnapshotIsBeforeSlowJSONUploadAndPinsOldConnection(t *testing.T) {
	var gotKey, gotModel string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("Authorization")
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		gotModel, _ = b["model"].(string)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
	}))
	defer up.Close()
	s, app, gid, mid := proxyFixture(t, HTTPOptions{Client: up.Client()})
	setProxiedBaseURL(t, s, gid, mid, up.URL+"/v1")
	started := make(chan struct{})
	resume := make(chan struct{})
	r := httptest.NewRequest("POST", "http://127.0.0.1:3001/v1/responses", nil)
	r.Host = "127.0.0.1:3001"
	r.Header.Set("Authorization", "Bearer "+s.TokenPair().ProxyToken)
	r.Header.Set("Content-Type", "application/json")
	r.Body = &gatedBody{first: []byte(`{"model":"adroute/work/label","input":`), started: started, resume: resume, last: []byte(`"hello","stream":true}`)}
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { app.ServeHTTP(w, r); close(done) }()
	<-started
	// A concurrent edit during upload must not change the already-received request.
	if e := s.UpdateModel(mid, ModelInput{GroupID: gid, Name: "label", Model: "replacement", BaseURL: up.URL + "/v1", APIKey: "new-key"}); e != nil {
		t.Fatal(e)
	}
	close(resume)
	<-done
	if w.Code != 200 || gotKey != "Bearer upstream-secret" || gotModel != "same-model" {
		t.Fatalf("snapshot failed status=%d key=%q model=%q body=%s", w.Code, gotKey, gotModel, w.Body.String())
	}
}

type gatedBody struct {
	first, last []byte
	started     chan struct{}
	resume      chan struct{}
	once        sync.Once
	stage       int
}

func (g *gatedBody) Read(p []byte) (int, error) {
	if g.stage == 0 {
		g.stage++
		n := copy(p, g.first)
		g.once.Do(func() { close(g.started) })
		<-g.resume
		return n, nil
	}
	if g.stage == 1 {
		g.stage++
		return copy(p, g.last), nil
	}
	return 0, io.EOF
}
func (*gatedBody) Close() error { return nil }

func TestProxyUpstreamErrorStatusRetryAndSecretPromptHistoryRedaction(t *testing.T) {
	logs := []map[string]any{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(429)
		fmt.Fprint(w, `{"error":{"message":"quota upstream-secret prompt-secret"}}`)
	}))
	defer up.Close()
	s, app, gid, mid := proxyFixture(t, HTTPOptions{Client: up.Client(), Log: func(m map[string]any) { logs = append(logs, m) }})
	setProxiedBaseURL(t, s, gid, mid, up.URL+"/v1")
	w := proxyCall(t, app, "adroute/default", map[string]any{"input": "prompt-secret", "stream": true}, nil)
	if w.Code != 429 || w.Header().Get("Retry-After") != "7" || strings.Contains(w.Body.String(), "upstream-secret") {
		t.Fatalf("error response %d %v %s", w.Code, w.Header(), w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "[REDACTED]") {
		t.Fatalf("secret not redacted: %s", w.Body.String())
	}
	state := app.stateJSON()
	requests := state["requests"].([]map[string]any)
	if len(requests) != 1 || requests[0]["status"] != 429 || strings.Contains(fmt.Sprint(requests[0]), "prompt-secret") || strings.Contains(fmt.Sprint(logs), "prompt-secret") {
		t.Fatalf("history/log privacy failed: %#v %#v", requests, logs)
	}
	if requests[0]["routeId"] != "adroute/work/label" {
		t.Fatalf("request history route id: %#v", requests[0])
	}
}

func TestProxyHeaderAndIdleTimeoutsAndCancellationAbort(t *testing.T) {
	t.Run("header", func(t *testing.T) {
		disconnected := make(chan struct{})
		transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
			<-r.Context().Done()
			close(disconnected)
			return nil, r.Context().Err()
		})
		s, a, gid, mid := proxyFixture(t, HTTPOptions{Client: &http.Client{Transport: transport}, HeaderTimeout: 40 * time.Millisecond})
		setProxiedBaseURL(t, s, gid, mid, "https://local.test/v1")
		w := proxyCall(t, a, "adroute/default", map[string]any{"stream": true}, nil)
		if w.Code != 504 || !strings.Contains(w.Body.String(), "UPSTREAM_TIMEOUT") {
			t.Fatalf("header timeout %d %s", w.Code, w.Body.String())
		}
		select {
		case <-disconnected:
		case <-time.After(time.Second):
			t.Fatal("upstream request not cancelled")
		}
	})
	t.Run("idle", func(t *testing.T) {
		disconnected := make(chan struct{})
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: first\n\n")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			close(disconnected)
		}))
		defer up.Close()
		logs := make(chan map[string]any, 1)
		s, a, gid, mid := proxyFixture(t, HTTPOptions{Client: up.Client(), IdleTimeout: 60 * time.Millisecond, Log: func(m map[string]any) { logs <- m }})
		setProxiedBaseURL(t, s, gid, mid, up.URL+"/v1")
		listener, _ := net.Listen("tcp", "127.0.0.1:0")
		port := listener.Addr().(*net.TCPAddr).Port
		a.opts.Port = port
		server := httptest.NewUnstartedServer(a)
		server.Listener = listener
		server.Start()
		defer server.Close()
		req, _ := http.NewRequest("POST", server.URL+"/v1/responses", strings.NewReader(`{"model":"adroute/default","stream":true}`))
		req.Header.Set("Authorization", "Bearer "+s.TokenPair().ProxyToken)
		resp, e := server.Client().Do(req)
		if e != nil {
			t.Fatal(e)
		}
		first := make([]byte, 1)
		_, _ = resp.Body.Read(first)
		select {
		case <-disconnected:
		case <-time.After(time.Second):
			t.Fatal("idle timeout did not cancel origin")
		}
		_ = resp.Body.Close()
		select {
		case event := <-logs:
			if event["status"] != 504 {
				t.Fatalf("idle status %#v", event)
			}
		case <-time.After(time.Second):
			t.Fatal("proxy request was not recorded")
		}
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestProxyCompressedRequestBodiesAreDecodedBeforeRouting(t *testing.T) {
	for _, kind := range []string{"gzip", "br", "deflate"} {
		t.Run(kind, func(t *testing.T) {
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				if body["input"] != "decoded" {
					t.Errorf("bad body %#v", body)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
			}))
			defer up.Close()
			s, a, gid, mid := proxyFixture(t, HTTPOptions{Client: up.Client()})
			setProxiedBaseURL(t, s, gid, mid, up.URL+"/v1")
			raw := []byte(`{"model":"adroute/default","input":"decoded","stream":true}`)
			var b bytes.Buffer
			switch kind {
			case "gzip":
				z := gzip.NewWriter(&b)
				_, _ = z.Write(raw)
				_ = z.Close()
			case "br":
				z := brotli.NewWriter(&b)
				_, _ = z.Write(raw)
				_ = z.Close()
			case "deflate":
				z, _ := zlib.NewWriterLevel(&b, zlib.DefaultCompression)
				_, _ = z.Write(raw)
				_ = z.Close()
			}
			r := httptest.NewRequest("POST", "http://127.0.0.1:3001/v1/responses", &b)
			r.Host = "127.0.0.1:3001"
			r.Header.Set("Authorization", "Bearer "+s.TokenPair().ProxyToken)
			r.Header.Set("Content-Encoding", kind)
			w := httptest.NewRecorder()
			a.ServeHTTP(w, r)
			if w.Code != 200 {
				t.Fatalf("%s decode failed %d %s", kind, w.Code, w.Body.String())
			}
		})
	}
}

func TestProxyClientCancellationAbortsUpstream(t *testing.T) {
	disconnected := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(disconnected)
	}))
	defer up.Close()
	s, a, gid, mid := proxyFixture(t, HTTPOptions{Client: up.Client()})
	setProxiedBaseURL(t, s, gid, mid, up.URL+"/v1")
	listener, _ := net.Listen("tcp", "127.0.0.1:0")
	port := listener.Addr().(*net.TCPAddr).Port
	a.opts.Port = port
	server := httptest.NewUnstartedServer(a)
	server.Listener = listener
	server.Start()
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "POST", server.URL+"/v1/responses", strings.NewReader(`{"model":"adroute/default","stream":true}`))
	req.Header.Set("Authorization", "Bearer "+s.TokenPair().ProxyToken)
	resp, e := server.Client().Do(req)
	if e != nil {
		t.Fatal(e)
	}
	_, _ = resp.Body.Read(make([]byte, 8))
	cancel()
	_ = resp.Body.Close()
	select {
	case <-disconnected:
	case <-time.After(time.Second):
		t.Fatal("client cancellation did not abort upstream")
	}
}

func TestProxyRepairEnginesUseVerifiedEntryLocalPackages(t *testing.T) {
	for _, file := range []string{"pixel-swe.adroute-adapter.json", "pixel-swe-v2.adroute-adapter.json", "pixel-swe-v3.adroute-adapter.json"} {
		t.Run(file, func(t *testing.T) {
			raw, e := os.ReadFile("../../examples/" + file)
			if e != nil {
				t.Fatal(e)
			}
			var pack AdapterPackage
			if e = json.Unmarshal(raw, &pack); e != nil {
				t.Fatal(e)
			}
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				for _, event := range pack.Fixtures[0].Events {
					var m map[string]any
					_ = json.Unmarshal(event, &m)
					compact, _ := json.Marshal(m)
					fmt.Fprintf(w, "event: %s\ndata: %s\n\n", m["type"], compact)
				}
			}))
			defer up.Close()
			s, e := OpenStore(t.TempDir())
			if e != nil {
				t.Fatal(e)
			}
			if e = s.ImportAdapter(pack); e != nil {
				t.Fatal(e)
			}
			gid, e := s.CreateGroup(GroupInput{Name: "repair"})
			if e != nil {
				t.Fatal(e)
			}
			mid, e := s.CreateModel(ModelInput{GroupID: gid, Name: "repair-model", Model: "repair-model", BaseURL: up.URL + "/v1", APIKey: "fixture-key", Profile: PackageRef(pack)})
			if e != nil {
				t.Fatal(e)
			}
			if e = s.SetDefault(mid); e != nil {
				t.Fatal(e)
			}
			a := mustHTTPApp(HTTPOptions{Store: s, Client: up.Client()})
			w := proxyCall(t, a, "adroute/default", map[string]any{"stream": true}, nil)
			if w.Code != 200 {
				t.Fatalf("proxy status %d %s", w.Code, w.Body.String())
			}
			events, e := ParseRepairedEvents(w.Body.Bytes())
			if e != nil || len(events) == 0 {
				t.Fatalf("repair output parse=%v events=%d body=%s", e, len(events), w.Body.String())
			}
			found := false
			for _, event := range events {
				var m map[string]any
				_ = json.Unmarshal(event, &m)
				found = found || m["type"] == "response.completed"
			}
			if !found {
				t.Fatalf("completion absent: %s; history=%#v; replay=%#v", w.Body.String(), a.requests, func() any { v, _ := VerifyPackage(pack); return v }())
			}
		})
	}
}

// Two entries may share the same real upstream model id while owning different
// credentials and addresses. Neither may borrow the other's connection.
func TestProxySameRealModelUsesExactEntryConnection(t *testing.T) {
	var got []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
	}))
	defer up.Close()
	s, e := OpenStore(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	gid, e := s.CreateGroup(GroupInput{Name: "dual"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.CreateModel(ModelInput{GroupID: gid, Name: "first", Model: "same", BaseURL: up.URL + "/v1", APIKey: "key-A"}); e != nil {
		t.Fatal(e)
	}
	if _, e = s.CreateModel(ModelInput{GroupID: gid, Name: "second", Model: "same", BaseURL: up.URL + "/v1", APIKey: "key-B"}); e != nil {
		t.Fatal(e)
	}
	a := mustHTTPApp(HTTPOptions{Store: s, Client: up.Client()})
	for _, route := range []string{"adroute/dual/first", "adroute/dual/second"} {
		w := proxyCall(t, a, route, map[string]any{"stream": true}, nil)
		if w.Code != 200 {
			t.Fatalf("route %s => %d %s", route, w.Code, w.Body.String())
		}
	}
	if fmt.Sprint(got) != "[Bearer key-A Bearer key-B]" {
		t.Fatalf("bound credentials %v", got)
	}
}

func TestProxyRefusesSelfTargetAndDoesNotFollowCredentialRedirects(t *testing.T) {
	t.Run("self-loop", func(t *testing.T) {
		s, e := OpenStore(t.TempDir())
		if e != nil {
			t.Fatal(e)
		}
		gid, e := s.CreateGroup(GroupInput{Name: "self"})
		if e != nil {
			t.Fatal(e)
		}
		mid, e := s.CreateModel(ModelInput{GroupID: gid, Name: "m", Model: "m", BaseURL: "http://127.0.0.1:3001/v1", APIKey: "secret"})
		if e != nil {
			t.Fatal(e)
		}
		_ = s.SetDefault(mid)
		a := mustHTTPApp(HTTPOptions{Store: s, Port: 3001})
		w := proxyCall(t, a, "adroute/default", map[string]any{"stream": true}, nil)
		if w.Code != 400 || !strings.Contains(w.Body.String(), "PROXY_LOOP") {
			t.Fatalf("self-target status=%d body=%s", w.Code, w.Body.String())
		}
	})
	t.Run("redirect", func(t *testing.T) {
		leaked := false
		dst := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked = true; w.WriteHeader(200) }))
		defer dst.Close()
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", dst.URL+"/steal")
			w.WriteHeader(302)
			fmt.Fprint(w, "redirect")
		}))
		defer up.Close()
		s, a, gid, mid := proxyFixture(t, HTTPOptions{Client: up.Client()})
		setProxiedBaseURL(t, s, gid, mid, up.URL+"/v1")
		w := proxyCall(t, a, "adroute/default", map[string]any{"stream": true}, nil)
		if w.Code != 302 || w.Header().Get("Location") != "" || leaked {
			t.Fatalf("redirect contract status=%d headers=%v leaked=%v", w.Code, w.Header(), leaked)
		}
	})
}
