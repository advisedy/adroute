package adroute

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestOpenCodeSyncPreservesJSONC(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "opencode.jsonc")
	original := "{// keep\r\n \"big\":900719925474099312345,\r\n \"providers\":{\"adroute\":{\"package\":\"@opencode/ai/providers/openai/responses\",\"settings\":{\"baseURL\":\"http://127.0.0.1:1/v1\",\"apiKey\":\"synthetic\"},\"models\":{\"old\":{}}}}\r\n}"
	if err := os.WriteFile(p, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := SyncOpenCode(OpenCodeSyncOptions{Models: []SyncModel{{Slug: "adroute/provider/entry", DisplayName: "Example"}}, ConfigPath: p})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Changed {
		t.Fatal("expected change")
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), "// keep") || !strings.Contains(string(b), "900719925474099312345") || strings.Contains(string(b), "old") {
		t.Fatalf("unexpected config %s", b)
	}
	backup, _ := os.ReadFile(r.BackupPath)
	if string(backup) != original {
		t.Fatal("backup not exact")
	}
}

func TestOpenCodeSyncInsertsModelsAndPreservesJSONCSyntax(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "opencode.jsonc")
	original := "\ufeff// heading\r\n{\r\n \"big\":900719925474099312345,\r\n \"providers\":{\"adroute\":{\"package\":\"@opencode/ai/providers/openai/responses\", // runtime\r\n \"settings\":{\"baseURL\":\"http://127.0.0.1:1/v1\",\"apiKey\":\"synthetic\"} /* retain */ ,},},\r\n \"x\":[1,],\r\n}"
	if e := os.WriteFile(p, []byte(original), 0600); e != nil {
		t.Fatal(e)
	}
	r, e := SyncOpenCode(OpenCodeSyncOptions{Models: []SyncModel{{Slug: "adroute/p/e", DisplayName: "Entry"}}, ConfigPath: p})
	if e != nil {
		t.Fatal(e)
	}
	if !r.Changed || r.ModelCount != 1 {
		t.Fatalf("bad result %#v", r)
	}
	b, _ := os.ReadFile(p)
	text := string(b)
	if !strings.HasPrefix(text, "\ufeff// heading\r\n") || !strings.Contains(text, "900719925474099312345") || !strings.Contains(text, "/* retain */") || !strings.Contains(text, "\r\n") || strings.Contains(strings.ReplaceAll(text, "\r\n", ""), "\n") {
		t.Fatalf("unrelated JSONC syntax changed: %q", text)
	}
	if !strings.Contains(text, `"adroute/p/e"`) {
		t.Fatalf("missing inserted models: %s", text)
	}
}

func TestOpenCodeSyncRejectsDuplicateKeysAndInvalidUTF8(t *testing.T) {
	for _, raw := range [][]byte{[]byte(`{"providers":{"adroute":{},"adroute":{}}}`), []byte(`{"providers":{"adroute":{"package":"@opencode/ai/providers/openai/responses","settings":{"baseURL":"http://127.0.0.1","apiKey":"x"},"models":{"a":{},"a":{}}}}}`), {0xff, 0xfe}} {
		d := t.TempDir()
		p := filepath.Join(d, "config.json")
		if e := os.WriteFile(p, raw, 0600); e != nil {
			t.Fatal(e)
		}
		_, e := SyncOpenCode(OpenCodeSyncOptions{Models: []SyncModel{}, ConfigPath: p})
		if !hasCode(e, "OPENCODE_CONFIG_INVALID") {
			t.Fatalf("invalid input accepted: %v", e)
		}
		b, _ := os.ReadFile(p)
		if !bytes.Equal(b, raw) {
			t.Fatal("rejected input modified")
		}
	}
}

func TestOpenCodeSyncRequiresSafeRegularSingleLinkConfigAndParent(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "config.json")
	raw := `{"providers":{"adroute":{"package":"@opencode/ai/providers/openai/responses","settings":{"baseURL":"http://127.0.0.1","apiKey":"x"}}}}`
	target := filepath.Join(d, "target")
	if e := os.WriteFile(target, []byte(raw), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.Link(target, p); e != nil {
		t.Fatal(e)
	}
	_, e := SyncOpenCode(OpenCodeSyncOptions{Models: []SyncModel{}, ConfigPath: p})
	if !hasCode(e, "OPENCODE_CONFIG_UNSAFE") {
		t.Fatalf("hardlink accepted: %v", e)
	}
}

func TestOpenCodeSyncRefusesSymlinkFileAndSymlinkParent(t *testing.T) {
	d := t.TempDir()
	raw := `{"providers":{"adroute":{"package":"@opencode/ai/providers/openai/responses","settings":{"baseURL":"http://127.0.0.1/v1","apiKey":"synthetic"}}}}`
	target := filepath.Join(d, "target.json")
	if e := os.WriteFile(target, []byte(raw), 0600); e != nil {
		t.Fatal(e)
	}
	alias := filepath.Join(d, "alias.json")
	if e := os.Symlink(target, alias); e != nil {
		t.Fatal(e)
	}
	_, e := SyncOpenCode(OpenCodeSyncOptions{Models: []SyncModel{}, ConfigPath: alias})
	if !hasCode(e, "OPENCODE_CONFIG_UNSAFE") {
		t.Fatalf("symlink config accepted: %v", e)
	}
	parentAlias := filepath.Join(d, "parent-alias")
	if e = os.Symlink(d, parentAlias); e != nil {
		t.Fatal(e)
	}
	_, e = SyncOpenCode(OpenCodeSyncOptions{Models: []SyncModel{}, ConfigPath: filepath.Join(parentAlias, "target.json")})
	if !hasCode(e, "OPENCODE_CONFIG_UNSAFE") {
		t.Fatalf("symlink parent accepted: %v", e)
	}
	if got := readConfigForTest(t, target); got != raw {
		t.Fatal("symlink target was changed")
	}
}

func TestOpenCodeSyncInsertsMissingMapWithAndWithoutTrailingComma(t *testing.T) {
	for _, trailing := range []bool{false, true} {
		d := t.TempDir()
		p := filepath.Join(d, "config.jsonc")
		comma := ""
		if trailing {
			comma = ","
		}
		original := `{"providers":{"adroute":{"package":"@opencode/ai/providers/openai/responses","settings":{"baseURL":"http://127.0.0.1/v1","apiKey":"x"}` + comma + ` /* adjacent */}}}`
		if e := os.WriteFile(p, []byte(original), 0600); e != nil {
			t.Fatal(e)
		}
		r, e := SyncOpenCode(OpenCodeSyncOptions{Models: []SyncModel{{Slug: "adroute/a/b", DisplayName: "B"}}, ConfigPath: p})
		if e != nil {
			t.Fatalf("trailing=%v: %v", trailing, e)
		}
		raw, _ := os.ReadFile(p)
		parsed, e := parseJSONC(raw)
		if e != nil {
			t.Fatal(e)
		}
		providers := parsed.props["providers"]
		provider := providers.props["adroute"]
		models := provider.props["models"]
		if models == nil || models.props["adroute/a/b"] == nil || !strings.Contains(string(raw), "/* adjacent */") || !r.Changed {
			t.Fatalf("bad insertion trailing=%v: %s", trailing, raw)
		}
	}
}

func TestOpenCodeSyncGlobalPathAmbiguityAndOverrides(t *testing.T) {
	d := t.TempDir()
	dir := filepath.Join(d, ".config", "opencode")
	if e := os.MkdirAll(dir, 0700); e != nil {
		t.Fatal(e)
	}
	raw := `{"providers":{"adroute":{"package":"@opencode/ai/providers/openai/responses","settings":{"baseURL":"http://127.0.0.1/v1","apiKey":"x"}}}}`
	for _, name := range []string{"opencode.json", "opencode.jsonc"} {
		if e := os.WriteFile(filepath.Join(dir, name), []byte(raw), 0600); e != nil {
			t.Fatal(e)
		}
	}
	models := []SyncModel{{Slug: "adroute/a/b", DisplayName: "B"}}
	// Both default files exist and both declare providers.adroute. OpenCode loads
	// the .jsonc copy last, so that is the file whose models it actually applies.
	r, e := SyncOpenCode(OpenCodeSyncOptions{Models: models, Env: map[string]string{"HOME": d}})
	if e != nil {
		t.Fatalf("resolvable ambiguity refused: %v", e)
	}
	if filepath.Base(r.ConfigPath) != "opencode.jsonc" {
		t.Fatalf("wrote to %s, want the file OpenCode applies last", r.ConfigPath)
	}
	if got := readConfigForTest(t, filepath.Join(dir, "opencode.json")); got != raw {
		t.Fatal("the shadowed .json copy was modified")
	}
	if got := readConfigForTest(t, filepath.Join(dir, "opencode.jsonc")); !strings.Contains(got, "adroute/a/b") {
		t.Fatalf("models not written to the applied copy: %s", got)
	}
	if _, e := SyncOpenCode(OpenCodeSyncOptions{Models: models, Env: map[string]string{"HOME": d, "OPENCODE_CONFIG_DIR": "elsewhere"}}); !hasCode(e, "OPENCODE_CONFIG_PATH") {
		t.Fatalf("override not refused: %v", e)
	}
	if _, e := SyncOpenCode(OpenCodeSyncOptions{Models: models, ConfigPath: filepath.Join(d, "missing.json")}); !hasCode(e, "OPENCODE_CONFIG_MISSING") {
		t.Fatalf("missing explicit config: %v", e)
	}
}

// Two files are only ambiguous when nothing tells us which one owns the
// provider. A single owner is written in place, and its sibling is untouched.
func TestOpenCodeSyncBothFilesSelectsTheSoleProviderOwner(t *testing.T) {
	for _, owner := range []string{"opencode.json", "opencode.jsonc"} {
		t.Run(owner, func(t *testing.T) {
			d := t.TempDir()
			dir := filepath.Join(d, ".config", "opencode")
			if e := os.MkdirAll(dir, 0700); e != nil {
				t.Fatal(e)
			}
			provider := `{"providers":{"adroute":{"package":"aisdk:@ai-sdk/openai","settings":{"baseURL":"http://127.0.0.1/v1","apiKey":"x"}}}}`
			other := `{"$schema":"https://opencode.ai/config.json","plugin":["x"]}`
			files := map[string]string{"opencode.json": other, "opencode.jsonc": other}
			files[owner] = provider
			for name, body := range files {
				if e := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); e != nil {
					t.Fatal(e)
				}
			}
			r, e := SyncOpenCode(OpenCodeSyncOptions{Models: []SyncModel{{Slug: "adroute/a/b", DisplayName: "B"}}, Env: map[string]string{"HOME": d}})
			if e != nil {
				t.Fatalf("sole owner not used: %v", e)
			}
			if r.ConfigPath != filepath.Join(dir, owner) {
				t.Fatalf("wrote %s, want the only provider owner %s", r.ConfigPath, owner)
			}
			for name, body := range files {
				got := readConfigForTest(t, filepath.Join(dir, name))
				if name == owner && !strings.Contains(got, "adroute/a/b") {
					t.Fatalf("%s did not receive models: %s", name, got)
				}
				if name != owner && got != body {
					t.Fatalf("%s was modified: %s", name, got)
				}
			}
		})
	}
	if _, e := SyncOpenCode(OpenCodeSyncOptions{Models: []SyncModel{}, Env: map[string]string{"HOME": t.TempDir()}}); !hasCode(e, "OPENCODE_CONFIG_MISSING") {
		t.Fatalf("no config: %v", e)
	}
}

func TestOpenCodeSyncRefusesAmbiguityWithoutProviderOwner(t *testing.T) {
	d := t.TempDir()
	dir := filepath.Join(d, ".config", "opencode")
	if e := os.MkdirAll(dir, 0700); e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"opencode.json", "opencode.jsonc"} {
		if e := os.WriteFile(filepath.Join(dir, name), []byte(`{"$schema":"https://opencode.ai/config.json"}`), 0600); e != nil {
			t.Fatal(e)
		}
	}
	_, e := SyncOpenCode(OpenCodeSyncOptions{Models: []SyncModel{}, Env: map[string]string{"HOME": d}})
	if !hasCode(e, "OPENCODE_CONFIG_AMBIGUOUS") {
		t.Fatalf("neither file owns the provider: %v", e)
	}
}

func TestOpenCodeSyncPreexistingLockIsNeverRemoved(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "config.json")
	raw := `{"providers":{"adroute":{"package":"@opencode/ai/providers/openai/responses","settings":{"baseURL":"http://127.0.0.1/v1","apiKey":"x"}}}}`
	if e := os.WriteFile(p, []byte(raw), 0600); e != nil {
		t.Fatal(e)
	}
	lock := p + ".adroute-sync.lock"
	if e := os.WriteFile(lock, []byte("owned by another operation"), 0600); e != nil {
		t.Fatal(e)
	}
	_, e := SyncOpenCode(OpenCodeSyncOptions{Models: []SyncModel{}, ConfigPath: p})
	if !hasCode(e, "OPENCODE_CONFIG_BUSY") {
		t.Fatalf("preexisting lock ignored: %v", e)
	}
	if got := readConfigForTest(t, p); got != raw {
		t.Fatal("config changed while locked")
	}
	if got := readConfigForTest(t, lock); got != "owned by another operation" {
		t.Fatal("preexisting lock was removed or overwritten")
	}
}

func TestOpenCodeSyncRejectsInvalidProviderShapeWithoutBackup(t *testing.T) {
	for _, raw := range []string{`{"providers":{"adroute":[]}}`, `{"providers":{"adroute":{"package":"@opencode/ai/providers/anthropic","settings":{"baseURL":"http://127.0.0.1","apiKey":"x"}}}}`, `{"providers":{"adroute":{"package":"@opencode/ai/providers/openai/responses","settings":{"baseURL":"javascript:bad" ,"apiKey":"x"}}}}`, `{"providers":{"adroute":{"package":"@opencode/ai/providers/openai/responses","settings":{"baseURL":"http://127.0.0.1"}}}}`, `{"providers":{"adroute":{"package":"@opencode/ai/providers/openai/responses","settings":{"baseURL":"http://127.0.0.1","apiKey":"x"},"models":null}}}`} {
		d := t.TempDir()
		p := filepath.Join(d, "c.json")
		if e := os.WriteFile(p, []byte(raw), 0600); e != nil {
			t.Fatal(e)
		}
		_, e := SyncOpenCode(OpenCodeSyncOptions{Models: []SyncModel{}, ConfigPath: p})
		if !hasCode(e, "OPENCODE_PROVIDER_INCOMPATIBLE") {
			t.Fatalf("shape accepted: %v\n%s", e, raw)
		}
		if got := readConfigForTest(t, p); got != raw {
			t.Fatal("incompatible config modified")
		}
		entries, _ := os.ReadDir(d)
		if len(entries) != 1 {
			t.Fatalf("backup created before validation: %v", entries)
		}
	}
}

func TestOpenCodeSyncFinalRecheckRejectsLateEditAndReplacement(t *testing.T) {
	for _, replace := range []bool{false, true} {
		t.Run(strconv.FormatBool(replace), func(t *testing.T) {
			d := t.TempDir()
			p := filepath.Join(d, "config.json")
			original := `{"providers":{"adroute":{"package":"@opencode/ai/providers/openai/responses","settings":{"baseURL":"http://127.0.0.1/v1","apiKey":"x"}}}}`
			if e := os.WriteFile(p, []byte(original), 0600); e != nil {
				t.Fatal(e)
			}
			late := original + "\n// external edit"
			_, e := syncOpenCodeWithHook(OpenCodeSyncOptions{Models: []SyncModel{{Slug: "adroute/a/b", DisplayName: "B"}}, ConfigPath: p}, func() {
				if replace {
					q := p + ".external"
					_ = os.WriteFile(q, []byte(late), 0600)
					_ = os.Rename(q, p)
				} else {
					_ = os.WriteFile(p, []byte(late), 0600)
				}
			})
			if !hasCode(e, "OPENCODE_CONFIG_CHANGED") {
				t.Fatalf("late concurrent write overwritten: %v", e)
			}
			if got := readConfigForTest(t, p); got != late {
				t.Fatalf("late edit lost: %q", got)
			}
			entries, _ := os.ReadDir(d)
			backups := 0
			for _, entry := range entries {
				if strings.HasSuffix(entry.Name(), ".bak") {
					backups++
					b, _ := os.ReadFile(filepath.Join(d, entry.Name()))
					if string(b) != original {
						t.Fatal("backup is not exact pre-edit source")
					}
				}
			}
			if backups != 1 {
				t.Fatalf("backup count=%d", backups)
			}
		})
	}
}

// Only runtime packages that reach the gateway as /v1/responses may be synced.
// adroute serves no chat completions route, so a chat package would publish
// models that cannot answer.
func TestOpenCodeSyncAcceptsResponsesRuntimePackagesOnly(t *testing.T) {
	for _, pkg := range []string{"aisdk:@ai-sdk/openai", "@opencode/ai/providers/openai", "@opencode/ai/providers/openai/responses", "@opencode/ai/providers/openai-compatible/responses"} {
		t.Run(pkg, func(t *testing.T) {
			d := t.TempDir()
			p := filepath.Join(d, "c.json")
			raw := `{"providers":{"adroute":{"package":"` + pkg + `","settings":{"baseURL":"http://127.0.0.1/v1","apiKey":"x"}}}}`
			if e := os.WriteFile(p, []byte(raw), 0600); e != nil {
				t.Fatal(e)
			}
			r, e := SyncOpenCode(OpenCodeSyncOptions{Models: []SyncModel{{Slug: "adroute/a/b", DisplayName: "B"}}, ConfigPath: p})
			if e != nil {
				t.Fatalf("%s refused: %v", pkg, e)
			}
			if !r.Changed || r.ModelCount != 1 {
				t.Fatalf("%s: %#v", pkg, r)
			}
		})
	}
	for _, pkg := range []string{"aisdk:@ai-sdk/openai-compatible", "@opencode/ai/providers/openai/chat", "@opencode/ai/providers/anthropic", "", "aisdk:@ai-sdk/openai/responses"} {
		t.Run("reject:"+pkg, func(t *testing.T) {
			d := t.TempDir()
			p := filepath.Join(d, "c.json")
			raw := `{"providers":{"adroute":{"package":"` + pkg + `","settings":{"baseURL":"http://127.0.0.1/v1","apiKey":"x"}}}}`
			if e := os.WriteFile(p, []byte(raw), 0600); e != nil {
				t.Fatal(e)
			}
			_, e := SyncOpenCode(OpenCodeSyncOptions{Models: []SyncModel{}, ConfigPath: p})
			if !hasCode(e, "OPENCODE_PROVIDER_INCOMPATIBLE") {
				t.Fatalf("%q accepted: %v", pkg, e)
			}
			if got := readConfigForTest(t, p); got != raw {
				t.Fatal("rejected package modified the config")
			}
		})
	}
}

// The published models carry the default reasoning menu so a client can pick a
// level without hand-editing opencode.json. The levels are a menu, not a
// guarantee: adroute forwards reasoning.effort unchanged, so an upstream that
// refuses one reports the error to the user.
func TestOpenCodeSyncPublishesReasoningVariants(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "c.json")
	raw := `{"providers":{"adroute":{"package":"aisdk:@ai-sdk/openai","settings":{"baseURL":"http://127.0.0.1/v1","apiKey":"x"}}}}`
	if e := os.WriteFile(p, []byte(raw), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := SyncOpenCode(OpenCodeSyncOptions{Models: []SyncModel{{Slug: "adroute/a/b", DisplayName: "B"}}, ConfigPath: p}); e != nil {
		t.Fatal(e)
	}
	parsed, e := parseJSONC([]byte(readConfigForTest(t, p)))
	if e != nil {
		t.Fatal(e)
	}
	model, _ := parsed.props["providers"].props["adroute"].props["models"].props["adroute/a/b"].value.(map[string]any)
	variants, _ := model["variants"].([]any)
	if len(variants) != len(ReasoningLevels) {
		t.Fatalf("variants = %#v", model["variants"])
	}
	for i, want := range ReasoningLevels {
		v, _ := variants[i].(map[string]any)
		settings, _ := v["settings"].(map[string]any)
		if v["id"] != want || settings["reasoningEffort"] != want {
			t.Fatalf("variant %d = %#v, want %s", i, v, want)
		}
	}
	// The map is idempotent: a second sync with the same catalog changes nothing.
	r, e := SyncOpenCode(OpenCodeSyncOptions{Models: []SyncModel{{Slug: "adroute/a/b", DisplayName: "B"}}, ConfigPath: p})
	if e != nil {
		t.Fatal(e)
	}
	if r.Changed || r.BackupPath != "" {
		t.Fatalf("repeat sync rewrote the file: %#v", r)
	}
}

// An entry published before variants existed gains them on the next sync,
// because the models map is compared semantically.
func TestOpenCodeSyncUpgradesEntriesWithoutVariants(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "c.json")
	raw := `{"providers":{"adroute":{"package":"aisdk:@ai-sdk/openai","settings":{"baseURL":"http://127.0.0.1/v1","apiKey":"x"},"models":{"adroute/a/b":{"modelID":"adroute/a/b","name":"B"}}}}}`
	if e := os.WriteFile(p, []byte(raw), 0600); e != nil {
		t.Fatal(e)
	}
	r, e := SyncOpenCode(OpenCodeSyncOptions{Models: []SyncModel{{Slug: "adroute/a/b", DisplayName: "B"}}, ConfigPath: p})
	if e != nil {
		t.Fatal(e)
	}
	if !r.Changed {
		t.Fatal("entry without variants was treated as up to date")
	}
	if got := readConfigForTest(t, p); !strings.Contains(got, `"variants"`) {
		t.Fatalf("variants not added: %s", got)
	}
}
