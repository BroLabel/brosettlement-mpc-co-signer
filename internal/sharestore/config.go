package sharestore

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

type StorePurpose string

const (
	StorePurposePrimary  StorePurpose = "primary"
	StorePurposeRecovery StorePurpose = "recovery"

	primaryPartyID  = "co-signer-primary"
	recoveryPartyID = "co-signer-recovery"

	maxBindingIdentifierBytes = 255
)

// StoreConfig is an immutable, purpose-bound filesystem store profile.
type StoreConfig struct {
	purpose     StorePurpose
	directory   string
	keyProvider *KeyProvider
}

func NewStoreConfig(purpose StorePurpose, directory string, keyProvider *KeyProvider) (StoreConfig, error) {
	if keyProvider == nil || keyProvider.KeyRef() == "" {
		return StoreConfig{}, errors.New("share encryption key provider is required")
	}
	if _, err := partyIDForPurpose(purpose); err != nil {
		return StoreConfig{}, err
	}
	directory = filepath.Clean(strings.TrimSpace(directory))
	if !filepath.IsAbs(directory) {
		return StoreConfig{}, fmt.Errorf("%s store directory must be absolute", purpose)
	}
	if directory == string(filepath.Separator) {
		return StoreConfig{}, fmt.Errorf("%s store directory must not be filesystem root", purpose)
	}

	return StoreConfig{
		purpose:     purpose,
		directory:   directory,
		keyProvider: keyProvider,
	}, nil
}

func ValidateStorePair(primary, recovery StoreConfig) error {
	if primary.purpose != StorePurposePrimary || recovery.purpose != StorePurposeRecovery {
		return errors.New("store pair must contain primary and recovery profiles")
	}
	if primary.keyProvider == nil || recovery.keyProvider == nil || primary.keyProvider != recovery.keyProvider {
		return errors.New("primary and recovery stores must share one key provider")
	}
	if pathsOverlap(primary.directory, recovery.directory) {
		return errors.New("primary and recovery store directories must be distinct and non-overlapping")
	}
	return nil
}

func (c StoreConfig) Purpose() StorePurpose { return c.purpose }

func (c StoreConfig) PartyID() string {
	partyID, _ := partyIDForPurpose(c.purpose)
	return partyID
}

func (c StoreConfig) Directory() string { return c.directory }

func (c StoreConfig) KeyRef() string {
	if c.keyProvider == nil {
		return ""
	}
	return c.keyProvider.KeyRef()
}

func partyIDForPurpose(purpose StorePurpose) (string, error) {
	switch purpose {
	case StorePurposePrimary:
		return primaryPartyID, nil
	case StorePurposeRecovery:
		return recoveryPartyID, nil
	default:
		return "", fmt.Errorf("unsupported store purpose %q", purpose)
	}
}

func validateBindingIdentifier(name, value string) error {
	if value == "" || len(value) > maxBindingIdentifierBytes || strings.TrimSpace(value) != value {
		return fmt.Errorf("%s must be a nonempty bounded printable ASCII identifier", name)
	}
	for i := 0; i < len(value); i++ {
		if value[i] < 0x20 || value[i] > 0x7e {
			return fmt.Errorf("%s must be a nonempty bounded printable ASCII identifier", name)
		}
	}
	return nil
}

func pathsOverlap(first, second string) bool {
	return first == second || isWithin(first, second) || isWithin(second, first)
}

func isWithin(path, parent string) bool {
	relative, err := filepath.Rel(parent, path)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}
