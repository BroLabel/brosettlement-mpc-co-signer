package mpc2of3

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

type sha256DigestV1 [sha256.Size]byte

func (d sha256DigestV1) String() string { return base64.RawURLEncoding.EncodeToString(d[:]) }

func parseSHA256Digest(raw string) (sha256DigestV1, error) {
	var digest sha256DigestV1
	if len(raw) != 43 {
		return digest, fmt.Errorf("SHA-256 digest must be exactly 43 characters")
	}
	for _, c := range raw {
		if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
			return digest, fmt.Errorf("SHA-256 digest contains a non-base64url character")
		}
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(decoded) != sha256.Size || base64.RawURLEncoding.EncodeToString(decoded) != raw {
		return digest, fmt.Errorf("SHA-256 digest is not canonical base64url")
	}
	copy(digest[:], decoded)
	return digest, nil
}

func digestFor(raw []byte) sha256DigestV1 { return sha256.Sum256(raw) }

type DescriptorFingerprint sha256DigestV1
type ArtifactFingerprint sha256DigestV1
type TerminalResultFingerprint sha256DigestV1
type ChainCodeHash sha256DigestV1

func (d DescriptorFingerprint) String() string     { return sha256DigestV1(d).String() }
func (d ArtifactFingerprint) String() string       { return sha256DigestV1(d).String() }
func (d TerminalResultFingerprint) String() string { return sha256DigestV1(d).String() }
func (d ChainCodeHash) String() string             { return sha256DigestV1(d).String() }

func ParseDescriptorFingerprint(raw string) (DescriptorFingerprint, error) {
	digest, err := parseSHA256Digest(raw)
	return DescriptorFingerprint(digest), err
}
func ParseArtifactFingerprint(raw string) (ArtifactFingerprint, error) {
	digest, err := parseSHA256Digest(raw)
	return ArtifactFingerprint(digest), err
}
func ParseTerminalResultFingerprint(raw string) (TerminalResultFingerprint, error) {
	digest, err := parseSHA256Digest(raw)
	return TerminalResultFingerprint(digest), err
}
func ParseChainCodeHash(raw string) (ChainCodeHash, error) {
	digest, err := parseSHA256Digest(raw)
	return ChainCodeHash(digest), err
}

func DescriptorFingerprintFor(raw []byte) DescriptorFingerprint {
	return DescriptorFingerprint(digestFor(raw))
}
func ArtifactFingerprintFor(raw []byte) ArtifactFingerprint {
	return ArtifactFingerprint(digestFor(raw))
}
func TerminalResultFingerprintFor(raw []byte) TerminalResultFingerprint {
	return TerminalResultFingerprint(digestFor(raw))
}
func ChainCodeHashFor(raw []byte) ChainCodeHash { return ChainCodeHash(digestFor(raw)) }
