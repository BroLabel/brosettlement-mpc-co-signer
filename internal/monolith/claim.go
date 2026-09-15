package monolith

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func (c *Client) ClaimIntent(ctx context.Context, intentType, intentID string) (ClaimResult, error) {
	pathType, err := intentTypePath(intentType)
	if err != nil {
		return ClaimResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 400*time.Millisecond)
	defer cancel()
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
	return validateSignPolicyContext(claim.Payload)
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

func validateSignPolicyContext(payload IntentPayload) error {
	policy := payload.PolicyContext
	if policy == nil || policy.Asset == "" || policy.AmountAtomic == "" || policy.FromAddress == "" || policy.ToAddress == "" || policy.Chain == "" {
		return errors.New("SIGN claim policy context is incomplete")
	}
	if policy.Chain != payload.Chain || policy.FromAddress != payload.DerivationContext.ExpectedAddress {
		return errors.New("SIGN claim policy context mismatch")
	}
	return nil
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
