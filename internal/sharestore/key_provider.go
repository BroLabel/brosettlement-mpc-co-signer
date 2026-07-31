package sharestore

import (
	"encoding/base64"
	"errors"
	"fmt"
)

const encryptionKeyBytes = 32

// KeyProvider owns the one v1 AES-256 key shared by both immutable stores.
// It deliberately exposes only its stable reference, never key material.
type KeyProvider struct {
	key    [encryptionKeyBytes]byte
	keyRef string
}

func NewKeyProvider(encodedKey, keyRef string) (*KeyProvider, error) {
	if err := validateBindingIdentifier("share encryption key reference", keyRef); err != nil {
		return nil, err
	}

	decoded, err := base64.StdEncoding.Strict().DecodeString(encodedKey)
	if err != nil {
		return nil, errors.New("share encryption key must be canonical standard base64")
	}
	if base64.StdEncoding.EncodeToString(decoded) != encodedKey {
		clear(decoded)
		return nil, errors.New("share encryption key must be canonical standard base64")
	}
	if len(decoded) != encryptionKeyBytes {
		clear(decoded)
		return nil, fmt.Errorf("share encryption key must decode to %d bytes", encryptionKeyBytes)
	}

	provider := &KeyProvider{keyRef: keyRef}
	copy(provider.key[:], decoded)
	clear(decoded)
	return provider, nil
}

func (p *KeyProvider) KeyRef() string {
	if p == nil {
		return ""
	}
	return p.keyRef
}

func (p *KeyProvider) String() string {
	if p == nil {
		return "KeyProvider<nil>"
	}
	return fmt.Sprintf("KeyProvider{keyRef:%q}", p.keyRef)
}

func clear(bytes []byte) {
	for i := range bytes {
		bytes[i] = 0
	}
}
