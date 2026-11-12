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

func TestQuickTestEstimatesOnlyVisibleTextWhenReasoningUsageIsMissing(t *testing.T) {
	for _, usage := range []string{`,"usage":{"output_tokens":700}`, ``} {
		t.Run(usage, func(t *testing.T) {
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"hidden thinking must never be counted as visible text\"}\n\n")
				w.(http.Flusher).Flush()
				time.Sleep(120 * time.Millisecond)
				for start := 1; start <= 120; start += 60 {
					if start > 1 {
						time.Sleep(100 * time.Millisecond)
					}
					var fragment strings.Builder
					for n := start; n < start+60; n++ {
						if n > 1 {
							fragment.WriteByte(' ')
						}
						fmt.Fprint(&fragment, n)
					}
					event, _ := json.Marshal(map[string]any{"type": "response.output_text.delta", "delta": fragment.String()})
					fmt.Fprintf(w, "data: %s\n\n", event)
					w.(http.Flusher).Flush()
				}
				fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]%s}}\n\n", usage)
			}))
			defer up.Close()
			result, err := NewUpstreams(up.Client(), 0).QuickTest(context.Background(), Route{BaseURL: up.URL, Model: "m"})
			if err != nil {
				t.Fatal(err)
			}
			metrics := quickTestMetrics(t, result)
			// cl100k_base encodes each 1..120 integer and each intervening
			// single space separately: 120 + 119 = 239 reference tokens.
			if metrics["textTokens"] != float64(239) || metrics["textTokensSource"] != "cl100k_base-estimate" {
				t.Fatalf("missing explicitly marked visible-text reference count: %v", metrics)
			}
			rate, ok := metrics["textTokensPerSecond"].(float64)
			if !ok || rate < 1800 || rate > 2700 {
				t.Fatalf("must exclude hidden reasoning and first wait without capping speed: %v", metrics)
			}
			if result.ReasoningTokens != nil || metrics["speedUnavailableReason"] != nil {
				t.Fatalf("must not invent a reported reasoning count: %v", metrics)
			}
		})
	}
}

func TestQuickTestReferenceCountRejectsTinyAndOversizedText(t *testing.T) {
	for _, text := range []string{"1 2 3", strings.Repeat("1 ", 3000)} {
		if count := quickTestReferenceTokens(text); count != nil {
			t.Fatalf("unsuitable sample accepted: bytes=%d tokens=%d", len(text), *count)
		}
	}
}

func TestQuickTestReferenceCountIsSafeForConcurrentProbes(t *testing.T) {
	var sample strings.Builder
	for n := 1; n <= 120; n++ {
		if n > 1 {
			sample.WriteByte(' ')
		}
		fmt.Fprint(&sample, n)
	}
	for n := 0; n < 16; n++ {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			t.Parallel()
			count := quickTestReferenceTokens(sample.String())
			if count == nil || *count != 239 {
				t.Fatalf("inconsistent reference count: %v", count)
			}
		})
	}
}
