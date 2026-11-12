//go:build windows

package adroute

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsOpenCodeSyncUsesUserProfile(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".config", "opencode")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "opencode.json")
	original := `{"providers":{"adroute":{"package":"@opencode/ai/providers/openai/responses","settings":{"baseURL":"http://127.0.0.1:3001/v1","apiKey":"synthetic"}}}}`
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := SyncOpenCode(OpenCodeSyncOptions{Env: map[string]string{"USERPROFILE": home}, Models: []SyncModel{{Slug: "adroute/work/model", DisplayName: "Model"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.ConfigPath != path {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestWindowsSyncRejectsDotSegmentsWithEitherSeparator(t *testing.T) {
	for _, path := range []string{`C:\config\..\secret.json`, `C:/config/../secret.json`, `C:\config/../secret.json`} {
		if validSyncPath(path) {
			t.Fatalf("unsafe path accepted: %s", path)
		}
	}
}
