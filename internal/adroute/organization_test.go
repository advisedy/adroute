package adroute

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOrganizationPersistsWithoutChangingRoutes(t *testing.T) {
	s, app, gid, mid := proxyFixture(t, HTTPOptions{})
	agg := mustAggregate(t, s, AggregateInput{Name: "chain", Members: []string{mid}})
	if err := s.SetDefault(mid); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/models/" + mid, "/api/aggregates/" + agg} {
		w := adminReq(t, app, "PUT", path+"/organization", map[string]bool{"favorite": true, "archived": true})
		if w.Code != 200 {
			t.Fatalf("organization: %d %s", w.Code, w.Body)
		}
	}
	w := adminReq(t, app, "PUT", "/api/groups/"+gid+"/organization", map[string]bool{"archived": true})
	if w.Code != 200 {
		t.Fatalf("group: %d %s", w.Code, w.Body)
	}
	if err := s.UpdateModel(mid, ModelInput{GroupID: gid, Name: "edited", Model: "up", BaseURL: "https://example.com/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateAggregate(agg, AggregateInput{Name: "chain", Members: []string{mid}}); err != nil {
		t.Fatal(err)
	}
	if err := s.RenameGroup(gid, GroupInput{Name: "work"}); err != nil {
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
	raw, _ := json.Marshal(reopened.Snapshot())
	var saved map[string]any
	json.Unmarshal(raw, &saved)
	for _, kind := range []string{"models", "aggregates"} {
		for _, v := range saved[kind].([]any) {
			item := v.(map[string]any)
			if item["favorite"] != true || item["archived"] != true {
				t.Fatalf("lost metadata on edit/copy/restart: %s", raw)
			}
		}
	}
	if saved["groups"].([]any)[0].(map[string]any)["archived"] != true {
		t.Fatal("group lost archive")
	}
	if reopened.Snapshot().DefaultModelID != mid {
		t.Fatal("organization cleared default")
	}
	for _, route := range []string{"adroute/work/edited", "adroute/agg/chain", DefaultRouteID} {
		if _, err := ResolveChain(reopened.Snapshot(), route); err != nil {
			t.Fatalf("archive blocked routing: %s %v", route, err)
		}
	}
	if _, err := reopened.ModelRoute(copyID); err != nil {
		t.Fatal(err)
	}
	// Unarchive only the group; individual archive flags must survive.
	w = adminReq(t, app, "PUT", "/api/groups/"+gid+"/organization", map[string]bool{"archived": false})
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	var state map[string]any
	json.Unmarshal(w.Body.Bytes(), &state)
	if state["models"].([]any)[0].(map[string]any)["archived"] != true {
		t.Fatal("group restore overwrote individual archive")
	}
}

func TestOrganizationDoesNotAlterCatalogOrExposure(t *testing.T) {
	s, app, gid, mid := proxyFixture(t, HTTPOptions{})
	agg := mustAggregate(t, s, AggregateInput{Name: "chain", Members: []string{mid}})
	s.SetDefault(mid)
	before, _ := json.Marshal(s.Catalog())
	listBefore, _ := json.Marshal(s.OpenAIModelList())
	for _, path := range []string{"/api/models/" + mid, "/api/aggregates/" + agg, "/api/groups/" + gid} {
		w := adminReq(t, app, "PUT", path+"/organization", map[string]bool{"archived": true})
		if w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
	}
	after, _ := json.Marshal(s.Catalog())
	listAfter, _ := json.Marshal(s.OpenAIModelList())
	if string(before) != string(after) || string(listBefore) != string(listAfter) {
		t.Fatal("organization changed external catalog")
	}
	if s.Snapshot().Models[0].Disabled || s.Snapshot().Models[0].ExposureDisabled || s.Snapshot().Aggregates[0].Disabled || s.Snapshot().Aggregates[0].ExposureDisabled {
		t.Fatal("organization changed permissions")
	}
	export := adminReq(t, app, "GET", "/api/config/export", nil)
	var backup configBackup
	if err := json.Unmarshal(export.Body.Bytes(), &backup); err != nil {
		t.Fatal(err)
	}
	rev := s.Snapshot().Revision
	if err := s.importConfig(configImport{ExpectedRevision: &rev, Backup: backup}); err != nil {
		t.Fatal(err)
	}
	w := adminReq(t, app, "GET", "/api/state", nil)
	if !strings.Contains(w.Body.String(), `"archived":true`) {
		t.Fatal("import lost metadata")
	}
}

func TestOrganizationStrictAdminOnlyAndMissingTargets(t *testing.T) {
	s, app, gid, mid := proxyFixture(t, HTTPOptions{})
	agg := mustAggregate(t, s, AggregateInput{Name: "chain", Members: []string{mid}})
	for _, path := range []string{"/api/models/" + mid + "/organization", "/api/aggregates/" + agg + "/organization", "/api/groups/" + gid + "/organization"} {
		for _, body := range []string{`{}`, `{"archived":null}`, `{"archived":"true"}`, `{"archived":true,"disabled":true}`, `{"favorite":null}`} {
			r := httptest.NewRequest("PUT", "http://127.0.0.1:3001"+path, strings.NewReader(body))
			r.Header.Set("Authorization", "Bearer "+s.TokenPair().AdminToken)
			w := httptest.NewRecorder()
			app.ServeHTTP(w, r)
			if w.Code != 400 {
				t.Fatalf("strict %s %s: %d %s", path, body, w.Code, w.Body)
			}
		}
		r := httptest.NewRequest("PUT", "http://127.0.0.1:3001"+path, strings.NewReader(`{"archived":true}`))
		r.Header.Set("Authorization", "Bearer "+s.TokenPair().ProxyToken)
		w := httptest.NewRecorder()
		app.ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("proxy modified metadata: %d", w.Code)
		}
	}
	for _, kind := range []string{"groups", "models", "aggregates"} {
		w := adminReq(t, app, "PUT", "/api/"+kind+"/missing/organization", map[string]bool{"archived": true})
		if w.Code != 404 {
			t.Fatalf("missing: %d", w.Code)
		}
	}
}

func TestOrganizationImportRejectsNullMetadata(t *testing.T) {
	s := newTestStore(t)
	gid := mustGroup(t, s, "work")
	mustModel(t, s, ModelInput{GroupID: gid, Name: "first", Model: "up", BaseURL: "https://example.com/v1", APIKey: "key"})
	for _, kind := range []string{"groups", "models"} {
		b, _ := json.Marshal(s.Snapshot())
		var raw map[string]any
		json.Unmarshal(b, &raw)
		raw[kind].([]any)[0].(map[string]any)["archived"] = nil
		b, _ = json.Marshal(raw)
		var st State
		if json.Unmarshal(b, &st) == nil {
			t.Fatalf("accepted null %s metadata", kind)
		}
	}
}
