package approval

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const journalIdentityName = ".approval-journal.json"

type journalIdentity struct {
	Version   int    `json:"version"`
	ID        string `json:"id"`
	Directory string `json:"directory"`
}

// OpenManagedJournal binds one journal to the primary share store. The caller
// must hold that store's lifetime lock. An explicit path must already exist;
// only the derived sibling directory may be created automatically.
func OpenManagedJournal(directory, primary string, autoInitialize bool) (*Journal, error) {
	return openManagedJournal(directory, primary, autoInitialize, realJournalOperations{})
}

func openManagedJournal(directory, primary string, autoInitialize bool, ops journalOperations) (_ *Journal, returnErr error) {
	if !filepath.IsAbs(primary) || !filepath.IsAbs(directory) {
		return nil, ErrIntegrity
	}
	directory = filepath.Clean(directory)
	primary, err := filepath.EvalSymlinks(primary)
	if err != nil {
		return nil, err
	}
	primaryInfo, err := os.Stat(primary)
	if err != nil {
		return nil, err
	}
	if err = validateJournalDirectory(primaryInfo); err != nil {
		return nil, err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(directory))
	if err != nil {
		return nil, err
	}
	directory = filepath.Join(parent, filepath.Base(directory))
	if err = ValidateDirectories(directory, primary); err != nil {
		return nil, err
	}
	info, err := os.Lstat(directory)
	missing := errors.Is(err, os.ErrNotExist)
	if err != nil && !missing {
		return nil, err
	}
	if !missing {
		directory, err = filepath.EvalSymlinks(directory)
		if err != nil {
			return nil, err
		}
		parent = filepath.Dir(directory)
		info, err = os.Stat(directory)
		if err != nil {
			return nil, err
		}
		if err = validateJournalDirectory(info); err != nil {
			return nil, err
		}
	}
	if autoInitialize {
		parentInfo, err := os.Stat(parent)
		if err != nil {
			return nil, err
		}
		if err = validateJournalDirectory(parentInfo); err != nil {
			return nil, err
		}
		if !sameFilesystem(primaryInfo, parentInfo) || (!missing && !sameFilesystem(primaryInfo, info)) {
			return nil, fmt.Errorf("%w: automatic journal must share the primary store filesystem; mount their common parent on durable storage", ErrIntegrity)
		}
	}
	markerPath := filepath.Join(primary, journalIdentityName)
	identityPath := filepath.Join(directory, journalIdentityName)
	marker, err := readJournalFile(markerPath)
	if err == nil {
		var identity journalIdentity
		if err = decodeRecord(marker, &identity, "version,id,directory"); err != nil {
			return nil, err
		}
		id, decodeErr := hex.DecodeString(identity.ID)
		if identity.Version != 1 || decodeErr != nil || len(id) != 32 || identity.Directory != directory {
			return nil, ErrIntegrity
		}
		stored, err := readJournalFile(identityPath)
		if err != nil || !bytes.Equal(stored, marker) {
			return nil, fmt.Errorf("%w: initialized journal is missing or replaced; restore its exact evidence", errors.Join(ErrIntegrity, err))
		}
		if err = ops.SyncDirectory(primary); err != nil {
			return nil, err
		}
		return OpenJournal(directory)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	// A surviving journal identity with a missing outer marker is evidence loss,
	// not a fresh installation. Never generate another identity in that case.
	if _, err = os.Lstat(identityPath); err == nil {
		return nil, ErrIntegrity
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if missing {
		if !autoInitialize {
			return nil, os.ErrNotExist
		}
		if err = os.Mkdir(directory, 0700); err != nil {
			return nil, err
		}
	}
	if err = ops.SyncDirectory(parent); err != nil {
		return nil, err
	}
	journal, err := OpenJournal(directory)
	if err != nil {
		return nil, err
	}
	defer func() {
		if returnErr != nil {
			journal.Close()
		}
	}()
	var id [32]byte
	if _, err = rand.Read(id[:]); err != nil {
		return nil, err
	}
	marker, err = json.Marshal(journalIdentity{Version: 1, ID: hex.EncodeToString(id[:]), Directory: directory})
	if err != nil {
		return nil, err
	}
	// Publish the outside marker first. An interrupted two-file publication fails
	// closed on restart; no dispatcher or signing worker has started at this point.
	if err = publishJournalFile(ops, markerPath, marker); err != nil {
		return nil, err
	}
	if err = publishJournalFile(ops, identityPath, marker); err != nil {
		return nil, err
	}
	return journal, nil
}
