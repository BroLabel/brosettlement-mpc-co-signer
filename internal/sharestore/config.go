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
	deploymentID string
	purpose      StorePurpose
	partyID      string
	directory    string
	keyProvider  *KeyProvider
}

func NewStoreConfig(deploymentID string, purpose StorePurpose, partyID, directory string, keyProvider *KeyProvider) (StoreConfig, error) {
	if err := validateDeploymentID(deploymentID); err != nil {
		return StoreConfig{}, err
	}
	if keyProvider == nil || keyProvider.KeyRef() == "" {
		return StoreConfig{}, errors.New("share encryption key provider is required")
	}
	if err := validatePurposeParty(purpose, partyID); err != nil {
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
		deploymentID: deploymentID,
		purpose:      purpose,
		partyID:      partyID,
		directory:    directory,
		keyProvider:  keyProvider,
	}, nil
}

func ValidateStorePair(primary, recovery StoreConfig) error {
	if primary.purpose != StorePurposePrimary || recovery.purpose != StorePurposeRecovery {
		return errors.New("store pair must contain primary and recovery profiles")
	}
	if primary.deploymentID != recovery.deploymentID {
		return errors.New("primary and recovery stores must use the same deployment ID")
	}
	if primary.keyProvider == nil || recovery.keyProvider == nil || primary.keyProvider != recovery.keyProvider {
		return errors.New("primary and recovery stores must share one key provider")
	}
	if primary.keyProvider.KeyRef() != recovery.keyProvider.KeyRef() {
		return errors.New("primary and recovery stores must share one key reference")
	}
	if pathsOverlap(primary.directory, recovery.directory) {
		return errors.New("primary and recovery store directories must be distinct and non-overlapping")
	}
	if primary.FinalPath("key") == recovery.FinalPath("key") {
		return errors.New("primary and recovery store final paths must be distinct")
	}
	return nil
}

func (c StoreConfig) DeploymentID() string { return c.deploymentID }

func (c StoreConfig) Purpose() StorePurpose { return c.purpose }

func (c StoreConfig) PartyID() string { return c.partyID }

func (c StoreConfig) Directory() string { return c.directory }

func (c StoreConfig) KeyRef() string {
	if c.keyProvider == nil {
		return ""
	}
	return c.keyProvider.KeyRef()
}

func (c StoreConfig) FinalPath(keyID string) string {
	return filepath.Join(c.directory, keyID+"."+string(c.purpose)+".json")
}

func validatePurposeParty(purpose StorePurpose, partyID string) error {
	switch purpose {
	case StorePurposePrimary:
		if partyID != primaryPartyID {
			return fmt.Errorf("primary store party ID must be %q", primaryPartyID)
		}
	case StorePurposeRecovery:
		if partyID != recoveryPartyID {
			return fmt.Errorf("recovery store party ID must be %q", recoveryPartyID)
		}
	default:
		return fmt.Errorf("unsupported store purpose %q", purpose)
	}
	return nil
}

func validateDeploymentID(value string) error {
	if value == "" || len(value) > maxBindingIdentifierBytes || !isAlphaNumeric(value[0]) {
		return errors.New("co-signer deployment ID must match the bounded backend identifier contract")
	}
	for i := 1; i < len(value); i++ {
		if !isAlphaNumeric(value[i]) && value[i] != '.' && value[i] != '_' && value[i] != ':' && value[i] != '-' {
			return errors.New("co-signer deployment ID must match the bounded backend identifier contract")
		}
	}
	return nil
}

func isAlphaNumeric(value byte) bool {
	return value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z' || value >= '0' && value <= '9'
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
