package adroute

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Mirrors the observed upstream failure: all 192 tokens were reasoning and
// the response ended as incomplete/max_output_tokens, without a text delta.
func TestQuickTestExplainsBudgetExhaustedBeforeText(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.reasoning_text.delta\",\"delta\":\"thinking\"}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.incomplete\",\"response\":{\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":\"max_output_tokens\"},\"output\":[{\"type\":\"reasoning\",\"content\":[]}],\"usage\":{\"output_tokens\":192,\"output_tokens_details\":{\"reasoning_tokens\":192}}}}\n\n")
	}))
	defer up.Close()
	result, err := NewUpstreams(up.Client(), 0).QuickTest(context.Background(), Route{BaseURL: up.URL, Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != "no-text" || !result.Truncated || result.IncompleteReason != "max_output_tokens" {
		t.Fatalf("lost incomplete outcome: %+v", result)
	}
	if !strings.Contains(result.Message, "推理") || !strings.Contains(result.Message, "额度") {
		t.Fatalf("must explain why no text arrived: %+v", result)
	}
}

func TestQuickTestLeavesRoomForReasoningAndTextInOneRequest(t *testing.T) {
	calls := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		budget, _ := body["max_output_tokens"].(float64)
		w.Header().Set("Content-Type", "application/json")
		if budget < 504 {
			fmt.Fprintf(w, `{"object":"response","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"reasoning"}],"usage":{"output_tokens":%d,"output_tokens_details":{"reasoning_tokens":%d}}}`, int(budget), int(budget))
			return
		}
		fmt.Fprint(w, `{"object":"response","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"A practical answer."}]}],"usage":{"output_tokens":504,"output_tokens_details":{"reasoning_tokens":411}}}`)
	}))
	defer up.Close()
	result, err := NewUpstreams(up.Client(), 0).QuickTest(context.Background(), Route{BaseURL: up.URL, Model: "m"})
	if err != nil || !result.TextReturned || result.Outcome != "ok" {
		t.Fatalf("reasoning exhausted the text budget: result=%+v err=%v", result, err)
	}
	if calls != 1 {
		t.Fatalf("must not silently retry a paid request: calls=%d", calls)
	}
}

func TestQuickTestTextSpeedExcludesFirstWaitReasoningAndUsageTail(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.created\"}\n\ndata: {\"type\":\"response.in_progress\"}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"reasoning\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.content_part.added\",\"part\":{\"type\":\"reasoning_text\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.reasoning_text.delta\",\"delta\":\"thinking\"}\n\n")
		w.(http.Flusher).Flush()
		time.Sleep(150 * time.Millisecond)
		fmt.Fprint(w, "data: {\"type\":\"response.reasoning_text.done\"}\n\ndata: {\"type\":\"response.content_part.done\",\"part\":{\"type\":\"reasoning_text\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"reasoning\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"message\",\"content\":[]}}\n\ndata: {\"type\":\"response.content_part.added\",\"part\":{\"type\":\"output_text\"}}\n\n")
		for i := 0; i < 3; i++ {
			if i > 0 {
				time.Sleep(100 * time.Millisecond)
			}
			fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"some text \"}\n\n")
			w.(http.Flusher).Flush()
		}
		// Final usage can be substantially later than the last text chunk.
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.done\"}\n\ndata: {\"type\":\"response.content_part.done\",\"part\":{\"type\":\"output_text\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"some text\"}]}}\n\n")
		w.(http.Flusher).Flush()
		time.Sleep(200 * time.Millisecond)
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[],\"usage\":{\"output_tokens\":120,\"output_tokens_details\":{\"reasoning_tokens\":60}}}}\n\n")
	}))
	defer up.Close()
	result, err := NewUpstreams(up.Client(), 0).QuickTest(context.Background(), Route{BaseURL: up.URL, Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	metrics := quickTestMetrics(t, result)
	rate, ok := metrics["textTokensPerSecond"].(float64)
	// 60 text tokens / ~0.2s = ~300, not 120/0.2, 60/0.4 or 120/0.55.
	if !ok || rate < 220 || rate > 380 {
		t.Fatalf("wrong output-phase rate (must support >200 token/s): %v", metrics)
	}
	if metrics["textTokens"] != float64(60) {
		t.Fatalf("hidden reasoning counted as text: %v", metrics)
	}
	span, ok := metrics["textDurationMs"].(float64)
	if !ok || span < 190 || span > float64(result.DurationMs)-float64(*result.FirstTextMs)-150 {
		t.Fatalf("usage tail included in output interval: %v", metrics)
	}
}

func TestQuickTestDoesNotInventTextSpeedFromMissingUsageOrShortSamples(t *testing.T) {
	for _, tc := range []struct {
		name, usage, reason string
		chunks              int
		output              string
	}{
		{"no usage", ``, "usage-unavailable", 2, `[]`},
		{"unknown reasoning", `,"usage":{"output_tokens":60}`, "reasoning-usage-unavailable", 2, `[]`},
		{"single buffered chunk", `,"usage":{"output_tokens":60,"output_tokens_details":{"reasoning_tokens":0}}`, "no-stream-timing", 1, `[]`},
		{"few text tokens", `,"usage":{"output_tokens":61,"output_tokens_details":{"reasoning_tokens":60}}`, "sample-too-short", 2, `[]`},
		{"text plus tool call", `,"usage":{"output_tokens":60,"output_tokens_details":{"reasoning_tokens":0}}`, "non-text-output", 2, `[{"type":"function_call","arguments":"{}"}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				for i := 0; i < tc.chunks; i++ {
					if i > 0 {
						time.Sleep(60 * time.Millisecond)
					}
					fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"text\"}\n\n")
					w.(http.Flusher).Flush()
				}
				fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":%s%s}}\n\n", tc.output, tc.usage)
			}))
			defer up.Close()
			result, err := NewUpstreams(up.Client(), 0).QuickTest(context.Background(), Route{BaseURL: up.URL, Model: "m"})
			if err != nil {
				t.Fatal(err)
			}
			metrics := quickTestMetrics(t, result)
			if metrics["textTokensPerSecond"] != nil || metrics["speedUnavailableReason"] != tc.reason {
				t.Fatalf("misleading text speed: %v", metrics)
			}
		})
	}
}

func TestQuickTestStreamedNonTextOutputDisqualifiesReducedTerminalSnapshot(t *testing.T) {
	for _, event := range []string{
		`{"type":"response.output_item.added","item":{"type":"function_call","name":"probe","arguments":""}}`,
		`{"type":"response.function_call_arguments.delta","delta":"{}"}`,
		`{"type":"response.output_audio.delta","delta":"AA=="}`,
		`{"type":"response.content_part.added","part":{"type":"output_audio"}}`,
	} {
		t.Run(event, func(t *testing.T) {
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "data: %s\n\n", event)
				fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"Hello\"}\n\n")
				w.(http.Flusher).Flush()
				time.Sleep(60 * time.Millisecond)
				fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\" world\"}\n\n")
				fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[],\"usage\":{\"output_tokens\":60,\"output_tokens_details\":{\"reasoning_tokens\":0}}}}\n\n")
			}))
			defer up.Close()
			result, err := NewUpstreams(up.Client(), 0).QuickTest(context.Background(), Route{BaseURL: up.URL, Model: "m"})
			if err != nil {
				t.Fatal(err)
			}
			metrics := quickTestMetrics(t, result)
			if metrics["textTokensPerSecond"] != nil || metrics["textTokens"] != nil || metrics["speedUnavailableReason"] != "non-text-output" {
				t.Fatalf("non-text usage mislabeled as text: %v", metrics)
			}
		})
	}
}
