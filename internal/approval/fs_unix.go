//go:build linux || darwin

package approval

import (
	"errors"
	"io"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func privateDirectory(i os.FileInfo) error {
	s, ok := i.Sys().(*syscall.Stat_t)
	if !i.IsDir() || i.Mode().Perm() != 0700 || !ok || s.Uid != uint32(os.Geteuid()) {
		return ErrIntegrity
	}
	return nil
}
func readJournalFile(path string) ([]byte, error) {
	fd, e := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if e != nil {
		return nil, e
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	i, e := f.Stat()
	if e != nil {
		return nil, e
	}
	s, ok := i.Sys().(*syscall.Stat_t)
	if !ok || s.Uid != uint32(os.Geteuid()) || !i.Mode().IsRegular() || i.Mode().Perm() != 0600 || i.Size() < 1 || i.Size() > 32768 {
		return nil, ErrIntegrity
	}
	b, e := io.ReadAll(io.LimitReader(f, 32769))
	if e != nil || len(b) > 32768 {
		return nil, errors.Join(ErrIntegrity, e)
	}
	if e = syncJournalFile(f); e != nil {
		return nil, e
	}
	return b, nil
}

type syncedJournalFile struct{ *os.File }

func (f syncedJournalFile) Sync() error { return syncJournalFile(f.File) }
