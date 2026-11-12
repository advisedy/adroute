package adroute

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

func TestRandomHexPropagatesEntropyFailures(t *testing.T) {
	want := errors.New("entropy unavailable")
	if _, err := randomHexFrom(failingReader{want}, 32); !errors.Is(err, want) {
		t.Fatalf("randomHexFrom error=%v want %v", err, want)
	}
	if _, err := randomHexFrom(io.LimitReader(strings.NewReader("short"), 2), 32); err == nil {
		t.Fatal("short random source accepted")
	}
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(filepath.Join(t.TempDir(), "home"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func mustGroup(t *testing.T, s *Store, name string) string {
	t.Helper()
	id, err := s.CreateGroup(GroupInput{Name: name})
	if err != nil {
		t.Fatalf("create group %q: %v", name, err)
	}
	return id
}

func mustModel(t *testing.T, s *Store, in ModelInput) string {
	t.Helper()
	id, err := s.CreateModel(in)
	if err != nil {
		t.Fatalf("create model %#v: %v", in, err)
	}
	return id
}

func TestFailedConfigCommitDoesNotPublishCatalog(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	s, err := OpenStore(home)
	if err != nil {
		t.Fatal(err)
	}
	gid := mustGroup(t, s, "work")
	mustModel(t, s, ModelInput{GroupID: gid, Name: "bound", Model: "upstream", BaseURL: "https://committed.example/v1", APIKey: "synthetic"})
	if err = s.EnableCatalog(); err != nil {
		t.Fatal(err)
	}
	beforeState := s.Snapshot()
	beforeConfig, err := os.ReadFile(s.Paths().Config)
	if err != nil {
		t.Fatal(err)
	}
	beforeCatalog, err := os.ReadFile(s.Paths().Catalog)
	if err != nil {
		t.Fatal(err)
	}
	s.writeConfig = func(string, []byte) error { return errors.New("injected config commit failure") }
	if _, err = s.CreateModel(ModelInput{GroupID: gid, Name: "uncommitted", Model: "upstream", BaseURL: "https://committed.example/v1", APIKey: "synthetic"}); err == nil {
		t.Fatal("injected config write failure was ignored")
	}
	got := s.Snapshot()
	gotJSON, _ := json.Marshal(got)
	beforeJSON, _ := json.Marshal(beforeState)
	if string(gotJSON) != string(beforeJSON) {
		t.Fatalf("memory state changed after failed config commit: %#v", got)
	}
	afterConfig, _ := os.ReadFile(s.Paths().Config)
	if string(afterConfig) != string(beforeConfig) {
		t.Fatal("config changed despite failed commit")
	}
	afterCatalog, _ := os.ReadFile(s.Paths().Catalog)
	if string(afterCatalog) != string(beforeCatalog) {
		t.Fatal("catalog published uncommitted routes")
	}
}

func TestPostCommitCatalogFailureIsReportedAndRetryable(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	s, err := OpenStore(home)
	if err != nil {
		t.Fatal(err)
	}
	gid := mustGroup(t, s, "work")
	if err = s.EnableCatalog(); err != nil {
		t.Fatal(err)
	}
	catalog := s.Paths().Catalog
	if err = os.Remove(catalog); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(catalog, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateModel(ModelInput{GroupID: gid, Name: "committed-route", Model: "upstream", BaseURL: "https://committed.example/v1", APIKey: "synthetic"}); err != nil {
		t.Fatalf("committed mutation returned publication failure: %v", err)
	}
	if s.Snapshot().Revision != 2 || len(s.Snapshot().Models) != 1 {
		t.Fatal("config mutation did not commit")
	}
	pending, publishErr := s.CatalogPublicationStatus()
	if !pending || publishErr == nil {
		t.Fatalf("pending=%t error=%v", pending, publishErr)
	}
	if err = os.Remove(catalog); err != nil {
		t.Fatal(err)
	}
	if err = s.RetryCatalogPublication(); err != nil {
		t.Fatal(err)
	}
	pending, publishErr = s.CatalogPublicationStatus()
	if pending || publishErr != nil {
		t.Fatalf("retry status pending=%t error=%v", pending, publishErr)
	}
	bytes, err := os.ReadFile(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(bytes), "adroute/work/committed-route") {
		t.Fatalf("retry missed committed route: %s", bytes)
	}
}

func TestStoreOpenSnapshotAndResolve(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	s, err := OpenStore(home)
	if err != nil {
		t.Fatal(err)
	}
	state := s.Snapshot()
	if state.Version != ConfigVersion || len(state.Groups) != 0 || len(state.Models) != 0 || state.DefaultModelID != "" {
		t.Fatalf("initial state: %#v", state)
	}
	state.Groups = append(state.Groups, Group{ID: "mutated", Name: "mutated"})
	if len(s.Snapshot().Groups) != 0 {
		t.Fatal("snapshot aliases store state")
	}
	gid := mustGroup(t, s, "work")
	mid := mustModel(t, s, ModelInput{GroupID: gid, Name: "coding", Model: "vendor/model", BaseURL: "https://up.example/v1", APIKey: "synthetic-key"})
	route, err := s.Resolve("adroute/work/coding")
	if err != nil || route.Model != "vendor/model" || route.APIKey != "synthetic-key" || route.ID != mid {
		t.Fatalf("resolve = %#v, %v", route, err)
	}
	if route.RouteID != "adroute/work/coding" || route.GroupName != "work" || route.ModelName != "coding" {
		t.Fatalf("public identity %#v", route)
	}
	s2, err := OpenStore(home)
	if err != nil {
		t.Fatal(err)
	}
	if s2.Snapshot().Revision != 2 {
		t.Fatalf("revision %d", s2.Snapshot().Revision)
	}
}

func TestConcurrentMutationsSerialize(t *testing.T) {
	s := newTestStore(t)
	gid := mustGroup(t, s, "work")
	const n = 24
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := s.CreateModel(ModelInput{GroupID: gid, Name: fmt.Sprintf("m-%d", i), Model: "upstream", BaseURL: "https://up.example/v1", APIKey: "synthetic"}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	got := s.Snapshot()
	if int(got.Revision) != n+1 || len(got.Models) != n {
		t.Fatalf("revision/models=%d/%d", got.Revision, len(got.Models))
	}
}

// Two concurrent create requests for the same group/name may only have one
// winner; uniqueness is enforced inside the persistence mutation.
func TestConcurrentDuplicateModelNamesOnlyOneWins(t *testing.T) {
	s := newTestStore(t)
	gid := mustGroup(t, s, "work")
	const n = 16
	var wg sync.WaitGroup
	var mu sync.Mutex
	created := 0
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.CreateModel(ModelInput{GroupID: gid, Name: "same", Model: "upstream", BaseURL: "https://up.example/v1", APIKey: "synthetic"})
			mu.Lock()
			if err == nil {
				created++
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	if created != 1 {
		t.Fatalf("created=%d, want exactly 1", created)
	}
}

func TestInvalidConfigIsPreserved(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "config.json")
	for _, data := range [][]byte{
		[]byte(`{"version":99}`),
		[]byte(`{"version":4,"revision":0,"providers":[],"active":null}`),
	} {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenStore(home); err == nil {
			t.Fatalf("expected invalid config error for %s", data)
		}
		got, _ := os.ReadFile(path)
		if string(got) != string(data) {
			t.Fatal("invalid config overwritten")
		}
	}
}

func TestStrictDecodeRejectsUnknownFields(t *testing.T) {
	var s State
	if json.Unmarshal([]byte(`{"version":5,"wat":1}`), &s) == nil {
		t.Fatal("unknown field accepted")
	}
	var m Model
	if json.Unmarshal([]byte(`{"id":"a","groupId":"b","name":"c","model":"d","baseUrl":"https://x.example/v1","apiKey":"k","profile":"none","extra":true}`), &m) == nil {
		t.Fatal("unknown model field accepted")
	}
	if json.Unmarshal([]byte(`{"version":5,"revision":0,"groups":[],"models":[],"defaultModelId":null}`), &s) == nil {
		t.Fatal("null default id accepted")
	}
}

func TestGroupLifecycleAndStrictNames(t *testing.T) {
	s := newTestStore(t)
	gid := mustGroup(t, s, "work")
	for _, bad := range []string{"", "Work", "wo rk", "work_group", "work/group", "-work", "work-", "work--group", "工作"} {
		if _, err := s.CreateGroup(GroupInput{Name: bad}); ErrorStatus(err) != 400 {
			t.Errorf("group name %q status=%v err=%v", bad, ErrorStatus(err), err)
		}
	}
	if _, err := s.CreateGroup(GroupInput{Name: "work"}); ErrorStatus(err) != 409 {
		t.Fatalf("duplicate group status=%v err=%v", ErrorStatus(err), err)
	}
	if err := s.RenameGroup(gid, GroupInput{Name: "Work"}); ErrorStatus(err) != 400 {
		t.Fatalf("rename to invalid name status=%v", ErrorStatus(err))
	}
	mustModel(t, s, ModelInput{GroupID: gid, Name: "coding", Model: "upstream", BaseURL: "https://up.example/v1", APIKey: "synthetic"})
	if err := s.DeleteGroup(gid); ErrorStatus(err) != 409 {
		t.Fatalf("non-empty group delete status=%v err=%v", ErrorStatus(err), err)
	}
	empty := mustGroup(t, s, "empty")
	if err := s.DeleteGroup(empty); err != nil {
		t.Fatal(err)
	}
	if err := s.RenameGroup(gid, GroupInput{Name: "renamed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Resolve("adroute/renamed/coding"); err != nil {
		t.Fatalf("renamed group route: %v", err)
	}
	if _, err := s.Resolve("adroute/work/coding"); ErrorStatus(err) != 404 {
		t.Fatalf("old group route status=%v", ErrorStatus(err))
	}
}

func TestModelNameRulesAndUniqueness(t *testing.T) {
	s := newTestStore(t)
	a := mustGroup(t, s, "alpha")
	b := mustGroup(t, s, "beta")
	for _, bad := range []string{"", "Cod ing", "编程", "vendor/model", "a b", "dot only ok."} {
		if bad == "dot only ok." {
			continue
		}
		if _, err := s.CreateModel(ModelInput{GroupID: a, Name: bad, Model: "upstream", BaseURL: "https://up.example/v1", APIKey: "synthetic"}); ErrorStatus(err) != 400 {
			t.Errorf("model name %q status=%v err=%v", bad, ErrorStatus(err), err)
		}
	}
	first := mustModel(t, s, ModelInput{GroupID: a, Name: "coding", Model: "vendor/model", BaseURL: "https://up.example/v1", APIKey: "synthetic"})
	if _, err := s.CreateModel(ModelInput{GroupID: a, Name: "coding", Model: "other", BaseURL: "https://other.example/v1", APIKey: "synthetic-2"}); ErrorStatus(err) != 409 {
		t.Fatalf("duplicate name status=%v err=%v", ErrorStatus(err), err)
	}
	if _, err := s.CreateModel(ModelInput{GroupID: b, Name: "coding", Model: "other", BaseURL: "https://other.example/v1", APIKey: "synthetic-2"}); err != nil {
		t.Fatalf("same name in another group should be allowed: %v", err)
	}
	if _, err := s.CreateModel(ModelInput{GroupID: a, Name: "Coding", Model: "other", BaseURL: "https://other.example/v1", APIKey: "synthetic-3"}); err != nil {
		t.Fatalf("names compare case-sensitively and exactly: %v", err)
	}
	// A closed entry keeps occupying its name.
	if err := s.SetModelEnabled(first, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateModel(ModelInput{GroupID: a, Name: "coding", Model: "third", BaseURL: "https://third.example/v1", APIKey: "synthetic-4"}); ErrorStatus(err) != 409 {
		t.Fatalf("closed entry name was reused: %v", err)
	}
	// Same real upstream id with a different name is allowed and fully separate.
	second := mustModel(t, s, ModelInput{GroupID: a, Name: "coding-alt", Model: "vendor/model", BaseURL: "https://alt.example/v1", APIKey: "synthetic-5"})
	r1, _ := s.ModelRoute(first)
	r2, _ := s.ModelRoute(second)
	if r1.APIKey == r2.APIKey || r1.BaseURL == r2.BaseURL {
		t.Fatalf("entries share connection settings: %#v %#v", r1, r2)
	}
	// Editing one entry's credential must not touch the other.
	if err := s.UpdateModel(second, ModelInput{GroupID: a, Name: "coding-alt", Model: "vendor/model", BaseURL: "https://alt.example/v1", APIKey: "rotated"}); err != nil {
		t.Fatal(err)
	}
	after, _ := s.ModelRoute(first)
	if after.APIKey != "synthetic" {
		t.Fatalf("unrelated entry credential changed: %#v", after)
	}
}

func TestUpstreamEditsKeepIdentityAndRouteUnlessNameChanges(t *testing.T) {
	s := newTestStore(t)
	a := mustGroup(t, s, "work")
	id := mustModel(t, s, ModelInput{GroupID: a, Name: "coding", Model: "vendor/one", BaseURL: "https://one.example/v1", APIKey: "key-one"})
	if err := s.UpdateModel(id, ModelInput{GroupID: a, Name: "coding", Model: "vendor/two", BaseURL: "https://two.example/v1", APIKey: "key-two"}); err != nil {
		t.Fatal(err)
	}
	r, err := s.Resolve("adroute/work/coding")
	if err != nil || r.ID != id || r.Model != "vendor/two" || r.APIKey != "key-two" || r.BaseURL != "https://two.example/v1" {
		t.Fatalf("upstream-only edit changed identity or route: %#v %v", r, err)
	}
	// Omitted apiKey keeps the stored secret.
	if err := s.UpdateModel(id, ModelInput{GroupID: a, Name: "coding", Model: "vendor/three", BaseURL: "https://three.example/v1"}); err != nil {
		t.Fatal(err)
	}
	r, _ = s.ModelRoute(id)
	if r.APIKey != "key-two" || r.Model != "vendor/three" {
		t.Fatalf("omitted apiKey was not preserved: %#v", r)
	}
	if err := s.UpdateModel(id, ModelInput{GroupID: a, Name: "renamed", Model: "vendor/three", BaseURL: "https://three.example/v1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Resolve("adroute/work/renamed"); err != nil {
		t.Fatalf("renamed route: %v", err)
	}
	if _, err := s.Resolve("adroute/work/coding"); ErrorStatus(err) != 404 {
		t.Fatalf("old route status=%v", ErrorStatus(err))
	}
	// Moving to another group changes the public id.
	other := mustGroup(t, s, "other")
	if err := s.UpdateModel(id, ModelInput{GroupID: other, Name: "renamed", Model: "vendor/three", BaseURL: "https://three.example/v1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Resolve("adroute/other/renamed"); err != nil {
		t.Fatalf("moved route: %v", err)
	}
	// Moving into a group that already owns the name is a conflict, not a rename.
	mustModel(t, s, ModelInput{GroupID: a, Name: "renamed", Model: "other-model", BaseURL: "https://a.example/v1", APIKey: "synthetic"})
	if err := s.UpdateModel(id, ModelInput{GroupID: a, Name: "renamed", Model: "vendor/three", BaseURL: "https://three.example/v1"}); ErrorStatus(err) != 409 {
		t.Fatalf("move conflict status=%v err=%v", ErrorStatus(err), err)
	}
}

func TestDefaultAndAvailabilityRules(t *testing.T) {
	s := newTestStore(t)
	gid := mustGroup(t, s, "work")
	a := mustModel(t, s, ModelInput{GroupID: gid, Name: "a", Model: "a", BaseURL: "https://a.example/v1", APIKey: "key-a"})
	b := mustModel(t, s, ModelInput{GroupID: gid, Name: "b", Model: "b", BaseURL: "https://b.example/v1", APIKey: "key-b"})
	if err := s.SetDefault(a); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDefault(b); err != nil {
		t.Fatal(err)
	}
	if !hasEnabledDefault(s.Snapshot()) || s.Snapshot().DefaultModelID != b {
		t.Fatal("default switch failed")
	}
	if m := s.Snapshot().Models[0]; m.Disabled {
		t.Fatal("setting the default must not close other models")
	}
	if err := s.SetModelEnabled(b, false); err != nil {
		t.Fatal(err)
	}
	if s.Snapshot().DefaultModelID != "" {
		t.Fatal("closing the default must clear it in the same transaction")
	}
	if err := s.SetModelEnabled(b, true); err != nil {
		t.Fatal(err)
	}
	if s.Snapshot().DefaultModelID != "" {
		t.Fatal("re-opening must not restore the default")
	}
	if err := s.SetDefault(b); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteModel(b); err != nil {
		t.Fatal(err)
	}
	if s.Snapshot().DefaultModelID != "" {
		t.Fatal("deleting the default must clear it")
	}
	if _, err := s.Resolve(DefaultRouteID); ErrorStatus(err) != 503 {
		t.Fatalf("default route without default status=%v", ErrorStatus(err))
	}
	if err := s.SetModelEnabled(a, false); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDefault(a); ErrorStatus(err) != 403 {
		t.Fatalf("closed model as default status=%v", ErrorStatus(err))
	}
	if _, err := s.Resolve("adroute/work/a"); ErrorStatus(err) != 403 {
		t.Fatalf("closed route status=%v err=%v", ErrorStatus(err), err)
	}
	for _, name := range []string{"", "adroute/work/unknown", "vendor/model", "adroute/work", "adroute/work/a/b"} {
		if _, err := s.Resolve(name); err == nil {
			t.Fatalf("unknown route %q resolved", name)
		}
	}
}

func TestAdapterProfileMustReferenceInstalledAdapter(t *testing.T) {
	s := newTestStore(t)
	gid := mustGroup(t, s, "work")
	if _, err := s.CreateModel(ModelInput{GroupID: gid, Name: "m", Model: "m", BaseURL: "https://a.example/v1", APIKey: "secret", Profile: "missing-adapter@1.0.0"}); ErrorStatus(err) != 400 {
		t.Fatalf("missing adapter profile accepted: %v", err)
	}
	if len(s.Snapshot().Models) != 0 {
		t.Fatal("rejected mutation changed store")
	}
}

func TestCatalogAndModelListNeverLeakCredentials(t *testing.T) {
	s := newTestStore(t)
	gid := mustGroup(t, s, "work")
	mid := mustModel(t, s, ModelInput{GroupID: gid, Name: "coding", Model: "vendor/model", BaseURL: "https://up.example/v1", APIKey: "synthetic-secret"})
	if err := s.SetDefault(mid); err != nil {
		t.Fatal(err)
	}
	catalog := s.Catalog()
	cat := catalog["models"].([]map[string]any)
	if len(cat) != 2 || cat[0]["slug"] != DefaultRouteID || cat[1]["slug"] != "adroute/work/coding" || cat[1]["display_name"] != "work / coding" {
		t.Fatalf("catalog %#v", cat)
	}
	if strings.Contains(fmt.Sprint(catalog), "synthetic-secret") || strings.Contains(fmt.Sprint(catalog), "vendor/model") {
		t.Fatal("catalog leaks upstream details")
	}
	open := s.OpenAIModelList()["data"].([]map[string]any)
	if len(open) != 2 || open[0]["id"] != "adroute/work/coding" || open[0]["name"] != "work / coding" || open[1]["id"] != DefaultRouteID {
		t.Fatalf("openai list %#v", open)
	}
	if strings.Contains(fmt.Sprint(open), "synthetic-secret") {
		t.Fatal("public list leaks credentials")
	}
	admin, _ := json.Marshal(s.AdminState())
	if !strings.Contains(string(admin), "synthetic-secret") {
		t.Fatal("admin state must expose the stored credential for management")
	}
}

func TestClosedModelsLeaveCatalogAndSyncSource(t *testing.T) {
	s := newTestStore(t)
	gid := mustGroup(t, s, "work")
	open1 := mustModel(t, s, ModelInput{GroupID: gid, Name: "open", Model: "open", BaseURL: "https://up.example/v1", APIKey: "synthetic"})
	closed := mustModel(t, s, ModelInput{GroupID: gid, Name: "closed", Model: "closed", BaseURL: "https://up.example/v1", APIKey: "synthetic"})
	if err := s.SetModelEnabled(closed, false); err != nil {
		t.Fatal(err)
	}
	cat := s.Catalog()["models"].([]map[string]any)
	if len(cat) != 1 || cat[0]["slug"] != "adroute/work/open" {
		t.Fatalf("catalog included closed models: %#v", cat)
	}
	for _, m := range s.OpenAIModelList()["data"].([]map[string]any) {
		if m["id"] == "adroute/work/closed" {
			t.Fatal("closed model listed")
		}
	}
	// Closed entries stay administrable.
	if _, err := s.ModelRoute(closed); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ModelRoute(open1); err != nil {
		t.Fatal(err)
	}
}

func TestTokenValidationAndUnsafeConfigPath(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	s, err := OpenStore(home)
	if err != nil {
		t.Fatal(err)
	}
	tokens := filepath.Join(home, "tokens.json")
	if err = os.WriteFile(tokens, []byte(`{"adminToken":"`+strings.Repeat("a", 64)+`","proxyToken":"`+strings.Repeat("b", 64)+`","installationId":"x","extra":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = OpenStore(home); err == nil {
		t.Fatal("unknown token field accepted")
	}
	if err = os.Remove(tokens); err != nil {
		t.Fatal(err)
	}
	badHome := filepath.Join(t.TempDir(), "linked")
	if err = os.Symlink(s.Paths().Config, badHome); err != nil {
		t.Fatal(err)
	}
	if _, err = OpenStore(badHome); err == nil {
		t.Fatal("symlink home accepted")
	}
}

func TestConfigSymlinkIsRejectedWithoutChangingTarget(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	s, err := OpenStore(home)
	if err != nil {
		t.Fatal(err)
	}
	config := s.Paths().Config
	target := filepath.Join(t.TempDir(), "outside.json")
	payload := []byte(`{"version":5}`)
	if err = os.WriteFile(target, payload, 0644); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(config); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(target, config); err != nil {
		t.Fatal(err)
	}
	if _, err = OpenStore(home); err == nil {
		t.Fatal("symlink config accepted")
	}
	got, _ := os.ReadFile(target)
	if string(got) != string(payload) {
		t.Fatal("symlink target was altered")
	}
	info, _ := os.Stat(target)
	if info.Mode().Perm() != 0644 {
		t.Fatalf("outside file mode changed: %o", info.Mode().Perm())
	}
}

func TestStrictAdapterFieldsAndSecretRedaction(t *testing.T) {
	adapter := json.RawMessage(`{"schemaVersion":1,"id":"valid-pack","version":"1.0.0","name":"Pack","description":"synthetic","engine":"responses-tools-v1","options":{"lateName":false,"splitCalls":false},"fixtures":[{"name":"fixture","events":[{"type":"response.completed"}],"expectedCalls":[]}],"untrusted":true}`)
	state := State{Version: ConfigVersion, Groups: []Group{}, Models: []Model{}, Adapters: []json.RawMessage{adapter}}
	if ValidateState(state) == nil {
		t.Fatal("unknown adapter field accepted")
	}
	text := Redact("Authorization Bearer very-secret caused synthetic-key", []string{"synthetic-key", "very-secret"})
	if strings.Contains(text, "synthetic-key") || strings.Contains(text, "very-secret") {
		t.Fatal(text)
	}
	payload := ErrorPayload(appError("failed with synthetic-key", 400, "INVALID_REQUEST"), []string{"synthetic-key"})
	encoded, _ := json.Marshal(payload)
	if strings.Contains(string(encoded), "synthetic-key") || !strings.Contains(string(encoded), "[REDACTED]") {
		t.Fatal(string(encoded))
	}
}

func TestAdapterReferencesMustExistAndValidReferencePasses(t *testing.T) {
	pack := json.RawMessage(`{"schemaVersion":1,"id":"valid-pack","version":"1.0.0","name":"Pack","description":"synthetic","engine":"responses-tools-v1","options":{"lateName":false,"splitCalls":false},"fixtures":[{"name":"fixture","events":[{"type":"response.completed"}],"expectedCalls":[]}]}`)
	state := State{Version: ConfigVersion, Groups: []Group{{ID: "group-id", Name: "work"}}, Models: []Model{{ID: "model-id", GroupID: "group-id", Name: "model", Model: "upstream", BaseURL: "https://api.example/v1", APIKey: "synthetic", Profile: "valid-pack@1.0.0"}}, Adapters: []json.RawMessage{pack}}
	if err := ValidateState(state); err != nil {
		t.Fatalf("valid adapter reference rejected: %v", err)
	}
	state.Adapters = nil
	if err := ValidateState(state); err == nil {
		t.Fatal("dangling adapter reference accepted")
	}
}

func TestEmptyArraysAndPrivateDirectoryModes(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	s, err := OpenStore(home)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(s.AdminState())
	var state map[string]any
	if err = json.Unmarshal(b, &state); err != nil {
		t.Fatal(err)
	}
	if _, ok := state["groups"].([]any); !ok {
		t.Fatalf("groups are not an array: %s", b)
	}
	if _, ok := state["models"].([]any); !ok {
		t.Fatalf("models are not an array: %s", b)
	}
	for _, dir := range []string{home, s.Paths().Run, s.Paths().Logs} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0700 {
			t.Fatalf("%s mode %o", dir, info.Mode().Perm())
		}
	}
}

func TestExplicitBlankNamesAndNullFieldsAreRejected(t *testing.T) {
	s := newTestStore(t)
	gid := mustGroup(t, s, "work")
	id := mustModel(t, s, ModelInput{GroupID: gid, Name: "saved", Model: "m", BaseURL: "https://a.example/v1", APIKey: "secret"})
	base := ModelInput{GroupID: gid, Name: "saved", Model: "m", BaseURL: "https://a.example/v1"}
	var blank ModelInput
	if err := json.Unmarshal([]byte(`{"groupId":"`+gid+`","name":"","model":"m","baseUrl":"https://a.example/v1"}`), &blank); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateModel(id, blank); ErrorStatus(err) != 400 {
		t.Fatalf("explicit blank model name status=%v", ErrorStatus(err))
	}
	var nullDisabled ModelInput
	if err := json.Unmarshal([]byte(`{"groupId":"`+gid+`","name":"saved","model":"m","baseUrl":"https://a.example/v1","disabled":null}`), &nullDisabled); err == nil {
		t.Fatal("null disabled accepted")
	}
	// An omitted apiKey keeps the stored credential.
	if err := s.UpdateModel(id, base); err != nil {
		t.Fatal(err)
	}
	r, _ := s.ModelRoute(id)
	if r.APIKey != "secret" {
		t.Fatalf("apiKey overwritten: %#v", r)
	}
}

func TestLimitsMatchJavaScriptUTF16Lengths(t *testing.T) {
	if err := strictLabel(strings.Repeat("😀", 51), 100); err == nil {
		t.Fatal("name beyond 100 UTF-16 code units accepted")
	}
	if !validSecret(strings.Repeat("😀", 8192)) {
		t.Fatal("API key within 16384 UTF-16 code units rejected")
	}
	if validModelName(strings.Repeat("a", MaxModelNameLength+1)) {
		t.Fatal("model name length limit not enforced")
	}
	if validGroupName(strings.Repeat("a", MaxGroupNameLength+1)) {
		t.Fatal("group name length limit not enforced")
	}
}

func TestValidationRejectsInconsistentStates(t *testing.T) {
	base := func() State {
		return State{Version: ConfigVersion, Groups: []Group{{ID: "g", Name: "work"}}, Models: []Model{{ID: "m", GroupID: "g", Name: "coding", Model: "vendor/model", BaseURL: "https://up.example/v1", APIKey: "synthetic", Profile: "none"}}}
	}
	if err := ValidateState(base()); err != nil {
		t.Fatalf("valid state rejected: %v", err)
	}
	cases := map[string]func(*State){
		"missing group":        func(s *State) { s.Models[0].GroupID = "missing" },
		"duplicate group name": func(s *State) { s.Groups = append(s.Groups, Group{ID: "g2", Name: "work"}) },
		"invalid group name":   func(s *State) { s.Groups[0].Name = "Work" },
		"duplicate model name": func(s *State) {
			s.Models = append(s.Models, Model{ID: "m2", GroupID: "g", Name: "coding", Model: "x", BaseURL: "https://up.example/v1", APIKey: "k", Profile: "none"})
		},
		"invalid model name": func(s *State) { s.Models[0].Name = "cod ing" },
		"invalid real id":    func(s *State) { s.Models[0].Model = " " },
		"unnormalized url":   func(s *State) { s.Models[0].BaseURL = "https://UP.example:443/v1/" },
		"blank key":          func(s *State) { s.Models[0].APIKey = "" },
		"default to closed": func(s *State) {
			s.Models[0].Disabled = true
			s.DefaultModelID = "m"
		},
		"default to missing": func(s *State) { s.DefaultModelID = "nope" },
	}
	for name, mutate := range cases {
		st := base()
		mutate(&st)
		if err := ValidateState(st); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	// Same real id twice is explicitly allowed.
	ok := base()
	ok.Models = append(ok.Models, Model{ID: "m2", GroupID: "g", Name: "coding-2", Model: "vendor/model", BaseURL: "https://up.example/v1", APIKey: "synthetic", Profile: "none"})
	if err := ValidateState(ok); err != nil {
		t.Fatalf("duplicate real id rejected: %v", err)
	}
}
