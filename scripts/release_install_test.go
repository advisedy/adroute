package scripts

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

func TestReleaseInstallerDownloadsVerifiesAndPreservesExistingOnMismatch(t *testing.T) {
	testReleaseInstaller(t, false)
}

func TestOnlineInstallerPipelineDownloadsVerifiesAndPreservesExistingOnMismatch(t *testing.T) {
	testReleaseInstaller(t, true)
}

func testReleaseInstaller(t *testing.T, online bool) {
	t.Helper()
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	suffix, helper, bootstrap := "", "install.sh", "install-release.sh"
	if runtime.GOOS == "windows" {
		suffix, helper, bootstrap = ".exe", "install.ps1", "install-release.ps1"
	}
	binary := filepath.Join(t.TempDir(), "adroute"+suffix)
	build := exec.Command("go", "build", "-o", binary, "./cmd/adroute")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, output)
	}
	payload, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	installer, err := os.ReadFile(filepath.Join(root, helper))
	if err != nil {
		t.Fatal(err)
	}
	bootstrapBody, err := os.ReadFile(filepath.Join(root, bootstrap))
	if err != nil {
		t.Fatal(err)
	}
	entry := "install-online.sh"
	if runtime.GOOS == "windows" {
		entry = "install-online.ps1"
	}
	var entryBody []byte
	if online {
		entryBody, err = os.ReadFile(filepath.Join(root, entry))
		if err != nil {
			t.Fatal(err)
		}
	}
	asset := "adroute_0.1.0_" + runtime.GOOS + "_" + runtime.GOARCH + suffix
	var corrupt atomic.Bool
	var corruptBootstrap atomic.Bool
	var unavailable atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/releases/latest" {
			if unavailable.Load() {
				http.NotFound(w, r)
				return
			}
			http.Redirect(w, r, "/releases/tag/v0.1.0", http.StatusFound)
			return
		}
		if r.URL.Path == "/releases/tag/v0.1.0" {
			return
		}
		switch filepath.Base(r.URL.Path) {
		case entry:
			w.Write(entryBody)
		case bootstrap:
			if corruptBootstrap.Load() {
				w.Write([]byte("invalid bootstrap"))
			} else {
				w.Write(bootstrapBody)
			}
		case asset:
			if corrupt.Load() {
				w.Write([]byte("corrupted download"))
			} else {
				w.Write(payload)
			}
		case helper:
			w.Write(installer)
		case "SHA256SUMS":
			fmt.Fprintf(w, "%x  %s\n%x  %s\n%x  %s\n", sha256.Sum256(payload), asset, sha256.Sum256(installer), helper, sha256.Sum256(bootstrapBody), bootstrap)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	prefix := filepath.Join(t.TempDir(), "with spaces")
	home := t.TempDir()
	run := func() ([]byte, error) {
		var cmd *exec.Cmd
		if runtime.GOOS == "windows" {
			if online {
				cmd = exec.Command("pwsh", "-NoProfile", "-NonInteractive", "-Command", "$ErrorActionPreference = 'Stop'; $previousUserPath = [Environment]::GetEnvironmentVariable('Path', 'User'); try { irm ($env:ADROUTE_INSTALL_REPOSITORY_URL + '/"+entry+"') | iex; if ((Get-Command adroute).Source -ne (Join-Path $env:ADROUTE_INSTALL_DIR 'adroute.exe')) { throw 'Installed command is missing from current PATH' } } finally { [Environment]::SetEnvironmentVariable('Path', $previousUserPath, 'User') }")
			} else {
				cmd = exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", filepath.Join(root, bootstrap), "-ReleaseBaseURL", server.URL, "-InstallDir", prefix, "-NoModifyPath")
			}
		} else {
			if online {
				cmd = exec.Command("bash", "-o", "pipefail", "-c", `curl -fsSL "$ADROUTE_INSTALL_REPOSITORY_URL/install-online.sh" | bash`)
			} else {
				cmd = exec.Command("bash", filepath.Join(root, bootstrap), "--prefix", prefix, "--no-modify-path")
			}
		}
		cmd.Dir = t.TempDir()
		cmd.Env = append(os.Environ(), "HOME="+home, "SHELL=/bin/bash", "ADROUTE_RELEASE_BASE_URL="+server.URL, "ADROUTE_INSTALL_REPOSITORY_URL="+server.URL, "ADROUTE_INSTALL_DIR="+prefix)
		return cmd.CombinedOutput()
	}
	for i := 0; i < 2; i++ {
		if output, err := run(); err != nil {
			t.Fatalf("install/update: %v %s", err, output)
		}
	}
	installed := filepath.Join(prefix, "bin", "adroute")
	if runtime.GOOS == "windows" {
		installed = filepath.Join(prefix, "adroute.exe")
	}
	if output, err := exec.Command(installed, "--version").CombinedOutput(); err != nil || strings.TrimSpace(string(output)) != "0.1.0" {
		t.Fatalf("version: %v %s", err, output)
	}
	if online {
		if runtime.GOOS != "windows" {
			profile, err := os.ReadFile(filepath.Join(home, ".bashrc"))
			if err != nil || !strings.Contains(string(profile), prefix+"/bin") {
				t.Fatalf("PATH configuration: %v %s", err, profile)
			}
		}
		corruptBootstrap.Store(true)
		if output, err := run(); err == nil {
			t.Fatalf("bootstrap checksum mismatch accepted: %s", output)
		}
		corruptBootstrap.Store(false)
		unavailable.Store(true)
		if output, err := run(); err == nil {
			t.Fatalf("missing latest release accepted: %s", output)
		}
		unavailable.Store(false)
	}
	corrupt.Store(true)
	if output, err := run(); err == nil {
		t.Fatalf("checksum mismatch accepted: %s", output)
	}
	if output, err := exec.Command(installed, "--version").CombinedOutput(); err != nil || strings.TrimSpace(string(output)) != "0.1.0" {
		t.Fatalf("failed update damaged installation: %v %s", err, output)
	}
}

// A truncated release download must resume rather than fail the install: the
// binary is several megabytes and comes from a different host than github.com.
func TestReleaseInstallerResumesTruncatedDownload(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	suffix, helper, bootstrap := "", "install.sh", "install-release.sh"
	if runtime.GOOS == "windows" {
		suffix, helper, bootstrap = ".exe", "install.ps1", "install-release.ps1"
	}
	binary := filepath.Join(t.TempDir(), "adroute"+suffix)
	build := exec.Command("go", "build", "-o", binary, "./cmd/adroute")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, output)
	}
	payload, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	installer, err := os.ReadFile(filepath.Join(root, helper))
	if err != nil {
		t.Fatal(err)
	}
	bootstrapBody, err := os.ReadFile(filepath.Join(root, bootstrap))
	if err != nil {
		t.Fatal(err)
	}
	asset := "adroute_0.1.0_" + runtime.GOOS + "_" + runtime.GOARCH + suffix
	var truncated atomic.Int32
	var resumed atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/releases/latest" {
			http.Redirect(w, r, "/releases/tag/v0.1.0", http.StatusFound)
			return
		}
		if r.URL.Path == "/releases/tag/v0.1.0" {
			return
		}
		name := filepath.Base(r.URL.Path)
		if name != asset {
			switch name {
			case bootstrap:
				w.Write(bootstrapBody)
			case helper:
				w.Write(installer)
			case "SHA256SUMS":
				fmt.Fprintf(w, "%x  %s\n%x  %s\n%x  %s\n", sha256.Sum256(payload), asset, sha256.Sum256(installer), helper, sha256.Sum256(bootstrapBody), bootstrap)
			default:
				http.NotFound(w, r)
			}
			return
		}
		// Cut the first two transfers in half, then serve the rest. A resumed
		// request proves the installer asked for the remaining bytes instead of
		// restarting, and the checksum still has to match for the install to pass.
		logRange := r.Header.Get("Range")
		start := 0
		if rest, ok := strings.CutPrefix(logRange, "bytes="); ok {
			fmt.Sscanf(strings.TrimSuffix(rest, "-"), "%d", &start)
		}
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(payload)-start))
		if start > 0 {
			resumed.Store(true)
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(payload)-1, len(payload)))
			w.WriteHeader(http.StatusPartialContent)
		}
		body := payload[start:]
		if truncated.Add(1) <= 2 {
			w.Write(body[:len(body)/2])
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			// Closing the connection mid-body reproduces the EOF users see.
			if hijacker, ok := w.(http.Hijacker); ok {
				if conn, _, err := hijacker.Hijack(); err == nil {
					conn.Close()
				}
			}
			return
		}
		w.Write(body)
	}))
	defer server.Close()

	prefix := filepath.Join(t.TempDir(), "with spaces")
	cmd := exec.Command("bash", filepath.Join(root, bootstrap), "--prefix", prefix, "--no-modify-path")
	if runtime.GOOS == "windows" {
		cmd = exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", filepath.Join(root, bootstrap), "-ReleaseBaseURL", server.URL, "-InstallDir", prefix, "-NoModifyPath")
	}
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), "HOME="+t.TempDir(), "SHELL=/bin/bash", "ADROUTE_RELEASE_BASE_URL="+server.URL)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("install after truncation: %v %s", err, output)
	}
	if !resumed.Load() {
		t.Fatal("installer restarted the download instead of resuming it")
	}
	installed := filepath.Join(prefix, "bin", "adroute")
	if runtime.GOOS == "windows" {
		installed = filepath.Join(prefix, "adroute.exe")
	}
	if output, err := exec.Command(installed, "--version").CombinedOutput(); err != nil || strings.TrimSpace(string(output)) != "0.1.0" {
		t.Fatalf("version after resumed download: %v %s", err, output)
	}
}

func TestOnlineInstallerRejectsWindowsPowerShell5(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows PowerShell is only available on Windows")
	}
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "$ErrorActionPreference = 'Stop'; Get-Content -Raw -LiteralPath $env:ADROUTE_TEST_ENTRY | Invoke-Expression")
	cmd.Env = append(os.Environ(), "ADROUTE_TEST_ENTRY="+filepath.Join(root, "install-online.ps1"))
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "requires PowerShell 7+") {
		t.Fatalf("version rejection: %v %s", err, output)
	}
}
