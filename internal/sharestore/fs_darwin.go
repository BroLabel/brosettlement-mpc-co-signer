//go:build darwin

package sharestore

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

const artifactFilePerm os.FileMode = 0o600

func publishPlatformSupported() bool { return true }

func validateStoreDirectoryOwner(info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return errors.New("artifact directory must be owned by the running process user")
	}
	return nil
}

func probePlatformPublishCapability(directory string) error {
	return probePublishCapability(darwinPublishOperations{}, readArtifactFile, directory, rand.Reader)
}

func publishArtifactFile(finalPath string, finalBytes []byte) error {
	return publishDurably(darwinPublishOperations{}, finalPath, finalBytes)
}

type darwinPublishOperations struct{}

func (darwinPublishOperations) CreateTemp(directory, pattern string) (publishTempFile, error) {
	file, err := os.CreateTemp(directory, pattern)
	if err != nil {
		return nil, err
	}
	return darwinPublishFile{File: file}, nil
}

func (darwinPublishOperations) Remove(path string) error { return os.Remove(path) }

func (darwinPublishOperations) RenameNoReplace(oldPath, newPath string) error {
	if err := unix.RenamexNp(oldPath, newPath, unix.RENAME_EXCL); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return ErrArtifactExists
		}
		return fmt.Errorf("publish immutable artifact: %w", err)
	}
	return nil
}

func (darwinPublishOperations) SyncDirectory(directory string) error {
	fd, err := unix.Open(directory, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("open artifact directory for sync: %w", err)
	}
	if err := unix.Fsync(fd); err != nil {
		_ = unix.Close(fd)
		return fmt.Errorf("sync artifact directory: %w", err)
	}
	if err := unix.Close(fd); err != nil {
		return fmt.Errorf("close artifact directory: %w", err)
	}
	return nil
}

type darwinPublishFile struct {
	*os.File
}

func (f darwinPublishFile) Sync() error {
	if err := f.File.Sync(); err != nil {
		return err
	}
	if _, err := unix.FcntlInt(f.Fd(), unix.F_FULLFSYNC, 0); err != nil {
		return fmt.Errorf("full sync artifact file: %w", err)
	}
	return nil
}

func readArtifactFile(path string) ([]byte, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), filepath.Base(path))
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("open immutable artifact")
	}
	defer file.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return nil, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil, errors.New("immutable artifact is not a regular file")
	}
	if os.FileMode(stat.Mode).Perm() != artifactFilePerm {
		return nil, errors.New("immutable artifact permissions must be 0600")
	}
	if stat.Size <= 0 || stat.Size > maxArtifactEnvelopeBytes {
		return nil, errors.New("immutable artifact size is invalid")
	}
	bytes, err := io.ReadAll(io.LimitReader(file, maxArtifactEnvelopeBytes+1))
	if err != nil {
		return nil, err
	}
	if len(bytes) == 0 || len(bytes) > maxArtifactEnvelopeBytes {
		clear(bytes)
		return nil, errors.New("immutable artifact size is invalid")
	}
	return bytes, nil
}
