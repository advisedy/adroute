package adroute

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"encoding/json"
	"fmt"
	"github.com/andybalholm/brotli"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func adminReq(t *testing.T, a *HTTPApp, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reader = bytes.NewReader(b)
	}
	r := httptest.NewRequest(method, "http://127.0.0.1:3001"+path, reader)
	r.Host = "127.0.0.1:3001"
	r.Header.Set("Authorization", "Bearer "+a.opts.Store.TokenPair().AdminToken)
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	return w
}

func stateOf(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("state json: %v body=%s", err, w.Body.String())
	}
	return v
}

func TestManagementGroupAndModelCRUDExactShape(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			if r.Header.Get("Authorization") != "Bearer unsaved-draft-key" {
				t.Errorf("discovery must use the unsaved draft credential: %q", r.Header.Get("Authorization"))
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"data":[{"id":"z-model"},{"id":"a-model"},{"id":"z-model"}]}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer up.Close()
	s := newTestStore(t)
	a := mustHTTPApp(HTTPOptions{Store: s})

	w := adminReq(t, a, http.MethodPost, "/api/groups", map[string]string{"name": "work"})
	if w.Code != 201 {
		t.Fatalf("create group: %d %s", w.Code, w.Body.String())
	}
	state := stateOf(t, w)
	groups := state["groups"].([]any)
	if len(groups) != 1 || groups[0].(map[string]any)["name"] != "work" || groups[0].(map[string]any)["modelCount"].(float64) != 0 {
		t.Fatalf("group shape %#v", groups)
	}
	gid := groups[0].(map[string]any)["id"].(string)

	w = adminReq(t, a, http.MethodPost, "/api/models", map[string]any{"groupId": gid, "name": "coding", "model": "vendor/model", "baseUrl": up.URL + "/v1", "apiKey": "upstream-key"})
	if w.Code != 201 {
		t.Fatalf("create model: %d %s", w.Code, w.Body.String())
	}
	state = stateOf(t, w)
	models := state["models"].([]any)
	if len(models) != 1 {
		t.Fatalf("models %#v", models)
	}
	created := models[0].(map[string]any)
	mid := created["id"].(string)
	if created["routeId"] != "adroute/work/coding" || created["groupName"] != "work" || created["open"] != true || created["apiKey"] != "upstream-key" {
		t.Fatalf("created model %#v", created)
	}

	// Duplicate name in the same group is a stable conflict.
	if w = adminReq(t, a, http.MethodPost, "/api/models", map[string]any{"groupId": gid, "name": "coding", "model": "other", "baseUrl": up.URL + "/v1", "apiKey": "k2"}); w.Code != 409 || !strings.Contains(w.Body.String(), "MODEL_NAME_CONFLICT") {
		t.Fatalf("duplicate name: %d %s", w.Code, w.Body.String())
	}
	// Invalid public name is rejected and never rewritten.
	if w = adminReq(t, a, http.MethodPost, "/api/models", map[string]any{"groupId": gid, "name": "中文 名称", "model": "other", "baseUrl": up.URL + "/v1", "apiKey": "k2"}); w.Code != 400 || !strings.Contains(w.Body.String(), "MODEL_NAME_INVALID") {
		t.Fatalf("invalid name: %d %s", w.Code, w.Body.String())
	}
	// Unknown fields are rejected.
	if w = adminReq(t, a, http.MethodPost, "/api/models", map[string]any{"groupId": gid, "name": "x", "model": "m", "baseUrl": up.URL + "/v1", "apiKey": "k", "slug": "legacy"}); w.Code != 400 {
		t.Fatalf("unknown field accepted: %d %s", w.Code, w.Body.String())
	}

	// Discovery uses the unsaved draft URL/key and saves nothing.
	w = adminReq(t, a, http.MethodPost, "/api/models/discover", map[string]string{"baseUrl": up.URL + "/v1", "apiKey": "unsaved-draft-key"})
	if w.Code != 200 {
		t.Fatalf("discover: %d %s", w.Code, w.Body.String())
	}
	var discovery struct {
		Models []struct {
			ID string `json:"id"`
		} `json:"models"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &discovery); err != nil {
		t.Fatal(err)
	}
	if len(discovery.Models) != 2 || discovery.Models[0].ID != "a-model" || discovery.Models[1].ID != "z-model" {
		t.Fatalf("discovery %#v", discovery.Models)
	}
	if len(s.Snapshot().Models) != 1 {
		t.Fatal("discovery persisted models")
	}

	// Editing the connection keeps the public id and the entry id.
	w = adminReq(t, a, http.MethodPut, "/api/models/"+mid, map[string]any{"groupId": gid, "name": "coding", "model": "vendor/other", "baseUrl": up.URL + "/v1", "apiKey": "rotated"})
	if w.Code != 200 {
		t.Fatalf("update: %d %s", w.Code, w.Body.String())
	}
	route, err := s.Resolve("adroute/work/coding")
	if err != nil || route.ID != mid || route.APIKey != "rotated" || route.Model != "vendor/other" {
		t.Fatalf("update identity %#v %v", route, err)
	}

	// Renaming changes the public id.
	w = adminReq(t, a, http.MethodPut, "/api/models/"+mid, map[string]any{"groupId": gid, "name": "renamed", "model": "vendor/other", "baseUrl": up.URL + "/v1"})
	if w.Code != 200 {
		t.Fatalf("rename: %d %s", w.Code, w.Body.String())
	}
	if _, err := s.Resolve("adroute/work/renamed"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Resolve("adroute/work/coding"); ErrorStatus(err) != 404 {
		t.Fatalf("old id still resolves: %v", err)
	}

	w = adminReq(t, a, http.MethodDelete, "/api/models/"+mid, nil)
	if w.Code != 200 || len(stateOf(t, w)["models"].([]any)) != 0 {
		t.Fatalf("delete model: %d %s", w.Code, w.Body.String())
	}
	if w = adminReq(t, a, http.MethodDelete, "/api/groups/"+gid, nil); w.Code != 200 {
		t.Fatalf("delete empty group: %d %s", w.Code, w.Body.String())
	}
	if w = adminReq(t, a, http.MethodDelete, "/api/groups/"+gid, nil); w.Code != 404 {
		t.Fatalf("delete missing group: %d %s", w.Code, w.Body.String())
	}
}

func TestManagementModelGroupErrors(t *testing.T) {
	s := newTestStore(t)
	a := mustHTTPApp(HTTPOptions{Store: s})
	if w := adminReq(t, a, http.MethodPost, "/api/groups", map[string]string{"name": "Work Group"}); w.Code != 400 || !strings.Contains(w.Body.String(), "GROUP_NAME_INVALID") {
		t.Fatalf("invalid group name: %d %s", w.Code, w.Body.String())
	}
	if w := adminReq(t, a, http.MethodPost, "/api/groups", map[string]string{"name": "work"}); w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	gid := stateOf(t, adminReq(t, a, http.MethodGet, "/api/state", nil))["groups"].([]any)[0].(map[string]any)["id"].(string)
	if w := adminReq(t, a, http.MethodPost, "/api/groups", map[string]string{"name": "work"}); w.Code != 409 {
		t.Fatalf("duplicate group: %d %s", w.Code, w.Body.String())
	}
	if w := adminReq(t, a, http.MethodPost, "/api/models", map[string]any{"groupId": "missing", "name": "m", "model": "m", "baseUrl": "https://a.example/v1", "apiKey": "k"}); w.Code != 404 || !strings.Contains(w.Body.String(), "GROUP_NOT_FOUND") {
		t.Fatalf("missing group: %d %s", w.Code, w.Body.String())
	}
	if w := adminReq(t, a, http.MethodPost, "/api/models", map[string]any{"groupId": gid, "name": "m", "model": "m", "baseUrl": "https://a.example/v1", "apiKey": ""}); w.Code != 400 || !strings.Contains(w.Body.String(), "UPSTREAM_KEY_INVALID") {
		t.Fatalf("blank credential: %d %s", w.Code, w.Body.String())
	}
	if w := adminReq(t, a, http.MethodPost, "/api/models", map[string]any{"groupId": gid, "name": "m", "model": "m", "baseUrl": "https://a.example/v1/responses", "apiKey": "k"}); w.Code != 400 {
		t.Fatalf("bad base url: %d %s", w.Code, w.Body.String())
	}
	if w := adminReq(t, a, http.MethodPost, "/api/models", map[string]any{"groupId": gid, "name": "m", "model": "m", "baseUrl": "https://a.example/v1", "apiKey": "k"}); w.Code != 201 {
		t.Fatalf("create model: %d %s", w.Code, w.Body.String())
	}
	if w := adminReq(t, a, http.MethodDelete, "/api/groups/"+gid, nil); w.Code != 409 || !strings.Contains(w.Body.String(), "GROUP_NOT_EMPTY") {
		t.Fatalf("non-empty group delete: %d %s", w.Code, w.Body.String())
	}
	if w := adminReq(t, a, http.MethodPut, "/api/groups/"+gid, map[string]string{"name": "renamed"}); w.Code != 200 {
		t.Fatalf("rename group: %d %s", w.Code, w.Body.String())
	}
	if w := adminReq(t, a, http.MethodGet, "/api/state", nil); !strings.Contains(w.Body.String(), `"adroute/renamed/m"`) {
		t.Fatalf("renamed group route id: %s", w.Body.String())
	}
}

func TestDefaultEndpointSelectsOnlyOpenModels(t *testing.T) {
	s := newTestStore(t)
	gid := mustGroup(t, s, "work")
	a := mustModel(t, s, ModelInput{GroupID: gid, Name: "a", Model: "a", BaseURL: "https://a.example/v1", APIKey: "k"})
	b := mustModel(t, s, ModelInput{GroupID: gid, Name: "b", Model: "b", BaseURL: "https://b.example/v1", APIKey: "k"})
	app := mustHTTPApp(HTTPOptions{Store: s})
	if w := adminReq(t, app, http.MethodPost, "/api/default", map[string]any{"modelId": a}); w.Code != 200 {
		t.Fatalf("set default: %d %s", w.Code, w.Body.String())
	}
	if s.Snapshot().DefaultModelID != a {
		t.Fatal("default not stored")
	}
	if err := s.SetModelEnabled(b, false); err != nil {
		t.Fatal(err)
	}
	if w := adminReq(t, app, http.MethodPost, "/api/default", map[string]any{"modelId": b}); w.Code != 403 {
		t.Fatalf("closed default accepted: %d %s", w.Code, w.Body.String())
	}
	if w := adminReq(t, app, http.MethodPost, "/api/default", map[string]any{"modelId": nil}); w.Code != 200 || s.Snapshot().DefaultModelID != "" {
		t.Fatalf("clear default: %d %s", w.Code, w.Body.String())
	}
	if w := adminReq(t, app, http.MethodPost, "/api/default", map[string]any{"modelId": "missing"}); w.Code != 404 {
		t.Fatalf("missing default: %d %s", w.Code, w.Body.String())
	}
}

func TestManagementCompatibilityConsentAndLimits(t *testing.T) {
	s := newTestStore(t)
	gid := mustGroup(t, s, "work")
	mid := mustModel(t, s, ModelInput{GroupID: gid, Name: "m", Model: "m", BaseURL: "https://a.example/v1", APIKey: "synthetic"})
	root, _ := filepath.Abs("../..")
	a := mustHTTPApp(HTTPOptions{Store: s, PromptRoot: root, Assets: os.DirFS(root)})
	if w := adminReq(t, a, http.MethodPost, "/api/compatibility/diagnosis", map[string]any{"modelId": mid, "raw": true, "consent": false}); w.Code != 400 || !strings.Contains(w.Body.String(), "DIAGNOSIS_CONSENT_REQUIRED") {
		t.Fatalf("raw without consent: %d %s", w.Code, w.Body.String())
	}
	if w := adminReq(t, a, http.MethodPost, "/api/compatibility/diagnosis", map[string]any{"modelId": mid, "raw": false, "consent": false}); w.Code != 200 {
		t.Fatalf("arm diagnosis: %d %s", w.Code, w.Body.String())
	}
	if w := adminReq(t, a, http.MethodDelete, "/api/compatibility/diagnosis", nil); w.Code != 200 {
		t.Fatalf("clear diagnosis: %d %s", w.Code, w.Body.String())
	}
	pack := AdapterPackage{SchemaVersion: 1, ID: "synthetic-pack", Version: "1.0.0", Name: "Pack", Description: "synthetic", Engine: "responses-tools-v1", Options: AdapterOptions{}, Fixtures: []AdapterFixture{{Name: "fixture", Events: []json.RawMessage{json.RawMessage(`{"type":"response.completed","response":{"status":"completed"}}`)}, ExpectedCalls: []AdapterCall{}}}}
	body, _ := json.Marshal(pack)
	if w := adminReq(t, a, http.MethodPost, "/api/compatibility/verify", json.RawMessage(body)); w.Code != 200 {
		t.Fatalf("verify: %d %s", w.Code, w.Body.String())
	}
	if w := adminReq(t, a, http.MethodPost, "/api/compatibility/packages", json.RawMessage(body)); w.Code != 201 {
		t.Fatalf("import: %d %s", w.Code, w.Body.String())
	}
	if w := adminReq(t, a, http.MethodPut, "/api/compatibility/selection/"+mid, map[string]string{"profile": "synthetic-pack@1.0.0"}); w.Code != 200 {
		t.Fatalf("selection: %d %s", w.Code, w.Body.String())
	}
	if w := adminReq(t, a, http.MethodPut, "/api/compatibility/selection/"+mid, map[string]string{"profile": "missing@1.0.0"}); w.Code != 400 {
		t.Fatalf("missing adapter selection: %d %s", w.Code, w.Body.String())
	}
	if w := adminReq(t, a, http.MethodPut, "/api/compatibility/selection/missing", map[string]string{"profile": "none"}); w.Code != 404 {
		t.Fatalf("missing model selection: %d %s", w.Code, w.Body.String())
	}
	w := adminReq(t, a, http.MethodPost, "/api/compatibility/prompt", map[string]any{"modelId": mid, "includeDiagnosis": false})
	if w.Code != 200 || !strings.Contains(w.Body.String(), "synthetic-pack") && !strings.Contains(w.Body.String(), "prompt") {
		t.Fatalf("prompt: %d %s", w.Code, w.Body.String())
	}
	if w := adminReq(t, a, http.MethodGet, "/api/compatibility/template", nil); w.Code != 200 || !strings.Contains(w.Body.String(), "pixel-swe") {
		t.Fatalf("template=%d %s", w.Code, w.Body.String())
	}
	if w := adminReq(t, a, http.MethodPost, "/api/compatibility/refresh", map[string]any{}); w.Code != 200 || !strings.Contains(w.Body.String(), "directoryStatus") {
		t.Fatalf("refresh=%d %s", w.Code, w.Body.String())
	}
}

func TestShutdownRequiresMatchingInstanceAndAdminToken(t *testing.T) {
	s := newTestStore(t)
	called := make(chan struct{}, 1)
	a := mustHTTPApp(HTTPOptions{Store: s, InstanceID: "run-1", Shutdown: func() { called <- struct{}{} }})
	r := httptest.NewRequest("POST", "http://127.0.0.1:3001/internal/shutdown", nil)
	r.Host = "127.0.0.1:3001"
	r.Header.Set("Authorization", "Bearer "+s.TokenPair().AdminToken)
	r.Header.Set("X-Adroute-Instance", "other")
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	if w.Code != 409 || len(called) > 0 {
		t.Fatalf("mismatched shutdown status=%d", w.Code)
	}
	r = httptest.NewRequest("POST", "http://127.0.0.1:3001/internal/shutdown", nil)
	r.Host = "127.0.0.1:3001"
	r.Header.Set("Authorization", "Bearer "+s.TokenPair().AdminToken)
	r.Header.Set("X-Adroute-Instance", "run-1")
	w = httptest.NewRecorder()
	a.ServeHTTP(w, r)
	if w.Code != 202 {
		t.Fatalf("shutdown=%d %s", w.Code, w.Body.String())
	}
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("shutdown callback missing")
	}
}

func TestHTTPRejectsHostAndSeparatesAdminAndProxyTokens(t *testing.T) {
	s := newTestStore(t)
	app := mustHTTPApp(HTTPOptions{Store: s})
	r := httptest.NewRequest("GET", "http://evil.test/api/state", nil)
	r.Host = "evil.test"
	w := httptest.NewRecorder()
	app.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("host status=%d body=%s", w.Code, w.Body.String())
	}
	r = httptest.NewRequest("GET", "http://127.0.0.1:3001/api/state", nil)
	r.Host = "127.0.0.1:3001"
	r.Header.Set("Origin", "http://localhost:3001/path")
	r.Header.Set("Authorization", "Bearer "+s.TokenPair().AdminToken)
	w = httptest.NewRecorder()
	app.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("non-origin URL path accepted: %d", w.Code)
	}
	r = httptest.NewRequest("GET", "http://127.0.0.1:3001/v1/models", nil)
	r.Host = "127.0.0.1:3001"
	r.Header.Set("Authorization", "Bearer "+s.TokenPair().AdminToken)
	w = httptest.NewRecorder()
	app.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("admin token accepted for proxy: %d", w.Code)
	}
	// The proxy token must be able to read the public list but not manage config.
	r = httptest.NewRequest("GET", "http://127.0.0.1:3001/v1/models", nil)
	r.Host = "127.0.0.1:3001"
	r.Header.Set("Authorization", "Bearer "+s.TokenPair().ProxyToken)
	w = httptest.NewRecorder()
	app.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("proxy token list status=%d", w.Code)
	}
	r = httptest.NewRequest("GET", "http://127.0.0.1:3001/api/state", nil)
	r.Host = "127.0.0.1:3001"
	r.Header.Set("Authorization", "Bearer "+s.TokenPair().ProxyToken)
	w = httptest.NewRecorder()
	app.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("proxy token reached management: %d", w.Code)
	}
}

func TestDecodeBodySupportedContentEncodings(t *testing.T) {
	const raw = `{"model":"m"}`
	encoders := map[string]func() []byte{
		"identity": func() []byte { return []byte(raw) },
		"gzip": func() []byte {
			var b bytes.Buffer
			z := gzip.NewWriter(&b)
			_, _ = io.WriteString(z, raw)
			_ = z.Close()
			return b.Bytes()
		},
		"deflate": func() []byte {
			var b bytes.Buffer
			z, _ := zlib.NewWriterLevel(&b, flate.DefaultCompression)
			_, _ = io.WriteString(z, raw)
			_ = z.Close()
			return b.Bytes()
		},
		"br": func() []byte {
			var b bytes.Buffer
			z := brotli.NewWriter(&b)
			_, _ = io.WriteString(z, raw)
			_ = z.Close()
			return b.Bytes()
		},
	}
	for coding, encode := range encoders {
		t.Run(coding, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(encode()))
			r.Header.Set("Content-Encoding", coding)
			got, e := decodeBody(r, 1024)
			if e != nil || string(got) != raw {
				t.Fatalf("decode=%q err=%v", got, e)
			}
		})
	}
}

func TestHTTPUnknownAPIEndpoint(t *testing.T) {
	s := newTestStore(t)
	app := mustHTTPApp(HTTPOptions{Store: s})
	if w := adminReq(t, app, http.MethodGet, "/api/nope", nil); w.Code != 404 {
		t.Fatalf("unknown endpoint status %d", w.Code)
	}
}

func TestModelWebsiteIsOptionalValidatedAndExposed(t *testing.T) {
	s := newTestStore(t)
	gid := mustGroup(t, s, "work")
	a := mustHTTPApp(HTTPOptions{Store: s})
	base := map[string]any{"groupId": gid, "name": "site", "model": "m", "baseUrl": "https://a.example/v1", "apiKey": "k"}

	// Omitted: saved with no website, so the UI keeps the button inert.
	body := map[string]any{}
	for k, v := range base {
		body[k] = v
	}
	if w := adminReq(t, a, http.MethodPost, "/api/models", body); w.Code != 201 {
		t.Fatalf("create without website: %d %s", w.Code, w.Body.String())
	}
	if got := stateOf(t, adminReq(t, a, http.MethodGet, "/api/state", nil))["models"].([]any)[0].(map[string]any)["website"]; got != "" {
		t.Fatalf("website should be empty, got %#v", got)
	}

	// Rejected schemes and non-URLs never reach the store.
	for _, bad := range []string{"ftp://example.com/files", "example.com", "javascript:alert(1)", "https://"} {
		body["name"], body["website"] = "bad", bad
		if w := adminReq(t, a, http.MethodPost, "/api/models", body); w.Code != 400 || !strings.Contains(w.Body.String(), "网站") {
			t.Fatalf("website %q: %d %s", bad, w.Code, w.Body.String())
		}
	}

	// Valid website is trimmed of a trailing slash and round-trips in state.
	body["name"], body["website"] = "valid", "https://example.com/models/"
	if w := adminReq(t, a, http.MethodPost, "/api/models", body); w.Code != 201 {
		t.Fatalf("create with website: %d %s", w.Code, w.Body.String())
	}
	var found map[string]any
	for _, raw := range stateOf(t, adminReq(t, a, http.MethodGet, "/api/state", nil))["models"].([]any) {
		model := raw.(map[string]any)
		if model["name"] == "valid" {
			found = model
		}
	}
	if found == nil || found["website"] != "https://example.com/models" {
		t.Fatalf("website not exposed: %#v", found)
	}

	// Copy keeps the website; update can clear it.
	id := found["id"].(string)
	if w := adminReq(t, a, http.MethodPost, "/api/models/"+id+"/copy", nil); w.Code != 201 {
		t.Fatalf("copy: %d %s", w.Code, w.Body.String())
	}
	copied := 0
	for _, raw := range stateOf(t, adminReq(t, a, http.MethodGet, "/api/state", nil))["models"].([]any) {
		model := raw.(map[string]any)
		if model["website"] == "https://example.com/models" {
			copied++
		}
	}
	if copied != 2 {
		t.Fatalf("copy should keep website, found %d entries", copied)
	}
	if w := adminReq(t, a, http.MethodPut, "/api/models/"+id, map[string]any{"groupId": gid, "name": "valid", "model": "m", "baseUrl": "https://a.example/v1", "website": ""}); w.Code != 200 {
		t.Fatalf("clear website: %d %s", w.Code, w.Body.String())
	}
	for _, raw := range stateOf(t, adminReq(t, a, http.MethodGet, "/api/state", nil))["models"].([]any) {
		model := raw.(map[string]any)
		if model["id"] == id && model["website"] != "" {
			t.Fatalf("website should be cleared, got %#v", model["website"])
		}
	}
}
