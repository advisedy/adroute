package adroute

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, e := os.Stat(path); e == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}

type codexFixture struct {
	root, home, codex, path string
	connect                 func(...CodexConfigOptions) (CodexConfigResult, error)
	restore                 func() (map[string]any, error)
}

func configFixture(t *testing.T, original string) codexFixture {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "adroute")
	codex := filepath.Join(root, "codex")
	if e := os.Mkdir(codex, 0700); e != nil {
		t.Fatal(e)
	}
	p := filepath.Join(codex, "config.toml")
	if e := os.WriteFile(p, []byte(original), 0600); e != nil {
		t.Fatal(e)
	}
	base := CodexConfigOptions{Home: home, CodexHome: codex, URL: "http://127.0.0.1:3001", ProxyToken: strings.Repeat("a", 64), Model: "synthetic-model", InstallationID: "synthetic-installation"}
	return codexFixture{root, home, codex, p, func(overrides ...CodexConfigOptions) (CodexConfigResult, error) {
		o := base
		if len(overrides) > 0 {
			o = overrides[0]
		}
		return ConnectCodex(o)
	}, func() (map[string]any, error) { return RestoreCodex(home) }}
}

func readConfigForTest(t *testing.T, p string) string {
	t.Helper()
	b, e := os.ReadFile(p)
	if e != nil {
		t.Fatal(e)
	}
	return string(b)
}

func hasCode(e error, code string) bool { var a *AppError; return errors.As(e, &a) && a.Code == code }

func decodeJournalForTest(t *testing.T, p string) map[string]any {
	t.Helper()
	b, e := os.ReadFile(p)
	if e != nil {
		t.Fatal(e)
	}
	var v map[string]any
	if e = json.Unmarshal(b, &v); e != nil {
		t.Fatal(e)
	}
	return v
}

func mustHTTPApp(o HTTPOptions) *HTTPApp {
	a, err := NewHTTPApp(o)
	if err != nil {
		panic(err)
	}
	return a
}
