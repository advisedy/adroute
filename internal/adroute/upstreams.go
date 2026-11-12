package adroute

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Quick test parameters. They are fixed product constants, not user settings:
// one short text request, a bounded output, and one absolute deadline.
const (
	QuickTestMaxOutputTokens = 1024
	QuickTestTimeout         = 18 * time.Second
	StructureTestTimeout     = 60 * time.Second
	quickTestPrompt          = "Output the integers from 1 through 120 in ascending order, separated by single spaces. Output only the numbers, with no heading, explanation or code block."
	quickTestInstructions    = "Follow the counting request directly in plain text. Do not call tools."
)

type Upstreams struct {
	Client      *http.Client
	ServicePort int
}

type UpstreamResult struct {
	Response *http.Response
	Secrets  []string
}

func NewUpstreams(client *http.Client, port int) *Upstreams {
	if client == nil {
		client = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	return &Upstreams{Client: client, ServicePort: port}
}

func (u *Upstreams) ValidateTarget(raw string) error {
	v, e := url.Parse(raw)
	if e != nil {
		return appError("invalid upstream address", 400, "INVALID_BASE_URL")
	}
	host := strings.ToLower(v.Hostname())
	p := v.Port()
	if p == "" {
		if v.Scheme == "https" {
			p = "443"
		} else {
			p = "80"
		}
	}
	if (host == "localhost" || host == "127.0.0.1" || host == "::1") && p == fmt.Sprint(u.ServicePort) && u.ServicePort != 0 {
		return appError("upstream cannot point to this service", 400, "PROXY_LOOP")
	}
	return nil
}

// Respond forwards an incoming Responses request to the model entry's own
// upstream connection.
func (u *Upstreams) Respond(ctx context.Context, p Route, body map[string]any, requestHeaders http.Header) (UpstreamResult, error) {
	normalized, e := NormalizeBaseURL(p.BaseURL)
	if e != nil {
		return UpstreamResult{}, e
	}
	if e = u.ValidateTarget(normalized); e != nil {
		return UpstreamResult{}, e
	}
	copyBody := make(map[string]any, len(body)+1)
	for k, v := range body {
		copyBody[k] = v
	}
	copyBody["model"] = p.Model
	raw, e := json.Marshal(copyBody)
	if e != nil {
		return UpstreamResult{}, appError("invalid request body", 400, "INVALID_REQUEST")
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, normalized+"/responses", bytes.NewReader(raw))
	if e != nil {
		return UpstreamResult{}, appError("invalid upstream URL", 502, "UPSTREAM_UNREACHABLE")
	}
	req.Header.Set("Authorization", "Bearer "+p.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	for _, name := range []string{"OpenAI-Beta", "Originator", "Session-Id", "Thread-Id", "X-Codex-Turn-State", "X-Client-Request-Id", "User-Agent"} {
		if v := requestHeaders.Get(name); v != "" {
			req.Header.Set(name, v)
		}
	}
	resp, e := u.Client.Do(req)
	if e != nil {
		return UpstreamResult{}, e
	}
	return UpstreamResult{Response: resp, Secrets: []string{p.APIKey}}, nil
}

// Discover lists models from an unsaved base URL/credential. It never persists
// anything and never infers a selection.
func (u *Upstreams) Discover(ctx context.Context, rawBaseURL, apiKey string, timeout time.Duration) (map[string]any, error) {
	base, e := NormalizeBaseURL(rawBaseURL)
	if e != nil {
		return nil, e
	}
	if !validSecret(strings.TrimSpace(apiKey)) {
		return nil, appError("获取模型需要上游 API Key。", 400, "UPSTREAM_KEY_INVALID")
	}
	if e = u.ValidateTarget(base); e != nil {
		return nil, e
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	req.Header.Set("Accept", "application/json")
	resp, e := u.Client.Do(req)
	if e != nil {
		if ctx.Err() != nil {
			return nil, appError("获取模型超时。", 504, "UPSTREAM_TIMEOUT")
		}
		return nil, appError("无法连接上游。", 502, "UPSTREAM_UNREACHABLE")
	}
	defer resp.Body.Close()
	b, e := io.ReadAll(io.LimitReader(resp.Body, 1<<20+1))
	if e != nil || len(b) > 1<<20 {
		return nil, appError("upstream response too large", 502, "UPSTREAM_BODY_TOO_LARGE")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, appError("获取模型失败，可手动填写真实模型 ID。", 502, "MODELS_UNAVAILABLE")
	}
	var data struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal(b, &data) != nil {
		return nil, appError("上游没有返回 JSON 模型列表。", 502, "INVALID_MODEL_LIST")
	}
	seen := map[string]bool{}
	models := []map[string]string{}
	for _, m := range data.Data {
		if modelIDRE.MatchString(m.ID) && !seen[m.ID] {
			seen[m.ID] = true
			models = append(models, map[string]string{"id": m.ID})
		}
		if len(models) >= 2000 {
			break
		}
	}
	sort.Slice(models, func(i, j int) bool { return models[i]["id"] < models[j]["id"] })
	return map[string]any{"models": models, "warning": "模型列表不代表每个模型都支持 Responses 或工具调用；请按需测速。"}, nil
}

// QuickTestResult is the outcome of one short text inference request.
type QuickTestResult struct {
	Outcome                string   `json:"outcome"`
	Status                 int      `json:"status"`
	DurationMs             int64    `json:"durationMs"`
	FirstTextMs            *int64   `json:"firstTextMs,omitempty"`
	TextReturned           bool     `json:"textReturned"`
	Text                   string   `json:"text,omitempty"`
	TextPreviewTruncated   bool     `json:"textPreviewTruncated,omitempty"`
	UsageReported          bool     `json:"usageReported"`
	OutputTokens           *int     `json:"outputTokens,omitempty"`
	ReasoningTokens        *int     `json:"reasoningTokens,omitempty"`
	TextTokens             *int     `json:"textTokens,omitempty"`
	TextTokensSource       string   `json:"textTokensSource,omitempty"`
	TextDurationMs         *int64   `json:"textDurationMs,omitempty"`
	TextTokensPerSecond    *float64 `json:"textTokensPerSecond,omitempty"`
	SpeedUnavailableReason string   `json:"speedUnavailableReason,omitempty"`
	// TokensPerSecond is the legacy end-to-end rate; never use it as text speed.
	TokensPerSecond  *float64 `json:"tokensPerSecond,omitempty"`
	Truncated        bool     `json:"truncated"`
	IncompleteReason string   `json:"incompleteReason,omitempty"`
	Message          string   `json:"message"`
}

// QuickTest sends exactly one short text request to the entry's own upstream
// connection and reports text availability, first-text latency and a sampled
// text-output rate using separated upstream usage or an explicitly labelled
// local reference-tokenizer estimate of just the visible text.
// The old end-to-end rate remains available for API compatibility only.
func (u *Upstreams) QuickTest(ctx context.Context, p Route) (QuickTestResult, error) {
	if p.BaseURL == "" {
		return QuickTestResult{}, appError("模型缺少上游地址。", 400, "INVALID_BASE_URL")
	}
	if e := u.ValidateTarget(p.BaseURL); e != nil {
		return QuickTestResult{}, e
	}
	started := time.Now()
	ctx, cancel := context.WithTimeout(ctx, QuickTestTimeout)
	defer cancel()
	body := map[string]any{
		"model":             p.Model,
		"input":             []any{map[string]any{"role": "user", "content": quickTestPrompt}},
		"instructions":      quickTestInstructions,
		"store":             false,
		"stream":            true,
		"max_output_tokens": QuickTestMaxOutputTokens,
	}
	raw, _ := json.Marshal(body)
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(p.BaseURL, "/")+"/responses", bytes.NewReader(raw))
	if e != nil {
		return QuickTestResult{}, appError("上游地址无效。", 400, "INVALID_BASE_URL")
	}
	req.Header.Set("Authorization", "Bearer "+p.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream, application/json")
	resp, e := u.Client.Do(req)
	if e != nil {
		return QuickTestResult{}, testTransportError(ctx)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return QuickTestResult{}, appError(fmt.Sprintf("上游返回 HTTP %d，本次测速未通过。", resp.StatusCode), 502, "RESPONSES_TEST_FAILED")
	}

	var (
		text                string
		tokens              *int
		terminal            map[string]any
		firstTextMs         *int64
		firstTextAt         time.Time
		lastTextAt          time.Time
		textChunks          int
		streamNonTextOutput bool
	)
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		var delta strings.Builder
		err := readSSEStream(io.LimitReader(resp.Body, 8<<20), func(ev map[string]any) (bool, error) {
			typ := str(ev, "type")
			switch typ {
			case "response.output_text.delta":
				fragment := str(ev, "delta")
				if strings.TrimSpace(fragment) != "" {
					now := time.Now()
					if firstTextMs == nil {
						firstTextAt = now
						ms := now.Sub(started).Milliseconds()
						firstTextMs = &ms
					}
					lastTextAt = now
					textChunks++
				}
				delta.WriteString(fragment)
			case "response.output_item.added", "response.output_item.done":
				if item := nested(ev, "item"); item != nil && hasNonTextOutput(map[string]any{"output": []any{item}}) {
					streamNonTextOutput = true
				}
			case "response.content_part.added", "response.content_part.done":
				if part := nested(ev, "part"); part != nil {
					switch str(part, "type") {
					case "output_text", "reasoning_text", "summary_text", "reasoning_summary_text":
					default:
						streamNonTextOutput = true
					}
				}
			case "response.completed", "response.incomplete":
				res := nested(ev, "response")
				expected := strings.TrimPrefix(str(ev, "type"), "response.")
				if res == nil || str(res, "status") != expected {
					return false, appError("上游结束事件的状态无效或不一致。", 502, "INVALID_RESPONSES_STREAM")
				}
				terminal = ev
				return true, nil
			case "response.failed", "error":
				return false, appError("上游返回错误事件，本次测速未通过。", 502, "RESPONSES_TEST_FAILED")
			default:
				// A reduced terminal snapshot may omit streamed tools/audio. Do
				// not treat their usage as text, even if no output item survived.
				if strings.HasPrefix(typ, "response.") && typ != "response.created" && typ != "response.in_progress" && typ != "response.queued" && !strings.HasPrefix(typ, "response.output_text.") && !strings.HasPrefix(typ, "response.reasoning") {
					streamNonTextOutput = true
				}
			}
			return false, nil
		})
		if err != nil {
			if app, ok := err.(*AppError); ok {
				return QuickTestResult{}, app
			}
			return QuickTestResult{}, testTransportError(ctx)
		}
		if terminal == nil {
			return QuickTestResult{}, appError("上游流没有返回 Responses 结束事件。", 502, "INVALID_RESPONSES_STREAM")
		}
		res := nested(terminal, "response")
		if res == nil {
			return QuickTestResult{}, appError("上游结束事件缺少响应内容。", 502, "INVALID_RESPONSES_RESPONSE")
		}
		if str(terminal, "type") == "response.failed" || str(res, "status") == "failed" {
			return QuickTestResult{}, appError("上游表示本次请求失败。", 502, "RESPONSES_TEST_FAILED")
		}
		text = responseText(res)
		if strings.TrimSpace(text) == "" {
			text = delta.String()
		}
		tokens = usageOutputTokens(res)
	} else {
		var res map[string]any
		// A top-level JSON object is self-delimiting. Decode it incrementally so
		// a completed response does not wait for a misbehaving chunked body EOF.
		err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&res)
		if err != nil || res == nil {
			if ctx.Err() != nil {
				return QuickTestResult{}, testTransportError(ctx)
			}
			return QuickTestResult{}, appError("上游没有返回可解析的 Responses 响应。", 502, "INVALID_RESPONSES_RESPONSE")
		}
		if str(res, "object") != "response" {
			return QuickTestResult{}, appError("上游返回的不是 Responses 响应。", 502, "INVALID_RESPONSES_RESPONSE")
		}
		if str(res, "status") == "failed" {
			return QuickTestResult{}, appError("上游表示本次请求失败。", 502, "RESPONSES_TEST_FAILED")
		}
		if status := str(res, "status"); status != "completed" && status != "incomplete" {
			return QuickTestResult{}, appError("上游没有返回有效的 Responses 结束状态。", 502, "INVALID_RESPONSES_RESPONSE")
		}
		text = responseText(res)
		tokens = usageOutputTokens(res)
		terminal = map[string]any{"response": res}
	}
	elapsed := time.Since(started)
	res := nested(terminal, "response")
	nonTextOutput := streamNonTextOutput || hasNonTextOutput(res)
	status := str(res, "status")
	// Reasons are protocol labels, never free-form upstream error text. Unknown
	// values may echo credentials and are deliberately not returned to callers.
	incompleteReason := str(nested(res, "incomplete_details"), "reason")
	if incompleteReason != "max_output_tokens" && incompleteReason != "content_filter" {
		incompleteReason = ""
	}
	truncated := status == "incomplete" && incompleteReason == "max_output_tokens"
	result := QuickTestResult{Status: resp.StatusCode, DurationMs: elapsed.Milliseconds(), TextReturned: strings.TrimSpace(text) != "", Truncated: truncated, IncompleteReason: incompleteReason}
	// This fixed-prompt reply is shown only in the management page's in-memory
	// details. Redact before bounding it, so a truncated secret cannot escape.
	preview := []rune(Redact(text, []string{p.APIKey}))
	if len(preview) > 4096 {
		preview = preview[:4096]
		result.TextPreviewTruncated = true
	}
	result.Text = string(preview)
	// Only actual text deltas establish first-text latency. Headers, lifecycle
	// events and a final JSON/snapshot cannot reveal when text began streaming.
	result.FirstTextMs = firstTextMs
	if tokens != nil {
		result.UsageReported = true
		result.OutputTokens = tokens
		result.ReasoningTokens = usageReasoningTokens(res, *tokens)
		if result.ReasoningTokens != nil && !nonTextOutput {
			textTokens := *tokens - *result.ReasoningTokens
			result.TextTokens = &textTokens
			result.TextTokensSource = "usage"
		}
		// Keep this explicitly end-to-end. Total output usage can include hidden
		// reasoning and the first SSE chunk can contain many tokens, so dividing
		// it by time after first text would not yield a trustworthy decode rate.
		if seconds := elapsed.Seconds(); seconds > 0 && math.IsInf(seconds, 0) == false {
			rate := float64(*tokens) / seconds
			if !math.IsNaN(rate) && !math.IsInf(rate, 0) && rate >= 0 {
				result.TokensPerSecond = &rate
			}
		}
	}
	// Some relays return total usage but omit reasoning details. Counting just
	// the visible reply with one explicitly labelled reference tokenizer lets
	// us estimate delivery speed without counting hidden thinking as text.
	if result.TextReturned && result.TextTokens == nil && !nonTextOutput && textChunks >= 2 {
		if reference := quickTestReferenceTokens(text); reference != nil {
			result.TextTokens = reference
			result.TextTokensSource = "cl100k_base-estimate"
		}
	}
	if result.TextReturned {
		switch {
		case result.TextTokens == nil && tokens == nil:
			result.SpeedUnavailableReason = "usage-unavailable"
		case result.TextTokens == nil && result.ReasoningTokens == nil:
			result.SpeedUnavailableReason = "reasoning-usage-unavailable"
		case nonTextOutput:
			result.SpeedUnavailableReason = "non-text-output"
		case textChunks < 2:
			result.SpeedUnavailableReason = "no-stream-timing"
		default:
			span := lastTextAt.Sub(firstTextAt)
			ms := span.Milliseconds()
			result.TextDurationMs = &ms
			// Short/buffered samples exaggerate speed, especially since the first
			// chunk can contain several tokens. This remains a sampled reference,
			// not an exact per-token decode benchmark.
			if *result.TextTokens < 32 || span < 50*time.Millisecond {
				result.SpeedUnavailableReason = "sample-too-short"
			} else {
				rate := float64(*result.TextTokens) / span.Seconds()
				result.TextTokensPerSecond = &rate
			}
		}
	}
	switch {
	case status == "incomplete" && result.TextReturned:
		result.Outcome = "truncated"
		result.Message = "已返回文本，但达到本次测试的输出上限或上游提前结束。"
	case result.TextReturned && (tokens != nil || result.TextTokensPerSecond != nil):
		result.Outcome = "ok"
		result.Message = "可用。"
	case result.TextReturned:
		result.Outcome = "no-usage"
		result.Message = "已返回文本，但上游未返回可信用量，速度不可统计。"
	default:
		result.Outcome = "no-text"
		switch {
		case truncated && tokens != nil && result.ReasoningTokens != nil && *tokens == *result.ReasoningTokens:
			result.Message = "本次输出额度全部用于推理，尚未返回正文；不代表模型不可用。"
		case truncated:
			result.Message = "本次输出额度已耗尽，尚未返回正文；不代表模型不可用。"
		case incompleteReason == "content_filter":
			result.Message = "上游因内容过滤提前结束，未返回正文。"
		default:
			result.Message = "上游没有返回文本内容，本次测速不算文本可用。"
		}
	}
	return result, nil
}

// ToolStructureTest keeps the existing advanced probe: it verifies the shape of
// one tool call, without executing the tool and without claiming a live
// round trip.
func (u *Upstreams) ToolStructureTest(ctx context.Context, p Route) (map[string]any, error) {
	started := time.Now()
	ctx, cancel := context.WithTimeout(ctx, StructureTestTimeout)
	defer cancel()
	body := map[string]any{"input": []any{map[string]any{"role": "user", "content": "Call adroute_probe exactly once with marker adroute-test. Do not answer with text."}}, "tools": []any{map[string]any{"type": "function", "name": "adroute_probe", "description": "Transport probe only. Never execute.", "parameters": map[string]any{"type": "object", "properties": map[string]any{"marker": map[string]any{"type": "string", "enum": []string{"adroute-test"}}}, "required": []string{"marker"}, "additionalProperties": false}}}, "tool_choice": map[string]any{"type": "function", "name": "adroute_probe"}, "store": false, "stream": true, "model": p.Model}
	if e := u.ValidateTarget(p.BaseURL); e != nil {
		return nil, e
	}
	b, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(p.BaseURL, "/")+"/responses", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+p.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, e := u.Client.Do(req)
	if e != nil {
		return nil, testTransportError(ctx)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return nil, appError("Responses 测试失败。", 502, "RESPONSES_TEST_FAILED")
	}
	if !strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		return nil, appError("工具结构测试需要 SSE 响应。", 502, "TOOL_STRUCTURE_TEST_FAILED")
	}
	toolCount := 0
	var call map[string]any
	completed := false
	var failure error
	err := readSSEStream(io.LimitReader(resp.Body, 4<<20), func(ev map[string]any) (bool, error) {
		switch str(ev, "type") {
		case "response.failed", "response.incomplete", "error":
			return false, appError("上游生成失败或未完成。", 502, "RESPONSES_TEST_FAILED")
		case "response.output_item.done":
			item := nested(ev, "item")
			if str(item, "type") == "function_call" {
				toolCount++
				call = item
			}
		case "response.completed":
			res := nested(ev, "response")
			if str(res, "status") != "completed" {
				return false, appError("上游生成失败或未完成。", 502, "RESPONSES_TEST_FAILED")
			}
			out := arr(res["output"])
			if len(out) != 1 || toolCount != 1 {
				return false, appError("工具流结构测试未通过。", 502, "TOOL_STRUCTURE_TEST_FAILED")
			}
			final, _ := out[0].(map[string]any)
			var args map[string]any
			_ = json.Unmarshal([]byte(fmt.Sprint(call["arguments"])), &args)
			if call["name"] != "adroute_probe" || call["call_id"] == nil || final["type"] != "function_call" || final["call_id"] != call["call_id"] || final["name"] != call["name"] || final["arguments"] != call["arguments"] || args["marker"] != "adroute-test" {
				return false, appError("工具流结构测试未通过。", 502, "TOOL_STRUCTURE_TEST_FAILED")
			}
			completed = true
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		if app, ok := err.(*AppError); ok {
			failure = app
		} else {
			failure = testTransportError(ctx)
		}
	}
	if failure != nil {
		return nil, failure
	}
	if !completed {
		return nil, appError("上游流未返回 Responses 完成事件。", 502, "INVALID_RESPONSES_STREAM")
	}
	return map[string]any{"ok": true, "status": resp.StatusCode, "durationMs": time.Since(started).Milliseconds(), "testKind": "tool-structure", "toolExecuted": false, "liveRoundTripVerified": false}, nil
}

func testTransportError(ctx context.Context) error {
	switch ctx.Err() {
	case context.DeadlineExceeded:
		return appError("本次测速超时。", 504, "UPSTREAM_TIMEOUT")
	case context.Canceled:
		return appError("测速已取消。", 499, "TEST_CANCELLED")
	}
	if cause := context.Cause(ctx); cause != nil {
		var app *AppError
		if errors.As(cause, &app) {
			return app
		}
		if cause == context.Canceled {
			return appError("测速已取消。", 499, "TEST_CANCELLED")
		}
	}
	return appError("无法连接上游或读取响应。", 502, "UPSTREAM_UNREACHABLE")
}

// readSSEStream splits an SSE stream into JSON events. UTF-8 payloads, line
// endings, and event boundaries may be split across arbitrary network chunks,
// so it buffers by line and dispatches on a blank line or EOF. A verified
// terminal event ends parsing immediately; an open HTTP body is not more work.
func readSSEStream(r io.Reader, handle func(map[string]any) (bool, error)) error {
	br := bufio.NewReaderSize(r, 32*1024)
	var data []string
	flush := func() (bool, error) {
		if len(data) == 0 {
			return false, nil
		}
		payload := strings.Join(data, "\n")
		data = nil
		var ev map[string]any
		if json.Unmarshal([]byte(payload), &ev) != nil || ev == nil {
			return false, nil
		}
		return handle(ev)
	}
	for {
		line, err := br.ReadString('\n')
		if len(line) > 0 {
			trimmed := strings.TrimRight(line, "\r\n")
			switch {
			case trimmed == "":
				if done, e := flush(); done || e != nil {
					return e
				}
			case strings.HasPrefix(trimmed, "data:"):
				value := strings.TrimPrefix(trimmed, "data:")
				value = strings.TrimPrefix(value, " ")
				data = append(data, value)
			case strings.HasPrefix(trimmed, ":"):
				// Comment / keepalive.
			}
		}
		if err != nil {
			if err == io.EOF {
				_, e := flush()
				return e
			}
			return err
		}
	}
}

// responseText collects assistant text from a Responses snapshot, skipping
// reasoning and tool-call items.
func responseText(res map[string]any) string {
	var b strings.Builder
	for _, item := range arr(res["output"]) {
		m, _ := item.(map[string]any)
		if m == nil {
			continue
		}
		switch str(m, "type") {
		case "message":
			for _, part := range arr(m["content"]) {
				pm, _ := part.(map[string]any)
				if str(pm, "type") == "output_text" {
					b.WriteString(str(pm, "text"))
				}
			}
		case "output_text":
			b.WriteString(str(m, "text"))
		}
	}
	return b.String()
}

// Subtracting reasoning yields text usage only for text/reasoning responses.
// Tool arguments, audio and other output must not be billed as text tokens.
func hasNonTextOutput(res map[string]any) bool {
	for _, raw := range arr(res["output"]) {
		item, _ := raw.(map[string]any)
		switch str(item, "type") {
		case "reasoning", "output_text":
		case "message":
			for _, raw := range arr(item["content"]) {
				part, _ := raw.(map[string]any)
				if str(part, "type") != "output_text" {
					return true
				}
			}
		default:
			return true
		}
	}
	return false
}

// usageOutputTokens reads usage.output_tokens exactly once from the terminal
// snapshot. Negative, non-numeric and zero values are treated as unreported.
func usageOutputTokens(res map[string]any) *int {
	usage := nested(res, "usage")
	if usage == nil {
		return nil
	}
	var raw any
	raw, ok := usage["output_tokens"]
	if !ok {
		return nil
	}
	var value float64
	switch v := raw.(type) {
	case float64:
		value = v
	case json.Number:
		f, err := v.Float64()
		if err != nil {
			return nil
		}
		value = f
	default:
		return nil
	}
	if math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 || value != math.Trunc(value) || value > QuickTestMaxOutputTokens {
		return nil
	}
	n := int(value)
	return &n
}

func usageReasoningTokens(res map[string]any, outputTokens int) *int {
	details := nested(nested(res, "usage"), "output_tokens_details")
	var value float64
	switch v := details["reasoning_tokens"].(type) {
	case float64:
		value = v
	case json.Number:
		f, err := v.Float64()
		if err != nil {
			return nil
		}
		value = f
	default:
		return nil
	}
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value != math.Trunc(value) || value > float64(outputTokens) {
		return nil
	}
	n := int(value)
	return &n
}

var modelIDRE = regexp.MustCompile(`^[^\x00-\x20\x7f]{1,256}$`)
