package platform

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

func OpenFile(path string, flag int, mode os.FileMode) (*os.File, error) {
	fd, err := syscall.Open(path, flag|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, uint32(mode.Perm()))
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}
func Stat(f *os.File) (Metadata, error) {
	st, err := f.Stat()
	if err != nil {
		return Metadata{}, err
	}
	s := st.Sys().(*syscall.Stat_t)
	return metadata(s), nil
}
func Lstat(path string) (Metadata, error) {
	var s syscall.Stat_t
	if err := syscall.Lstat(path, &s); err != nil {
		return Metadata{}, err
	}
	if s.Mode&syscall.S_IFMT == syscall.S_IFLNK {
		return Metadata{}, os.ErrPermission
	}
	return metadata(&s), nil
}
func metadata(s *syscall.Stat_t) Metadata {
	return Metadata{uint64(s.Dev), s.Ino, uint64(s.Nlink), s.Size, s.Mtim.Sec*1e9 + s.Mtim.Nsec, s.Ctim.Sec*1e9 + s.Ctim.Nsec}
}
func lockFile(f *os.File) error   { return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) }
func unlockFile(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
func Detach(cmd *exec.Cmd)          { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
func StopChild(p *os.Process) error { return p.Signal(syscall.SIGTERM) }
func Signals() []os.Signal          { return []os.Signal{os.Interrupt, syscall.SIGTERM} }
func SyncDir(path string) error {
	f, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
func PrivateFile(path string, info os.FileInfo) bool { return info.Mode().Perm()&0077 == 0 }
func ChmodFile(f *os.File, mode os.FileMode) error   { return f.Chmod(mode) }
func Chmod(path string, mode os.FileMode) error      { return os.Chmod(path, mode) }
