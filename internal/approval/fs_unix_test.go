//go:build linux || darwin

package approval

import (
	"errors"
	"os"
	"syscall"
	"testing"
)

type directoryInfoWithOwner struct {
	os.FileInfo
	stat *syscall.Stat_t
}

func (i directoryInfoWithOwner) Sys() any { return i.stat }

func TestJournalDirectoryStillRequiresProcessOwnership(t *testing.T) {
	dir := privateDir(t)
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateJournalDirectory(info); err != nil {
		t.Fatalf("process-owned directory rejected: %v", err)
	}
	stat := *info.Sys().(*syscall.Stat_t)
	stat.Uid = uint32(os.Geteuid()) + 1
	if err := validateJournalDirectory(directoryInfoWithOwner{info, &stat}); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("foreign-owned directory accepted: %v", err)
	}
}
