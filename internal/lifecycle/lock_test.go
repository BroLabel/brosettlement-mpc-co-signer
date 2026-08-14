package lifecycle

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestAcquireLifetimeLockFailsFastUntilOwnerCloses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "co-signer.lock")
	owner, err := AcquireLifetimeLock(path)
	if err != nil {
		t.Fatalf("AcquireLifetimeLock(owner) error = %v", err)
	}
	defer owner.Close()

	if _, err := AcquireLifetimeLock(path); !errors.Is(err, ErrLockHeld) {
		t.Fatalf("AcquireLifetimeLock(contender) error = %v, want ErrLockHeld", err)
	}
	if err := owner.Close(); err != nil {
		t.Fatalf("owner.Close() error = %v", err)
	}
	replacement, err := AcquireLifetimeLock(path)
	if err != nil {
		t.Fatalf("AcquireLifetimeLock(after close) error = %v", err)
	}
	if err := replacement.Close(); err != nil {
		t.Fatalf("replacement.Close() error = %v", err)
	}
}

func TestLifetimeLockDescriptorIsCloseOnExec(t *testing.T) {
	lock, err := AcquireLifetimeLock(filepath.Join(t.TempDir(), "co-signer.lock"))
	if err != nil {
		t.Fatalf("AcquireLifetimeLock() error = %v", err)
	}
	defer lock.Close()

	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, lock.fd.Fd(), syscall.F_GETFD, 0)
	if errno != 0 {
		t.Fatalf("fcntl(F_GETFD) error = %v", errno)
	}
	if flags&syscall.FD_CLOEXEC == 0 {
		t.Fatal("lifetime lock descriptor is inherited across exec")
	}
}

func TestAcquireLifetimeLockRejectsMissingParentWithoutCreatingBackendState(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "missing", "co-signer.lock")

	if _, err := AcquireLifetimeLock(path); err == nil {
		t.Fatal("AcquireLifetimeLock() error = nil")
	}
	if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock acquisition created parent state: %v", err)
	}
}
