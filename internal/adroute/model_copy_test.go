package adroute

import (
	"net/http"
	"strings"
	"sync"
	"testing"
)

func TestCopyModelCreatesIndependentNumberedEntries(t *testing.T) {
	s := newTestStore(t)
	a := mustHTTPApp(HTTPOptions{Store: s})
	gid := mustGroup(t, s, "work")
	disabled := true
	id := mustModel(t, s, ModelInput{GroupID: gid, Name: "coding", Model: "vendor/model", BaseURL: "https://example.invalid/v1", APIKey: "secret", Disabled: &disabled})
	for _, want := range []string{"coding-copy", "coding-copy-2", "coding-copy-3"} {
		w := adminReq(t, a, http.MethodPost, "/api/models/"+id+"/copy", nil)
		if w.Code != http.StatusCreated {
			t.Fatalf("copy: %d %s", w.Code, w.Body.String())
		}
		state := s.Snapshot()
		got := state.Models[len(state.Models)-1]
		if got.Name != want || got.ID == id || got.GroupID != gid || got.Model != "vendor/model" || got.BaseURL != "https://example.invalid/v1" || got.APIKey != "secret" || !got.Disabled || got.Profile != "none" {
			t.Fatalf("unexpected copy: %#v", got)
		}
		if state.DefaultModelID != "" {
			t.Fatal("copy changed default")
		}
	}
	long := mustModel(t, s, ModelInput{GroupID: gid, Name: strings.Repeat("a", 100), Model: "m", BaseURL: "https://example.invalid/v1", APIKey: "secret"})
	w := adminReq(t, a, http.MethodPost, "/api/models/"+long+"/copy", nil)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "MODEL_NAME") {
		t.Fatalf("long name: %d %s", w.Code, w.Body.String())
	}
}

func TestConcurrentCopiesUseUniqueNames(t *testing.T) {
	s := newTestStore(t)
	gid := mustGroup(t, s, "work")
	id := mustModel(t, s, ModelInput{GroupID: gid, Name: "model", Model: "m", BaseURL: "https://example.invalid/v1", APIKey: "secret"})
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := s.CopyModel(id); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	names := map[string]bool{}
	for _, model := range s.Snapshot().Models {
		names[model.Name] = true
	}
	for _, name := range []string{"model-copy", "model-copy-2", "model-copy-3", "model-copy-4", "model-copy-5", "model-copy-6", "model-copy-7", "model-copy-8"} {
		if !names[name] {
			t.Errorf("missing %s", name)
		}
	}
}
