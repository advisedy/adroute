package platform

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestChmodFileDoesNotChangeReplacementACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "original")
	f, err := OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err = os.Rename(path, path+".moved"); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err = windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
	// A stale path may fail closed, but must never change the replacement's ACL.
	_ = ChmodFile(f, 0600)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if PrivateFile(path, info) {
		t.Fatal("securing the open file changed the replacement instead")
	}
}
