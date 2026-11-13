package adroute

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A published context window reaches both clients in their own shape, and a
// model that declares none publishes nothing so the client keeps its fallback.
func TestContextWindowReachesBothClients(t *testing.T) {
	d := t.TempDir()
	openCode := filepath.Join(d, "opencode.json")
	raw := `{"providers":{"adroute":{"package":"aisdk:@ai-sdk/openai","settings":{"baseURL":"http://127.0.0.1/v1","apiKey":"x"}}}}`
	if e := os.WriteFile(openCode, []byte(raw), 0600); e != nil {
		t.Fatal(e)
	}
	models := []SyncModel{
		{Slug: "adroute/a/known", DisplayName: "A / known", ContextWindow: 1048576},
		{Slug: "adroute/a/unknown", DisplayName: "A / unknown"},
	}
	if _, e := SyncOpenCode(OpenCodeSyncOptions{Models: models, ConfigPath: openCode}); e != nil {
		t.Fatal(e)
	}
	parsed, e := parseJSONC([]byte(readConfigForTest(t, openCode)))
	if e != nil {
		t.Fatal(e)
	}
	entries := parsed.props["providers"].props["adroute"].props["models"].props

	known, _ := entries["adroute/a/known"].value.(map[string]any)
	limit, _ := known["limit"].(map[string]any)
	// The JSONC parser keeps numbers as json.Number so a round trip cannot
	// reshape them.
	if fmt.Sprint(limit["context"]) != "1048576" {
		t.Fatalf("known limit = %#v", known["limit"])
	}
	// Only context is published; output is OpenCode's own budget.
	if _, ok := limit["output"]; ok {
		t.Fatalf("unexpected output limit: %#v", limit)
	}

	unknown, _ := entries["adroute/a/unknown"].value.(map[string]any)
	if _, ok := unknown["limit"]; ok {
		t.Fatalf("unknown model must omit limit, got %#v", unknown["limit"])
	}
}

// The Codex catalog carries context_window only when a value exists, and never
// max_context_window, which Codex derives itself.
func TestContextWindowInCodexCatalog(t *testing.T) {
	with := catalogEntry("adroute/a/known", "A / known", 0, 1048576)
	if with["context_window"] != 1048576 {
		t.Fatalf("context_window = %#v", with["context_window"])
	}
	if _, ok := with["max_context_window"]; ok {
		t.Fatal("max_context_window must not be published")
	}
	without := catalogEntry("adroute/a/unknown", "A / unknown", 0, 0)
	if _, ok := without["context_window"]; ok {
		t.Fatalf("zero window must be omitted, got %#v", without["context_window"])
	}
}

// A failover chain can only promise what its weakest member delivers, and only
// when every member declares a value. Any undeclared member makes the total a
// guess, so nothing is published.
func TestAggregateContextWindowUsesSmallestDeclaredMember(t *testing.T) {
	s := newTestStore(t)
	gid := mustGroup(t, s, "work")
	wide := mustModel(t, s, ModelInput{GroupID: gid, Name: "wide", Model: "up", BaseURL: "https://a.example/v1", APIKey: "k"})
	narrow := mustModel(t, s, ModelInput{GroupID: gid, Name: "narrow", Model: "up", BaseURL: "https://b.example/v1", APIKey: "k"})
	unknown := mustModel(t, s, ModelInput{GroupID: gid, Name: "unknown", Model: "up", BaseURL: "https://c.example/v1", APIKey: "k"})
	if e := s.SetModelContextWindow(wide, 1000000); e != nil {
		t.Fatal(e)
	}
	if e := s.SetModelContextWindow(narrow, 200000); e != nil {
		t.Fatal(e)
	}

	chain, e := s.CreateAggregate(AggregateInput{Name: "chain", Members: []string{wide, narrow}})
	if e != nil {
		t.Fatal(e)
	}
	st := s.Snapshot()
	for _, agg := range st.Aggregates {
		if agg.ID == chain && aggregateContextWindow(st, &agg) != 200000 {
			t.Fatalf("aggregate window = %d, want the smallest member 200000", aggregateContextWindow(st, &agg))
		}
	}

	// Adding a member without a declared window must clear the aggregate value.
	if e := s.UpdateAggregate(chain, AggregateInput{Name: "chain", Members: []string{wide, narrow, unknown}}); e != nil {
		t.Fatal(e)
	}
	st = s.Snapshot()
	for _, agg := range st.Aggregates {
		if agg.ID == chain && aggregateContextWindow(st, &agg) != 0 {
			t.Fatalf("aggregate window = %d, want 0 when a member is undeclared", aggregateContextWindow(st, &agg))
		}
	}
}

// The narrow endpoint writes only the window: identity, credential and open
// state survive, and 0 clears it.
func TestModelParamsEndpointSetsAndClearsContextWindow(t *testing.T) {
	s := newTestStore(t)
	gid := mustGroup(t, s, "work")
	id := mustModel(t, s, ModelInput{GroupID: gid, Name: "coding", Model: "upstream", BaseURL: "https://x.example/v1", APIKey: "synthetic-key"})
	a := mustHTTPApp(HTTPOptions{Store: s, Port: 3001})

	w := adminReq(t, a, http.MethodPut, "/api/models/"+id+"/params", map[string]any{"contextWindow": 1048576})
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	model := stateModel(t, stateOf(t, w), id)
	if model["contextWindow"] != float64(1048576) {
		t.Fatalf("contextWindow = %#v", model["contextWindow"])
	}
	if model["apiKey"] != "synthetic-key" || model["name"] != "coding" || model["baseUrl"] != "https://x.example/v1" {
		t.Fatalf("narrow write changed other fields: %#v", model)
	}

	w = adminReq(t, a, http.MethodPut, "/api/models/"+id+"/params", map[string]any{"contextWindow": 0})
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if model := stateModel(t, stateOf(t, w), id); model["contextWindow"] != float64(0) {
		t.Fatalf("contextWindow after clear = %#v", model["contextWindow"])
	}
}

// Out-of-range and malformed values are refused without touching the entry.
func TestModelParamsEndpointRejectsInvalidWindow(t *testing.T) {
	s := newTestStore(t)
	gid := mustGroup(t, s, "work")
	id := mustModel(t, s, ModelInput{GroupID: gid, Name: "coding", Model: "upstream", BaseURL: "https://x.example/v1", APIKey: "k"})
	a := mustHTTPApp(HTTPOptions{Store: s, Port: 3001})

	for _, body := range []map[string]any{
		{"contextWindow": MinContextWindow - 1},
		{"contextWindow": MaxContextWindow + 1},
		{"contextWindow": -1},
		{},
	} {
		if w := adminReq(t, a, http.MethodPut, "/api/models/"+id+"/params", body); w.Code != 400 {
			t.Fatalf("body %#v: %d %s", body, w.Code, w.Body.String())
		}
	}
	if got := s.Snapshot().Models[0].ContextWindow; got != 0 {
		t.Fatalf("rejected writes changed the entry: %d", got)
	}
	// An unknown entry is a 404, not a silent success.
	if w := adminReq(t, a, http.MethodPut, "/api/models/nope/params", map[string]any{"contextWindow": 200000}); w.Code != 404 {
		t.Fatalf("unknown model: %d %s", w.Code, w.Body.String())
	}
}

// A hand-edited config file cannot smuggle an impossible value past the API.
func TestContextWindowValidatedInState(t *testing.T) {
	s := newTestStore(t)
	gid := mustGroup(t, s, "work")
	mustModel(t, s, ModelInput{GroupID: gid, Name: "coding", Model: "upstream", BaseURL: "https://x.example/v1", APIKey: "k"})
	bad := s.Snapshot()
	bad.Models[0].ContextWindow = MaxContextWindow + 1
	if e := ValidateState(bad); e == nil {
		t.Fatal("out-of-range context window accepted")
	}
	ok := s.Snapshot()
	ok.Models[0].ContextWindow = MinContextWindow
	if e := ValidateState(ok); e != nil {
		t.Fatalf("minimum window rejected: %v", e)
	}
	ok.Models[0].ContextWindow = 0
	if e := ValidateState(ok); e != nil {
		t.Fatalf("unset window rejected: %v", e)
	}
}

// Copying a model keeps its window, and a repeat sync stays idempotent so a
// refresh never rewrites an unchanged file.
func TestContextWindowSurvivesCopyAndResync(t *testing.T) {
	s := newTestStore(t)
	gid := mustGroup(t, s, "work")
	id := mustModel(t, s, ModelInput{GroupID: gid, Name: "coding", Model: "upstream", BaseURL: "https://x.example/v1", APIKey: "k"})
	if e := s.SetModelContextWindow(id, 400000); e != nil {
		t.Fatal(e)
	}
	copied, e := s.CopyModel(id)
	if e != nil {
		t.Fatal(e)
	}
	st := s.Snapshot()
	for _, m := range st.Models {
		if m.ID == copied && m.ContextWindow != 400000 {
			t.Fatalf("copied window = %d", m.ContextWindow)
		}
	}

	d := t.TempDir()
	p := filepath.Join(d, "c.json")
	if e := os.WriteFile(p, []byte(`{"providers":{"adroute":{"package":"aisdk:@ai-sdk/openai","settings":{"baseURL":"http://127.0.0.1/v1","apiKey":"x"}}}}`), 0600); e != nil {
		t.Fatal(e)
	}
	models := []SyncModel{{Slug: "adroute/work/coding", DisplayName: "work / coding", ContextWindow: 400000}}
	if _, e := SyncOpenCode(OpenCodeSyncOptions{Models: models, ConfigPath: p}); e != nil {
		t.Fatal(e)
	}
	r, e := SyncOpenCode(OpenCodeSyncOptions{Models: models, ConfigPath: p})
	if e != nil {
		t.Fatal(e)
	}
	if r.Changed || r.BackupPath != "" {
		t.Fatalf("repeat sync rewrote the file: %#v", r)
	}
}

func stateModel(t *testing.T, state map[string]any, id string) map[string]any {
	t.Helper()
	models, _ := state["models"].([]any)
	for _, raw := range models {
		model, _ := raw.(map[string]any)
		if model["id"] == id {
			return model
		}
	}
	t.Fatalf("model %s missing from state", id)
	return nil
}

// The default alias publishes the window of whatever it resolves to.
func TestDefaultAliasPublishesResolvedWindow(t *testing.T) {
	s := newTestStore(t)
	gid := mustGroup(t, s, "work")
	id := mustModel(t, s, ModelInput{GroupID: gid, Name: "coding", Model: "upstream", BaseURL: "https://x.example/v1", APIKey: "k"})
	if e := s.SetModelContextWindow(id, 200000); e != nil {
		t.Fatal(e)
	}
	if e := s.SetDefault(id); e != nil {
		t.Fatal(e)
	}
	catalog := s.Catalog()
	for _, raw := range catalog["models"].([]map[string]any) {
		if raw["slug"] == DefaultRouteID && raw["context_window"] != 200000 {
			t.Fatalf("default alias window = %#v", raw["context_window"])
		}
	}
}

// The state payload is JSON, so the field must survive the round trip a browser
// actually performs.
func TestContextWindowAppearsInStateJSON(t *testing.T) {
	s := newTestStore(t)
	gid := mustGroup(t, s, "work")
	id := mustModel(t, s, ModelInput{GroupID: gid, Name: "coding", Model: "upstream", BaseURL: "https://x.example/v1", APIKey: "k"})
	if e := s.SetModelContextWindow(id, 123456); e != nil {
		t.Fatal(e)
	}
	a := mustHTTPApp(HTTPOptions{Store: s, Port: 3001})
	w := adminReq(t, a, http.MethodGet, "/api/state", nil)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"contextWindow":123456`) {
		t.Fatalf("state body lacks contextWindow: %s", w.Body.String())
	}
	var decoded map[string]any
	if e := json.Unmarshal(w.Body.Bytes(), &decoded); e != nil {
		t.Fatal(e)
	}
	if stateModel(t, decoded, id)["contextWindow"] != float64(123456) {
		t.Fatal("contextWindow lost in JSON round trip")
	}
}
