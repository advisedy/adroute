package adroute

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAvailabilityClosesRouteAndDefaultWithoutFallback(t *testing.T) {
	s := newTestStore(t)
	gid := mustGroup(t, s, "work")
	a := mustModel(t, s, ModelInput{GroupID: gid, Name: "a", Model: "a", BaseURL: "https://a.example/v1", APIKey: "key-a"})
	b := mustModel(t, s, ModelInput{GroupID: gid, Name: "b", Model: "b", BaseURL: "https://b.example/v1", APIKey: "key-b"})
	if err := s.SetDefault(a); err != nil {
		t.Fatal(err)
	}
	if err := s.SetModelEnabled(a, false); err != nil {
		t.Fatal(err)
	}
	if s.Snapshot().DefaultModelID != "" {
		t.Fatal("closing default must clear it")
	}
	if _, err := s.Resolve("adroute/work/a"); ErrorStatus(err) != 403 {
		t.Fatalf("closed route status=%v", ErrorStatus(err))
	}
	if _, err := s.Resolve(DefaultRouteID); ErrorStatus(err) != 503 {
		t.Fatalf("default must not fall back: %v", err)
	}
	r, err := s.Resolve("adroute/work/b")
	if err != nil || r.ID != b {
		t.Fatalf("other model must stay reachable: %#v %v", r, err)
	}
}

func TestRoutingSnapshotRemainsImmutableAcrossClose(t *testing.T) {
	s := newTestStore(t)
	gid := mustGroup(t, s, "work")
	mid := mustModel(t, s, ModelInput{GroupID: gid, Name: "m", Model: "up", BaseURL: "https://up.example/v1", APIKey: "synthetic"})
	snapshot := s.RoutingSnapshot()
	if err := s.SetModelEnabled(mid, false); err != nil {
		t.Fatal(err)
	}
	if _, err := snapshot("adroute/work/m"); err != nil {
		t.Fatalf("in-flight snapshot must still resolve: %v", err)
	}
	if _, err := s.Resolve("adroute/work/m"); ErrorStatus(err) != 403 {
		t.Fatalf("new request must see the close: %v", err)
	}
}

func TestClosedFixedRouteRejectsBeforeUpstream(t *testing.T) {
	var calls int
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer up.Close()
	s, app, gid, mid := proxyFixture(t, HTTPOptions{Client: up.Client()})
	setProxiedBaseURL(t, s, gid, mid, up.URL+"/v1")
	if err := s.SetModelEnabled(mid, false); err != nil {
		t.Fatal(err)
	}
	w := proxyCall(t, app, "adroute/work/label", map[string]any{"stream": true}, nil)
	if w.Code != 403 || !strings.Contains(w.Body.String(), "MODEL_ROUTE_DISABLED") {
		t.Fatalf("closed proxy status=%d body=%s", w.Code, w.Body.String())
	}
	if calls != 0 {
		t.Fatalf("upstream was called for a closed route: %d", calls)
	}
	w = proxyCall(t, app, "adroute/work/unknown", map[string]any{"stream": true}, nil)
	if w.Code != 404 {
		t.Fatalf("unknown route status=%d", w.Code)
	}
}

func TestAvailabilityHTTPStrictBodyAndAdminOnly(t *testing.T) {
	s, app, _, mid := proxyFixture(t, HTTPOptions{})
	for _, tc := range []struct {
		name string
		body string
		want int
	}{
		{"missing", `{}`, 400},
		{"null", `{"enabled":null}`, 400},
		{"string", `{"enabled":"false"}`, 400},
		{"unknown field", `{"enabled":false,"extra":1}`, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPut, "http://127.0.0.1:3001/api/models/"+mid+"/availability", strings.NewReader(tc.body))
			r.Host = "127.0.0.1:3001"
			r.Header.Set("Authorization", "Bearer "+s.TokenPair().AdminToken)
			w := httptest.NewRecorder()
			app.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status=%d body=%s want %d", w.Code, w.Body.String(), tc.want)
			}
		})
	}
	// Proxy token must never change configuration.
	r := httptest.NewRequest(http.MethodPut, "http://127.0.0.1:3001/api/models/"+mid+"/availability", strings.NewReader(`{"enabled":false}`))
	r.Host = "127.0.0.1:3001"
	r.Header.Set("Authorization", "Bearer "+s.TokenPair().ProxyToken)
	w := httptest.NewRecorder()
	app.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("proxy token status=%d", w.Code)
	}
	if got := adminReq(t, app, http.MethodPut, "/api/models/"+mid+"/availability", map[string]bool{"enabled": false}); got.Code != 200 {
		t.Fatalf("admin close failed: %d %s", got.Code, got.Body)
	}
	if got := adminReq(t, app, http.MethodPut, "/api/models/missing/availability", map[string]bool{"enabled": false}); got.Code != 404 {
		t.Fatalf("unknown model status=%d", got.Code)
	}
}

func TestAvailabilitySurvivesEditsAndRestart(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	s, err := OpenStore(home)
	if err != nil {
		t.Fatal(err)
	}
	gid := mustGroup(t, s, "work")
	mid := mustModel(t, s, ModelInput{GroupID: gid, Name: "coding", Model: "up", BaseURL: "https://up.example/v1", APIKey: "synthetic"})
	if err = s.SetModelEnabled(mid, false); err != nil {
		t.Fatal(err)
	}
	// Ordinary edits that omit "disabled" must not silently reopen the entry.
	if err = s.UpdateModel(mid, ModelInput{GroupID: gid, Name: "coding", Model: "up-2", BaseURL: "https://up2.example/v1", APIKey: "rotated"}); err != nil {
		t.Fatal(err)
	}
	if !s.Snapshot().Models[0].Disabled {
		t.Fatal("edit reopened a closed entry")
	}
	reopened, err := OpenStore(home)
	if err != nil {
		t.Fatal(err)
	}
	if !reopened.Snapshot().Models[0].Disabled {
		t.Fatal("closed state was not persisted")
	}
}

func TestPublishedCodexCatalogTracksCloseAndRestartWithoutRestoringDefault(t *testing.T) {
	home := t.TempDir()
	s, err := OpenStore(home)
	if err != nil {
		t.Fatal(err)
	}
	gid := mustGroup(t, s, "catalog")
	active := mustModel(t, s, ModelInput{GroupID: gid, Name: "active", Model: "active", BaseURL: "https://catalog.example/v1", APIKey: "synthetic"})
	other := mustModel(t, s, ModelInput{GroupID: gid, Name: "other", Model: "other", BaseURL: "https://catalog.example/v1", APIKey: "synthetic"})
	if err = s.SetDefault(active); err != nil {
		t.Fatal(err)
	}
	a := mustHTTPApp(HTTPOptions{Store: s}) // publishes the actual Codex catalog file
	read := func() string {
		t.Helper()
		b, e := os.ReadFile(s.Paths().Catalog)
		if e != nil {
			t.Fatal(e)
		}
		return string(b)
	}
	if got := read(); !strings.Contains(got, "adroute/default") || !strings.Contains(got, "adroute/catalog/active") {
		t.Fatalf("initial published catalog missing enabled default/route: %s", got)
	}
	if w := adminReq(t, a, http.MethodPut, "/api/models/"+active+"/availability", map[string]bool{"enabled": false}); w.Code != 200 {
		t.Fatalf("close: %d %s", w.Code, w.Body.String())
	}
	got := read()
	if strings.Contains(got, "adroute/default") || strings.Contains(got, "adroute/catalog/active") || !strings.Contains(got, "adroute/catalog/other") {
		t.Fatalf("published catalog after close: %s", got)
	}
	reopened, err := OpenStore(home)
	if err != nil {
		t.Fatal(err)
	}
	_ = mustHTTPApp(HTTPOptions{Store: reopened}) // re-publish persisted state on restart
	restarted, err := os.ReadFile(reopened.Paths().Catalog)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(restarted), "adroute/default") || strings.Contains(string(restarted), "adroute/catalog/active") {
		t.Fatalf("restart restored a closed route/default: %s", restarted)
	}
	// Delete the remaining entry and make sure the catalog empties.
	if err := reopened.DeleteModel(other); err != nil {
		t.Fatal(err)
	}
	final, _ := os.ReadFile(reopened.Paths().Catalog)
	if strings.Contains(string(final), "adroute/catalog/") {
		t.Fatalf("deleted model still published: %s", final)
	}
}

func TestExplicitOpenCodeSyncUsesFilteredSnapshotAndAvailabilityDoesNotWriteClientConfig(t *testing.T) {
	s := newTestStore(t)
	gid := mustGroup(t, s, "sync")
	mid := mustModel(t, s, ModelInput{GroupID: gid, Name: "chosen", Model: "chosen", BaseURL: "https://sync.example/v1", APIKey: "synthetic"})
	if err := s.SetDefault(mid); err != nil {
		t.Fatal(err)
	}
	a := mustHTTPApp(HTTPOptions{Store: s})
	config := filepath.Join(t.TempDir(), "opencode.jsonc")
	original := "{// preserve this comment\n \"providers\":{\"adroute\":{\"package\":\"@opencode/ai/providers/openai/responses\",\"settings\":{\"baseURL\":\"http://127.0.0.1:1/v1\",\"apiKey\":\"synthetic\"},\"models\":{}}}}"
	if err := os.WriteFile(config, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	sync := func() *httptest.ResponseRecorder {
		return adminReq(t, a, http.MethodPost, "/api/opencode/sync", map[string]string{"configPath": config})
	}
	if w := sync(); w.Code != 200 {
		t.Fatalf("explicit initial sync: %d %s", w.Code, w.Body.String())
	}
	before, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(before), "adroute/default") || !strings.Contains(string(before), "adroute/sync/chosen") {
		t.Fatalf("initial explicit sync omitted enabled models: %s", before)
	}
	if w := adminReq(t, a, http.MethodPut, "/api/models/"+mid+"/availability", map[string]bool{"enabled": false}); w.Code != 200 {
		t.Fatalf("close: %d %s", w.Code, w.Body.String())
	}
	afterMutation, _ := os.ReadFile(config)
	if string(afterMutation) != string(before) {
		t.Fatal("availability mutation wrote external OpenCode config without explicit sync")
	}
	if w := sync(); w.Code != 200 {
		t.Fatalf("explicit filtered sync: %d %s", w.Code, w.Body.String())
	}
	afterSync, _ := os.ReadFile(config)
	if strings.Contains(string(afterSync), "adroute/default") || strings.Contains(string(afterSync), "adroute/sync/chosen") {
		t.Fatalf("explicit sync retained closed route/default: %s", afterSync)
	}
	if !strings.Contains(string(afterSync), "preserve this comment") {
		t.Fatalf("sync dropped user comments: %s", afterSync)
	}
}

func TestModelStateJSONShape(t *testing.T) {
	_, app, _, mid := proxyFixture(t, HTTPOptions{})
	w := adminReq(t, app, http.MethodGet, "/api/state", nil)
	if w.Code != 200 {
		t.Fatalf("state status=%d", w.Code)
	}
	var state map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	groups := state["groups"].([]any)
	if len(groups) != 1 || groups[0].(map[string]any)["name"] != "work" {
		t.Fatalf("groups %#v", groups)
	}
	models := state["models"].([]any)
	m := models[0].(map[string]any)
	if m["id"] != mid || m["routeId"] != "adroute/work/label" || m["name"] != "label" || m["model"] != "same-model" || m["groupName"] != "work" || m["open"] != true {
		t.Fatalf("model shape %#v", m)
	}
	if m["apiKey"] != "upstream-secret" {
		t.Fatalf("admin state must include the upstream credential: %#v", m)
	}
	caps := state["capabilities"].(map[string]any)
	for _, key := range []string{"groups", "models", "modelAvailability", "quickTest", "compatibility", "adapterDirectory"} {
		if caps[key] != true {
			t.Fatalf("capability %s missing: %#v", key, caps)
		}
	}
	if caps["quickTestTimeoutMs"].(float64) != float64(QuickTestTimeout.Milliseconds()) {
		t.Fatalf("quickTestTimeoutMs %#v", caps["quickTestTimeoutMs"])
	}
	if fmt.Sprint(state["defaultModelId"]) != mid {
		t.Fatalf("defaultModelId %#v", state["defaultModelId"])
	}
	if state["defaultRouteId"] != "adroute/default" {
		t.Fatalf("defaultRouteId %#v", state["defaultRouteId"])
	}
}
