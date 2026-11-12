package adroute

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// failoverBudget bounds the whole header stage of an aggregate request. It is a
// product constant, not a user setting: members keep their per-try timeouts,
// and this cap only stops a bad chain from stacking them. It is sized so the
// largest allowed chain still gets one full pass: MaxAggregateSize members times
// the default header timeout, with headroom for the retry pause.
const failoverBudget = 13 * time.Minute

// aggregateMaxAttempts caps the candidate list so one request stays bounded
// even if a chain's limits are later loosened. Current worst case is 8 members
// times 10 rounds.
const aggregateMaxAttempts = 80

func (a *HTTPApp) proxy(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/responses" || r.Method != http.MethodPost {
		a.fail(w, appError("支持 POST /v1/responses 和 GET /v1/models。", 404, "UNSUPPORTED_ENDPOINT"))
		return
	}
	// Capture routing and installed adapters before body reads can block on upload.
	snapshot := a.opts.Store.Snapshot()
	body, err := decodeBody(r, 32<<20)
	if err != nil {
		a.fail(w, err)
		return
	}
	var payload map[string]any
	if json.Unmarshal(body, &payload) != nil || payload == nil {
		a.fail(w, appError("JSON 请求格式错误。", 400, "INVALID_JSON"))
		return
	}
	modelValue, exists := payload["model"]
	model := "adroute/default"
	if exists {
		var ok bool
		model, ok = modelValue.(string)
		if !ok {
			a.fail(w, appError("invalid model", 400, "INVALID_MODEL"))
			return
		}
	}
	if model == "" {
		a.fail(w, appError("invalid model", 400, "INVALID_MODEL"))
		return
	}
	chain, err := ResolveChain(snapshot, model)
	if err != nil {
		a.fail(w, err)
		return
	}
	a.forwardResponse(w, r, chain, model, payload)
}

// upstreamError records one failed header-stage attempt. Status keeps the
// upstream code when there was one so the final error mirrors what a direct
// call would have returned.
type upstreamError struct {
	Status int
	Text   string
}

func (e *upstreamError) Error() string { return e.Text }

// forwardResponse walks the candidate chain until one upstream accepts the
// request. Once a response is committed, a broken stream is never retried:
// clients already received bytes, so replaying elsewhere would corrupt output.
func (a *HTTPApp) forwardResponse(w http.ResponseWriter, r *http.Request, chain []Route, routeID string, payload map[string]any) {
	if len(chain) > aggregateMaxAttempts {
		chain = chain[:aggregateMaxAttempts]
	}
	// Members that failed within the cooldown window move to the back of the
	// chain for this request only. The configured order still decides everything
	// when every member is healthy, so the steady state keeps hitting the first
	// member and its prompt cache.
	chain = orderByCooldown(a.cooldowns, chain)
	if len(chain) == 1 {
		// A lone candidate skips the failover envelope entirely: errors reach
		// the client exactly as the upstream produced them.
		_, _ = a.tryCandidate(w, r, chain[0], payload, routeID, nil)
		return
	}
	ctx, cancel := context.WithDeadline(r.Context(), time.Now().Add(failoverBudget))
	defer cancel()
	var lastFailure error
	for attempt, route := range chain {
		if attempt > 0 {
			if sleep, e := a.attemptGate(ctx, attempt); e != nil {
				lastFailure = e
				break
			} else if sleep > 0 {
				timer := time.NewTimer(sleep)
				select {
				case <-ctx.Done():
					timer.Stop()
				case <-timer.C:
				}
			}
		}
		committed, _ := a.tryCandidate(w, r.WithContext(ctx), route, payload, routeID, &lastFailure)
		if committed {
			return
		}
	}
	if lastFailure == nil {
		lastFailure = appError("聚合的所有成员都不可用。", 503, "AGGREGATE_EMPTY")
	}
	status := 502
	if upstream, ok := lastFailure.(*upstreamError); ok {
		status = upstream.Status
	} else if app, ok := lastFailure.(*AppError); ok {
		status = app.Status
	}
	detail := safeProxyMessage(lastFailure)
	if upstream, ok := lastFailure.(*upstreamError); ok {
		detail = upstream.Text
	}
	message := fmt.Sprintf("聚合的所有成员都不可用。最后一次失败：%s", detail)
	a.respondProxyError(w, appError(message, status, "AGGREGATE_EXHAUSTED"), a.opts.Store.Secrets())
}

// attemptGate throttles retries so a fully broken chain cannot spin. Attempts
// 1-3 are immediate; after that a short pause caps the pace of upstream churn.
func (a *HTTPApp) attemptGate(ctx context.Context, attempt int) (time.Duration, error) {
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return 0, appError("聚合尝试已超出总时长上限。", 504, "AGGREGATE_TIMEOUT")
		}
		return 0, proxyCause(ctx, err)
	}
	if cause := context.Cause(ctx); cause != nil {
		return 0, proxyCause(ctx, cause)
	}
	if attempt >= 3 {
		return 250 * time.Millisecond, nil
	}
	return 0, nil
}

// tryCandidate performs one upstream attempt. It reports committed=true the
// moment response headers reach the client; from then on nothing may retry.
// Failures are forwarded through sink so the caller can shape the final error.
func (a *HTTPApp) tryCandidate(w http.ResponseWriter, r *http.Request, route Route, payload map[string]any, routeID string, sink *error) (bool, error) {
	started := time.Now()
	status := 502
	detail, code := "", ""
	fail := func(err error) (bool, error) {
		// Health memory is an aggregate concept: a lone model route keeps
		// behaving exactly as before, with no extra state recorded.
		if sink != nil && memberUnhealthy(err) {
			a.cooldowns.failed(route.ID)
		}
		if sink != nil {
			*sink = err
		}
		return false, err
	}
	secrets := append(a.opts.Store.Secrets(), route.APIKey)
	var diagnosis *DiagnosticCapture
	if a.diagnostics != nil {
		diagnosis = a.diagnostics.Claim(route, secrets)
	}
	adapter, err := a.adapterFor(route, payload)
	if err != nil {
		status = ErrorStatus(err)
		code = proxyErrorCode(err)
		detail = Redact(safeProxyMessage(err), secrets)
		a.recordProxy(route, routeID, payload, started, status, detail, nil)
		if diagnosis != nil {
			_ = diagnosis.Finish(status, code)
		}
		if sink == nil {
			a.respondProxyError(w, err, secrets)
		}
		return fail(err)
	}
	defer func() {
		if diagnosis != nil {
			_ = diagnosis.Finish(status, code)
		}
		a.recordProxy(route, routeID, payload, started, status, detail, adapter)
	}()

	ctx, cancel := context.WithCancelCause(r.Context())
	defer cancel(nil)
	upstreams := NewUpstreams(a.opts.Client, a.opts.Port)
	setTimeout := func(msg, c string) { cancel(appError(msg, 504, c)) }
	headerTimer := time.AfterFunc(a.opts.HeaderTimeout, func() { setTimeout("等待上游响应超时。", "UPSTREAM_TIMEOUT") })
	result, err := upstreams.Respond(ctx, route, prepareAdapterRequest(payload, adapter), r.Header)
	if err != nil {
		headerTimer.Stop()
		err = proxyCause(ctx, err)
		status = ErrorStatus(err)
		code = proxyErrorCode(err)
		detail = Redact(safeProxyMessage(err), secrets)
		if sink == nil {
			a.respondProxyError(w, err, secrets)
		}
		return fail(err)
	}
	secrets = append(secrets, result.Secrets...)
	headerTimer.Stop()
	if cause := context.Cause(ctx); cause != nil {
		_ = result.Response.Body.Close()
		err = proxyCause(ctx, cause)
		status = ErrorStatus(err)
		code = proxyErrorCode(err)
		detail = Redact(safeProxyMessage(err), secrets)
		if sink == nil {
			a.respondProxyError(w, err, secrets)
		}
		return fail(err)
	}
	resp := result.Response
	defer resp.Body.Close()
	status = resp.StatusCode
	// Only a 2xx header proves the member healthy. A retryable status still
	// reaches fail() below and keeps the cooldown it just earned.
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		a.cooldowns.served(route.ID)
	}
	if diagnosis != nil {
		diagnosis.Headers(status)
	}

	var idleMu sync.Mutex
	idleTimer := time.AfterFunc(a.opts.IdleTimeout, func() { setTimeout("上游流长时间没有输出。", "UPSTREAM_IDLE_TIMEOUT") })
	defer idleTimer.Stop()
	resetIdle := func() { idleMu.Lock(); idleTimer.Stop(); idleTimer.Reset(a.opts.IdleTimeout); idleMu.Unlock() }
	input := &observedReadCloser{ReadCloser: resp.Body, onBytes: func(b []byte) {
		resetIdle()
		if diagnosis != nil {
			diagnosis.ObserveSSE(b)
		}
	}}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, e := readBounded(input, 65536)
		if e != nil {
			err = proxyCause(ctx, e)
			status = ErrorStatus(err)
			code = proxyErrorCode(err)
			detail = Redact(safeProxyMessage(err), secrets)
			if sink == nil {
				a.respondProxyError(w, err, secrets)
			}
			return fail(err)
		}
		text := Redact(string(raw), secrets)
		detail = historySafeDetail(text, payload)
		if len(text) > 4096 {
			text = text[:4096]
		}
		if len(detail) > 4096 {
			detail = detail[:4096]
		}
		// Deterministic request errors retrying cannot fix, and a single
		// candidate has nothing to retry with: both report the upstream
		// response to the client unchanged.
		if sink == nil || !retryableStatus(resp.StatusCode) {
			copyProxyHeaders(w.Header(), resp.Header)
			w.Header().Del("Content-Type")
			if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "application/json") {
				var value any
				if json.Unmarshal([]byte(text), &value) == nil {
					w.Header().Set("Content-Type", "application/json; charset=utf-8")
				} else {
					w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				}
			} else {
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			}
			w.WriteHeader(resp.StatusCode)
			if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "application/json") {
				var value any
				if json.Unmarshal([]byte(text), &value) == nil {
					_ = json.NewEncoder(w).Encode(value)
				} else {
					_, _ = io.WriteString(w, text)
				}
			} else {
				if text == "" {
					text = "上游返回 HTTP " + http.StatusText(resp.StatusCode)
				}
				_, _ = io.WriteString(w, text)
			}
			return true, nil
		}
		return fail(&upstreamError{Status: resp.StatusCode, Text: fmt.Sprintf("上游返回 HTTP %d。", resp.StatusCode)})
	}
	if adapter != nil && !strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		err = appError("此适配包需要 SSE 响应，但上游返回了其他内容。", 502, "ADAPTER_SSE_REQUIRED")
		status = 502
		code = proxyErrorCode(err)
		detail = err.Error()
		if sink == nil {
			a.respondProxyError(w, err, secrets)
		}
		return fail(err)
	}
	copyProxyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	writer := flushWriter{w: w}
	if adapter != nil {
		err = RepairStream(*adapter, input, writer, RepairStreamOptions{Wait: 30 * time.Second, ResponseWait: a.opts.IdleTimeout})
	} else {
		_, err = io.Copy(writer, input)
	}
	if err != nil {
		err = proxyCause(ctx, err)
		if ErrorStatus(err) >= 400 {
			status = ErrorStatus(err)
		}
		code = proxyErrorCode(err)
		detail = Redact(safeProxyMessage(err), secrets)
	}
	return true, nil
}

// retryableStatus separates failures that plausibly heal on the next member
// from request errors every member would answer identically.
func retryableStatus(status int) bool {
	if status == 429 || status == 401 || status == 403 || status == 404 || status == 408 {
		return true
	}
	return status >= 500
}

// adapterFor resolves the bound repair package and applies the stream gate the
// single-model path already enforced.
func (a *HTTPApp) adapterFor(route Route, payload map[string]any) (*AdapterPackage, error) {
	if route.Profile == "" || route.Profile == "none" {
		return nil, nil
	}
	var adapter *AdapterPackage
	for _, raw := range a.opts.Store.Snapshot().Adapters {
		var p AdapterPackage
		if json.Unmarshal(raw, &p) == nil && PackageRef(p) == route.Profile {
			adapter = &p
			break
		}
	}
	if adapter == nil {
		return nil, appError("此路由引用的适配包不存在；未静默恢复透传。", 409, "ADAPTER_NOT_FOUND")
	}
	if stream, _ := payload["stream"].(bool); !stream {
		return nil, appError("此适配包仅支持流式 Responses 请求，请启用 stream。", 400, "ADAPTER_STREAM_REQUIRED")
	}
	return adapter, nil
}

// prepareAdapterRequest leaves the client's payload unchanged. The optional
// rewrite is limited to the exact known incompatible instruction phrase.
func prepareAdapterRequest(payload map[string]any, adapter *AdapterPackage) map[string]any {
	if adapter == nil || !adapter.Options.NeutralizeCursorInstructions {
		return payload
	}
	instructions, ok := payload["instructions"].(string)
	if !ok || !strings.Contains(instructions, "You operate in Cursor.") {
		return payload
	}
	copyPayload := make(map[string]any, len(payload))
	for k, v := range payload {
		copyPayload[k] = v
	}
	copyPayload["instructions"] = strings.ReplaceAll(instructions, "You operate in Cursor.", "You operate in an editor.")
	return copyPayload
}

type observedReadCloser struct {
	io.ReadCloser
	onBytes func([]byte)
}

func (o *observedReadCloser) Read(p []byte) (int, error) {
	n, e := o.ReadCloser.Read(p)
	if n > 0 && o.onBytes != nil {
		o.onBytes(p[:n])
	}
	return n, e
}

type flushWriter struct{ w http.ResponseWriter }

func (f flushWriter) Write(b []byte) (int, error) {
	n, e := f.w.Write(b)
	if e == nil {
		if flush, ok := f.w.(http.Flusher); ok {
			flush.Flush()
		}
	}
	return n, e
}

func copyProxyHeaders(dst, src http.Header) {
	for k, values := range src {
		lk := strings.ToLower(k)
		if lk == "content-type" || lk == "retry-after" || lk == "request-id" || lk == "x-request-id" || lk == "openai-model" || lk == "openai-processing-ms" || strings.HasPrefix(lk, "x-ratelimit-") || strings.HasPrefix(lk, "x-codex-") {
			for _, v := range values {
				dst.Add(k, v)
			}
		}
	}
	dst.Set("Cache-Control", "no-store")
}
func readBounded(r io.Reader, limit int64) ([]byte, error) {
	var b bytes.Buffer
	buf := make([]byte, 32<<10)
	for {
		n, e := r.Read(buf)
		if n > 0 {
			if int64(b.Len()+n) > limit {
				return nil, appError("上游响应过大，已停止读取。", 502, "UPSTREAM_BODY_TOO_LARGE")
			}
			_, _ = b.Write(buf[:n])
		}
		if e == io.EOF {
			return b.Bytes(), nil
		}
		if e != nil {
			return nil, e
		}
	}
}
func proxyCause(ctx context.Context, err error) error {
	if cause := context.Cause(ctx); cause != nil {
		if errors.Is(cause, context.Canceled) {
			return appError("客户端已取消请求。", 499, "CLIENT_CANCELLED")
		}
		return cause
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return appError("等待上游响应超时。", 504, "UPSTREAM_TIMEOUT")
	}
	return err
}
func safeProxyMessage(err error) string {
	var app *AppError
	if errors.As(err, &app) {
		return app.Message
	}
	return "上游流中断或响应读取失败。"
}
func proxyErrorCode(err error) string {
	var app *AppError
	if errors.As(err, &app) {
		return app.Code
	}
	return "UPSTREAM_STREAM_ERROR"
}
func (a *HTTPApp) respondProxyError(w http.ResponseWriter, e error, secrets []string) {
	var app *AppError
	if !errors.As(e, &app) {
		e = appError("上游流中断或响应读取失败。", 502, "UPSTREAM_STREAM_ERROR")
	}
	a.json(w, ErrorStatus(e), ErrorPayload(e, secrets))
}
func (a *HTTPApp) recordProxy(route Route, routeID string, payload map[string]any, started time.Time, status int, detail string, adapter *AdapterPackage) {
	secrets := a.opts.Store.Secrets()
	effort := ""
	if r, ok := payload["reasoning"].(map[string]any); ok {
		effort, _ = r["effort"].(string)
	}
	item := map[string]any{"time": time.Now().UTC().Format(time.RFC3339Nano), "modelId": route.ID, "routeId": Redact(route.RouteID, secrets), "groupName": Redact(route.GroupName, secrets), "model": Redact(route.Model, secrets), "modelName": Redact(route.ModelName, secrets), "effort": Redact(effort, secrets), "status": status, "durationMs": time.Since(started).Milliseconds()}
	if routeID != route.RouteID {
		item["aggregateRouteId"] = Redact(routeID, secrets)
	}
	if adapter != nil {
		item["adapter"] = PackageRef(*adapter)
	}
	if detail != "" {
		item["detail"] = historySafeDetail(Redact(detail, secrets), payload)
	}
	a.requestMu.Lock()
	a.requests = append([]map[string]any{item}, a.requests...)
	if len(a.requests) > 100 {
		a.requests = a.requests[:100]
	}
	a.requestMu.Unlock()
	if a.opts.Log != nil {
		copy := make(map[string]any, len(item))
		for k, v := range item {
			copy[k] = v
		}
		a.opts.Log(copy)
	}
}
func historySafeDetail(detail string, payload map[string]any) string {
	values := []string{}
	for _, key := range []string{"input", "instructions"} {
		collectPromptText(payload[key], &values)
	}
	for _, value := range values {
		if len(value) > 0 {
			detail = strings.ReplaceAll(detail, value, "[PROMPT REDACTED]")
		}
	}
	return detail
}

func collectPromptText(value any, out *[]string) {
	switch x := value.(type) {
	case string:
		if len(x) > 0 {
			*out = append(*out, x)
		}
	case []any:
		for _, item := range x {
			collectPromptText(item, out)
		}
	case map[string]any:
		for _, item := range x {
			collectPromptText(item, out)
		}
	}
}
