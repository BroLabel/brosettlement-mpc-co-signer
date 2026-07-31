//go:build !linux

package sharestore

import (
	"errors"
	"io"
	"os"
)

const artifactFilePerm os.FileMode = 0o600

func publishPlatformSupported() bool { return false }

func validateStoreDirectoryOwner(os.FileInfo) error { return nil }

func probePlatformPublishCapability(string) error { return ErrUnsupportedPublishPlatform }

func publishArtifactFile(string, []byte) error { return ErrUnsupportedPublishPlatform }

func readArtifactFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("immutable artifact is not a regular no-follow file")
	}
	if info.Mode().Perm() != artifactFilePerm || info.Size() <= 0 || info.Size() > maxArtifactEnvelopeBytes {
		return nil, errors.New("immutable artifact metadata is invalid")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(info, openedInfo) || !openedInfo.Mode().IsRegular() {
		return nil, errors.New("immutable artifact changed during no-follow open")
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
