package adroute

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// aggregateFixture builds two saved model entries in group "work" plus one
// failover chain over them, so tests can flip upstream behavior per member.
func aggregateFixture(t *testing.T, options HTTPOptions) (*Store, *HTTPApp, string, string, string) {
	t.Helper()
	s, e := OpenStore(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	gid, e := s.CreateGroup(GroupInput{Name: "work"})
	if e != nil {
		t.Fatal(e)
	}
	first, e := s.CreateModel(ModelInput{GroupID: gid, Name: "first", Model: "first-model", BaseURL: testUpstreamURL, APIKey: "first-secret"})
	if e != nil {
		t.Fatal(e)
	}
	second, e := s.CreateModel(ModelInput{GroupID: gid, Name: "second", Model: "second-model", BaseURL: testUpstreamURL, APIKey: "second-secret"})
	if e != nil {
		t.Fatal(e)
	}
	options.Store = s
	if options.Port == 0 {
		options.Port = 3001
	}
	app := mustHTTPApp(options)
	return s, app, gid, first, second
}

func mustAggregate(t *testing.T, s *Store, in AggregateInput) string {
	t.Helper()
	id, err := s.CreateAggregate(in)
	if err != nil {
		t.Fatalf("create aggregate %#v: %v", in, err)
	}
	return id
}

func countingUpstream(t *testing.T, calls *int32, status int, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(calls, 1)
		if status >= 200 && status < 300 {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
			return
		}
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
}

func pointModel(t *testing.T, s *Store, gid, id, name, model, base string) {
	t.Helper()
	if e := s.UpdateModel(id, ModelInput{GroupID: gid, Name: name, Model: model, BaseURL: base, APIKey: name + "-secret"}); e != nil {
		t.Fatal(e)
	}
}

func TestAggregateCreateUpdateDeleteAndValidation(t *testing.T) {
	s := newTestStore(t)
	gid := mustGroup(t, s, "work")
	first := mustModel(t, s, ModelInput{GroupID: gid, Name: "a", Model: "m", BaseURL: "https://up.example/v1", APIKey: "synthetic"})
	second := mustModel(t, s, ModelInput{GroupID: gid, Name: "b", Model: "m", BaseURL: "https://up.example/v1", APIKey: "synthetic"})

	if _, err := s.CreateAggregate(AggregateInput{Name: "bad name!", Members: []string{first}}); ErrorStatus(err) != 400 {
		t.Fatalf("invalid name status=%v", ErrorStatus(err))
	}
	if _, err := s.CreateAggregate(AggregateInput{Name: "chain", Members: []string{}}); ErrorStatus(err) != 400 {
		t.Fatalf("empty members status=%v", ErrorStatus(err))
	}
	if _, err := s.CreateAggregate(AggregateInput{Name: "chain", Members: []string{first, first}}); ErrorStatus(err) != 400 {
		t.Fatalf("duplicate member status=%v", ErrorStatus(err))
	}
	if _, err := s.CreateAggregate(AggregateInput{Name: "chain", Members: []string{first, "missing-id"}}); ErrorStatus(err) != 404 {
		t.Fatalf("unknown member status=%v", ErrorStatus(err))
	}
	if _, err := s.CreateAggregate(AggregateInput{Name: "chain", Members: []string{first}, MaxRounds: 99}); ErrorStatus(err) != 400 {
		t.Fatalf("bad rounds status=%v", ErrorStatus(err))
	}

	agg := mustAggregate(t, s, AggregateInput{Name: "chain", Members: []string{first, second}})
	if _, err := s.CreateAggregate(AggregateInput{Name: "chain", Members: []string{first}}); ErrorStatus(err) != 409 {
		t.Fatalf("duplicate name status=%v", ErrorStatus(err))
	}
	if err := s.UpdateAggregate(agg, AggregateInput{Name: "chain-v2", Members: []string{second, first}, MaxRounds: 3}); err != nil {
		t.Fatalf("update: %v", err)
	}
	got := s.Snapshot().Aggregates[0]
	if got.Name != "chain-v2" || got.MaxRounds != 3 || len(got.Members) != 2 || got.Members[0] != second {
		t.Fatalf("updated aggregate %#v", got)
	}
	if err := s.DeleteAggregate(agg); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(s.Snapshot().Aggregates) != 0 {
		t.Fatal("aggregate not deleted")
	}
}

func TestAggregateModelDeletionIsBlockedWhileReferenced(t *testing.T) {
	s := newTestStore(t)
	gid := mustGroup(t, s, "work")
	first := mustModel(t, s, ModelInput{GroupID: gid, Name: "a", Model: "m", BaseURL: "https://up.example/v1", APIKey: "synthetic"})
	second := mustModel(t, s, ModelInput{GroupID: gid, Name: "b", Model: "m", BaseURL: "https://up.example/v1", APIKey: "synthetic"})
	mustAggregate(t, s, AggregateInput{Name: "chain", Members: []string{first, second}})
	if err := s.DeleteModel(first); ErrorStatus(err) != 409 {
		t.Fatalf("referenced delete status=%v err=%v", ErrorStatus(err), err)
	}
	if err := s.DeleteModel(second); ErrorStatus(err) != 409 {
		t.Fatalf("referenced delete status=%v err=%v", ErrorStatus(err), err)
	}
	if err := s.DeleteAggregate(s.Snapshot().Aggregates[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteModel(first); err != nil {
		t.Fatalf("unreferenced delete: %v", err)
	}
}

func TestAggregateResolveChainExpandsRounds(t *testing.T) {
	s := newTestStore(t)
	gid := mustGroup(t, s, "work")
	first := mustModel(t, s, ModelInput{GroupID: gid, Name: "a", Model: "m", BaseURL: "https://up.example/v1", APIKey: "synthetic"})
	second := mustModel(t, s, ModelInput{GroupID: gid, Name: "b", Model: "m", BaseURL: "https://up.example/v1", APIKey: "synthetic"})
	mustAggregate(t, s, AggregateInput{Name: "chain", Members: []string{first, second}, MaxRounds: 3})

	chain, err := ResolveChain(s.Snapshot(), "adroute/agg/chain")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(chain) != 6 {
		t.Fatalf("chain length %d, want 6", len(chain))
	}
	for i, route := range chain {
		want := first
		if i%2 == 1 {
			want = second
		}
		if route.ID != want {
			t.Fatalf("chain[%d]=%s want %s", i, route.ID, want)
		}
	}
}

func TestAggregateProxyFailsOverInOrder(t *testing.T) {
	var firstCalls, secondCalls int32
	first := countingUpstream(t, &firstCalls, 503, `{"error":"down"}`)
	defer first.Close()
	second := countingUpstream(t, &secondCalls, 200, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
	defer second.Close()

	s, app, gid, mid1, mid2 := aggregateFixture(t, HTTPOptions{})
	pointModel(t, s, gid, mid1, "first", "first-model", first.URL+"/v1")
	pointModel(t, s, gid, mid2, "second", "second-model", second.URL+"/v1")
	mustAggregate(t, s, AggregateInput{Name: "chain", Members: []string{mid1, mid2}})

	w := proxyCall(t, app, "adroute/agg/chain", map[string]any{"stream": true}, nil)
	if w.Code != 200 {
		t.Fatalf("status %d body=%s", w.Code, w.Body.String())
	}
	if atomic.LoadInt32(&firstCalls) != 1 || atomic.LoadInt32(&secondCalls) != 1 {
		t.Fatalf("calls first=%d second=%d, want 1/1", firstCalls, secondCalls)
	}
}

func TestAggregateProxyDeterministicErrorDoesNotRetry(t *testing.T) {
	var firstCalls, secondCalls int32
	first := countingUpstream(t, &firstCalls, 400, `{"error":{"message":"bad request"}}`)
	defer first.Close()
	second := countingUpstream(t, &secondCalls, 200, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
	defer second.Close()

	s, app, gid, mid1, mid2 := aggregateFixture(t, HTTPOptions{})
	pointModel(t, s, gid, mid1, "first", "first-model", first.URL+"/v1")
	pointModel(t, s, gid, mid2, "second", "second-model", second.URL+"/v1")
	mustAggregate(t, s, AggregateInput{Name: "chain", Members: []string{mid1, mid2}})

	w := proxyCall(t, app, "adroute/agg/chain", map[string]any{"stream": true}, nil)
	if w.Code != 400 {
		t.Fatalf("status %d body=%s", w.Code, w.Body.String())
	}
	if atomic.LoadInt32(&secondCalls) != 0 {
		t.Fatalf("deterministic error retried: second=%d", secondCalls)
	}
}

func TestAggregateProxyExhaustionReportsFailure(t *testing.T) {
	var calls int32
	down := countingUpstream(t, &calls, 503, `{"error":"down"}`)
	defer down.Close()

	s, app, gid, mid1, mid2 := aggregateFixture(t, HTTPOptions{})
	pointModel(t, s, gid, mid1, "first", "first-model", down.URL+"/v1")
	pointModel(t, s, gid, mid2, "second", "second-model", down.URL+"/v1")
	mustAggregate(t, s, AggregateInput{Name: "chain", Members: []string{mid1, mid2}, MaxRounds: 2})

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
	if atomic.LoadInt32(&calls) != 4 {
		t.Fatalf("attempts %d, want 4 (2 members x 2 rounds)", calls)
	}
}

func TestAggregateProxyWrapsBackToFirstMember(t *testing.T) {
	var firstCalls, secondCalls int32
	first := countingUpstream(t, &firstCalls, 503, `{"error":"down"}`)
	defer first.Close()
	second := countingUpstream(t, &secondCalls, 503, `{"error":"down"}`)
	defer second.Close()

	s, app, gid, mid1, mid2 := aggregateFixture(t, HTTPOptions{})
	pointModel(t, s, gid, mid1, "first", "first-model", first.URL+"/v1")
	pointModel(t, s, gid, mid2, "second", "second-model", second.URL+"/v1")
	mustAggregate(t, s, AggregateInput{Name: "chain", Members: []string{mid1, mid2}, MaxRounds: 3})

	w := proxyCall(t, app, "adroute/agg/chain", map[string]any{"stream": true}, nil)
	if w.Code != 503 {
		t.Fatalf("status %d body=%s", w.Code, w.Body.String())
	}
	if atomic.LoadInt32(&firstCalls) != 3 || atomic.LoadInt32(&secondCalls) != 3 {
		t.Fatalf("calls first=%d second=%d, want 3/3 across rounds", firstCalls, secondCalls)
	}
}

func TestAggregateProxySkipsDisabledMembers(t *testing.T) {
	var firstCalls, secondCalls int32
	first := countingUpstream(t, &firstCalls, 503, `{"error":"down"}`)
	defer first.Close()
	second := countingUpstream(t, &secondCalls, 200, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
	defer second.Close()

	s, app, gid, mid1, mid2 := aggregateFixture(t, HTTPOptions{})
	pointModel(t, s, gid, mid1, "first", "first-model", first.URL+"/v1")
	pointModel(t, s, gid, mid2, "second", "second-model", second.URL+"/v1")
	mustAggregate(t, s, AggregateInput{Name: "chain", Members: []string{mid1, mid2}})
	if err := s.SetModelEnabled(mid1, false); err != nil {
		t.Fatal(err)
	}

	w := proxyCall(t, app, "adroute/agg/chain", map[string]any{"stream": true}, nil)
	if w.Code != 200 {
		t.Fatalf("status %d body=%s", w.Code, w.Body.String())
	}
	if atomic.LoadInt32(&firstCalls) != 0 || atomic.LoadInt32(&secondCalls) != 1 {
		t.Fatalf("calls first=%d second=%d, want 0/1", firstCalls, secondCalls)
	}
}

func TestAggregateAsDefaultAndDisabledAggregate(t *testing.T) {
	var calls int32
	up := countingUpstream(t, &calls, 200, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
	defer up.Close()

	s, app, gid, mid1, mid2 := aggregateFixture(t, HTTPOptions{})
	_ = mid2
	pointModel(t, s, gid, mid1, "first", "first-model", up.URL+"/v1")
	agg := mustAggregate(t, s, AggregateInput{Name: "chain", Members: []string{mid1}})
	if err := s.SetDefault(agg); err != nil {
		t.Fatalf("set default: %v", err)
	}
	w := proxyCall(t, app, "adroute/default", map[string]any{"stream": true}, nil)
	if w.Code != 200 {
		t.Fatalf("default via aggregate status %d body=%s", w.Code, w.Body.String())
	}
	if err := s.SetAggregateEnabled(agg, false); err != nil {
		t.Fatal(err)
	}
	w = proxyCall(t, app, "adroute/agg/chain", map[string]any{"stream": true}, nil)
	if w.Code != 403 {
		t.Fatalf("disabled aggregate status %d", w.Code)
	}
}

func TestAggregateCatalogAndModelListExposeRoute(t *testing.T) {
	s := newTestStore(t)
	gid := mustGroup(t, s, "work")
	mid := mustModel(t, s, ModelInput{GroupID: gid, Name: "a", Model: "m", BaseURL: "https://up.example/v1", APIKey: "synthetic"})
	mustAggregate(t, s, AggregateInput{Name: "chain", Members: []string{mid}})
	a := mustHTTPApp(HTTPOptions{Store: s})

	w := adminReq(t, a, http.MethodGet, "/api/state", nil)
	state := stateOf(t, w)
	aggregates := state["aggregates"].([]any)
	if len(aggregates) != 1 || aggregates[0].(map[string]any)["routeId"] != "adroute/agg/chain" {
		t.Fatalf("state aggregates %#v", aggregates)
	}
	members := aggregates[0].(map[string]any)["members"].([]any)
	if members[0].(map[string]any)["routeId"] != "adroute/work/a" {
		t.Fatalf("member resolution %#v", members[0])
	}

	list := s.OpenAIModelList()
	data := list["data"].([]map[string]any)
	found := false
	for _, entry := range data {
		if entry["id"] == "adroute/agg/chain" {
			found = true
		}
	}
	if !found {
		t.Fatalf("aggregate not in model list %#v", data)
	}
}

func TestAggregateManagementEndpoints(t *testing.T) {
	s := newTestStore(t)
	a := mustHTTPApp(HTTPOptions{Store: s})
	w := adminReq(t, a, http.MethodPost, "/api/groups", map[string]string{"name": "work"})
	gid := stateOf(t, w)["groups"].([]any)[0].(map[string]any)["id"].(string)
	w = adminReq(t, a, http.MethodPost, "/api/models", map[string]any{"groupId": gid, "name": "m", "model": "x", "baseUrl": "https://up.example/v1", "apiKey": "k"})
	mid := stateOf(t, w)["models"].([]any)[0].(map[string]any)["id"].(string)

	w = adminReq(t, a, http.MethodPost, "/api/aggregates", map[string]any{"name": "chain", "members": []string{mid}, "maxRounds": 2})
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	state := stateOf(t, w)
	aggID := state["aggregates"].([]any)[0].(map[string]any)["id"].(string)

	w = adminReq(t, a, http.MethodPut, "/api/aggregates/"+aggID, map[string]any{"name": "chain-v2", "members": []string{mid}, "maxRounds": 1})
	if w.Code != 200 || stateOf(t, w)["aggregates"].([]any)[0].(map[string]any)["name"] != "chain-v2" {
		t.Fatalf("update: %d %s", w.Code, w.Body.String())
	}
	w = adminReq(t, a, http.MethodPut, "/api/aggregates/"+aggID+"/availability", map[string]any{"enabled": false})
	if w.Code != 200 || stateOf(t, w)["aggregates"].([]any)[0].(map[string]any)["disabled"] != true {
		t.Fatalf("disable: %d %s", w.Code, w.Body.String())
	}
	w = adminReq(t, a, http.MethodDelete, "/api/aggregates/"+aggID, nil)
	if w.Code != 200 {
		t.Fatalf("delete: %d", w.Code)
	}
	if got := stateOf(t, w)["aggregates"].([]any); len(got) != 0 {
		t.Fatalf("aggregates %#v", got)
	}
}

func TestAggregateProxyRecordsEachAttempt(t *testing.T) {
	var calls int32
	down := countingUpstream(t, &calls, 503, `{"error":"down"}`)
	defer down.Close()
	up := countingUpstream(t, &calls, 200, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
	defer up.Close()

	s, app, gid, mid1, mid2 := aggregateFixture(t, HTTPOptions{})
	pointModel(t, s, gid, mid1, "first", "first-model", down.URL+"/v1")
	pointModel(t, s, gid, mid2, "second", "second-model", up.URL+"/v1")
	mustAggregate(t, s, AggregateInput{Name: "chain", Members: []string{mid1, mid2}})

	w := proxyCall(t, app, "adroute/agg/chain", map[string]any{"stream": true}, nil)
	if w.Code != 200 {
		t.Fatalf("status %d body=%s", w.Code, w.Body.String())
	}
	requests := app.stateJSON()["requests"].([]map[string]any)
	if len(requests) != 2 {
		t.Fatalf("recorded attempts %#v", requests)
	}
	if requests[1]["status"] != 503 || requests[0]["status"] != 200 {
		t.Fatalf("attempt order %#v", requests)
	}
	if requests[0]["aggregateRouteId"] != "adroute/agg/chain" || requests[1]["aggregateRouteId"] != "adroute/agg/chain" {
		t.Fatalf("aggregate route label %#v", requests)
	}
}

func TestAggregateChainHonorsClientCancellation(t *testing.T) {
	// A hanging member must stop as soon as the client goes away; the chain
	// budget cannot help a caller that already left.
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	s, app, gid, mid1, mid2 := aggregateFixture(t, HTTPOptions{Client: &http.Client{Transport: transport}, HeaderTimeout: 30 * time.Second})
	pointModel(t, s, gid, mid1, "first", "first-model", "https://hang.example/v1")
	pointModel(t, s, gid, mid2, "second", "second-model", "https://hang.example/v1")
	mustAggregate(t, s, AggregateInput{Name: "chain", Members: []string{mid1, mid2}, MaxRounds: 1})

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(80*time.Millisecond))
	defer cancel()
	req := httptest.NewRequest("POST", "http://127.0.0.1:3001/v1/responses", strings.NewReader(`{"model":"adroute/agg/chain","stream":true}`))
	req = req.WithContext(ctx)
	req.Host = "127.0.0.1:3001"
	req.Header.Set("Authorization", "Bearer "+s.TokenPair().ProxyToken)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	app.ServeHTTP(w, req)
	if w.Code != 504 && w.Code != 499 {
		t.Fatalf("status %d body=%s", w.Code, w.Body.String())
	}
}
