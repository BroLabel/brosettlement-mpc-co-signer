package monolith

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"strings"
)

func (c *Client) ClaimIntent(ctx context.Context, intentType, intentID string) (ClaimResult, error) {
	pathType, err := intentTypePath(intentType)
	if err != nil {
		return ClaimResult{}, err
	}
	path := "/api/v1/co-signer/intents/" + pathType + "/" + url.PathEscape(intentID) + "/claim"
	var out ClaimResult
	if err := c.doJSONAttempts(ctx, http.MethodPost, path, nil, intentID, &out, http.StatusOK, 1); err != nil {
		switch {
		case statusCode(err) == http.StatusConflict:
			return ClaimResult{}, ErrAlreadyClaimed
		case statusCode(err) == http.StatusNotFound:
			return ClaimResult{}, ErrNotFound
		case isAmbiguous(err), statusCode(err) >= 500, errors.Is(err, context.Canceled):
			return ClaimResult{}, ErrClaimOutcomeUnknown
		default:
			return ClaimResult{}, err
		}
	}
	if out.HTTPStatus != http.StatusOK {
		return ClaimResult{}, fmt.Errorf("%w: claim response body HTTP status mismatch", ErrClaimOutcomeUnknown)
	}
	if err := validateClaimResult(out, intentType); err != nil {
		return ClaimResult{}, fmt.Errorf("%w: %w", ErrClaimOutcomeUnknown, err)
	}
	return out, nil
}

func validateClaimResult(claim ClaimResult, expectedType string) error {
	if claim.SessionID == "" {
		return errors.New("claim session identity is required")
	}
	if claim.Deadline.IsZero() || claim.DeadlineRaw == "" {
		return errors.New("claim deadline is required")
	}
	if err := claim.Session.Validate(expectedType, claim.SessionID, claim.Deadline); err != nil {
		return err
	}
	if claim.Status != "CLAIMED" {
		return errors.New("claim response status is invalid")
	}
	actualType := claim.Intent().Type
	if !strings.EqualFold(actualType, expectedType) {
		return errors.New("claim response kind does not match requested intent type")
	}
	if actualType == "SIGN" {
		return validateSignClaimResult(claim)
	}
	if actualType == "DKG" {
		if claim.Payload.Type != "" && claim.Payload.Type != "DKG" {
			return errors.New("DKG claim payload kind mismatch")
		}
		return nil
	}
	if len(claim.DescriptorBytes) > 0 {
		return nil
	}
	return errors.New("claim response kind is invalid")
}

func validateSignClaimResult(claim ClaimResult) error {
	if err := validateSignClaimPayload(claim); err != nil {
		return err
	}
	if err := validateSignDerivationContext(claim.Payload); err != nil {
		return err
	}
	return ValidateSignPolicyContext(claim.Payload)
}

func validateSignClaimPayload(claim ClaimResult) error {
	payload := claim.Payload
	if claim.IntentID == "" || claim.SessionID == "" || claim.Deadline.IsZero() || claim.DeadlineRaw == "" ||
		claim.OrgID != "" || claim.KeyID != "" || len(claim.DescriptorBytes) != 0 ||
		claim.DescriptorFingerprint != "" || len(claim.ChainCode) != 0 ||
		payload.Type != "SIGN" || payload.OrgID == "" || payload.KeyID == "" || payload.WalletID == "" || payload.ProfileID == "" || payload.ProfileTemplateID == "" ||
		payload.ProfileVersion == 0 || len(payload.Parties) < 2 || payload.Threshold < 2 || payload.Algorithm == "" || payload.Curve == "" || payload.Chain == "" ||
		len(payload.Digest) == 0 || payload.DigestType == "" || payload.HashAlgorithm == "" || payload.SigningPayloadType == "" || payload.DerivationContextHash == "" ||
		payload.PartyID == "" || payload.DerivationContext == nil || payload.ChainCode != "" || payload.ChainCodeHash != "" || payload.DerivationScheme != "" ||
		len(payload.DescriptorBytes) != 0 || payload.DescriptorFingerprint != "" {
		return errors.New("SIGN claim response is incomplete")
	}
	return nil
}

func validateSignDerivationContext(payload IntentPayload) error {
	derivation := payload.DerivationContext
	if derivation.ProfileID == "" || derivation.ProfileTemplateID == "" || derivation.Chain == "" || derivation.Algorithm == "" || derivation.Curve == "" ||
		derivation.Scheme == "" || derivation.AccountPath == "" || derivation.ChildPath == "" || derivation.FullPath == "" || derivation.PublicKeyFormat == "" ||
		derivation.DescriptorVersion == 0 || derivation.ProfileVersion == 0 || derivation.KeyVersion == 0 {
		return errors.New("SIGN claim derivation context is incomplete")
	}
	if payload.ProfileID != derivation.ProfileID || payload.ProfileTemplateID != derivation.ProfileTemplateID || payload.ProfileVersion != derivation.ProfileVersion ||
		payload.Chain != derivation.Chain || !strings.EqualFold(payload.Algorithm, derivation.Algorithm) || !strings.EqualFold(payload.Curve, derivation.Curve) {
		return errors.New("SIGN claim derivation context mismatch")
	}
	return nil
}

// ValidateSignPolicyContext validates the closed policy variant against the
// authenticated signing and derivation identity carried by the same claim.
func ValidateSignPolicyContext(payload IntentPayload) error {
	policy := payload.PolicyContext
	if policy == nil || policy.Asset == "" || policy.AmountAtomic == "" || policy.FromAddress == "" || policy.ToAddress == "" || policy.Chain == "" {
		return errors.New("SIGN claim policy context is incomplete")
	}
	if policy.Chain != payload.Chain || policy.FromAddress != payload.DerivationContext.ExpectedAddress {
		return errors.New("SIGN claim policy context mismatch")
	}
	if payload.SigningPayloadType == "tron-transaction" {
		if policy.Version != 0 || policy.TransactionType != 0 || policy.ChainID != "" || policy.Nonce != "" || policy.GasLimit != "" ||
			policy.MaxFeePerGas != "" || policy.MaxPriorityFeePerGas != "" {
			return errors.New("SIGN claim policy context mismatch")
		}
		return nil
	}
	if payload.SigningPayloadType != "ethereum-transaction" {
		return nil
	}
	wantChainID := map[string]string{"ethereum:mainnet": "1", "ethereum:sepolia": "11155111"}[policy.Chain]
	if policy.Version != 1 || policy.TransactionType != 2 || wantChainID == "" || policy.ChainID != wantChainID ||
		!canonicalUint(policy.AmountAtomic) || !canonicalUint(policy.Nonce) || !canonicalPositiveUint(policy.GasLimit) ||
		!canonicalPositiveUint(policy.MaxFeePerGas) || !canonicalUint(policy.MaxPriorityFeePerGas) ||
		compareUint(policy.MaxPriorityFeePerGas, policy.MaxFeePerGas) > 0 || !canonicalEVMAddress(policy.FromAddress) || !canonicalEVMAddress(policy.ToAddress) {
		return errors.New("SIGN claim Ethereum policy context is invalid")
	}
	if policy.TokenStandard == nil {
		if policy.Asset != "ETH" || policy.TokenContractCanonical != nil || policy.TokenDecimals != nil {
			return errors.New("SIGN claim Ethereum native asset context is invalid")
		}
		return nil
	}
	if *policy.TokenStandard != "erc20" || policy.TokenContractCanonical == nil || !canonicalEVMAddress(*policy.TokenContractCanonical) ||
		policy.TokenDecimals == nil || *policy.TokenDecimals < 0 || *policy.TokenDecimals > 255 || policy.Asset == "ETH" {
		return errors.New("SIGN claim Ethereum token context is invalid")
	}
	return nil
}

func canonicalUint(value string) bool {
	if value == "0" {
		return true
	}
	if value == "" || value[0] < '1' || value[0] > '9' {
		return false
	}
	for i := 1; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return true
}

func canonicalPositiveUint(value string) bool { return value != "0" && canonicalUint(value) }

func compareUint(left, right string) int {
	leftInt, leftOK := new(big.Int).SetString(left, 10)
	rightInt, rightOK := new(big.Int).SetString(right, 10)
	if !leftOK || !rightOK {
		return 1
	}
	return leftInt.Cmp(rightInt)
}

func canonicalEVMAddress(value string) bool {
	if len(value) != 42 || !strings.HasPrefix(value, "0x") {
		return false
	}
	for _, character := range value[2:] {
		if character < '0' || character > '9' && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func intentTypePath(intentType string) (string, error) {
	switch strings.ToUpper(strings.TrimSpace(intentType)) {
	case "DKG":
		return "dkg", nil
	case "SIGN":
		return "sign", nil
	default:
		return "", errors.New("unsupported intent type")
	}
}
