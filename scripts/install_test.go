//go:build linux

package scripts

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func runInstaller(t *testing.T, home string, extraEnv []string, args ...string) (string, error) {
	t.Helper()
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", append([]string{filepath.Join(root, "install.sh")}, args...)...)
	cmd.Dir = t.TempDir()
	tmp := filepath.Join(home, ".tmp")
	if err := os.MkdirAll(tmp, 0700); err != nil {
		t.Fatal(err)
	}
	cmd.Env = []string{"HOME=" + home, "SHELL=/bin/bash", "PATH=/usr/bin:/bin", "TMPDIR=" + tmp}
	cmd.Env = overrideEnv(cmd.Env, extraEnv)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func overrideEnv(base, overrides []string) []string {
	values := map[string]string{}
	for _, entry := range append(base, overrides...) {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			values[key] = value
		}
	}
	result := make([]string, 0, len(values))
	for key, value := range values {
		result = append(result, key+"="+value)
	}
	return result
}

func runInteractiveInstaller(t *testing.T, home, input string, extraEnv []string, args ...string) (string, error) {
	t.Helper()
	if _, err := exec.LookPath("script"); err != nil {
		t.Skip("util-linux script(1) required for PTY consent test")
	}
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(home, ".tmp")
	if err := os.MkdirAll(tmp, 0700); err != nil {
		t.Fatal(err)
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	words := []string{"HOME=" + quote(home), "SHELL=/bin/bash", "PATH=/usr/bin:/bin", "TMPDIR=" + quote(tmp)}
	words = append(words, extraEnv...)
	words = append(words, quote(filepath.Join(root, "install.sh")))
	for _, arg := range args {
		words = append(words, quote(arg))
	}
	cmd := exec.Command("script", "-q", "-e", "-c", strings.Join(words, " "), "/dev/null")
	cmd.Stdin = strings.NewReader(input)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func payload(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "adroute-payload")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	return p
}

func shellSingleQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }

func assertNoInstallerTemps(t *testing.T, directories ...string) {
	t.Helper()
	for _, dir := range directories {
		for _, pattern := range []string{".adroute-rc.*", ".adroute-block.*", ".adroute-original.*", ".adroute-snapshot.*"} {
			matches, err := filepath.Glob(filepath.Join(dir, pattern))
			if err != nil || len(matches) != 0 {
				t.Fatalf("temporary installer artifacts in %s (%s): %v, %v", dir, pattern, matches, err)
			}
		}
	}
}

func requireInstallerError(t *testing.T, home string, args ...string) string {
	t.Helper()
	out, err := runInstaller(t, home, nil, args...)
	if err == nil {
		t.Fatalf("expected installer error, output: %s", out)
	}
	return out
}

func TestInstallerBuildsSourceFromOutsideRepository(t *testing.T) {
	home, prefix := t.TempDir(), filepath.Join(t.TempDir(), "local")
	goPath, err := exec.LookPath("go")
	if err != nil {
		t.Skip("Go toolchain unavailable")
	}
	path := filepath.Dir(goPath) + ":/usr/bin:/bin"
	if runtime.GOOS != "linux" {
		t.Skip("installer targets Linux/WSL")
	}
	cache, err := exec.Command(goPath, "env", "GOMODCACHE").Output()
	if err != nil {
		t.Fatal(err)
	}
	out, err := runInstaller(t, home, []string{"PATH=" + path, "SHELL=/bin/unknown", "GOMODCACHE=" + strings.TrimSpace(string(cache)), "GOPROXY=off"}, "--prefix", prefix)
	if err != nil {
		t.Fatalf("source install failed: %v\n%s", err, out)
	}
	got, err := exec.Command(filepath.Join(prefix, "bin", "adroute"), "--version").CombinedOutput()
	if err != nil || strings.TrimSpace(string(got)) == "" {
		t.Fatalf("installed version command: %q %v", got, err)
	}
}

func TestInstallerPrebuiltAndIdempotentPath(t *testing.T) {
	home := t.TempDir()
	prefix := filepath.Join(t.TempDir(), "pre fix's $dir")
	payload := filepath.Join(t.TempDir(), "payload")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\necho installed\n"), 0755); err != nil {
		t.Fatal(err)
	}
	args := []string{"--binary", payload, "--prefix", prefix, "--configure-path", "--shell", "bash"}
	if out, err := runInstaller(t, home, nil, args...); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	rc := filepath.Join(home, ".bashrc")
	first, err := os.ReadFile(rc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(first), "ADROUTE PATH BEGIN") {
		t.Fatalf("managed block missing: %s", first)
	}
	info, err := os.Stat(rc)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("new rc mode = %o", info.Mode().Perm())
	}
	if out, err := runInstaller(t, home, nil, args...); err != nil {
		t.Fatalf("repeat: %v\n%s", err, out)
	}
	assertNoInstallerTemps(t, home)
	second, _ := os.ReadFile(rc)
	if string(first) != string(second) {
		t.Fatalf("repeat modified managed file\nbefore=%q\nafter=%q", first, second)
	}
	if matches, _ := filepath.Glob(filepath.Join(home, ".adroute-backup.*")); len(matches) != 0 {
		t.Fatalf("matching managed block created backup(s): %v", matches)
	}
	installed := filepath.Join(prefix, "bin", "adroute")
	got, err := exec.Command(installed).Output()
	if err != nil || string(got) != "installed\n" {
		t.Fatalf("installed executable: %q %v", got, err)
	}
}

func TestInstallerNoTTYDoesNotPersistPath(t *testing.T) {
	home := t.TempDir()
	payload := filepath.Join(t.TempDir(), "payload")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	_, err := runInstaller(t, home, []string{"SHELL=/bin/zsh"}, "--binary", payload)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".zshrc")); !os.IsNotExist(err) {
		t.Fatal("noninteractive install edited startup file")
	}
}

func TestInstallerRejectsSameSizeEditDuringStaging(t *testing.T) {
	home := t.TempDir()
	oldPrefix := filepath.Join(t.TempDir(), "old-prefix")
	newPrefix := filepath.Join(t.TempDir(), "new-prefix")
	p := payload(t, "exit 0")
	args := []string{"--binary", p, "--prefix", oldPrefix, "--configure-path", "--shell", "bash"}
	if out, err := runInstaller(t, home, nil, args...); err != nil {
		t.Fatalf("initial install: %v %s", err, out)
	}
	rc := filepath.Join(home, ".bashrc")
	baseline, err := os.ReadFile(rc)
	if err != nil {
		t.Fatal(err)
	}
	oldTime := time.Unix(1_700_000_000, 123_000_000)
	if err := os.Chtimes(rc, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	edited := bytes.Replace(baseline, []byte(filepath.Join(oldPrefix, "bin")), []byte(strings.Repeat("X", len(filepath.Join(oldPrefix, "bin")))), 1)
	if bytes.Equal(edited, baseline) || len(edited) != len(baseline) {
		t.Fatal("test edit is not same-size and different")
	}
	mutator := filepath.Join(t.TempDir(), "bin")
	if err := os.Mkdir(mutator, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(mutator, "mutated")
	head := filepath.Join(mutator, "head")
	editPath := filepath.Join(mutator, "edited-rc")
	if err := os.WriteFile(editPath, edited, 0600); err != nil {
		t.Fatal(err)
	}
	program := fmt.Sprintf("#!/bin/sh\nif [ ! -e %q ]; then : > %q; cat %q > %q; touch -d %q %q; fi\nexec /usr/bin/head \"$@\"\n", marker, marker, editPath, rc, "@1700000000.123000000", rc)
	if err := os.WriteFile(head, []byte(program), 0755); err != nil {
		t.Fatal(err)
	}
	path := mutator + ":/usr/bin:/bin"
	out, err := runInstaller(t, home, []string{"PATH=" + path}, "--binary", p, "--prefix", newPrefix, "--configure-path", "--shell", "bash")
	if err == nil {
		t.Fatalf("concurrent same-size edit was overwritten without conflict: %s", out)
	}
	after, _ := os.ReadFile(rc)
	if !bytes.Equal(after, edited) {
		t.Fatalf("later external edit was not preserved\nwant=%q\ngot =%q", edited, after)
	}
	profile, _ := os.ReadFile(filepath.Join(home, ".profile"))
	if !bytes.Equal(profile, baseline) {
		t.Fatalf("another selected rc committed before conflict: %q", profile)
	}
	gotInfo, _ := os.Stat(rc)
	if !gotInfo.ModTime().Equal(oldTime) {
		t.Fatalf("test failed to restore same-second mtime: %v", gotInfo.ModTime())
	}
	backups, _ := filepath.Glob(filepath.Join(home, ".adroute-backup.*"))
	if len(backups) != 0 {
		t.Fatalf("conflict unexpectedly created backup(s): %v", backups)
	}
	assertNoInstallerTemps(t, home)
}

func TestInstallerNoTTYReportsCurrentPathAndShadowWithoutWriting(t *testing.T) {
	home, p := t.TempDir(), payload(t, "exit 0")
	old := t.TempDir()
	shadow := filepath.Join(old, "adroute")
	if err := os.WriteFile(shadow, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/usr/bin:/bin", old + ":/usr/bin:/bin", filepath.Join(home, ".local", "bin") + ":" + old + ":/usr/bin:/bin"} {
		h := t.TempDir()
		out, err := runInstaller(t, h, []string{"PATH=" + path}, "--binary", p)
		if err != nil {
			t.Fatalf("install: %v %s", err, out)
		}
		if !strings.Contains(out, "PATH was NOT configured") || !strings.Contains(out, filepath.Join(h, ".local", "bin", "adroute")) {
			t.Fatalf("status missing: %s", out)
		}
		if strings.Contains(path, old) && (!strings.Contains(out, "shadow") || !strings.Contains(out, shadow)) {
			t.Fatalf("existing command shadow omitted: %s", out)
		}
		if _, err := os.Stat(filepath.Join(h, ".bashrc")); !os.IsNotExist(err) {
			t.Fatal("no-TTY install edited rc")
		}
	}
}

func TestInstallerMalformedManagedMarkersFailSafely(t *testing.T) {
	home := t.TempDir()
	rc := filepath.Join(home, ".bashrc")
	original := []byte("keep\n# >>> ADROUTE PATH BEGIN >>>\npartial\n")
	if err := os.WriteFile(rc, original, 0644); err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(t.TempDir(), "payload")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	out, err := runInstaller(t, home, nil, "--binary", payload, "--configure-path", "--shell", "bash")
	if err == nil {
		t.Fatalf("expected malformed marker failure: %s", out)
	}
	after, _ := os.ReadFile(rc)
	if string(after) != string(original) {
		t.Fatal("malformed file was changed")
	}
}

func TestInstallerBackupAndLoginProfilePriority(t *testing.T) {
	home := t.TempDir()
	profile := filepath.Join(home, ".bash_login")
	original := []byte("# preserve this byte-for-byte\n")
	if err := os.WriteFile(profile, original, 0640); err != nil {
		t.Fatal(err)
	}
	// A lower-priority .profile must not be selected when .bash_login exists.
	if err := os.WriteFile(filepath.Join(home, ".profile"), []byte("unused\n"), 0600); err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(t.TempDir(), "payload")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	out, err := runInstaller(t, home, nil, "--binary", payload, "--configure-path", "--shell", "bash")
	if err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	backups, err := filepath.Glob(filepath.Join(home, ".adroute-backup.*"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("unique backup list %v: %v", backups, err)
	}
	backup, err := os.ReadFile(backups[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(backup) != string(original) {
		t.Fatalf("backup differs: %q", backup)
	}
	info, err := os.Stat(backups[0])
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("backup mode: %v %v", info, err)
	}
	installedInfo, err := os.Stat(profile)
	if err != nil || installedInfo.Mode().Perm() != 0640 {
		t.Fatalf("original startup mode not preserved: %v %v", installedInfo, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".profile.adroute-backup")); !os.IsNotExist(err) {
		t.Fatal("unused lower-priority profile modified")
	}
}

func TestInstallerNoChangeFirstTargetThenChangedSecondTarget(t *testing.T) {
	home, prefix := t.TempDir(), filepath.Join(t.TempDir(), "prefix")
	p := payload(t, "exit 0")
	args := []string{"--binary", p, "--prefix", prefix, "--configure-path", "--shell", "bash"}
	if out, err := runInstaller(t, home, nil, args...); err != nil {
		t.Fatalf("first install: %v %s", err, out)
	}
	profile := filepath.Join(home, ".profile")
	before, _ := os.ReadFile(profile)
	stale := bytes.Replace(before, []byte(filepath.Join(prefix, "bin")), []byte(filepath.Join(t.TempDir(), "stale", "bin")), 1)
	if err := os.WriteFile(profile, stale, 0600); err != nil {
		t.Fatal(err)
	}
	out, err := runInstaller(t, home, nil, args...)
	if err != nil {
		t.Fatalf("update with unchanged first file: %v\n%s", err, out)
	}
	backups, err := filepath.Glob(filepath.Join(home, ".adroute-backup.*"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("expected only the changed target backup: %v %v", backups, err)
	}
	got, err := os.ReadFile(backups[0])
	if err != nil || !bytes.Equal(got, stale) {
		t.Fatalf("backup content: %q %v", got, err)
	}
}

func TestInstallerZshZdotdirBlockAddsPathExactlyOnce(t *testing.T) {
	home, zd := t.TempDir(), t.TempDir()
	prefix := filepath.Join(t.TempDir(), "with space apostrophe's $value")
	payload := filepath.Join(t.TempDir(), "payload")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\necho zsh-ok\n"), 0755); err != nil {
		t.Fatal(err)
	}
	_, err := runInstaller(t, home, []string{"SHELL=/usr/bin/zsh", "ZDOTDIR=" + zd}, "--binary", payload, "--prefix", prefix, "--configure-path")
	if err != nil {
		t.Fatal(err)
	}
	if out, err := runInstaller(t, home, []string{"SHELL=/usr/bin/zsh", "ZDOTDIR=" + zd}, "--binary", payload, "--prefix", prefix, "--configure-path"); err != nil {
		t.Fatalf("repeat zsh install: %v %s", err, out)
	}
	assertNoInstallerTemps(t, zd)
	cmd := exec.Command("zsh", "-fc", "source \"$ZDOTDIR/.zshrc\"; source \"$ZDOTDIR/.zshrc\"; print -r -- $PATH")
	cmd.Env = []string{"HOME=" + home, "ZDOTDIR=" + zd, "PATH=/usr/bin:/bin"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("controlled zsh: %v\n%s", err, out)
	}
	want := filepath.Join(prefix, "bin")
	count := 0
	for _, dir := range strings.Split(strings.TrimSpace(string(out)), ":") {
		if dir == want {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("destination occurs %d times in PATH %q", count, out)
	}
	cmd = exec.Command("zsh", "-fc", "source \"$ZDOTDIR/.zshrc\"; command -v adroute")
	cmd.Env = []string{"HOME=" + home, "ZDOTDIR=" + zd, "PATH=/usr/bin:/bin"}
	got, err := cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(got)) != filepath.Join(prefix, "bin", "adroute") {
		t.Fatalf("zsh command resolution: %q %v", got, err)
	}
}

func TestInstallerBytePreservingManagedBlockReplacement(t *testing.T) {
	for _, suffix := range [][]byte{[]byte("# trailing\r\n\r\n"), []byte("# no final newline")} {
		t.Run(fmt.Sprintf("suffix-%x", suffix[len(suffix)-1]), func(t *testing.T) {
			home := t.TempDir()
			oldPrefix := filepath.Join(t.TempDir(), "old")
			newPrefix := filepath.Join(t.TempDir(), "new apostrophe's $literal")
			p := payload(t, "exit 0")
			var want []byte
			for _, prefix := range []string{oldPrefix, newPrefix} {
				if out, err := runInstaller(t, home, nil, "--binary", p, "--prefix", prefix, "--configure-path", "--shell", "bash"); err != nil {
					t.Fatalf("install: %v\n%s", err, out)
				}
				if prefix == oldPrefix {
					rc := filepath.Join(home, ".bashrc")
					initial, _ := os.ReadFile(rc)
					start := bytes.Index(initial, []byte("# >>> ADROUTE PATH BEGIN >>>"))
					endMarker := []byte("# <<< ADROUTE PATH END <<<")
					end := bytes.Index(initial, endMarker) + len(endMarker)
					block := bytes.ReplaceAll(initial[start:end], []byte("\n"), []byte("\r\n"))
					block = append(block, '\r', '\n')
					fixture := append([]byte("# prefix sentinel\r\n"), block...)
					fixture = append(fixture, suffix...)
					if err := os.WriteFile(rc, fixture, 0640); err != nil {
						t.Fatal(err)
					}
					wantBlock := bytes.Replace(block, []byte(shellSingleQuote(filepath.Join(oldPrefix, "bin"))), []byte(shellSingleQuote(filepath.Join(newPrefix, "bin"))), 1)
					want = append([]byte("# prefix sentinel\r\n"), wantBlock...)
					want = append(want, suffix...)
				}
			}
			rc := filepath.Join(home, ".bashrc")
			got, _ := os.ReadFile(rc)
			// Reconstruct the exact desired bytes, including all sentinels and line endings.
			if !bytes.Equal(got, want) {
				t.Fatalf("byte splice changed outside bytes\nwant=%q\ngot =%q", want, got)
			}
		})
	}
}

func TestInstallerInteractiveConsentPrintsTargetsAndDeclineIsSafe(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		accepted    bool
	}{{"accept", "y\n", true}, {"decline", "n\n", false}, {"eof", "", false}} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			p := payload(t, "exit 0")
			out, err := runInteractiveInstaller(t, home, tc.input, nil, "--binary", p, "--shell", "bash")
			if err != nil {
				t.Fatalf("installer: %v\n%s", err, out)
			}
			if !strings.Contains(out, filepath.Join(home, ".bashrc")) || !strings.Contains(out, filepath.Join(home, ".profile")) {
				t.Fatalf("prompt omitted concrete targets: %s", out)
			}
			_, statErr := os.Stat(filepath.Join(home, ".bashrc"))
			if tc.accepted != (statErr == nil) {
				t.Fatalf("accepted=%v rc stat=%v output=%s", tc.accepted, statErr, out)
			}
			if !strings.Contains(out, "parent PATH") || !strings.Contains(out, filepath.Join(home, ".local", "bin", "adroute")) {
				t.Fatalf("status/full path omitted: %s", out)
			}
		})
	}
}

func TestInstallerPathModesShellErrorsAndNoGoBinary(t *testing.T) {
	t.Run("no-modify", func(t *testing.T) {
		home := t.TempDir()
		p := payload(t, "exit 0")
		out, err := runInstaller(t, home, nil, "--binary", p, "--no-modify-path")
		if err != nil || !strings.Contains(out, "Current process PATH") || !strings.Contains(out, "Use the installed executable directly") {
			t.Fatalf("status: %v %s", err, out)
		}
		if _, err := os.Stat(filepath.Join(home, ".bashrc")); !os.IsNotExist(err) {
			t.Fatal("no-modify edited rc")
		}
	})
	t.Run("unknown-shell", func(t *testing.T) {
		home := t.TempDir()
		p := payload(t, "exit 0")
		out, err := runInstaller(t, home, []string{"SHELL=/bin/fish"}, "--binary", p)
		if err != nil || !strings.Contains(out, filepath.Join(home, ".local", "bin", "adroute")) || !strings.Contains(out, "No startup file") {
			t.Fatalf("unknown status: %v %s", err, out)
		}
		_, err = runInstaller(t, t.TempDir(), []string{"SHELL=/bin/fish"}, "--binary", p, "--configure-path")
		if err == nil {
			t.Fatal("explicit unknown-shell configuration succeeded")
		}
	})
	t.Run("binary-without-Go", func(t *testing.T) {
		home := t.TempDir()
		p := payload(t, "echo no-go")
		out, err := runInstaller(t, home, []string{"PATH=/usr/bin:/bin"}, "--binary", p, "--no-modify-path")
		if err != nil {
			t.Fatalf("binary install unexpectedly needed Go: %v %s", err, out)
		}
		got, err := exec.Command(filepath.Join(home, ".local", "bin", "adroute")).CombinedOutput()
		if err != nil || string(got) != "no-go\n" {
			t.Fatalf("installed payload: %q %v", got, err)
		}
	})
	t.Run("invalid-flags", func(t *testing.T) {
		home := t.TempDir()
		p := payload(t, "exit 0")
		for _, args := range [][]string{{"--wat"}, {"--prefix"}, {"--configure-path", "--no-modify-path"}, {"--shell", "fish"}, {"unexpected"}} {
			if out := requireInstallerError(t, home, append([]string{"--binary", p}, args...)...); strings.TrimSpace(out) == "" {
				t.Fatalf("no clear error for %v", args)
			}
		}
	})
	t.Run("unsafe-paths", func(t *testing.T) {
		p := payload(t, "exit 0")
		if out := requireInstallerError(t, t.TempDir(), "--binary", p, "--prefix", filepath.Join(t.TempDir(), "bad:prefix")); !strings.Contains(out, "colon") {
			t.Fatalf("unsafe prefix error: %s", out)
		}
		home := t.TempDir()
		if out, err := runInstaller(t, home, []string{"ZDOTDIR=relative"}, "--binary", p, "--configure-path", "--shell", "zsh"); err == nil || !strings.Contains(out, "not absolute") {
			t.Fatalf("relative ZDOTDIR: %v %s", err, out)
		}
		if out, err := runInstaller(t, "relative-home", nil, "--binary", p); err == nil || !strings.Contains(out, "absolute") {
			t.Fatalf("relative HOME: %v %s", err, out)
		}
	})
}

func TestInstallerUnsafeStartupFilesAndAllFilePreflight(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			home := t.TempDir()
			p := payload(t, "exit 0")
			rc := filepath.Join(home, ".bashrc")
			switch kind {
			case "symlink":
				if err := os.Symlink(filepath.Join(home, "target"), rc); err != nil {
					t.Skip(err)
				}
			case "hardlink":
				source := filepath.Join(home, "source")
				if err := os.WriteFile(source, []byte("preserve\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(source, rc); err != nil {
					t.Skip(err)
				}
			case "directory":
				if err := os.Mkdir(rc, 0700); err != nil {
					t.Fatal(err)
				}
			}
			_, err := runInstaller(t, home, nil, "--binary", p, "--configure-path", "--shell", "bash")
			if err == nil {
				t.Fatal("unsafe startup target accepted")
			}
		})
	}
	t.Run("parent-symlink", func(t *testing.T) {
		home, outside := t.TempDir(), t.TempDir()
		if err := os.Symlink(outside, filepath.Join(home, ".local")); err != nil {
			t.Skip(err)
		}
		p := payload(t, "exit 0")
		if _, err := runInstaller(t, home, nil, "--binary", p); err == nil {
			t.Fatal("symlink prefix ancestor accepted")
		}
	})
	t.Run("later-target-malformed-no-partial-write", func(t *testing.T) {
		home := t.TempDir()
		p := payload(t, "exit 0")
		rc := filepath.Join(home, ".bashrc")
		good := []byte("keep me exactly\n")
		bad := []byte("# <<< ADROUTE PATH END <<<\n# >>> ADROUTE PATH BEGIN >>>\n")
		if err := os.WriteFile(rc, good, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, ".profile"), bad, 0600); err != nil {
			t.Fatal(err)
		}
		_, err := runInstaller(t, home, nil, "--binary", p, "--configure-path", "--shell", "bash")
		if err == nil {
			t.Fatal("reversed markers accepted")
		}
		after, _ := os.ReadFile(rc)
		if !bytes.Equal(good, after) {
			t.Fatalf("first file partially changed: %q", after)
		}
		profile, _ := os.ReadFile(filepath.Join(home, ".profile"))
		if !bytes.Equal(bad, profile) {
			t.Fatal("malformed profile modified")
		}
		staged, _ := filepath.Glob(filepath.Join(home, ".adroute-rc.*"))
		if len(staged) != 0 {
			t.Fatalf("staging files leaked: %v", staged)
		}
	})
	for name, malformed := range map[string][]byte{
		"inline-start": []byte("prefix # >>> ADROUTE PATH BEGIN >>>\n# <<< ADROUTE PATH END <<<\n"),
		"inline-end":   []byte("# >>> ADROUTE PATH BEGIN >>>\n# <<< ADROUTE PATH END <<< suffix\n"),
		"reversed":     []byte("# <<< ADROUTE PATH END <<<\n# >>> ADROUTE PATH BEGIN >>>\n"),
		"duplicate":    []byte("# >>> ADROUTE PATH BEGIN >>>\n# <<< ADROUTE PATH END <<<\n# >>> ADROUTE PATH BEGIN >>>\n# <<< ADROUTE PATH END <<<\n"),
		"broken":       []byte("# >>> ADROUTE PATH BEGIN >>>\npartial\n"),
	} {
		t.Run("marker-"+name, func(t *testing.T) {
			home := t.TempDir()
			p := payload(t, "exit 0")
			rc := filepath.Join(home, ".bashrc")
			if err := os.WriteFile(rc, malformed, 0600); err != nil {
				t.Fatal(err)
			}
			_, err := runInstaller(t, home, nil, "--binary", p, "--configure-path", "--shell", "bash")
			if err == nil {
				t.Fatal("malformed marker accepted")
			}
			after, _ := os.ReadFile(rc)
			if !bytes.Equal(malformed, after) {
				t.Fatalf("malformed file changed: %q", after)
			}
		})
	}
}

func TestInstallerFailedBuildPreservesBinaryAndStartup(t *testing.T) {
	home, prefix := t.TempDir(), t.TempDir()
	p := payload(t, "echo old")
	if _, err := runInstaller(t, home, nil, "--binary", p, "--prefix", prefix, "--configure-path", "--shell", "bash"); err != nil {
		t.Fatal(err)
	}
	installed := filepath.Join(prefix, "bin", "adroute")
	before, _ := os.ReadFile(installed)
	rc := filepath.Join(home, ".bashrc")
	rcBefore, _ := os.ReadFile(rc)
	fakeBin := filepath.Join(t.TempDir(), "bin")
	if err := os.Mkdir(fakeBin, 0700); err != nil {
		t.Fatal(err)
	}
	fakeGo := filepath.Join(fakeBin, "go")
	if err := os.WriteFile(fakeGo, []byte("#!/bin/sh\necho forced-build-failure >&2\nexit 19\n"), 0755); err != nil {
		t.Fatal(err)
	}
	_, err := runInstaller(t, home, []string{"PATH=" + fakeBin + ":/usr/bin:/bin"}, "--prefix", prefix)
	if err == nil {
		t.Fatal("fake failing Go build succeeded")
	}
	after, _ := os.ReadFile(installed)
	if !bytes.Equal(before, after) {
		t.Fatal("failed build replaced installed binary")
	}
	rcAfter, _ := os.ReadFile(rc)
	if !bytes.Equal(rcBefore, rcAfter) {
		t.Fatal("failed build changed startup file")
	}
}

func TestInstallerCurrentPathStillConfiguresAndWarnsOnShadow(t *testing.T) {
	home, prefix := t.TempDir(), filepath.Join(t.TempDir(), "prefix")
	p := payload(t, "exit 0")
	bindir := filepath.Join(prefix, "bin")
	old := filepath.Join(t.TempDir(), "old-bin")
	if err := os.Mkdir(old, 0700); err != nil {
		t.Fatal(err)
	}
	shadow := filepath.Join(old, "adroute")
	if err := os.WriteFile(shadow, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	path := old + ":" + bindir + ":/usr/bin:/bin"
	out, err := runInstaller(t, home, []string{"PATH=" + path}, "--binary", p, "--prefix", prefix, "--configure-path")
	if err != nil {
		t.Fatalf("configure despite current PATH: %v %s", err, out)
	}
	if !strings.Contains(out, "shadow") || !strings.Contains(out, shadow) {
		t.Fatalf("shadow not reported: %s", out)
	}
	if _, err := os.Stat(filepath.Join(home, ".bashrc")); err != nil {
		t.Fatal("current PATH incorrectly suppressed persistent configuration")
	}
}

func TestInstallerBashRcAndLoginProfileFreshPath(t *testing.T) {
	home, prefix := t.TempDir(), filepath.Join(t.TempDir(), "quote's $bin")
	p := payload(t, "echo bash-ok")
	if _, err := runInstaller(t, home, nil, "--binary", p, "--prefix", prefix, "--configure-path", "--shell", "bash"); err != nil {
		t.Fatal(err)
	}
	for _, rcName := range []string{".bashrc", ".profile"} {
		cmd := exec.Command("bash", "--noprofile", "--norc", "-c", "source \"$HOME/"+rcName+"\"; source \"$HOME/"+rcName+"\"; command -v adroute; printf '%s' \"$PATH\"")
		cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin"}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bash source %s: %v %s", rcName, err, out)
		}
		lines := strings.SplitN(string(out), "\n", 2)
		if len(lines) != 2 || strings.TrimSpace(lines[0]) != filepath.Join(prefix, "bin", "adroute") {
			t.Fatalf("bash command not resolved via prefix: %q", out)
		}
		count := 0
		for _, dir := range strings.Split(lines[1], ":") {
			if dir == filepath.Join(prefix, "bin") {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("bash PATH contains prefix %d times: %q", count, lines[1])
		}
	}
}
