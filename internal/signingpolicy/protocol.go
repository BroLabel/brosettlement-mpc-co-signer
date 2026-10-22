package signingpolicy

import (
	"encoding/json"
	"time"

	wire "github.com/BroLabel/brosettlement-mpc-co-signer/internal/contract/approval"
)

// protocol is the fixed routing table for the two supported wire protocols.
// Rules and transaction decoding live in the corresponding protocol files.
type protocol struct {
	hashAlgorithm   string
	supportsChain   func(string) bool
	validTuple      func(Tuple) bool
	validateContext func(*Context) error
	verifyPrepared  func([]byte, *PreparedTransfer, *Context, wire.Object, time.Time) (preparedVerification, error)
}

type preparedVerification struct {
	digest  []byte
	budget  string
	network *networkRequirement
}

func protocolForPayload(payloadType string) (protocol, bool) {
	switch payloadType {
	case ethereumPayloadType:
		return protocol{
			hashAlgorithm: ethereumHashAlgorithm,
			supportsChain: isEthereumChain, validTuple: validEthereumTuple,
			validateContext: validateEthereumContext, verifyPrepared: verifyEthereumPrepared,
		}, true
	case tronPayloadType:
		return protocol{
			hashAlgorithm: tronHashAlgorithm,
			supportsChain: isTronChain, validTuple: validTronTuple,
			validateContext: validateTronContext, verifyPrepared: verifyTronPrepared,
		}, true
	default:
		return protocol{}, false
	}
}

// Preserve the existing wire discriminator. Network support is checked during
// policy validation, after the caller can authenticate and report a rejection.
func policyFieldsForContext(fields map[string]json.RawMessage) []string {
	if _, ok := fields["version"]; ok {
		return ethereumPolicyFields
	}
	return tronPolicyFields
}

// ValidPayloadHash also guards execution bindings read back from the journal.
func ValidPayloadHash(payloadType, hashAlgorithm string) bool {
	p, ok := protocolForPayload(payloadType)
	return ok && p.hashAlgorithm == hashAlgorithm
}
