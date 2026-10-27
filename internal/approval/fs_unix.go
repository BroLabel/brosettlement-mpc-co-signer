//go:build linux || darwin

package approval

import (
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func validateJournalDirectory(i os.FileInfo) error {
	s, ok := i.Sys().(*syscall.Stat_t)
	if !i.IsDir() || !ok || s.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("%w: approval directory must be owned by the running process user", ErrIntegrity)
	}
	// Other users may list the directory, but cannot replace its journal files.
	// File contents remain private through the separate 0600 checks below.
	permissions := i.Mode().Perm()
	if permissions&0700 != 0700 || permissions&0022 != 0 {
		return fmt.Errorf("%w: approval directory requires owner read/write/search access and no group or other write access (mode %04o)", ErrIntegrity, permissions)
	}
	return nil
}

func sameFilesystem(a, b os.FileInfo) bool {
	x, xOK := a.Sys().(*syscall.Stat_t)
	y, yOK := b.Sys().(*syscall.Stat_t)
	return xOK && yOK && x.Dev == y.Dev
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
