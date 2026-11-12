package adroute

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	toml "github.com/pelletier/go-toml/v2"
)

func TestTask3OpenCodeSyncPreservesJSONC(t *testing.T) {
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
	models := []SyncModel{}
	if _, e := SyncOpenCode(OpenCodeSyncOptions{Models: models, Env: map[string]string{"HOME": d}}); !hasCode(e, "OPENCODE_CONFIG_AMBIGUOUS") {
		t.Fatalf("ambiguous config: %v", e)
	}
	if _, e := SyncOpenCode(OpenCodeSyncOptions{Models: models, Env: map[string]string{"HOME": d, "OPENCODE_CONFIG_DIR": "elsewhere"}}); !hasCode(e, "OPENCODE_CONFIG_PATH") {
		t.Fatalf("override not refused: %v", e)
	}
	if _, e := SyncOpenCode(OpenCodeSyncOptions{Models: models, ConfigPath: filepath.Join(d, "missing.json")}); !hasCode(e, "OPENCODE_CONFIG_MISSING") {
		t.Fatalf("missing explicit config: %v", e)
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

func TestCodexFinalRecheckProtectsLateReplaceAndRemove(t *testing.T) {
	for _, remove := range []bool{false, true} {
		t.Run(strconv.FormatBool(remove), func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "config.toml")
			original := "model='managed'\n"
			if e := os.WriteFile(p, []byte(original), 0600); e != nil {
				t.Fatal(e)
			}
			previous, e := readSafeText(p)
			if e != nil {
				t.Fatal(e)
			}
			backup := p + ".before"
			if e = os.WriteFile(backup, []byte(original), 0600); e != nil {
				t.Fatal(e)
			}
			late := "model='external'\n"
			e = replaceSafeTextWithHook(p, previous, "", remove, func() { q := p + ".external"; _ = os.WriteFile(q, []byte(late), 0600); _ = os.Rename(q, p) })
			if !hasCode(e, "CODEX_CONFIG_CHANGED") {
				t.Fatalf("late config mutation wasn't rejected: %v", e)
			}
			if got := readConfigForTest(t, p); got != late {
				t.Fatalf("late user edit lost: %q", got)
			}
			if got := readConfigForTest(t, backup); got != original {
				t.Fatal("original backup changed")
			}
		})
	}
}

func TestCodexStaleLockReclaimChecksObservedIdentityAndNonce(t *testing.T) {
	p := filepath.Join(t.TempDir(), "lock.json")
	if e := os.WriteFile(p, []byte(`{"pid":99999999,"nonce":"stale"}`), 0600); e != nil {
		t.Fatal(e)
	}
	observed, e := observeCodexLock(p)
	if e != nil {
		t.Fatal(e)
	}
	replacement := []byte(`{"pid":123,"nonce":"new-owner"}`)
	if e = os.WriteFile(p+".new", replacement, 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.Rename(p+".new", p); e != nil {
		t.Fatal(e)
	}
	if reclaimed := reclaimCodexLock(p, observed); reclaimed {
		t.Fatal("reclaimed a swapped lock")
	}
	if got := readConfigForTest(t, p); got != string(replacement) {
		t.Fatalf("new lock changed: %q", got)
	}
	if e = os.WriteFile(p, []byte(`{"pid":99999999,"nonce":"old"}`), 0600); e != nil {
		t.Fatal(e)
	}
	observed, e = observeCodexLock(p)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(p, []byte(`{"pid":123,"nonce":"changed-in-place"}`), 0600); e != nil {
		t.Fatal(e)
	}
	if reclaimCodexLock(p, observed) {
		t.Fatal("reclaimed lock whose contents changed on observed inode")
	}
	if got := readConfigForTest(t, p); got != `{"pid":123,"nonce":"changed-in-place"}` {
		t.Fatalf("changed lock lost: %q", got)
	}
}

func TestCodexStaleLockReclaimSerializesCooperatingSubprocesses(t *testing.T) {
	root := t.TempDir()
	codex := filepath.Join(root, "codex")
	if e := os.Mkdir(codex, 0700); e != nil {
		t.Fatal(e)
	}
	state := filepath.Join(codex, ".adroute")
	if e := os.Mkdir(state, 0700); e != nil {
		t.Fatal(e)
	}
	stale := filepath.Join(state, "lock.json")
	if e := os.WriteFile(stale, []byte(`{"pid":99999999,"nonce":"dead-owner"}`), 0600); e != nil {
		t.Fatal(e)
	}
	release := filepath.Join(root, "release")
	reached1 := filepath.Join(root, "reached-1")
	reached2 := filepath.Join(root, "reached-2")
	entered := filepath.Join(root, "entered")
	startChild := func(reached string, expectBusy bool) *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=^TestCodexLockGuardSubprocessHelper$")
		env := append(os.Environ(), "ADROUTE_LOCK_HELPER=1", "ADROUTE_LOCK_HOME="+codex, "ADROUTE_LOCK_RELEASE="+release, "ADROUTE_LOCK_REACHED="+reached, "ADROUTE_LOCK_ENTERED="+entered)
		if expectBusy {
			env = append(env, "ADROUTE_LOCK_EXPECT_BUSY=1")
		}
		cmd.Env = env
		return cmd
	}
	first := startChild(reached1, false)
	firstOut := &bytes.Buffer{}
	first.Stdout = firstOut
	first.Stderr = firstOut
	if e := first.Start(); e != nil {
		t.Fatal(e)
	}
	waitForFile(t, reached1)
	second := startChild(reached2, true)
	secondOut := &bytes.Buffer{}
	second.Stdout = secondOut
	second.Stderr = secondOut
	secondDone := make(chan error, 1)
	if e := second.Start(); e != nil {
		_ = os.WriteFile(release, nil, 0600)
		_ = first.Wait()
		t.Fatal(e)
	}
	go func() { secondDone <- second.Wait() }()
	select {
	case e := <-secondDone:
		if e != nil {
			_ = os.WriteFile(release, nil, 0600)
			_ = first.Wait()
			t.Fatalf("second process didn't report expected lock contention: %v\n%s", e, secondOut.String())
		}
	case <-time.After(3 * time.Second):
		_ = os.WriteFile(release, nil, 0600)
		_ = first.Wait()
		t.Fatal("second process blocked instead of promptly honoring guard lock")
	}
	if _, e := os.Stat(reached2); e == nil {
		_ = os.WriteFile(release, nil, 0600)
		_ = first.Wait()
		t.Fatal("both cooperating reclaimers passed the stale-lock observation seam")
	}
	if e := os.WriteFile(release, nil, 0600); e != nil {
		t.Fatal(e)
	}
	firstDone := make(chan error, 1)
	go func() { firstDone <- first.Wait() }()
	select {
	case e := <-firstDone:
		if e != nil {
			t.Fatalf("first process failed to recover dead owner: %v\n%s", e, firstOut.String())
		}
	case <-time.After(3 * time.Second):
		_ = first.Process.Kill()
		t.Fatal("first process did not finish after releasing stale-lock seam")
	}
	if _, e := os.Stat(entered); e != nil {
		t.Fatal("no reclaimer entered critical section")
	}
	if _, e := os.Stat(stale); !os.IsNotExist(e) {
		t.Fatalf("stale marker wasn't cleared: %v", e)
	}
}

func TestCodexLockGuardSubprocessHelper(t *testing.T) {
	if os.Getenv("ADROUTE_LOCK_HELPER") != "1" {
		return
	}
	home := os.Getenv("ADROUTE_LOCK_HOME")
	reached := os.Getenv("ADROUTE_LOCK_REACHED")
	release := os.Getenv("ADROUTE_LOCK_RELEASE")
	entered := os.Getenv("ADROUTE_LOCK_ENTERED")
	err := withCodexLockHook(home, func(string) error { return os.WriteFile(entered, []byte("entered"), 0600) }, func() {
		_ = os.WriteFile(reached, []byte("observed"), 0600)
		deadline := time.Now().Add(4 * time.Second)
		for time.Now().Before(deadline) {
			if _, e := os.Stat(release); e == nil {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	})
	if os.Getenv("ADROUTE_LOCK_EXPECT_BUSY") == "1" {
		if !hasCode(err, "CODEX_CONFIG_BUSY") {
			t.Fatalf("expected lock busy, got %v", err)
		}
		return
	}
	if err != nil {
		t.Fatalf("lock owner failed: %v", err)
	}
}

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

func TestCodexRangeEditorActiveProfilesAllShapes(t *testing.T) {
	fixtures := []string{
		"profile='daily.work'\r\nprofiles.\"daily.work\".model_provider='old'\r\nprofiles.\"daily.work\".model='old-model'\r\nfeatures.api_key_model_discovery=true\r\n",
		"profile = 'daily.work'\n[profiles.\"daily.work\"] # active\nmodel_provider='old' # keep this\nmodel='old-model'\nreasoning_effort='high'\n",
		"profile='daily.work'\nprofiles = { \"daily.work\" = { model_provider='old', model='old-model', reasoning_effort='high' } }\nmodel_providers = { adroute = { name='prior', http_headers={Authorization='old-secret', Other='keep'} } }\n",
	}
	for i, original := range fixtures {
		t.Run(string(rune('a'+i)), func(t *testing.T) {
			f := configFixture(t, original)
			if _, e := f.connect(); e != nil {
				t.Fatal(e)
			}
			current, _ := os.ReadFile(f.path)
			var got map[string]any
			if e := toml.Unmarshal(current, &got); e != nil {
				t.Fatalf("edited TOML invalid: %v\n%s", e, current)
			}
			profile := got["profiles"].(map[string]any)["daily.work"].(map[string]any)
			if profile["model_provider"] != "adroute" || profile["model"] != "synthetic-model" {
				t.Fatalf("active profile not edited: %#v", profile)
			}
			if strings.Contains(original, "Authorization") {
				provider := got["model_providers"].(map[string]any)["adroute"].(map[string]any)
				if _, exists := provider["http_headers"].(map[string]any)["Authorization"]; exists {
					t.Fatal("old Authorization retained")
				}
				if provider["http_headers"].(map[string]any)["Other"] != "keep" {
					t.Fatal("unrelated inline header lost")
				}
			}
			if strings.Contains(original, "\r\n") && !strings.Contains(string(current), "\r\n") {
				t.Fatal("CRLF normalized")
			}
			if _, e := f.restore(); e != nil {
				t.Fatal(e)
			}
			back, _ := os.ReadFile(f.path)
			if string(back) != original {
				t.Fatalf("restore mismatch\nwant %q\ngot  %q", original, back)
			}
		})
	}
}

func TestCodexRestorePreservesManagedAndUnrelatedUserChanges(t *testing.T) {
	original := "model='before'\nmodel_provider='other'\n[features]\nshell_snapshot=false\n"
	f := configFixture(t, original)
	if _, e := f.connect(); e != nil {
		t.Fatal(e)
	}
	changed := strings.Replace(string(readConfigForTest(t, f.path)), "model=\"synthetic-model\"", "model=\"user-model\"", 1) + "\n[other]\nlarge = 9007199254740993123\n"
	if !strings.Contains(changed, "user-model") {
		t.Fatalf("test mutation did not change the managed model:\n%s", changed)
	}
	if e := os.WriteFile(f.path, []byte(changed), 0600); e != nil {
		t.Fatal(e)
	}
	result, e := f.restore()
	if e != nil {
		t.Fatal(e)
	}
	if result["preservedChanges"].(int) < 1 {
		t.Fatalf("user field not marked preserved: %#v", result)
	}
	var got map[string]any
	b, _ := os.ReadFile(f.path)
	if e = toml.Unmarshal(b, &got); e != nil {
		t.Fatal(e)
	}
	if got["model"] != "user-model" || got["model_provider"] != "other" || got["other"].(map[string]any)["large"].(int64) != 9007199254740993123 {
		t.Fatalf("user changes lost: %#v", got)
	}
}

func TestCodexSetupRejectsUnsafePathsAndDoesNotTakeOver(t *testing.T) {
	f := configFixture(t, "# safe\n")
	if _, e := f.connect(); e != nil {
		t.Fatal(e)
	}
	before := readConfigForTest(t, f.path)
	if _, e := f.connect(CodexConfigOptions{Home: filepath.Join(f.home, "other"), CodexHome: f.codex, URL: "http://127.0.0.1:3001", ProxyToken: strings.Repeat("b", 64), InstallationID: "another"}); !hasCode(e, "CODEX_CONFIG_CONFLICT") {
		t.Fatalf("takeover error=%v", e)
	}
	if _, e := f.connect(CodexConfigOptions{Home: f.home, CodexHome: filepath.Join(f.root, "missing"), URL: "http://127.0.0.1:3001", ProxyToken: strings.Repeat("b", 64), InstallationID: "synthetic-installation"}); !hasCode(e, "CODEX_CONFIG_CONFLICT") {
		t.Fatalf("CODEX_HOME switch error=%v", e)
	}
	if e := os.Remove(f.path); e != nil {
		t.Fatal(e)
	}
	outside := filepath.Join(f.root, "outside")
	if e := os.WriteFile(outside, []byte("untouched"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(outside, f.path); e != nil {
		t.Fatal(e)
	}
	if _, e := f.restore(); !hasCode(e, "CODEX_CONFIG_UNREADABLE") {
		t.Fatalf("symlink restore error=%v", e)
	}
	if readConfigForTest(t, outside) != "untouched" {
		t.Fatal("symlink target changed")
	}
	_ = before
}

func TestCodexTargetLockSerializesConcurrentCalls(t *testing.T) {
	f := configFixture(t, "model='before'\n")
	lockDir := filepath.Join(f.codex, ".adroute")
	if e := os.Mkdir(lockDir, 0700); e != nil {
		t.Fatal(e)
	}
	lockPath := filepath.Join(lockDir, "lock.json")
	if e := os.WriteFile(lockPath, []byte(`{"pid":`+strconv.Itoa(os.Getpid())+`,"nonce":"other"}`), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := f.connect(); !hasCode(e, "CODEX_CONFIG_BUSY") {
		t.Fatalf("live lock wasn't honored: %v", e)
	}
	if e := os.Remove(lockPath); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := f.connect(); errs <- e }()
	}
	wg.Wait()
	close(errs)
	success := 0
	for e := range errs {
		if e == nil {
			success++
		} else if !hasCode(e, "CODEX_CONFIG_BUSY") {
			t.Fatalf("unexpected concurrent error: %v", e)
		}
	}
	if success == 0 {
		t.Fatal("concurrent operations made no progress")
	}
	var parsed map[string]any
	if e := toml.Unmarshal([]byte(readConfigForTest(t, f.path)), &parsed); e != nil {
		t.Fatalf("concurrent operation corrupted config: %v", e)
	}
}

func TestCodexConfigInvalidAndConflictPreserveOriginal(t *testing.T) {
	for _, original := range []string{"bad = \"unterminated\n", "profile='missing'\n", "[model_providers.adroute.auth]\ncommand='do-not-touch'\n", "model_providers='scalar'\n"} {
		t.Run(strconv.Itoa(len(original)), func(t *testing.T) {
			f := configFixture(t, original)
			_, err := f.connect()
			if err == nil {
				t.Fatal("invalid/conflicting TOML accepted")
			}
			if got := readConfigForTest(t, f.path); got != original {
				t.Fatalf("configuration changed on rejection: %q", got)
			}
			if _, e := os.Stat(filepath.Join(f.home, "codex", "connection.json")); !os.IsNotExist(e) {
				t.Fatalf("journal unexpectedly created: %v", e)
			}
		})
	}
}

func TestCodexPendingJournalRecoversBeforeConfigReplacement(t *testing.T) {
	original := "# preserve\nmodel='original'\n"
	f := configFixture(t, original)
	if _, e := f.connect(); e != nil {
		t.Fatal(e)
	}
	first := readConfigForTest(t, f.path)
	o := CodexConfigOptions{Home: f.home, CodexHome: f.codex, URL: "http://127.0.0.1:3002", ProxyToken: strings.Repeat("b", 64), Model: "second", InstallationID: "synthetic-installation"}
	if _, e := ConnectCodex(o); e != nil {
		t.Fatal(e)
	}
	recordPath := filepath.Join(f.home, "codex", "connection.json")
	var j codexJournal
	data, _ := os.ReadFile(recordPath)
	if e := json.Unmarshal(data, &j); e != nil {
		t.Fatal(e)
	}
	j.PendingText = &first
	pending, _ := json.Marshal(j)
	if e := os.WriteFile(recordPath, pending, 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(f.path, []byte(first), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := f.restore(); e != nil {
		t.Fatal(e)
	}
	if readConfigForTest(t, f.path) != original {
		t.Fatalf("pending journal failed recovery: %q", readConfigForTest(t, f.path))
	}
}

func TestCodexRestoreUsesRecordedTargetAndPreservesAuthOwnership(t *testing.T) {
	original := "[model_providers.adroute]\nenv_key='OLD_USER_KEY'\n"
	f := configFixture(t, original)
	if _, e := f.connect(); e != nil {
		t.Fatal(e)
	}
	connected := strings.Replace(readConfigForTest(t, f.path), strings.Repeat("a", 64), strings.Repeat("c", 64), 1)
	if e := os.WriteFile(f.path, []byte(connected), 0600); e != nil {
		t.Fatal(e)
	}
	r, e := RestoreCodex(f.home)
	if e != nil {
		t.Fatal(e)
	}
	if r["configPath"] != f.path {
		t.Fatalf("restore chose wrong target: %#v", r)
	}
	var got map[string]any
	b, _ := os.ReadFile(f.path)
	if e = toml.Unmarshal(b, &got); e != nil {
		t.Fatal(e)
	}
	provider := got["model_providers"].(map[string]any)["adroute"].(map[string]any)
	if provider["experimental_bearer_token"] != strings.Repeat("c", 64) || provider["env_key"] != nil {
		t.Fatalf("changed auth ownership was overwritten: %#v", provider)
	}
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

func TestTask3CodexConfigRoundTripActiveProfile(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "adroute")
	codex := filepath.Join(root, "codex")
	if err := os.Mkdir(codex, 0700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(codex, "config.toml")
	original := "profile = 'daily.work'\r\n[profiles.\"daily.work\"]\r\nmodel_provider = 'other' # retain\r\nmodel = 'old'\r\nreasoning_effort = 'high'\r\n"
	if err := os.WriteFile(p, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	args := CodexConfigOptions{Home: home, CodexHome: codex, URL: "http://127.0.0.1:3001", ProxyToken: strings.Repeat("a", 64), Model: "synthetic", InstallationID: "test"}
	if _, err := ConnectCodex(args); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), "# retain") || !strings.Contains(string(b), "reasoning_effort = 'high'") {
		t.Fatalf("lost unrelated syntax: %s", b)
	}
	if _, err := RestoreCodex(home); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(p)
	if string(b) != original {
		t.Fatalf("roundtrip mismatch: %q", b)
	}
}
