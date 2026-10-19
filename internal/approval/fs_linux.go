//go:build linux

package approval

import (
	"os"

	"golang.org/x/sys/unix"
)

func (realJournalOperations) Publish(a, b string) error {
	return unix.Renameat2(unix.AT_FDCWD, a, unix.AT_FDCWD, b, unix.RENAME_NOREPLACE)
}
func syncJournalFile(f *os.File) error { return f.Sync() }
