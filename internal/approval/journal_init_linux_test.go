//go:build linux

package approval

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestAutomaticJournalCannotEscapeMountedShareFilesystem(t *testing.T) {
	primary, err := os.MkdirTemp("/dev/shm", "approval-primary-*")
	if err != nil {
		t.Skipf("separate test filesystem unavailable: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(primary) })
	root := privateDir(t)
	primaryInfo, err := os.Stat(primary)
	if err != nil {
		t.Fatal(err)
	}
	rootInfo, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if primaryInfo.Sys().(*syscall.Stat_t).Dev == rootInfo.Sys().(*syscall.Stat_t).Dev {
		t.Skip("test requires two different filesystems")
	}
	directory := filepath.Join(root, "approval")
	if journal, err := OpenManagedJournal(directory, primary, true); !errors.Is(err, ErrIntegrity) {
		if journal != nil {
			journal.Close()
		}
		t.Fatalf("journal outside primary filesystem accepted: %v", err)
	}
	if _, err := os.Stat(directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("created directory outside persistent share filesystem: %v", err)
	}
}
