package adroute

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAdapterReplayFailureOnPersistedConfigIsRejectedWithoutRewrite(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	s, err := OpenStore(home)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join("..", "..", "examples", "pixel-swe.adroute-adapter.json"))
	if err != nil {
		t.Fatal(err)
	}
	var pack AdapterPackage
	if err = json.Unmarshal(b, &pack); err != nil {
		t.Fatal(err)
	}
	pack.Fixtures[0].ExpectedCalls[0].CallID = "wrong-call-id"
	raw, err := json.Marshal(pack)
	if err != nil {
		t.Fatal(err)
	}
	state := s.Snapshot()
	state.Adapters = []json.RawMessage{raw}
	if err = s.persist(state); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "config.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = OpenStore(home); err == nil {
		t.Fatal("replay-failing persisted package was accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("invalid config bytes were changed")
	}
}

func TestInitialCatalogFailureIsReturnedByHTTPAppConstruction(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "home"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(s.Paths().Home, "codex"), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = NewHTTPApp(HTTPOptions{Store: s}); err == nil {
		t.Fatal("catalog startup failure was swallowed")
	}
}

func TestLoadTokensIsIndependentAndNeverCreatesMissingTokenFile(t *testing.T) {
	home := t.TempDir()
	if _, err := LoadTokens(home); err == nil {
		t.Fatal("missing token file accepted")
	}
	if _, err := os.Stat(filepath.Join(home, "tokens.json")); !os.IsNotExist(err) {
		t.Fatalf("token file was created: %v", err)
	}
}

func mustHTTPApp(o HTTPOptions) *HTTPApp {
	a, err := NewHTTPApp(o)
	if err != nil {
		panic(err)
	}
	return a
}
