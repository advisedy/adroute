package platform

import (
	"os"
	"path/filepath"
	"testing"
)

// These tests protect the file/lock contract shared by config editing and CLI
// lifecycle operations, including the Windows implementation in native CI.
func TestGuardExcludesOtherWritersAndReleasesOnClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "guard")
	first, err := Lock(path)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := Lock(path); err == nil {
		second.Close()
		t.Fatal("concurrent writer acquired the guard")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := Lock(path)
	if err != nil {
		t.Fatalf("guard remained locked after close: %v", err)
	}
	next.Close()
}

func TestSafeOpenRejectsLinksAndDetectsReplacement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	if err := os.WriteFile(path, []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	f, err := OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	before, err := Stat(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if before.Nlink != 1 {
		t.Fatalf("link count=%d", before.Nlink)
	}
	replacement := filepath.Join(dir, "replacement")
	if err := os.WriteFile(replacement, []byte("after"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	after, err := Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if before.Dev == after.Dev && before.Ino == after.Ino {
		t.Fatal("replacement kept old file identity")
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Logf("symlink creation unavailable: %v", err)
		return
	}
	if linked, err := OpenFile(link, os.O_RDONLY, 0); err == nil {
		linked.Close()
		t.Fatal("followed a symbolic link")
	}
}

func TestProcessAliveAndDirectorySync(t *testing.T) {
	if !ProcessAlive(os.Getpid()) {
		t.Fatal("current process reported dead")
	}
	if ProcessAlive(-1) {
		t.Fatal("invalid process reported alive")
	}
	if err := SyncDir(filepath.Join(t.TempDir(), "config")); err != nil {
		t.Fatal(err)
	}
}

func TestPrivateDirectoryAndFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "tokens.json")
	if err := os.WriteFile(path, []byte("synthetic"), 0600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = ChmodFile(f, 0600); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()
	for _, path := range []string{dir, path} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !PrivateFile(path, info) {
			t.Fatalf("path not private: %s", path)
		}
	}
}
