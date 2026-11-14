package adroute

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

// Exposure is an external boundary, not an internal availability switch.
func TestExposureBlocksPublicAccessWithoutDisablingMembersOrDefault(t *testing.T) {
	var calls int32
	up := countingUpstream(t, &calls, 200, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
	defer up.Close()
	s, app, gid, mid, _ := aggregateFixture(t, HTTPOptions{})
	pointModel(t, s, gid, mid, "first", "first-model", up.URL+"/v1")
	agg := mustAggregate(t, s, AggregateInput{Name: "chain", Members: []string{mid}, MaxRounds: 1})
	if err := s.SetDefault(mid); err != nil {
		t.Fatal(err)
	}
	closeExposure := func(kind, id string, enabled bool) {
		t.Helper()
		w := adminReq(t, app, http.MethodPut, "/api/"+kind+"/"+id+"/exposure", map[string]bool{"enabled": enabled})
		if w.Code != 200 {
			t.Fatalf("exposure: %d %s", w.Code, w.Body)
		}
	}
	closeExposure("models", mid, false)
	if s.Snapshot().Models[0].Disabled || s.Snapshot().DefaultModelID != mid {
		t.Fatal("exposure changed internal availability/default")
	}
	if _, err := s.ModelRoute(mid); err != nil {
		t.Fatal("internal diagnostics lost access", err)
	}
	for _, route := range []string{"adroute/work/first", DefaultRouteID} {
		w := proxyCall(t, app, route, map[string]any{"stream": true}, nil)
		if w.Code != 403 {
			t.Fatalf("closed route %s: %d %s", route, w.Code, w.Body)
		}
		if _, err := s.Resolve(route); ErrorStatus(err) != 403 {
			t.Fatalf("resolver bypass: %s %v", route, err)
		}
	}
	if atomic.LoadInt32(&calls) != 0 {
		t.Fatal("closed route reached upstream")
	}
	list, _ := json.Marshal(s.OpenAIModelList())
	catalog, err := os.ReadFile(s.Paths().Catalog)
	if err != nil {
		t.Fatal(err)
	}
	for _, data := range [][]byte{list, catalog} {
		if strings.Contains(string(data), "adroute/work/first") || strings.Contains(string(data), DefaultRouteID) || !strings.Contains(string(data), "adroute/agg/chain") {
			t.Fatalf("exposure filtering: %s", data)
		}
	}
	w := proxyCall(t, app, "adroute/agg/chain", map[string]any{"stream": true}, nil)
	if w.Code != 200 || atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("private member unavailable through public aggregate: %d %s", w.Code, w.Body)
	}
	if err := s.SetDefault(agg); err != nil {
		t.Fatal(err)
	}
	closeExposure("aggregates", agg, false)
	if s.Snapshot().Aggregates[0].Disabled || s.Snapshot().DefaultModelID != agg {
		t.Fatal("exposure changed aggregate availability/default")
	}
	for _, route := range []string{"adroute/agg/chain", DefaultRouteID} {
		if w := proxyCall(t, app, route, map[string]any{"stream": true}, nil); w.Code != 403 {
			t.Fatalf("aggregate bypass: %s %d", route, w.Code)
		}
	}
	closeExposure("models", mid, true)
	if w := proxyCall(t, app, "adroute/work/first", map[string]any{"stream": true}, nil); w.Code != 200 {
		t.Fatal("closing aggregate affected member", w.Code)
	}
	closeExposure("aggregates", agg, true)
	if w := proxyCall(t, app, DefaultRouteID, map[string]any{"stream": true}, nil); w.Code != 200 {
		t.Fatal("reopening did not restore access to retained default", w.Code)
	}
}

func TestExposurePersistsAcrossEditsCopyAndRestart(t *testing.T) {
	s, app, gid, mid := proxyFixture(t, HTTPOptions{})
	agg := mustAggregate(t, s, AggregateInput{Name: "chain", Members: []string{mid}})
	for _, path := range []string{"/api/models/" + mid, "/api/aggregates/" + agg} {
		if w := adminReq(t, app, http.MethodPut, path+"/exposure", map[string]bool{"enabled": false}); w.Code != 200 {
			t.Fatalf("close: %d %s", w.Code, w.Body)
		}
	}
	if err := s.UpdateModel(mid, ModelInput{GroupID: gid, Name: "edited", Model: "up", BaseURL: "https://example.com/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateAggregate(agg, AggregateInput{Name: "edited", Members: []string{mid}}); err != nil {
		t.Fatal(err)
	}
	copyID, err := s.CopyModel(mid)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(s.Paths().Home)
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{"adroute/work/edited", "adroute/agg/edited"} {
		if _, err := ResolveChain(reopened.Snapshot(), route); ErrorStatus(err) != 403 {
			t.Fatalf("restart reopened %s: %v", route, err)
		}
	}
	for _, m := range reopened.Snapshot().Models {
		if m.ID == copyID {
			if _, err := reopened.Resolve("adroute/work/" + m.Name); ErrorStatus(err) != 403 {
				t.Fatal("copy reopened exposure", err)
			}
		}
	}
}

func TestExposureEndpointStrictAndAdminOnly(t *testing.T) {
	s, app, _, mid := proxyFixture(t, HTTPOptions{})
	agg := mustAggregate(t, s, AggregateInput{Name: "chain", Members: []string{mid}})
	for _, path := range []string{"/api/models/" + mid + "/exposure", "/api/aggregates/" + agg + "/exposure"} {
		for _, body := range []string{`{}`, `{"enabled":null}`, `{"enabled":"false"}`, `{"enabled":false,"extra":1}`} {
			r := httptest.NewRequest(http.MethodPut, "http://127.0.0.1:3001"+path, strings.NewReader(body))
			r.Header.Set("Authorization", "Bearer "+s.TokenPair().AdminToken)
			w := httptest.NewRecorder()
			app.ServeHTTP(w, r)
			if w.Code != 400 {
				t.Fatalf("strict %s %s: %d", path, body, w.Code)
			}
		}
		r := httptest.NewRequest(http.MethodPut, "http://127.0.0.1:3001"+path, strings.NewReader(`{"enabled":false}`))
		r.Header.Set("Authorization", "Bearer "+s.TokenPair().ProxyToken)
		w := httptest.NewRecorder()
		app.ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatalf("proxy token modified exposure: %d", w.Code)
		}
	}
}

// The page-level switches are bulk operations: one atomic commit, no partial
// state, and models and aggregates never reach into each other.
func TestBulkExposureCommitsOnceAndSkipsDisabled(t *testing.T) {
	disabled := true
	s, app, gid, first, second := aggregateFixture(t, HTTPOptions{})
	disabledID, e := s.CreateModel(ModelInput{GroupID: gid, Name: "parked", Model: "parked-model", BaseURL: testUpstreamURL, APIKey: "parked-secret", Disabled: &disabled})
	if e != nil {
		t.Fatal(e)
	}
	agg := mustAggregate(t, s, AggregateInput{Name: "chain", Members: []string{first}, MaxRounds: 1})
	disabledAgg := mustAggregate(t, s, AggregateInput{Name: "parked-chain", Members: []string{second}, MaxRounds: 1, Disabled: &disabled})

	bulk := func(kind string, enabled bool) map[string]any {
		t.Helper()
		w := adminReq(t, app, http.MethodPut, "/api/exposure/"+kind, map[string]bool{"enabled": enabled})
		if w.Code != 200 {
			t.Fatalf("bulk %s=%v: %d %s", kind, enabled, w.Code, w.Body)
		}
		return stateOf(t, w)
	}
	exposureOf := func(state map[string]any, kind, id string) bool {
		t.Helper()
		for _, raw := range state[kind].([]any) {
			entry := raw.(map[string]any)
			if entry["id"] == id {
				return entry["exposureDisabled"].(bool)
			}
		}
		t.Fatalf("%s %s missing from state", kind, id)
		return false
	}
	revisionBefore := int64(s.Snapshot().Revision)
	after := bulk("models", false)
	if revision := int64(after["revision"].(float64)); revision != revisionBefore+1 {
		t.Fatalf("bulk close used %d revisions, want exactly 1", revision-revisionBefore)
	}
	for _, id := range []string{first, second} {
		if !exposureOf(after, "models", id) {
			t.Fatalf("model %s was not closed", id)
		}
	}
	if exposureOf(after, "models", disabledID) {
		t.Fatal("disabled model was given a hidden closed state")
	}
	// Aggregates are a different switch and stay untouched.
	if exposureOf(after, "aggregates", agg) || exposureOf(after, "aggregates", disabledAgg) {
		t.Fatal("closing models changed aggregate exposure")
	}

	// Repeating a no-op change must not write, bump the revision or republish.
	settled := int64(s.Snapshot().Revision)
	again := bulk("models", false)
	if revision := int64(again["revision"].(float64)); revision != settled {
		t.Fatalf("no-op bulk changed revision %d -> %d", settled, revision)
	}

	after = bulk("aggregates", false)
	if !exposureOf(after, "aggregates", agg) || exposureOf(after, "aggregates", disabledAgg) {
		t.Fatal("aggregate bulk switch touched the wrong entries")
	}
	if !exposureOf(after, "models", first) {
		t.Fatal("closing aggregates reopened models")
	}

	// Reopening skips disabled entries but restores everything else.
	after = bulk("models", true)
	if exposureOf(after, "models", first) || exposureOf(after, "models", second) {
		t.Fatal("models were not reopened")
	}
	if exposureOf(after, "models", disabledID) {
		t.Fatal("disabled model was silently exposed")
	}
	// A disabled entry stays disabled: bulk exposure is never an availability switch.
	for _, raw := range after["models"].([]any) {
		entry := raw.(map[string]any)
		if entry["id"] == disabledID && entry["disabled"] != true {
			t.Fatal("bulk exposure changed internal availability")
		}
	}
}

func TestBulkExposureEndpointsAreStrictAndAdminOnly(t *testing.T) {
	s, app, _, _ := proxyFixture(t, HTTPOptions{})
	for _, path := range []string{"/api/exposure/models", "/api/exposure/aggregates"} {
		for _, body := range []string{`{}`, `{"enabled":null}`, `{"enabled":"false"}`, `{"enabled":false,"extra":1}`, ``} {
			r := httptest.NewRequest(http.MethodPut, "http://127.0.0.1:3001"+path, strings.NewReader(body))
			r.Header.Set("Authorization", "Bearer "+s.TokenPair().AdminToken)
			w := httptest.NewRecorder()
			app.ServeHTTP(w, r)
			if w.Code != 400 {
				t.Fatalf("strict %s %s: %d %s", path, body, w.Code, w.Body)
			}
		}
		// Only the documented method is bound.
		if w := adminReq(t, app, http.MethodPost, path, map[string]bool{"enabled": false}); w.Code != 404 {
			t.Fatalf("wrong method on %s: %d", path, w.Code)
		}
		r := httptest.NewRequest(http.MethodPut, "http://127.0.0.1:3001"+path, strings.NewReader(`{"enabled":false}`))
		r.Header.Set("Authorization", "Bearer "+s.TokenPair().ProxyToken)
		recorder := httptest.NewRecorder()
		app.ServeHTTP(recorder, r)
		if recorder.Code != 401 {
			t.Fatalf("proxy token used bulk switch on %s: %d", path, recorder.Code)
		}
	}
}

// Closing every model must hide it from external clients while internal use and
// the default selection survive.
func TestBulkModelExposureHidesFromClientsButKeepsInternalUse(t *testing.T) {
	s, app, _, first, second := aggregateFixture(t, HTTPOptions{})
	if e := s.SetDefault(first); e != nil {
		t.Fatal(e)
	}
	mustAggregate(t, s, AggregateInput{Name: "chain", Members: []string{first}, MaxRounds: 1})
	if w := adminReq(t, app, http.MethodPut, "/api/exposure/models", map[string]bool{"enabled": false}); w.Code != 200 {
		t.Fatalf("bulk close: %d %s", w.Code, w.Body)
	}
	st := s.Snapshot()
	if st.DefaultModelID != first {
		t.Fatal("bulk exposure cleared the default selection")
	}
	for _, id := range []string{first, second} {
		if st.Models[0].ID == id && st.Models[0].Disabled {
			t.Fatal("bulk exposure changed internal availability")
		}
		if _, err := s.ModelRoute(id); err != nil {
			t.Fatalf("internal diagnostics lost %s: %v", id, err)
		}
	}
	for _, route := range []string{"adroute/work/first", DefaultRouteID} {
		if w := proxyCall(t, app, route, map[string]any{"stream": true}, nil); w.Code != 403 {
			t.Fatalf("closed route %s: %d %s", route, w.Code, w.Body)
		}
	}
	// The aggregate itself is a separate switch and still resolves externally.
	if w := proxyCall(t, app, "adroute/agg/chain", map[string]any{"stream": true}, nil); w.Code == 403 {
		t.Fatalf("closing models also closed the aggregate: %s", w.Body)
	}
	list, _ := json.Marshal(s.OpenAIModelList())
	catalog, err := os.ReadFile(s.Paths().Catalog)
	if err != nil {
		t.Fatal(err)
	}
	for _, data := range [][]byte{list, catalog} {
		if strings.Contains(string(data), "adroute/work/first") || strings.Contains(string(data), DefaultRouteID) {
			t.Fatalf("bulk-closed model still published: %s", data)
		}
	}
}
