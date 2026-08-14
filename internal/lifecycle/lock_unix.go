package lifecycle

import (
	"errors"
	"fmt"
	"os"
	"sync"

	"golang.org/x/sys/unix"
)

var ErrLockHeld = errors.New("co-signer lifetime lock is already held")

type LifetimeLock struct {
	fd   *os.File
	once sync.Once
	err  error
}

func AcquireLifetimeLock(path string) (*LifetimeLock, error) {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lifetime lock: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, ErrLockHeld
		}
		return nil, fmt.Errorf("acquire lifetime lock: %w", err)
	}
	return &LifetimeLock{fd: file}, nil
}

func (l *LifetimeLock) Close() error {
	if l == nil {
		return nil
	}
	l.once.Do(func() {
		l.err = l.fd.Close()
	})
	return l.err
}
