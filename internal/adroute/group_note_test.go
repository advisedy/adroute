package adroute

import (
	"net/http"
	"strings"
	"testing"
)

func TestGroupNoteLifecycle(t *testing.T) {
	s := newTestStore(t)
	gid := mustGroup(t, s, "relay")
	other := mustGroup(t, s, "other")

	before := s.AdminState()
	if err := s.SetGroupNote(gid, "  线路稳，晚高峰偶尔排队。\n账单按量。  "); err != nil {
		t.Fatal(err)
	}
	after := s.AdminState()
	if got := groupNote(after, gid); got != "  线路稳，晚高峰偶尔排队。\n账单按量。  " {
		t.Fatalf("note = %q, want the stored text with line breaks and spacing intact", got)
	}
	if groupNote(after, other) != "" {
		t.Fatalf("another group picked up the note: %q", groupNote(after, other))
	}
	if after.Revision != before.Revision+1 {
		t.Fatalf("revision %d -> %d, want exactly one bump", before.Revision, after.Revision)
	}

	// Clearing removes the note without touching the group or its models.
	if err := s.SetGroupNote(gid, ""); err != nil {
		t.Fatal(err)
	}
	if got := groupNote(s.AdminState(), gid); got != "" {
		t.Fatalf("cleared note = %q", got)
	}

	// Renaming keeps the note attached to the same group.
	if err := s.SetGroupNote(gid, "keep me"); err != nil {
		t.Fatal(err)
	}
	if err := s.RenameGroup(gid, GroupInput{Name: "relay-2"}); err != nil {
		t.Fatal(err)
	}
	if got := groupNote(s.AdminState(), gid); got != "keep me" {
		t.Fatalf("note after rename = %q", got)
	}

	// Re-saving the same text is a no-op: autosave repeats must not churn the
	// revision or force the workbench to re-render.
	revision := s.AdminState().Revision
	if err := s.SetGroupNote(gid, "keep me"); err != nil {
		t.Fatal(err)
	}
	if got := s.AdminState().Revision; got != revision {
		t.Fatalf("idempotent save bumped revision %d -> %d", revision, got)
	}

	if err := s.SetGroupNote("missing-group", "x"); ErrorStatus(err) != 404 {
		t.Fatalf("unknown group status = %v", ErrorStatus(err))
	}
	if err := s.SetGroupNote(gid, strings.Repeat("字", MaxGroupNote+1)); ErrorStatus(err) != 400 {
		t.Fatalf("overlong note status = %v", ErrorStatus(err))
	}
	if got := groupNote(s.AdminState(), gid); got != "keep me" {
		t.Fatalf("rejected write changed the note to %q", got)
	}
}

func TestGroupNoteRejectsControlCharacters(t *testing.T) {
	s := newTestStore(t)
	gid := mustGroup(t, s, "relay")
	for _, bad := range []string{"bell\x07", "nul\x00", "escape\x1b[31m"} {
		if err := s.SetGroupNote(gid, bad); ErrorStatus(err) != 400 {
			t.Errorf("note %q status = %v", bad, ErrorStatus(err))
		}
	}
	if err := s.SetGroupNote(gid, "tab\tand\nnewline\r\nallowed"); err != nil {
		t.Fatalf("ordinary whitespace rejected: %v", err)
	}
}

func TestGroupNoteSurvivesExportImport(t *testing.T) {
	s := newTestStore(t)
	gid := mustGroup(t, s, "relay")
	if err := s.SetGroupNote(gid, "评价：稳定，价格偏高"); err != nil {
		t.Fatal(err)
	}
	exported := s.AdminState()
	if groupNote(exported, gid) != "评价：稳定，价格偏高" {
		t.Fatalf("export missing note: %q", groupNote(exported, gid))
	}

	revision := exported.Revision
	if err := s.importConfig(configImport{ExpectedRevision: &revision, Backup: configBackup{Format: "adroute-config", Version: 1, Config: exported}}); err != nil {
		t.Fatalf("import: %v", err)
	}
	if got := groupNote(s.AdminState(), gid); got != "评价：稳定，价格偏高" {
		t.Fatalf("note after import = %q", got)
	}
}

func TestGroupStateExposesNoteAndCapability(t *testing.T) {
	a := mustHTTPApp(HTTPOptions{Store: newTestStore(t)})
	w := adminReq(t, a, http.MethodPost, "/api/groups", map[string]string{"name": "relay"})
	if w.Code != 201 {
		t.Fatalf("create group status=%d body=%s", w.Code, w.Body.String())
	}
	gid := stateOf(t, w)["groups"].([]any)[0].(map[string]any)["id"].(string)

	if w = adminReq(t, a, http.MethodPut, "/api/groups/"+gid+"/note", map[string]string{"note": "按月结算"}); w.Code != 200 {
		t.Fatalf("save note status=%d body=%s", w.Code, w.Body.String())
	}
	state := stateOf(t, w)
	if got := state["groups"].([]any)[0].(map[string]any)["note"]; got != "按月结算" {
		t.Fatalf("state note = %v", got)
	}
	if state["capabilities"].(map[string]any)["groupNotes"] != true {
		t.Fatal("groupNotes capability missing")
	}

	if w = adminReq(t, a, http.MethodPut, "/api/groups/"+gid+"/note", map[string]any{"note": strings.Repeat("x", MaxGroupNote+1)}); w.Code != 400 {
		t.Fatalf("overlong note status=%d", w.Code)
	}
	if w = adminReq(t, a, http.MethodPut, "/api/groups/unknown/note", map[string]string{"note": "x"}); w.Code != 404 {
		t.Fatalf("unknown group status=%d", w.Code)
	}
	// The note endpoint must not double as a rename.
	if strings.Contains(w.Body.String(), "GROUP_NOT_FOUND") != true {
		t.Fatalf("unknown group error body = %s", w.Body.String())
	}
}

func groupNote(s State, id string) string {
	for _, g := range s.Groups {
		if g.ID == id {
			return g.Note
		}
	}
	return ""
}
