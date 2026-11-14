package adroute

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestOpenCodeSyncAcceptsCurrentPublicModelNames(t *testing.T) {
	for _, name := range []string{"gpt-5.2", "coding_fast", "GPT-5", "coding"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "opencode.jsonc")
			original := `{"providers":{"adroute":{"package":"@opencode/ai/providers/openai/responses","settings":{"baseURL":"http://127.0.0.1:1/v1","apiKey":"synthetic"},"models":{}}}}`
			if err := os.WriteFile(path, []byte(original), 0600); err != nil {
				t.Fatal(err)
			}
			result, err := SyncOpenCode(OpenCodeSyncOptions{ConfigPath: path, Models: []SyncModel{{Slug: "adroute/work/" + name, DisplayName: "work / " + name}}})
			if err != nil || !result.Changed {
				t.Fatalf("legal public name rejected: %#v %v", result, err)
			}
			contents, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var config struct {
				Providers map[string]struct {
					Models map[string]struct {
						ModelID string `json:"modelID"`
					} `json:"models"`
				} `json:"providers"`
			}
			if err := json.Unmarshal(contents, &config); err != nil {
				t.Fatal(err)
			}
			if config.Providers["adroute"].Models["adroute/work/"+name].ModelID != "adroute/work/"+name {
				t.Fatalf("wrong actual client mapping: %s", contents)
			}
		})
	}
}

func TestMalformedManagementFieldsCannotClearOrChangeSavedState(t *testing.T) {
	s := newTestStore(t)
	gid := mustGroup(t, s, "work")
	mid := mustModel(t, s, ModelInput{GroupID: gid, Name: "coding", Model: "old-model", BaseURL: "https://synthetic.invalid/v1", APIKey: "synthetic"})
	pack := fixturePackage(t, "pixel-swe.adroute-adapter.json")
	if err := s.ImportAdapter(pack); err != nil {
		t.Fatal(err)
	}
	if err := s.SetModelProfile(mid, PackageRef(pack)); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDefault(mid); err != nil {
		t.Fatal(err)
	}
	app := mustHTTPApp(HTTPOptions{Store: s})
	for _, tc := range []struct {
		name, method, path string
		body               map[string]any
	}{
		{"missing default", "POST", "/api/default", map[string]any{}},
		{"null profile", "PUT", "/api/compatibility/selection/" + mid, map[string]any{"profile": nil}},
		{"missing profile", "PUT", "/api/compatibility/selection/" + mid, map[string]any{}},
		{"empty profile", "PUT", "/api/compatibility/selection/" + mid, map[string]any{"profile": ""}},
		{"null model key", "PUT", "/api/models/" + mid, map[string]any{"groupId": gid, "name": "coding", "model": "new-model", "baseUrl": "https://synthetic.invalid/v1", "apiKey": nil}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := s.Snapshot()
			response := adminReq(t, app, tc.method, tc.path, tc.body)
			if response.Code != 400 {
				t.Errorf("malformed body accepted: %d %s", response.Code, response.Body.String())
			}
			if !reflect.DeepEqual(before, s.Snapshot()) {
				t.Fatal("malformed request changed saved state")
			}
		})
	}
}

func TestExplicitDefaultClearAndRepairDisableRemainSupported(t *testing.T) {
	for _, clear := range []any{nil, ""} {
		s := newTestStore(t)
		gid := mustGroup(t, s, "work")
		mid := mustModel(t, s, ModelInput{GroupID: gid, Name: "coding", Model: "m", BaseURL: "https://synthetic.invalid/v1", APIKey: "synthetic"})
		if err := s.SetDefault(mid); err != nil {
			t.Fatal(err)
		}
		app := mustHTTPApp(HTTPOptions{Store: s})
		if response := adminReq(t, app, "POST", "/api/default", map[string]any{"modelId": clear}); response.Code != 200 || s.Snapshot().DefaultModelID != "" {
			t.Fatalf("explicit clear failed: %d %s", response.Code, response.Body.String())
		}
		pack := fixturePackage(t, "pixel-swe.adroute-adapter.json")
		if err := s.ImportAdapter(pack); err != nil {
			t.Fatal(err)
		}
		if err := s.SetModelProfile(mid, PackageRef(pack)); err != nil {
			t.Fatal(err)
		}
		if response := adminReq(t, app, "PUT", "/api/compatibility/selection/"+mid, map[string]any{"profile": "none"}); response.Code != 200 {
			t.Fatalf("explicit disable failed: %d %s", response.Code, response.Body.String())
		}
		if route, err := s.ModelRoute(mid); err != nil || route.Profile != "none" {
			t.Fatalf("explicit disable did not clear actual binding: %#v %v", route, err)
		}
	}
}

func TestDiagnosticCaptureDoesNotFollowAnEditedUpstreamTarget(t *testing.T) {
	for _, changed := range []string{"model", "baseUrl", "apiKey"} {
		t.Run(changed, func(t *testing.T) {
			s := newTestStore(t)
			gid := mustGroup(t, s, "work")
			mid := mustModel(t, s, ModelInput{GroupID: gid, Name: "coding", Model: "old-model", BaseURL: "https://old.invalid/v1", APIKey: "old-key"})
			route, _ := s.ModelRoute(mid)
			d := NewDiagnostics(s.Paths().Home, DiagnosticLimits{})
			capture, err := d.Arm(route, route.Model, true, true)
			if err != nil {
				t.Fatal(err)
			}
			writer := d.Claim(route, s.Secrets())
			if writer == nil {
				t.Fatal("original target not captured")
			}
			writer.ObserveSSE([]byte("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n"))
			if err := writer.Finish(200, ""); err != nil {
				t.Fatal(err)
			}
			in := ModelInput{GroupID: gid, Name: "coding", Model: route.Model, BaseURL: route.BaseURL}
			switch changed {
			case "model":
				in.Model = "new-model"
			case "baseUrl":
				in.BaseURL = "https://new.invalid/v1"
			case "apiKey":
				in.APIKey = "new-key"
			}
			if err := s.UpdateModel(mid, in); err != nil {
				t.Fatal(err)
			}
			root, err := filepath.Abs(filepath.Join("..", ".."))
			if err != nil {
				t.Fatal(err)
			}
			result, err := GenerateRepairPrompt(root, s, NewAdapterDirectory(s), d, mid, true, capture.CaptureID)
			if err != nil {
				t.Fatal(err)
			}
			if result.DiagnosisAvailable || strings.Contains(result.Prompt, "【本次已授权诊断身份】") {
				t.Fatal("old target sample handed to edited target")
			}
		})
	}
}

func TestCatalogPublicationFailureIsVisibleAndExplicitlyRetryable(t *testing.T) {
	s := newTestStore(t)
	gid := mustGroup(t, s, "work")
	app := mustHTTPApp(HTTPOptions{Store: s})
	if err := os.Remove(s.Paths().Catalog); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(s.Paths().Catalog, 0700); err != nil {
		t.Fatal(err)
	}
	response := adminReq(t, app, "POST", "/api/models", map[string]any{"groupId": gid, "name": "coding", "model": "m", "baseUrl": "https://synthetic.invalid/v1", "apiKey": "synthetic"})
	if response.Code != 201 {
		t.Fatalf("domain save failed: %d %s", response.Code, response.Body.String())
	}
	publication, ok := stateOf(t, response)["catalogPublication"].(map[string]any)
	if !ok || publication["pending"] != true || publication["message"] == "" {
		t.Fatal("saved configuration's directory publication failure was hidden")
	}
	before := s.Snapshot()
	if failed := adminReq(t, app, http.MethodPost, "/api/catalog/retry", nil); failed.Code != 503 {
		t.Fatalf("retry failure not reported: %d %s", failed.Code, failed.Body.String())
	}
	if err := os.Remove(s.Paths().Catalog); err != nil {
		t.Fatal(err)
	}
	retried := adminReq(t, app, http.MethodPost, "/api/catalog/retry", nil)
	if retried.Code != 200 {
		t.Fatalf("retry failed: %d %s", retried.Code, retried.Body.String())
	}
	publication, ok = stateOf(t, retried)["catalogPublication"].(map[string]any)
	if !ok || publication["pending"] != false {
		t.Fatal("pending publication was not cleared")
	}
	if !reflect.DeepEqual(before, s.Snapshot()) {
		t.Fatal("publication retry resubmitted or changed domain configuration")
	}
	contents, err := os.ReadFile(s.Paths().Catalog)
	if err != nil || !bytes.Contains(contents, []byte("adroute/work/coding")) {
		t.Fatalf("actual client catalog was not republished: %v", err)
	}
}

func TestManagementRoutingTableBindsMethodAndShape(t *testing.T) {
	s := newTestStore(t)
	gid := mustGroup(t, s, "work")
	mid := mustModel(t, s, ModelInput{GroupID: gid, Name: "coding", Model: "m", BaseURL: "https://synthetic.invalid/v1", APIKey: "k"})
	app := mustHTTPApp(HTTPOptions{Store: s})
	for _, tc := range []struct {
		name, method, path string
		body               any
		code               int
		want               string
	}{
		// A method the route table does not bind must not be answered by a
		// different method's handler.
		{"unknown path", http.MethodGet, "/api/nope", nil, 404, "NOT_FOUND"},
		{"unbound method on collection", http.MethodGet, "/api/groups", nil, 404, "NOT_FOUND"},
		{"unbound method on item", http.MethodPost, "/api/models/" + mid, nil, 404, "NOT_FOUND"},
		{"extra segment", http.MethodPut, "/api/models/" + mid + "/exposure/extra", nil, 404, "NOT_FOUND"},
		{"missed sub-resource", http.MethodPut, "/api/models/" + mid + "/unknown", nil, 404, "NOT_FOUND"},
		// A "|" choice binds both literals to the same handler.
		{"choice literal reaches aggregate", http.MethodPut, "/api/aggregates/" + mid + "/exposure", map[string]any{"enabled": true}, 404, "AGGREGATE_NOT_FOUND"},
		// A bound route still reaches its own validation, not a generic 404.
		{"bound route validates", http.MethodPut, "/api/models/" + mid + "/availability", map[string]any{}, 400, "INVALID_REQUEST"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := adminReq(t, app, tc.method, tc.path, tc.body)
			if w.Code != tc.code || (tc.want != "" && !strings.Contains(w.Body.String(), tc.want)) {
				t.Fatalf("%s %s = %d %s, want %d %s", tc.method, tc.path, w.Code, w.Body.String(), tc.code, tc.want)
			}
		})
	}
	// Pattern matching captures exactly one segment and honours "|" choices.
	if arg, ok := matchManagementPattern("api/models|aggregates/*/exposure", "api/aggregates/G1/exposure"); !ok || arg != "G1" {
		t.Fatalf("choice pattern captured %q %v", arg, ok)
	}
	if _, ok := matchManagementPattern("api/groups/*", "api/groups"); ok {
		t.Fatal("pattern matched a shorter path")
	}
	if _, ok := matchManagementPattern("api/groups|models/*", "api/aggregate/X"); ok {
		t.Fatal("choice pattern matched a wrong literal")
	}
}
