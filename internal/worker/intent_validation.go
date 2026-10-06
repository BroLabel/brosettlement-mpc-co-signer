package worker

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/monolith"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/signingpolicy"
	coretss "github.com/BroLabel/brosettlement-mpc-core/tss"
)

func validateClaimIdentity(intent, claimedIntent monolith.Intent, claim monolith.ClaimResult, kind intentKind) error {
	if err := claim.Session.Validate(strings.ToUpper(claimedIntent.Type), claimedIntent.SessionID, claim.Deadline); err != nil {
		return monolith.ErrInvalidLifecycle
	}
	if claim.Deadline.IsZero() || !intent.ExpiresAt.IsZero() && !intent.ExpiresAt.Equal(claim.Deadline) {
		return monolith.ErrInvalidLifecycle
	}
	if intent.SessionID != "" && intent.SessionID != claimedIntent.SessionID {
		return monolith.ErrInvalidLifecycle
	}
	if kind != intentKindSIGN {
		return nil
	}
	if intent.IntentID != claimedIntent.IntentID {
		return monolith.ErrInvalidLifecycle
	}
	if intent.Payload.OrgID != "" && intent.Payload.OrgID != claimedIntent.Payload.OrgID {
		return monolith.ErrInvalidLifecycle
	}
	if intent.Payload.KeyID != "" && intent.Payload.KeyID != claimedIntent.Payload.KeyID {
		return monolith.ErrInvalidLifecycle
	}
	return nil
}

func validateIntent(intent monolith.Intent, localPartyID string) error {
	if strings.TrimSpace(intent.SessionID) == "" {
		return fmt.Errorf("%w: session id is required", errInvalidIntent)
	}

	intentType := strings.ToUpper(strings.TrimSpace(intent.Type))
	if intentType != "DKG" && intentType != "SIGN" {
		return fmt.Errorf("%w: unsupported intent type %q", errInvalidIntent, intent.Type)
	}
	if err := validatePayloadType(intentType, intent.Payload.Type); err != nil {
		return err
	}

	uniqueParties := dedupeParties(intent.Payload.Parties)
	if len(uniqueParties) < 2 {
		return fmt.Errorf("%w: at least 2 unique parties are required", errInvalidIntent)
	}

	if intent.Payload.Threshold < 2 || int(intent.Payload.Threshold) > len(uniqueParties) {
		return fmt.Errorf("%w: invalid threshold %d for %d parties", errInvalidIntent, intent.Payload.Threshold, len(uniqueParties))
	}

	if !slices.Contains(uniqueParties, localPartyID) {
		return fmt.Errorf("%w: local party %q is not part of intent", errInvalidIntent, localPartyID)
	}

	if !strings.EqualFold(strings.TrimSpace(intent.Payload.Algorithm), "ECDSA") {
		return fmt.Errorf("%w: unsupported algorithm %q", errInvalidIntent, intent.Payload.Algorithm)
	}

	curve := strings.TrimSpace(intent.Payload.Curve)
	if curve == "" {
		return fmt.Errorf("%w: curve is required", errInvalidIntent)
	}
	if !strings.EqualFold(curve, "secp256k1") {
		return fmt.Errorf("%w: unsupported ecdsa curve %q", errInvalidIntent, intent.Payload.Curve)
	}

	if err := validateCommonPayload(intent.Payload); err != nil {
		return err
	}

	switch intentType {
	case "DKG":
		return validateDKGPayload(intent.Payload)
	case "SIGN":
		if err := validateProductionSignRoster(intent.Payload, localPartyID); err != nil {
			return err
		}
		return validateSignPayload(intent.Payload)
	default:
		return nil
	}
}

func validateProductionSignRoster(payload monolith.IntentPayload, localPartyID string) error {
	if localPartyID != primaryPartyID || payload.PartyID != primaryPartyID {
		return fmt.Errorf("%w: SIGN local party must be %q", errInvalidIntent, primaryPartyID)
	}
	if payload.Threshold != 2 || len(payload.Parties) != 2 ||
		payload.Parties[0] != platformPartyID || payload.Parties[1] != primaryPartyID {
		return fmt.Errorf("%w: SIGN parties must be exactly [%q, %q] with threshold 2", errInvalidIntent, platformPartyID, primaryPartyID)
	}
	return nil
}

func validateCommonPayload(payload monolith.IntentPayload) error {
	if strings.TrimSpace(payload.OrgID) == "" {
		return fmt.Errorf("%w: org id is required", errInvalidIntent)
	}
	if strings.TrimSpace(payload.KeyID) == "" {
		return fmt.Errorf("%w: key id is required", errInvalidIntent)
	}
	return nil
}

func validatePayloadType(intentType, payloadType string) error {
	payloadType = strings.TrimSpace(payloadType)
	if payloadType == "" {
		return nil
	}
	if strings.ToUpper(payloadType) != intentType {
		return fmt.Errorf("%w: payload type %q conflicts with intent type %q", errInvalidIntent, payloadType, intentType)
	}
	return nil
}

func validateDKGPayload(payload monolith.IntentPayload) error {
	if strings.TrimSpace(payload.Chain) != "" {
		return fmt.Errorf("%w: chain is not allowed for DKG", errInvalidIntent)
	}
	if payload.DerivationContext != nil {
		return fmt.Errorf("%w: derivation context is not allowed for DKG", errInvalidIntent)
	}
	if len(payload.Digest) != 0 {
		return fmt.Errorf("%w: digest is not allowed for DKG", errInvalidIntent)
	}
	if !isLowerHex64(payload.ChainCode) {
		return fmt.Errorf("%w: chain code must be 32-byte lowercase hex", errInvalidIntent)
	}
	if strings.TrimSpace(payload.ChainCodeHash) == "" {
		return fmt.Errorf("%w: chain code hash is required for DKG", errInvalidIntent)
	}
	if err := validateChainCodeHash(payload.ChainCode, payload.ChainCodeHash); err != nil {
		return err
	}
	if strings.TrimSpace(payload.DerivationScheme) == "" {
		return fmt.Errorf("%w: derivation scheme is required for DKG", errInvalidIntent)
	}
	if strings.TrimSpace(payload.DerivationScheme) != coretss.DerivationSchemeBIP32Secp256k1 {
		return fmt.Errorf("%w: unsupported derivation scheme %q", errInvalidIntent, payload.DerivationScheme)
	}
	return nil
}

func validateSignPayload(payload monolith.IntentPayload) error {
	if err := validateSignMetadata(payload); err != nil {
		return err
	}
	if err := validateSignTuple(payload); err != nil {
		return err
	}
	if strings.TrimSpace(payload.ChainCode) != "" {
		return fmt.Errorf("%w: chain code is not allowed for SIGN", errInvalidIntent)
	}
	if strings.TrimSpace(payload.DerivationScheme) != "" {
		return fmt.Errorf("%w: derivation scheme is not allowed for SIGN", errInvalidIntent)
	}
	if payload.DerivationContext == nil {
		return fmt.Errorf("%w: derivation context is required for SIGN", errInvalidIntent)
	}
	if payload.DerivationContext.ProfileVersion == 0 {
		return fmt.Errorf("%w: derivation context profile version is required for SIGN", errInvalidIntent)
	}
	if payload.DerivationContext.DescriptorVersion == 0 {
		return fmt.Errorf("%w: derivation context descriptor version is required for SIGN", errInvalidIntent)
	}
	if payload.DerivationContext.KeyVersion == 0 {
		return fmt.Errorf("%w: derivation context key version is required for SIGN", errInvalidIntent)
	}

	normalized, err := coretss.NormalizeDerivationContext(toCoreDerivationContext(*payload.DerivationContext))
	if err != nil {
		return fmt.Errorf("%w: %v", errInvalidIntent, err)
	}
	if strings.TrimSpace(payload.Chain) != normalized.Chain {
		return fmt.Errorf("%w: chain conflicts with derivation context", errInvalidIntent)
	}
	if strings.ToLower(strings.TrimSpace(payload.Algorithm)) != normalized.Algorithm {
		return fmt.Errorf("%w: algorithm conflicts with derivation context", errInvalidIntent)
	}
	if strings.ToLower(strings.TrimSpace(payload.Curve)) != normalized.Curve {
		return fmt.Errorf("%w: curve conflicts with derivation context", errInvalidIntent)
	}
	if strings.TrimSpace(payload.ProfileID) != normalized.ProfileID {
		return fmt.Errorf("%w: profile id conflicts with derivation context", errInvalidIntent)
	}
	if strings.TrimSpace(payload.ProfileTemplateID) != normalized.ProfileTemplateID {
		return fmt.Errorf("%w: profile template id conflicts with derivation context", errInvalidIntent)
	}
	if payload.ProfileVersion != normalized.ProfileVersion {
		return fmt.Errorf("%w: profile version conflicts with derivation context", errInvalidIntent)
	}
	hash, err := coretss.DerivationContextHashV1(normalized)
	if err != nil {
		return fmt.Errorf("%w: %v", errInvalidIntent, err)
	}
	if strings.TrimSpace(payload.DerivationContextHash) != hash {
		return fmt.Errorf("%w: derivation context hash mismatch", errInvalidIntent)
	}
	return nil
}

func validateSignMetadata(payload monolith.IntentPayload) error {
	required := []struct {
		name  string
		value string
	}{
		{name: "wallet id", value: payload.WalletID},
		{name: "profile id", value: payload.ProfileID},
		{name: "profile template id", value: payload.ProfileTemplateID},
		{name: "digest type", value: payload.DigestType},
		{name: "hash algorithm", value: payload.HashAlgorithm},
		{name: "signing payload type", value: payload.SigningPayloadType},
		{name: "derivation context hash", value: payload.DerivationContextHash},
		{name: "party id", value: payload.PartyID},
		{name: "chain", value: payload.Chain},
	}
	for _, field := range required {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("%w: %s is required for SIGN", errInvalidIntent, field.name)
		}
	}
	if payload.ProfileVersion == 0 {
		return fmt.Errorf("%w: profile version is required for SIGN", errInvalidIntent)
	}
	if len(payload.Digest) != sha256.Size {
		return fmt.Errorf("%w: digest must be 32 bytes for SIGN", errInvalidIntent)
	}
	return nil
}

func validateSignTuple(payload monolith.IntentPayload) error {
	addressEncoding := ""
	if payload.DerivationContext != nil {
		addressEncoding = payload.DerivationContext.AddressEncoding
	}
	if err := signingpolicy.ValidateTuple(signingpolicy.Tuple{
		Algorithm: payload.Algorithm, Curve: payload.Curve, Chain: payload.Chain,
		DigestType: payload.DigestType, HashAlgorithm: payload.HashAlgorithm,
		PayloadType: payload.SigningPayloadType, AddressEncoding: addressEncoding,
	}); err != nil {
		return fmt.Errorf("%w: %v", errInvalidIntent, err)
	}
	return nil
}

func validateChainCodeHash(chainCodeHex, expectedHash string) error {
	chainCode, err := hex.DecodeString(chainCodeHex)
	if err != nil {
		return fmt.Errorf("%w: chain code must be 32-byte lowercase hex", errInvalidIntent)
	}
	sum := sha256.Sum256(chainCode)
	actualHash := base64.RawURLEncoding.EncodeToString(sum[:])
	if strings.TrimSpace(expectedHash) != actualHash {
		return fmt.Errorf("%w: chain code hash mismatch", errInvalidIntent)
	}
	return nil
}

func isLowerHex64(input string) bool {
	if len(input) != 64 {
		return false
	}
	for _, r := range input {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func dedupeParties(parties []string) []string {
	seen := make(map[string]struct{}, len(parties))
	unique := make([]string, 0, len(parties))

	for _, party := range parties {
		partyID := strings.TrimSpace(party)
		if partyID == "" {
			continue
		}
		if _, ok := seen[partyID]; ok {
			continue
		}
		seen[partyID] = struct{}{}
		unique = append(unique, partyID)
	}
	return unique
}
