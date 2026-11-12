package adroute

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// okStream is the smallest SSE body the failover envelope accepts as success.
const okStream = "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n"

func TestCooldownTableWindowExpiry(t *testing.T) {
	now := time.Unix(1000, 0)
	table := newCooldownTable(60*time.Second, func() time.Time { return now })

	if table.cooling("m1") {
		t.Fatal("unknown member reported cooling")
	}
	table.failed("m1")
	if !table.cooling("m1") {
		t.Fatal("failed member not cooling")
	}

	now = now.Add(59 * time.Second)
	if !table.cooling("m1") {
		t.Fatal("cooldown expired early")
	}
	now = now.Add(2 * time.Second)
	if table.cooling("m1") {
		t.Fatal("cooldown did not expire")
	}
	if _, found := table.until["m1"]; found {
		t.Fatal("expired entry not dropped")
	}
}

func TestCooldownTableServedClearsState(t *testing.T) {
	table := newCooldownTable(time.Minute, nil)
	table.failed("m1")
	table.served("m1")
	if table.cooling("m1") {
		t.Fatal("served member still cooling")
	}
}

func TestCooldownTableDisabledWhenWindowIsZero(t *testing.T) {
	table := newCooldownTable(0, nil)
	table.failed("m1")
	if table.cooling("m1") {
		t.Fatal("zero window must never cool")
	}
}

func TestOrderByCooldownMovesFailedMembersBehindHealthyOnes(t *testing.T) {
	now := time.Unix(2000, 0)
	table := newCooldownTable(time.Minute, func() time.Time { return now })
	chain := []Route{{ID: "a"}, {ID: "b"}, {ID: "c"}}

	// A healthy head keeps the configured order exactly.
	if got := orderByCooldown(table, chain); len(got) != 3 || got[0].ID != "a" || got[1].ID != "b" || got[2].ID != "c" {
		t.Fatalf("healthy chain reordered: %#v", got)
	}

	// A failed head moves back; the rest keep their relative order.
	table.failed("a")
	got := orderByCooldown(table, chain)
	if len(got) != 3 || got[0].ID != "b" || got[1].ID != "c" || got[2].ID != "a" {
		t.Fatalf("cooled head not demoted: %#v", got)
	}
	if len(chain) != 3 || chain[0].ID != "a" {
		t.Fatalf("input chain mutated: %#v", chain)
	}

	// When everything is cooling the chain is returned untouched: health
	// memory may reorder a chain, never shrink it.
	table.failed("b")
	table.failed("c")
	if got := orderByCooldown(table, chain); len(got) != 3 || got[0].ID != "a" || got[1].ID != "b" || got[2].ID != "c" {
		t.Fatalf("fully cooling chain altered: %#v", got)
	}
}

func TestMemberUnhealthySeparatesHealthFromRequestErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"header timeout", appError("等待上游响应超时。", 504, "UPSTREAM_TIMEOUT"), true},
		{"unreachable", appError("invalid upstream URL", 502, "UPSTREAM_UNREACHABLE"), true},
		{"retryable status", &upstreamError{Status: 503, Text: "上游返回 HTTP 503。"}, true},
		{"rate limited", &upstreamError{Status: 429, Text: "上游返回 HTTP 429。"}, true},
		{"client cancelled", appError("客户端已取消请求。", 499, "CLIENT_CANCELLED"), false},
		{"deterministic 400", &upstreamError{Status: 400, Text: "上游返回 HTTP 400。"}, false},
		{"adapter missing", appError("此路由引用的适配包不存在；未静默恢复透传。", 409, "ADAPTER_NOT_FOUND"), false},
		{"bad upstream address", appError("invalid upstream address", 400, "INVALID_BASE_URL"), false},
	}
	for _, tc := range cases {
		if got := memberUnhealthy(tc.err); got != tc.want {
			t.Errorf("%s: memberUnhealthy=%v want %v", tc.name, got, tc.want)
		}
	}
}

func TestNewHTTPAppDefaultsHeaderTimeoutTo90Seconds(t *testing.T) {
	app := mustHTTPApp(HTTPOptions{})
	if app.opts.HeaderTimeout != 90*time.Second {
		t.Fatalf("default header timeout %v", app.opts.HeaderTimeout)
	}
	if app.opts.IdleTimeout != 5*time.Minute {
		t.Fatalf("default idle timeout %v", app.opts.IdleTimeout)
	}
}

// aggregateProxyChain wires two saved entries into a chain and returns the app
// plus both model ids, so a test can point each member at its own upstream.
func aggregateProxyChain(t *testing.T) (*Store, *HTTPApp, string, string, string) {
	t.Helper()
	s, app, gid, mid1, mid2 := aggregateFixture(t, HTTPOptions{})
	mustAggregate(t, s, AggregateInput{Name: "chain", Members: []string{mid1, mid2}})
	return s, app, gid, mid1, mid2
}

func TestAggregateProxySkipsCoolingMemberOnNextRequest(t *testing.T) {
	var firstCalls, secondCalls int32
	first := countingUpstream(t, &firstCalls, 503, `{"error":"down"}`)
	defer first.Close()
	second := countingUpstream(t, &secondCalls, 200, okStream)
	defer second.Close()

	s, app, gid, mid1, mid2 := aggregateProxyChain(t)
	pointModel(t, s, gid, mid1, "first", "first-model", first.URL+"/v1")
	pointModel(t, s, gid, mid2, "second", "second-model", second.URL+"/v1")

	// First request pays the failed head once.
	if w := proxyCall(t, app, "adroute/agg/chain", map[string]any{"stream": true}, nil); w.Code != 200 {
		t.Fatalf("first request status %d body=%s", w.Code, w.Body.String())
	}
	if atomic.LoadInt32(&firstCalls) != 1 || atomic.LoadInt32(&secondCalls) != 1 {
		t.Fatalf("first request calls first=%d second=%d, want 1/1", firstCalls, secondCalls)
	}

	// The second request must not pay the same member's failure again.
	if w := proxyCall(t, app, "adroute/agg/chain", map[string]any{"stream": true}, nil); w.Code != 200 {
		t.Fatalf("second request status %d body=%s", w.Code, w.Body.String())
	}
	if atomic.LoadInt32(&firstCalls) != 1 || atomic.LoadInt32(&secondCalls) != 2 {
		t.Fatalf("second request calls first=%d second=%d, want 1/2", firstCalls, secondCalls)
	}
}

func TestAggregateProxySuccessClearsCooldown(t *testing.T) {
	var firstCalls, secondCalls int32
	first := countingUpstream(t, &firstCalls, 503, `{"error":"down"}`)
	defer first.Close()
	second := countingUpstream(t, &secondCalls, 200, okStream)
	defer second.Close()

	s, app, gid, mid1, mid2 := aggregateProxyChain(t)
	pointModel(t, s, gid, mid1, "first", "first-model", first.URL+"/v1")
	pointModel(t, s, gid, mid2, "second", "second-model", second.URL+"/v1")

	// Drive the cooldown window with a controllable clock instead of sleeping.
	clock := time.Unix(5000, 0)
	app.cooldowns.now = func() time.Time { return clock }

	// First request cools the head and lands on the second member.
	if w := proxyCall(t, app, "adroute/agg/chain", map[string]any{"stream": true}, nil); w.Code != 200 {
		t.Fatalf("first request status %d body=%s", w.Code, w.Body.String())
	}
	if !app.cooldowns.cooling(mid1) {
		t.Fatal("failed head not cooling")
	}
	// The head now answers; once its cooldown lapses it must be preferred again
	// and its success must clear the record.
	ok := countingUpstream(t, &firstCalls, 200, okStream)
	defer ok.Close()
	pointModel(t, s, gid, mid1, "first", "first-model", ok.URL+"/v1")
	clock = clock.Add(failoverCooldown + time.Second)

	if w := proxyCall(t, app, "adroute/agg/chain", map[string]any{"stream": true}, nil); w.Code != 200 {
		t.Fatalf("second request status %d body=%s", w.Code, w.Body.String())
	}
	if app.cooldowns.cooling(mid1) {
		t.Fatal("successful head still cooling")
	}
	if atomic.LoadInt32(&secondCalls) != 1 {
		t.Fatalf("healthy head not preferred: second=%d", secondCalls)
	}
}

func TestAggregateProxyKeepsTryingWhenEveryMemberIsCooling(t *testing.T) {
	var calls int32
	up := countingUpstream(t, &calls, 200, okStream)
	defer up.Close()

	s, app, gid, mid1, mid2 := aggregateProxyChain(t)
	pointModel(t, s, gid, mid1, "first", "first-model", up.URL+"/v1")
	pointModel(t, s, gid, mid2, "second", "second-model", up.URL+"/v1")
	app.cooldowns.failed(mid1)
	app.cooldowns.failed(mid2)

	if w := proxyCall(t, app, "adroute/agg/chain", map[string]any{"stream": true}, nil); w.Code != 200 {
		t.Fatalf("all-cooling chain status %d body=%s", w.Code, w.Body.String())
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("attempts %d, want 1", calls)
	}
}

func TestAggregateProxyClientCancellationDoesNotCoolMember(t *testing.T) {
	// A client that hangs up mid-request must not demote its own primary
	// member: the failure says nothing about the upstream's health.
	var calls int32
	release := make(chan struct{})
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		atomic.AddInt32(&calls, 1)
		<-release
		return nil, r.Context().Err()
	})

	s, app, gid, mid1, mid2 := aggregateFixture(t, HTTPOptions{Client: &http.Client{Transport: transport}})
	pointModel(t, s, gid, mid1, "first", "first-model", "https://local.test/v1")
	pointModel(t, s, gid, mid2, "second", "second-model", "https://local.test/v1")
	mustAggregate(t, s, AggregateInput{Name: "chain", Members: []string{mid1, mid2}})

	b, _ := json.Marshal(map[string]any{"model": "adroute/agg/chain", "stream": true})
	req := httptest.NewRequest("POST", "http://127.0.0.1:3001/v1/responses", bytes.NewReader(b))
	req.Host = "127.0.0.1:3001"
	req.Header.Set("Authorization", "Bearer "+s.TokenPair().ProxyToken)
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		app.ServeHTTP(httptest.NewRecorder(), req)
	}()
	// Wait for the upstream attempt to start, then cancel like an Esc press.
	for atomic.LoadInt32(&calls) == 0 {
		time.Sleep(time.Millisecond)
	}
	cancel()
	close(release)
	<-done

	if app.cooldowns.cooling(mid1) {
		t.Fatal("client cancellation must not cool the member")
	}
}

func TestAggregateProxyDeterministicErrorDoesNotCoolMember(t *testing.T) {
	var firstCalls, secondCalls int32
	first := countingUpstream(t, &firstCalls, 400, `{"error":{"message":"bad request"}}`)
	defer first.Close()
	second := countingUpstream(t, &secondCalls, 200, okStream)
	defer second.Close()

	s, app, gid, mid1, mid2 := aggregateProxyChain(t)
	pointModel(t, s, gid, mid1, "first", "first-model", first.URL+"/v1")
	pointModel(t, s, gid, mid2, "second", "second-model", second.URL+"/v1")

	if w := proxyCall(t, app, "adroute/agg/chain", map[string]any{"stream": true}, nil); w.Code != 400 {
		t.Fatalf("status %d body=%s", w.Code, w.Body.String())
	}
	if app.cooldowns.cooling(mid1) {
		t.Fatal("request-shaped 400 must not cool the member")
	}
	// The next request still starts at the configured head.
	proxyCall(t, app, "adroute/agg/chain", map[string]any{"stream": true}, nil)
	if atomic.LoadInt32(&firstCalls) != 2 {
		t.Fatalf("head not retried after a deterministic error: first=%d", firstCalls)
	}
}

func TestAggregateProxyBrokenStreamAfterCommitDoesNotCoolMember(t *testing.T) {
	var firstCalls int32
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&firstCalls, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		// Cut the connection after the header stage committed. The client has
		// already received bytes, so the chain must not retry and must not blame
		// the member: it proved it was reachable.
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			return
		}
		conn, _, err := hijacker.Hijack()
		if err == nil {
			_ = conn.Close()
		}
	}))
	defer first.Close()
	var secondCalls int32
	second := countingUpstream(t, &secondCalls, 200, okStream)
	defer second.Close()

	s, app, gid, mid1, mid2 := aggregateProxyChain(t)
	pointModel(t, s, gid, mid1, "first", "first-model", first.URL+"/v1")
	pointModel(t, s, gid, mid2, "second", "second-model", second.URL+"/v1")

	proxyCall(t, app, "adroute/agg/chain", map[string]any{"stream": true}, nil)
	if app.cooldowns.cooling(mid1) {
		t.Fatal("mid-stream break after commit must not cool the member")
	}
	if atomic.LoadInt32(&secondCalls) != 0 {
		t.Fatalf("committed stream retried on another member: second=%d", secondCalls)
	}
}

func TestAggregateExhaustionStillReportsLastFailureWhenCooling(t *testing.T) {
	var calls int32
	down := countingUpstream(t, &calls, 503, `{"error":"down"}`)
	defer down.Close()

	s, app, gid, mid1, mid2 := aggregateProxyChain(t)
	pointModel(t, s, gid, mid1, "first", "first-model", down.URL+"/v1")
	pointModel(t, s, gid, mid2, "second", "second-model", down.URL+"/v1")

	w := proxyCall(t, app, "adroute/agg/chain", map[string]any{"stream": true}, nil)
	if w.Code != 503 {
		t.Fatalf("status %d body=%s", w.Code, w.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	errObj, _ := payload["error"].(map[string]any)
	if errObj["code"] != "AGGREGATE_EXHAUSTED" {
		t.Fatalf("code %v", errObj["code"])
	}
	if !app.cooldowns.cooling(mid1) || !app.cooldowns.cooling(mid2) {
		t.Fatal("failed members not cooled after exhaustion")
	}
}

func TestCooldownDoesNotAffectSingleMemberRoute(t *testing.T) {
	var calls int32
	up := countingUpstream(t, &calls, 200, okStream)
	defer up.Close()

	s, app, gid, mid1, _ := aggregateFixture(t, HTTPOptions{})
	pointModel(t, s, gid, mid1, "first", "first-model", up.URL+"/v1")
	app.cooldowns.failed(mid1)

	if w := proxyCall(t, app, "adroute/work/first", map[string]any{"stream": true}, nil); w.Code != 200 {
		t.Fatalf("single member route status %d body=%s", w.Code, w.Body.String())
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("attempts %d, want 1", calls)
	}
	if app.cooldowns.cooling(mid1) {
		t.Fatal("successful single-member call did not clear cooldown")
	}
}

// Ensure the error classification keeps working through wrapping, which is how
// proxyCause hands failures back to the chain.
func TestMemberUnhealthySeesWrappedAppError(t *testing.T) {
	wrapped := errors.Join(appError("等待上游响应超时。", 504, "UPSTREAM_TIMEOUT"))
	if !memberUnhealthy(wrapped) {
		t.Fatal("wrapped timeout not detected")
	}
}
