//go:build darwin

package approval

import (
	"os"

	"golang.org/x/sys/unix"
)

func (realJournalOperations) Publish(a, b string) error {
	return unix.RenamexNp(a, b, unix.RENAME_EXCL)
}
func syncJournalFile(f *os.File) error {
	if e := f.Sync(); e != nil {
		return e
	}
	_, e := unix.FcntlInt(f.Fd(), unix.F_FULLFSYNC, 0)
	return e
}
