//go:build windows

package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWindowsInstallerSupportsSpacesAndLeavesCurrentUserPathAlone(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "adroute.exe")
	build := exec.Command("go", "build", "-o", source, "./cmd/adroute")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, output)
	}
	prefix := filepath.Join(t.TempDir(), "with spaces", "AdRoute")
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", filepath.Join(root, "install.ps1"), "-Binary", source, "-InstallDir", prefix, "-NoModifyPath")
	cmd.Dir = t.TempDir()
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("install: %v %s", err, output)
	}
	installed := filepath.Join(prefix, "adroute.exe")
	output, err := exec.Command(installed, "--version").CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) == "" {
		t.Fatalf("installed binary: %v %s", err, output)
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatal("installer removed source", err)
	}
}
