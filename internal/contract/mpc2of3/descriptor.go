package mpc2of3

import (
	"fmt"
	"regexp"
)

const maxCanonicalDescriptorBytesV1 = 2048

var keyIDPattern = regexp.MustCompile(`^mpc_key_[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type PartyV1 struct {
	PartyID string `json:"partyId"`
	Purpose string `json:"purpose"`
}

type KeyDescriptorV1 struct {
	Algorithm         string    `json:"algorithm"`
	ChainCodeHash     string    `json:"chainCodeHash"`
	Curve             string    `json:"curve"`
	DerivationScheme  string    `json:"derivationScheme"`
	DescriptorKind    string    `json:"descriptorKind"`
	DescriptorVersion int       `json:"descriptorVersion"`
	KeyID             string    `json:"keyId"`
	Parties           []PartyV1 `json:"parties"`
	ProtocolVersion   int       `json:"protocolVersion"`
	PublicKeyFormat   string    `json:"publicKeyFormat"`
	Threshold         int       `json:"threshold"`
}

func ParseCanonicalDescriptor(raw []byte) (KeyDescriptorV1, DescriptorFingerprint, error) {
	var descriptor KeyDescriptorV1
	if len(raw) > maxCanonicalDescriptorBytesV1 || !printableASCII(raw) {
		return descriptor, DescriptorFingerprint{}, fmt.Errorf("descriptor requires nonempty bounded ASCII bytes")
	}
	if err := requireCanonicalJCS(raw); err != nil {
		return descriptor, DescriptorFingerprint{}, err
	}
	if err := decodeClosed(raw, &descriptor); err != nil {
		return descriptor, DescriptorFingerprint{}, fmt.Errorf("decode descriptor: %w", err)
	}
	if err := descriptor.validate(); err != nil {
		return descriptor, DescriptorFingerprint{}, err
	}
	return descriptor, DescriptorFingerprintFor(raw), nil
}

func (d KeyDescriptorV1) validate() error {
	if d.DescriptorKind != "mpc-key-descriptor" || d.DescriptorVersion != 1 || d.ProtocolVersion != 1 || d.Algorithm != "ECDSA" || d.Curve != "secp256k1" || d.Threshold != 2 || d.DerivationScheme != "bip32_secp256k1" || d.PublicKeyFormat != "compressed_sec1" {
		return fmt.Errorf("unsupported descriptor contract")
	}
	if !keyIDPattern.MatchString(d.KeyID) {
		return fmt.Errorf("invalid keyId")
	}
	if _, err := ParseChainCodeHash(d.ChainCodeHash); err != nil {
		return fmt.Errorf("invalid chainCodeHash: %w", err)
	}
	want := [][2]string{{"mpc-signer", "platform"}, {"co-signer-primary", "primary"}, {"co-signer-recovery", "recovery"}}
	if len(d.Parties) != len(want) {
		return fmt.Errorf("descriptor must contain exactly three parties")
	}
	for i, party := range d.Parties {
		if !ascii(party.PartyID) || party.PartyID != want[i][0] || party.Purpose != want[i][1] {
			return fmt.Errorf("invalid party at index %d", i)
		}
	}
	return nil
}
