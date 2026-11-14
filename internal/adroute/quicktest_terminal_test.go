package adroute

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestQuickTestRejectsMissingAndContradictoryTerminalStates(t *testing.T) {
	text := `"output":[{"type":"message","content":[{"type":"output_text","text":"hello"}]}],"usage":{"output_tokens":8}`
	for _, tc := range []struct{ name, contentType, body string }{
		{"json still running", "application/json", `{"object":"response","status":"in_progress",` + text + `}`},
		{"json missing status", "application/json", `{"object":"response",` + text + `}`},
		{"completed event still running", "text/event-stream", "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"in_progress\"," + text + "}}\n\n"},
		{"incomplete event missing status", "text/event-stream", "data: {\"type\":\"response.incomplete\",\"response\":{" + text + "}}\n\n"},
		{"completed event incomplete status", "text/event-stream", "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"incomplete\"," + text + "}}\n\n"},
		{"failure cannot be overwritten", "text/event-stream", "data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\"}}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"," + text + "}}\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				fmt.Fprint(w, tc.body)
			}))
			defer up.Close()
			result, err := NewUpstreams(up.Client(), 0).QuickTest(context.Background(), Route{BaseURL: up.URL + "/v1", APIKey: "synthetic", Model: "m"})
			if ErrorStatus(err) != 502 || result.TextReturned || result.Outcome == "ok" {
				t.Fatalf("invalid terminal accepted: result=%#v err=%v", result, err)
			}
		})
	}
}

func TestInferenceProbesFinishAtTerminalAndCloseHeldOpenStream(t *testing.T) {
	text := "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]}],\"usage\":{\"output_tokens\":8}}}\n\n"
	tool := "data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\",\"call_id\":\"c1\",\"name\":\"adroute_probe\",\"arguments\":\"{\\\"marker\\\":\\\"adroute-test\\\"}\"}}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"function_call\",\"call_id\":\"c1\",\"name\":\"adroute_probe\",\"arguments\":\"{\\\"marker\\\":\\\"adroute-test\\\"}\"}]}}\n\n"
	for _, tc := range []struct{ name, stream string }{{"quick text", text}, {"tool structure", tool}} {
		t.Run(tc.name, func(t *testing.T) {
			disconnected := make(chan struct{})
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, tc.stream)
				w.(http.Flusher).Flush()
				<-r.Context().Done()
				close(disconnected)
			}))
			defer up.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
			defer cancel()
			u := NewUpstreams(up.Client(), 0)
			route := Route{BaseURL: up.URL + "/v1", APIKey: "synthetic", Model: "m"}
			if tc.name == "quick text" {
				result, err := u.QuickTest(ctx, route)
				if err != nil || result.Outcome != "ok" || ctx.Err() != nil {
					t.Fatalf("completed text waited for EOF/deadline: %#v %v", result, err)
				}
			} else {
				result, err := u.ToolStructureTest(ctx, route)
				if err != nil || result["ok"] != true || ctx.Err() != nil {
					t.Fatalf("completed tool stream waited for EOF/deadline: %#v %v", result, err)
				}
			}
			select {
			case <-disconnected:
			case <-time.After(2 * time.Second):
				t.Fatal("terminal response was not closed promptly")
			}
		})
	}
}

func TestQuickTestFinishesAtCompleteJSONObjectWithoutWaitingForBodyEOF(t *testing.T) {
	disconnected := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"object":"response","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"hello"}]}],"usage":{"output_tokens":8}}`)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(disconnected)
	}))
	defer up.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	result, err := NewUpstreams(up.Client(), 0).QuickTest(ctx, Route{BaseURL: up.URL + "/v1", APIKey: "synthetic", Model: "m"})
	if err != nil || result.Outcome != "ok" || ctx.Err() != nil {
		t.Fatalf("complete JSON response waited for HTTP body EOF: %#v %v", result, err)
	}
	select {
	case <-disconnected:
	case <-time.After(2 * time.Second):
		t.Fatal("completed JSON response body was not closed")
	}
}

func TestQuickTestDoesNotTrustOverBudgetUsageOrEchoUnknownReason(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"object":"response","status":"incomplete","incomplete_details":{"reason":"upstream-secret"},"output":[{"type":"message","content":[{"type":"output_text","text":"hello"}]}],"usage":{"output_tokens":999999999}}`)
	}))
	defer up.Close()
	_, app, mid := quickFixture(t, up.URL+"/v1")
	result, response := runQuickTest(t, app, mid)
	if response.Code != 200 || !result.TextReturned {
		t.Fatalf("text outcome missing: %d %#v", response.Code, result)
	}
	if result.UsageReported || result.OutputTokens != nil || result.TokensPerSecond != nil {
		t.Fatalf("unreasonable short-test usage trusted: %#v", result)
	}
	if strings.Contains(response.Body.String(), "upstream-secret") {
		t.Fatal("unknown upstream incomplete reason echoed a credential")
	}
}
