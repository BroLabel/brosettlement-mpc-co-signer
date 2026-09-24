// Package signingpolicy validates supported transaction-signing protocols.
// It has no transport, storage, RPC, or MPC execution dependencies.
package signingpolicy

import (
	"errors"
	"strings"
)

// Tuple identifies the protocol and cryptographic format of a signing request.
type Tuple struct {
	Algorithm       string
	Curve           string
	Chain           string
	DigestType      string
	HashAlgorithm   string
	PayloadType     string
	AddressEncoding string
}

func ValidateTuple(tuple Tuple) error {
	tuple.Algorithm = strings.ToLower(strings.TrimSpace(tuple.Algorithm))
	tuple.Curve = strings.ToLower(strings.TrimSpace(tuple.Curve))
	tuple.Chain = strings.TrimSpace(tuple.Chain)
	tuple.DigestType = strings.TrimSpace(tuple.DigestType)
	tuple.HashAlgorithm = strings.TrimSpace(tuple.HashAlgorithm)
	tuple.PayloadType = strings.TrimSpace(tuple.PayloadType)
	tuple.AddressEncoding = strings.TrimSpace(tuple.AddressEncoding)
	if tuple.Algorithm != "ecdsa" || tuple.Curve != "secp256k1" || tuple.DigestType != "transaction" {
		return errors.New("unsupported signing tuple")
	}
	switch tuple.PayloadType {
	case tronPayloadType:
		if validTronTuple(tuple) {
			return nil
		}
	case ethereumPayloadType:
		if validEthereumTuple(tuple) {
			return nil
		}
	}
	return errors.New("unsupported signing tuple")
}

// ValidateContext binds policy details to the authenticated signing identity.
func ValidateContext(policy *Context, chain, payloadType, expectedAddress string) error {
	if policy == nil || policy.Asset == "" || policy.AmountAtomic == "" || policy.FromAddress == "" || policy.ToAddress == "" || policy.Chain == "" {
		return errors.New("SIGN claim policy context is incomplete")
	}
	if policy.Chain != chain || policy.FromAddress != expectedAddress {
		return errors.New("SIGN claim policy context mismatch")
	}
	switch payloadType {
	case tronPayloadType:
		return validateTronContext(policy)
	case ethereumPayloadType:
		return validateEthereumContext(policy)
	default:
		return errors.New("SIGN claim signing payload type is unsupported")
	}
}
