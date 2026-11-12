package adroute

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestQuickTestReturnsActualTextForDetails(t *testing.T) {
	for _, tc := range []struct {
		name, contentType, body, want string
	}{
		{"final snapshot", "text/event-stream", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"1 2\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"1 2 3 4\"}]}]}}\n\n", "1 2 3 4"},
		{"stream fallback", "text/event-stream", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"1 2\"}\n\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\" 3 4\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n", "1 2 3 4"},
		{"json", "application/json", `{"object":"response","status":"completed","output":[{"type":"reasoning","content":[{"type":"reasoning_text","text":"private thinking"}]},{"type":"message","content":[{"type":"output_text","text":"1 2 3 4"}]}]}`, "1 2 3 4"},
		{"redacted credentials", "application/json", `{"object":"response","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"1 upstream-secret Bearer echoed-token 2"}]}]}`, "1 [REDACTED] Bearer [REDACTED] 2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				fmt.Fprint(w, tc.body)
			}))
			defer up.Close()
			result, err := NewUpstreams(up.Client(), 0).QuickTest(context.Background(), Route{BaseURL: up.URL, Model: "m", APIKey: "upstream-secret"})
			if err != nil {
				t.Fatal(err)
			}
			if got := quickTestMetrics(t, result)["text"]; got != tc.want {
				t.Fatalf("actual text preview=%v want %q", got, tc.want)
			}
		})
	}
}

func TestQuickTestPreviewIsBoundedWithoutBreakingUnicode(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "response", "status": "completed", "output": []any{map[string]any{"type": "output_text", "text": "upstream-secret " + strings.Repeat("数", 5000)}}})
	}))
	defer up.Close()
	result, err := NewUpstreams(up.Client(), 0).QuickTest(context.Background(), Route{BaseURL: up.URL, Model: "m", APIKey: "upstream-secret"})
	if err != nil {
		t.Fatal(err)
	}
	metrics := quickTestMetrics(t, result)
	preview, _ := metrics["text"].(string)
	if !strings.HasPrefix(preview, "[REDACTED] ") || utf8.RuneCountInString(preview) != 4096 || !utf8.ValidString(preview) || metrics["textPreviewTruncated"] != true {
		t.Fatalf("unsafe or unbounded preview: chars=%d truncated=%v", utf8.RuneCountInString(preview), metrics["textPreviewTruncated"])
	}
}

func TestQuickTestRequestsContinuousCountingSample(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		input, _ := json.Marshal(body["input"])
		if !strings.Contains(string(input), "120") || !strings.Contains(string(input), "integers") {
			t.Errorf("probe must request a continuous counting sample, got %s", input)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"object":"response","status":"completed","output":[{"type":"output_text","text":"1 2 3"}]}`)
	}))
	defer up.Close()
	_, err := NewUpstreams(up.Client(), 0).QuickTest(context.Background(), Route{BaseURL: up.URL, Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
}
