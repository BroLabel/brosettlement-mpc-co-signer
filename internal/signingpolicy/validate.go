// Package signingpolicy validates supported transaction-signing protocols.
// Protocol implementations own transaction rules; the workflow owns persistence and MPC.
package signingpolicy

import (
	"errors"
	"strings"
)

// AddressValidationError provides a safe diagnostic without retaining the address.
type AddressValidationError struct {
	Field  string
	Reason string
}

func (e *AddressValidationError) Error() string { return e.Field + ": " + e.Reason }

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
	p, ok := protocolForPayload(tuple.PayloadType)
	if ok && tuple.HashAlgorithm == p.hashAlgorithm && p.validTuple(tuple) {
		return nil
	}
	return errors.New("unsupported signing tuple")
}

// ValidateContext binds policy details to the authenticated signing identity.
func ValidateContext(policy *Context, chain, payloadType, expectedAddress string) error {
	if policy == nil || policy.Asset == "" || policy.AmountAtomic == "" || policy.FromAddress == "" || policy.ToAddress == "" || policy.Chain == "" {
		return errors.New("SIGN claim policy context is incomplete")
	}
	p, ok := protocolForPayload(payloadType)
	if !ok {
		return errors.New("SIGN claim signing payload type is unsupported")
	}
	if err := p.validateContext(policy); err != nil {
		return err
	}
	if policy.Chain != chain || policy.FromAddress != expectedAddress {
		return errors.New("SIGN claim policy context mismatch")
	}
	return nil
}
