package bundle

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Sync validates both complete source bundles before staging and replacing the
// two consumer-owned fixed destinations. Invalid sources cannot change either
// destination.
func Sync(repositoryRoot, signerSource, httpSource string) error {
	return syncWith(repositoryRoot, signerSource, httpSource, contractVerifier{verifySigner: VerifySignerBundle, verifyHTTP: VerifyHTTPBundle}, osPairFilesystem())
}

type contractVerifier struct {
	verifySigner func(string) (string, error)
	verifyHTTP   func(string) (string, error)
}

type pairFilesystem struct {
	lstat     func(string) (os.FileInfo, error)
	rename    func(string, string) error
	removeAll func(string) error
}

func osPairFilesystem() pairFilesystem {
	return pairFilesystem{lstat: os.Lstat, rename: os.Rename, removeAll: os.RemoveAll}
}

func syncWith(repositoryRoot, signerSource, httpSource string, verifier contractVerifier, filesystem pairFilesystem) error {
	if _, err := verifier.verifySigner(signerSource); err != nil {
		return fmt.Errorf("verify signer source: %w", err)
	}
	if _, err := verifier.verifyHTTP(httpSource); err != nil {
		return fmt.Errorf("verify HTTP source: %w", err)
	}
	staging, err := os.MkdirTemp(repositoryRoot, ".contract-sync-")
	if err != nil {
		return err
	}
	cleanupStaging := true
	defer func() {
		if cleanupStaging {
			_ = os.RemoveAll(staging)
		}
	}()
	stagedSigner := filepath.Join(staging, "signer-v1")
	stagedHTTP := filepath.Join(staging, "http-v1")
	if err := copyTree(signerSource, stagedSigner); err != nil {
		return err
	}
	if err := copyTree(httpSource, stagedHTTP); err != nil {
		return err
	}
	if _, err := verifier.verifySigner(stagedSigner); err != nil {
		return fmt.Errorf("verify staged signer bundle: %w", err)
	}
	if _, err := verifier.verifyHTTP(stagedHTTP); err != nil {
		return fmt.Errorf("verify staged HTTP bundle: %w", err)
	}
	destinationSigner := filepath.Join(repositoryRoot, "contracts", "mpc-2of3", "v1")
	destinationHTTP := filepath.Join(repositoryRoot, "testdata", "mpc-co-signer-http", "v1")
	if err := os.MkdirAll(filepath.Dir(destinationSigner), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destinationHTTP), 0o755); err != nil {
		return err
	}
	replacement, err := replacePairWithFilesystem(destinationSigner, stagedSigner, destinationHTTP, stagedHTTP, staging, filesystem)
	if err != nil {
		if replacement != nil && replacement.rollbackFailed {
			cleanupStaging = false
			return fmt.Errorf("%w; preserved rollback staging: %s", err, staging)
		}
		return err
	}
	if _, err := verifier.verifySigner(destinationSigner); err != nil {
		err = replacement.rollback(fmt.Errorf("verify final signer bundle: %w", err))
		if replacement.rollbackFailed {
			cleanupStaging = false
			return fmt.Errorf("%w; preserved rollback staging: %s", err, staging)
		}
		return err
	}
	if _, err := verifier.verifyHTTP(destinationHTTP); err != nil {
		err = replacement.rollback(fmt.Errorf("verify final HTTP bundle: %w", err))
		if replacement.rollbackFailed {
			cleanupStaging = false
			return fmt.Errorf("%w; preserved rollback staging: %s", err, staging)
		}
		return err
	}
	return nil
}

func copyTree(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("source bundle entry is not a regular file")
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, contents, 0o600)
	})
}

type replacement struct {
	destinationSigner, destinationHTTP, backupSigner, backupHTTP string
	signerBackedUp, httpBackedUp, signerPublished, httpPublished bool
	rollbackFailed                                               bool
	filesystem                                                   pairFilesystem
}

func replacePairWithFilesystem(destinationSigner, stagedSigner, destinationHTTP, stagedHTTP, staging string, filesystem pairFilesystem) (*replacement, error) {
	replacement := &replacement{
		destinationSigner: destinationSigner, destinationHTTP: destinationHTTP,
		backupSigner: filepath.Join(staging, "backup-signer-v1"), backupHTTP: filepath.Join(staging, "backup-http-v1"),
		filesystem: filesystem,
	}
	if _, err := filesystem.lstat(destinationSigner); err == nil {
		if err := filesystem.rename(destinationSigner, replacement.backupSigner); err != nil {
			return nil, err
		}
		replacement.signerBackedUp = true
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if _, err := filesystem.lstat(destinationHTTP); err == nil {
		if err := filesystem.rename(destinationHTTP, replacement.backupHTTP); err != nil {
			return replacement, replacement.rollback(err)
		}
		replacement.httpBackedUp = true
	} else if !os.IsNotExist(err) {
		return replacement, replacement.rollback(err)
	}
	if err := filesystem.rename(stagedSigner, destinationSigner); err != nil {
		return replacement, replacement.rollback(err)
	}
	replacement.signerPublished = true
	if err := filesystem.rename(stagedHTTP, destinationHTTP); err != nil {
		return replacement, replacement.rollback(err)
	}
	replacement.httpPublished = true
	return replacement, nil
}

func (replacement *replacement) rollback(cause error) error {
	var restoreErrors []error
	if replacement.signerPublished {
		if err := replacement.filesystem.removeAll(replacement.destinationSigner); err != nil {
			restoreErrors = append(restoreErrors, fmt.Errorf("remove published signer destination: %w", err))
		}
	}
	if replacement.httpPublished {
		if err := replacement.filesystem.removeAll(replacement.destinationHTTP); err != nil {
			restoreErrors = append(restoreErrors, fmt.Errorf("remove published HTTP destination: %w", err))
		}
	}
	if replacement.signerBackedUp {
		if err := replacement.filesystem.rename(replacement.backupSigner, replacement.destinationSigner); err != nil {
			restoreErrors = append(restoreErrors, fmt.Errorf("restore signer destination: %w", err))
		}
	}
	if replacement.httpBackedUp {
		if err := replacement.filesystem.rename(replacement.backupHTTP, replacement.destinationHTTP); err != nil {
			restoreErrors = append(restoreErrors, fmt.Errorf("restore HTTP destination: %w", err))
		}
	}
	if len(restoreErrors) == 0 {
		return cause
	}
	replacement.rollbackFailed = true
	return errors.Join(append([]error{cause}, restoreErrors...)...)
}
