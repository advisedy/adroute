package adroute

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func quickTestMetrics(t *testing.T, result QuickTestResult) map[string]any {
	t.Helper()
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var metrics map[string]any
	if err := json.Unmarshal(raw, &metrics); err != nil {
		t.Fatal(err)
	}
	return metrics
}

// Response headers, lifecycle events and empty deltas must not stop the
// first-text clock; later deltas and delayed usage must not reset it.
func TestQuickTestMeasuresFirstNonemptyTextSeparatelyFromTotalTime(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.created\"}\n\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"\"}\n\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\" \\n\"}\n\n")
		w.(http.Flusher).Flush()
		time.Sleep(120 * time.Millisecond)
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"Hello\"}\n\n")
		w.(http.Flusher).Flush()
		time.Sleep(80 * time.Millisecond)
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\" world\"}\n\n")
		w.(http.Flusher).Flush()
		time.Sleep(80 * time.Millisecond)
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[],\"usage\":{\"output_tokens\":40}}}\n\n")
	}))
	defer up.Close()
	result, err := NewUpstreams(up.Client(), 0).QuickTest(context.Background(), Route{BaseURL: up.URL, Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	first, ok := quickTestMetrics(t, result)["firstTextMs"].(float64)
	if !ok || first < 100 || first > float64(result.DurationMs)-120 {
		t.Fatalf("first text must exclude headers and later output: first=%v result=%+v", first, result)
	}
	// The existing rate remains end-to-end, not output_tokens / post-first-text time.
	if result.TokensPerSecond == nil || *result.TokensPerSecond >= 200 {
		t.Fatalf("end-to-end rate lost its waiting time: %+v", result)
	}
}

func TestQuickTestReportsOnlyValidReasoningUsage(t *testing.T) {
	for _, tc := range []struct {
		name, details string
		want          any
	}{
		{"reported", `{"reasoning_tokens":24}`, float64(24)},
		{"zero", `{"reasoning_tokens":0}`, float64(0)},
		{"missing", `{}`, nil},
		{"negative", `{"reasoning_tokens":-1}`, nil},
		{"fractional", `{"reasoning_tokens":1.5}`, nil},
		{"over total", `{"reasoning_tokens":41}`, nil},
		{"wrong type", `{"reasoning_tokens":"24"}`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"object":"response","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"Hello"}]}],"usage":{"output_tokens":40,"output_tokens_details":%s}}`, tc.details)
			}))
			defer up.Close()
			result, err := NewUpstreams(up.Client(), 0).QuickTest(context.Background(), Route{BaseURL: up.URL, Model: "m"})
			if err != nil {
				t.Fatal(err)
			}
			metrics := quickTestMetrics(t, result)
			if got := metrics["reasoningTokens"]; got != tc.want {
				t.Fatalf("reasoningTokens=%v want %v", got, tc.want)
			}
			if _, ok := metrics["firstTextMs"]; ok {
				t.Fatal("non-streaming JSON cannot provide first-text latency")
			}
			if result.OutputTokens == nil || *result.OutputTokens != 40 {
				t.Fatal("reasoning tokens must remain part of total output usage")
			}
		})
	}
}
