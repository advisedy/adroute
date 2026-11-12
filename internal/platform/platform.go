// Package platform contains the OS operations used by configuration transactions
// and service lifecycle management. Callers retain the same no-link and locking
// contract on Linux and Windows.
package platform

import (
	"os"
)

type Metadata struct {
	Dev, Ino, Nlink    uint64
	Size, Mtime, Ctime int64
}

func Lstat(path string) (Metadata, error) {
	f, err := OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return Metadata{}, err
	}
	defer f.Close()
	return Stat(f)
}

type Guard struct{ file *os.File }

func Lock(path string) (*Guard, error) {
	f, err := OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	meta, metaErr := Stat(f)
	if err == nil {
		err = metaErr
	}
	if err == nil && (!st.Mode().IsRegular() || meta.Nlink != 1) {
		err = os.ErrPermission
	}
	if err == nil {
		err = ChmodFile(f, 0600)
	}
	if err == nil {
		err = lockFile(f)
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return &Guard{f}, nil
}

func (g *Guard) Close() error {
	err := unlockFile(g.file)
	closeErr := g.file.Close()
	if err != nil {
		return err
	}
	return closeErr
}
