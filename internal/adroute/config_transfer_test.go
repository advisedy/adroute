package adroute

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestConfigTransferRestoresModelsAndKeepsLocalTokens(t *testing.T) {
	source := newTestStore(t)
	group, err := source.CreateGroup(GroupInput{Name: "work"})
	if err != nil {
		t.Fatal(err)
	}
	model, err := source.CreateModel(ModelInput{GroupID: group, Name: "coding", Model: "vendor/model", BaseURL: "https://example.com/v1", APIKey: "secret-key", Website: "https://example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if err := source.SetDefault(model); err != nil {
		t.Fatal(err)
	}
	a := mustHTTPApp(HTTPOptions{Store: source})
	export := adminReq(t, a, http.MethodGet, "/api/config/export", nil)
	if export.Code != 200 {
		t.Fatalf("export: %d %s", export.Code, export.Body.String())
	}
	var backup map[string]any
	if err := json.Unmarshal(export.Body.Bytes(), &backup); err != nil {
		t.Fatal(err)
	}
	if _, exists := backup["adminToken"]; exists {
		t.Fatal("backup contains local tokens")
	}

	target := newTestStore(t)
	tokens := target.TokenPair()
	b := mustHTTPApp(HTTPOptions{Store: target})
	result := adminReq(t, b, http.MethodPost, "/api/config/import", map[string]any{"expectedRevision": target.Snapshot().Revision, "backup": backup})
	if result.Code != 200 {
		t.Fatalf("import: %d %s", result.Code, result.Body.String())
	}
	got := target.Snapshot()
	if !reflect.DeepEqual(got.Groups, source.Snapshot().Groups) || !reflect.DeepEqual(got.Models, source.Snapshot().Models) || got.DefaultModelID != model {
		t.Fatalf("incomplete restore: %#v", got)
	}
	if target.TokenPair() != tokens {
		t.Fatal("import changed local tokens")
	}
	if route, err := target.Resolve("adroute/work/coding"); err != nil || route.APIKey != "secret-key" {
		t.Fatalf("route not restored: %#v %v", route, err)
	}
	reopened, err := OpenStore(target.Paths().Home)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reopened.Snapshot(), got) {
		t.Fatal("import was not persisted")
	}
}

func TestConfigImportRejectsInvalidAndStaleWithoutChangingState(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.CreateGroup(GroupInput{Name: "existing"}); err != nil {
		t.Fatal(err)
	}
	a := mustHTTPApp(HTTPOptions{Store: s})
	before := s.Snapshot()
	valid := map[string]any{"format": "adroute-config", "version": 1, "config": map[string]any{"version": 6, "revision": 0, "groups": []any{}, "models": []any{}, "aggregates": []any{}}}
	for _, tc := range []struct {
		name     string
		revision int64
		backup   any
		status   int
	}{
		{"stale", before.Revision - 1, valid, 409},
		{"invalid", before.Revision, map[string]any{"format": "adroute-config", "version": 1, "config": map[string]any{"version": 6, "revision": 0, "groups": []any{}, "models": []any{map[string]any{"id": "bad"}}, "aggregates": []any{}}}, 400},
		{"unknown format", before.Revision, map[string]any{"format": "other", "version": 1, "config": valid["config"]}, 400},
		{"null groups", before.Revision, map[string]any{"format": "adroute-config", "version": 1, "config": map[string]any{"version": 6, "revision": 0, "groups": nil, "models": []any{}, "aggregates": []any{}}}, 400},
		{"missing aggregates", before.Revision, map[string]any{"format": "adroute-config", "version": 1, "config": map[string]any{"version": 6, "revision": 0, "groups": []any{}, "models": []any{}}}, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := adminReq(t, a, http.MethodPost, "/api/config/import", map[string]any{"expectedRevision": tc.revision, "backup": tc.backup})
			if w.Code != tc.status {
				t.Fatalf("status: %d %s", w.Code, w.Body.String())
			}
			if !reflect.DeepEqual(before, s.Snapshot()) {
				t.Fatal("failed import changed state")
			}
		})
	}
}

func TestConfigTransferRequiresAdminAndRejectsMalformedJSON(t *testing.T) {
	s := newTestStore(t)
	a := mustHTTPApp(HTTPOptions{Store: s})
	for _, path := range []string{"/api/config/export", "/api/config/import"} {
		r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:3001"+path, nil)
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:3001/api/config/import", strings.NewReader("not json"))
	r.Header.Set("Authorization", "Bearer "+s.TokenPair().AdminToken)
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatalf("malformed JSON: %d %s", w.Code, w.Body.String())
	}
}
