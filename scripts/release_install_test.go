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
	asset := "adroute_0.1.0_" + runtime.GOOS + "_" + runtime.GOARCH + suffix
	var corrupt atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch filepath.Base(r.URL.Path) {
		case asset:
			if corrupt.Load() {
				w.Write([]byte("corrupted download"))
			} else {
				w.Write(payload)
			}
		case helper:
			w.Write(installer)
		case "SHA256SUMS":
			fmt.Fprintf(w, "%x  %s\n%x  %s\n", sha256.Sum256(payload), asset, sha256.Sum256(installer), helper)
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
			cmd = exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", filepath.Join(root, bootstrap), "-ReleaseBaseURL", server.URL, "-InstallDir", prefix, "-NoModifyPath")
		} else {
			cmd = exec.Command("bash", filepath.Join(root, bootstrap), "--prefix", prefix, "--no-modify-path")
		}
		cmd.Dir = t.TempDir()
		cmd.Env = append(os.Environ(), "HOME="+home, "ADROUTE_RELEASE_BASE_URL="+server.URL)
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
	corrupt.Store(true)
	if output, err := run(); err == nil {
		t.Fatalf("checksum mismatch accepted: %s", output)
	}
	if output, err := exec.Command(installed, "--version").CombinedOutput(); err != nil || strings.TrimSpace(string(output)) != "0.1.0" {
		t.Fatalf("failed update damaged installation: %v %s", err, output)
	}
}
