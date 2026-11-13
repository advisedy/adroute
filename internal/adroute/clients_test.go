package adroute

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func clientsFixture(t *testing.T) (*HTTPApp, string, string) {
	t.Helper()
	s := newTestStore(t)
	gid := mustGroup(t, s, "work")
	mid := mustModel(t, s, ModelInput{GroupID: gid, Name: "coding", Model: "upstream", BaseURL: "https://clients.example/v1", APIKey: "synthetic"})
	if err := s.SetDefault(mid); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	codexHome := filepath.Join(home, ".codex")
	openCodeDir := filepath.Join(home, ".config", "opencode")
	if err := os.MkdirAll(openCodeDir, 0700); err != nil {
		t.Fatal(err)
	}
	config := `{"providers":{"adroute":{"package":"@opencode/ai/providers/openai/responses","settings":{"baseURL":"http://127.0.0.1:1/v1","apiKey":"synthetic"}}}}`
	if err := os.WriteFile(filepath.Join(openCodeDir, "opencode.jsonc"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	a := mustHTTPApp(HTTPOptions{Store: s, Port: 3001, Env: map[string]string{"HOME": home, "CODEX_HOME": codexHome}})
	return a, codexHome, filepath.Join(openCodeDir, "opencode.jsonc")
}

func clientStateOf(t *testing.T, w map[string]any, id string) map[string]any {
	t.Helper()
	list, _ := w["clients"].([]any)
	for _, raw := range list {
		c, _ := raw.(map[string]any)
		if c["id"] == id {
			return c
		}
	}
	t.Fatalf("client %s missing from %#v", id, w)
	return nil
}

func TestClientsListOnlySupportedClientsAndToggleBothWays(t *testing.T) {
	a, codexHome, openCodeConfig := clientsFixture(t)
	w := adminReq(t, a, http.MethodGet, "/api/clients", nil)
	if w.Code != 200 {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	var listed map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed["clients"].([]any)) != 2 {
		t.Fatalf("expected exactly codex and opencode: %#v", listed)
	}
	if c := clientStateOf(t, listed, ClientCodex); c["enabled"] != false || c["available"] != true {
		t.Fatalf("codex initial state %#v", c)
	}
	if c := clientStateOf(t, listed, ClientOpenCode); c["enabled"] != false || c["available"] != true {
		t.Fatalf("opencode initial state %#v", c)
	}

	for _, id := range []string{ClientCodex, ClientOpenCode} {
		w = adminReq(t, a, http.MethodPost, "/api/clients/"+id+"/enabled", map[string]bool{"enabled": true})
		if w.Code != 200 {
			t.Fatalf("%s enable: %d %s", id, w.Code, w.Body.String())
		}
		var on map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &on)
		if c := clientStateOf(t, on, id); c["enabled"] != true || c["stale"] != false {
			t.Fatalf("%s should be enabled and fresh: %#v", id, c)
		}
	}
	if raw, err := os.ReadFile(filepath.Join(codexHome, "config.toml")); err != nil || len(raw) == 0 {
		t.Fatalf("codex config not written: %v", err)
	}
	if raw, err := os.ReadFile(openCodeConfig); err != nil || !containsAll(string(raw), `"adroute/default"`, `"adroute/work/coding"`) {
		t.Fatalf("opencode models not written: %v %s", err, raw)
	}

	for _, id := range []string{ClientCodex, ClientOpenCode} {
		w = adminReq(t, a, http.MethodPost, "/api/clients/"+id+"/enabled", map[string]bool{"enabled": false})
		if w.Code != 200 {
			t.Fatalf("%s disable: %d %s", id, w.Code, w.Body.String())
		}
		var off map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &off)
		if c := clientStateOf(t, off, id); c["enabled"] != false {
			t.Fatalf("%s should be disabled: %#v", id, c)
		}
	}
	if raw, _ := os.ReadFile(openCodeConfig); containsAll(string(raw), `"adroute/work/coding"`) {
		t.Fatalf("opencode models should be cleared: %s", raw)
	}
	if raw, _ := os.ReadFile(openCodeConfig); !containsAll(string(raw), `"providers"`, `"@opencode/ai/providers/openai/responses"`) {
		t.Fatalf("opencode provider config must survive disable: %s", raw)
	}
}

func TestClientsDetectDriftAndRefresh(t *testing.T) {
	a, codexHome, _ := clientsFixture(t)
	if w := adminReq(t, a, http.MethodPost, "/api/clients/opencode/enabled", map[string]bool{"enabled": true}); w.Code != 200 {
		t.Fatalf("enable: %d %s", w.Code, w.Body.String())
	}
	if w := adminReq(t, a, http.MethodPost, "/api/clients/codex/enabled", map[string]bool{"enabled": true}); w.Code != 200 {
		t.Fatalf("enable codex: %d %s", w.Code, w.Body.String())
	}
	// Simulate a user edit that removed the AdRoute model selection.
	config := filepath.Join(codexHome, "config.toml")
	raw, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, []byte(removeLine(string(raw), "model = ")), 0600); err != nil {
		t.Fatal(err)
	}
	w := adminReq(t, a, http.MethodGet, "/api/clients", nil)
	var listed map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &listed)
	if c := clientStateOf(t, listed, ClientCodex); c["stale"] != true {
		t.Fatalf("codex drift not detected: %#v", c)
	}
	refreshedResponse := adminReq(t, a, http.MethodPost, "/api/clients/codex/refresh", map[string]any{})
	if refreshedResponse.Code != 200 {
		t.Fatalf("refresh: %d %s", refreshedResponse.Code, refreshedResponse.Body.String())
	}
	var refreshed map[string]any
	_ = json.Unmarshal(refreshedResponse.Body.Bytes(), &refreshed)
	if c := clientStateOf(t, refreshed, ClientCodex); c["stale"] != false || c["enabled"] != true {
		t.Fatalf("refresh should restore a fresh state: %#v", c)
	}
}

func TestClientsRejectUnknownClientAndBadPayload(t *testing.T) {
	a, _, _ := clientsFixture(t)
	if w := adminReq(t, a, http.MethodPost, "/api/clients/emacs/enabled", map[string]bool{"enabled": true}); w.Code != 404 {
		t.Fatalf("unknown client: %d %s", w.Code, w.Body.String())
	}
	if w := adminReq(t, a, http.MethodPost, "/api/clients/codex/enabled", map[string]any{}); w.Code != 400 {
		t.Fatalf("missing enabled: %d %s", w.Code, w.Body.String())
	}
	if w := adminReq(t, a, http.MethodPost, "/api/clients/opencode/refresh", map[string]any{}); w.Code != 409 {
		t.Fatalf("refresh must not bypass the off switch: %d %s", w.Code, w.Body.String())
	}
	for _, id := range []string{ClientCodex, ClientOpenCode} {
		if w := adminReq(t, a, http.MethodPost, "/api/clients/"+id+"/refresh", map[string]any{}); w.Code != 409 || !strings.Contains(w.Body.String(), "CLIENT_NOT_ENABLED") {
			t.Fatalf("%s refresh on a disabled client: %d %s", id, w.Code, w.Body.String())
		}
		if w := adminReq(t, a, http.MethodPost, "/api/clients/"+id+"/enabled", map[string]bool{"enabled": true}); w.Code != 200 {
			t.Fatalf("%s enable: %d %s", id, w.Code, w.Body.String())
		}
		if w := adminReq(t, a, http.MethodPost, "/api/clients/"+id+"/refresh", map[string]any{}); w.Code != 200 {
			t.Fatalf("%s refresh while enabled: %d %s", id, w.Code, w.Body.String())
		}
	}
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if !contains(s, p) {
			return false
		}
	}
	return true
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func removeLine(s, prefix string) string {
	out := ""
	for _, line := range splitLines(s) {
		if len(line) >= len(prefix) && line[:len(prefix)] == prefix {
			continue
		}
		out += line + "\n"
	}
	return out
}

func splitLines(s string) []string {
	lines := []string{}
	cur := ""
	for _, r := range s {
		if r == '\n' {
			lines = append(lines, cur)
			cur = ""
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}
