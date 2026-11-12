package platform

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMetadataOnlyNeedsDirectoryTraversal(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "searchable")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0111); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0700)
	if _, err := Lstat(dir); err != nil {
		t.Fatalf("metadata lookup required directory read permission: %v", err)
	}
}
