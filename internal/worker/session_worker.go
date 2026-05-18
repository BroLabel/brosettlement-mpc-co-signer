package worker

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/monolith"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/transport"
	coretss "github.com/BroLabel/brosettlement-mpc-core/tss"
)

const (
	intentStatusCompleted = "COMPLETED"
	intentStatusFailed    = "FAILED"
	postResultTimeout     = 5 * time.Second
)

var (
	errInvalidIntent  = errors.New("invalid intent")
	errAlreadyExpired = errors.New("claimed intent already expired")
	errMPCProtocol    = errors.New("mpc protocol error")
)

type sessionClient interface {
	ClaimIntent(ctx context.Context, intentID string) (monolith.ClaimResult, error)
	PostResult(ctx context.Context, intentID string, result monolith.IntentResult) error
	PostMessage(ctx context.Context, sessionID string, frame monolith.OutboundFrame) error
	GetMessages(ctx context.Context, sessionID string, afterSeq uint64) ([]monolith.InboundMessage, error)
}

type sessionRunner interface {
	RunDKGSession(ctx context.Context, req coretss.DKGSessionRequest) (coretss.DKGOutput, error)
	RunSignSession(ctx context.Context, req coretss.SignSessionRequest) error
}

func RunSession(
	ctx context.Context,
	intent monolith.Intent,
	client sessionClient,
	runner sessionRunner,
	localPartyID string,
	framePollInterval time.Duration,
	sem chan struct{},
	repollCh chan struct{},
	log *slog.Logger,
) {
	if log == nil {
		log = slog.Default()
	}
	defer releaseSem(sem, repollCh)

	claim, err := client.ClaimIntent(ctx, intent.IntentID)
	if err != nil {
		switch {
		case errors.Is(err, monolith.ErrAlreadyClaimed):
			log.Debug("intent already claimed", "intent_id", intent.IntentID)
		case errors.Is(err, monolith.ErrNotFound):
			log.Debug("intent not found while claiming", "intent_id", intent.IntentID)
		case errors.Is(err, monolith.ErrClaimOutcomeUnknown):
			log.Warn("intent claim outcome unknown", "intent_id", intent.IntentID, "err", err)
		default:
			log.Warn("intent claim failed", "intent_id", intent.IntentID, "err", err)
		}
		return
	}
	intent = claim.IntentOrFallback(intent)

	if err := validateIntent(intent, localPartyID); err != nil {
		postResult(ctx, client, intent.IntentID, monolith.IntentResult{
			Status:       intentStatusFailed,
			ErrorCode:    ErrorCodeInvalidIntent,
			ErrorMessage: err.Error(),
		}, log)
		return
	}

	if !claim.ExpiresAt.After(time.Now()) {
		postResult(ctx, client, intent.IntentID, monolith.IntentResult{
			Status:    intentStatusFailed,
			ErrorCode: ErrorCodeAlreadyExpired,
		}, log)
		return
	}

	sessionCtx, cancel := context.WithDeadline(ctx, claim.ExpiresAt)
	defer cancel()

	frameCtx := transport.FrameContext{
		SessionID: intent.SessionID,
		Stage:     strings.ToLower(intent.Type),
		Protocol:  intent.Payload.Algorithm,
	}

	tr := transport.NewHTTPTransport(client, frameCtx, framePollInterval, log)
	defer tr.Close()
	tr.Start(sessionCtx)

	var runErr error
	var dkgOutput coretss.DKGOutput
	switch strings.ToUpper(strings.TrimSpace(intent.Type)) {
	case "DKG":
		dkgOutput, runErr = runner.RunDKGSession(sessionCtx, buildDKGRequest(intent, localPartyID, tr))
	case "SIGN":
		runErr = runner.RunSignSession(sessionCtx, buildSignRequest(intent, localPartyID, tr))
	default:
		runErr = fmt.Errorf("%w: unknown intent type: %s", errInvalidIntent, intent.Type)
	}

	result := BuildResult(runErr, sessionCtx, intent)
	if runErr == nil && strings.EqualFold(strings.TrimSpace(intent.Type), "DKG") {
		result = buildDKGSuccessResult(intent, localPartyID, dkgOutput)
	}
	postResult(ctx, client, intent.IntentID, result, log)
}

func BuildResult(runErr error, sessionCtx context.Context, _ monolith.Intent) monolith.IntentResult {
	if runErr == nil {
		return monolith.IntentResult{Status: intentStatusCompleted}
	}

	switch {
	case errors.Is(runErr, context.DeadlineExceeded):
		return failedResult(ErrorCodeSessionTimeout, runErr)
	case errors.Is(runErr, context.Canceled):
		return failedResult(ErrorCodeWorkerShutdown, runErr)
	case errors.Is(runErr, errInvalidIntent),
		errors.Is(runErr, coretss.ErrInvalidSessionDescriptor),
		errors.Is(runErr, coretss.ErrLocalPartyRequired),
		errors.Is(runErr, coretss.ErrTransportRequired),
		errors.Is(runErr, coretss.ErrKeyIDRequired),
		errors.Is(runErr, coretss.ErrDigestMissing),
		errors.Is(runErr, coretss.ErrChainCodeMissing),
		errors.Is(runErr, coretss.ErrChainCodeInvalid),
		errors.Is(runErr, coretss.ErrDerivationContextRequired),
		errors.Is(runErr, coretss.ErrInvalidDerivationContext),
		errors.Is(runErr, coretss.ErrUnsupportedDerivationScheme),
		errors.Is(runErr, coretss.ErrDerivedSigningUnsupported),
		errors.Is(runErr, coretss.ErrDerivationPathInvalid),
		errors.Is(runErr, coretss.ErrDerivationContextMismatch),
		errors.Is(runErr, coretss.ErrUnsupportedAlgorithmCurve):
		return failedResult(ErrorCodeInvalidIntent, runErr)
	case errors.Is(runErr, errAlreadyExpired):
		return failedResult(ErrorCodeAlreadyExpired, runErr)
	case errors.Is(runErr, coretss.ErrShareNotFound):
		return failedResult(ErrorCodeShareNotFound, runErr)
	case errors.Is(runErr, coretss.ErrShareDisabled):
		return failedResult(ErrorCodeShareDisabled, runErr)
	case errors.Is(runErr, coretss.ErrInvalidSharePayload):
		return failedResult(ErrorCodeInvalidSharePayload, runErr)
	case errors.Is(runErr, coretss.ErrMetadataMismatch):
		return failedResult(ErrorCodeShareMetadata, runErr)
	case errors.Is(runErr, coretss.ErrMissingDKGPublicKey):
		return failedResult(ErrorCodeMissingPublicKey, runErr)
	case errors.Is(runErr, coretss.ErrMissingDKGAddress):
		return failedResult(ErrorCodeMissingAddress, runErr)
	case errors.Is(runErr, errMPCProtocol), isProtocolError(runErr):
		return failedResult(ErrorCodeProtocol, runErr)
	case sessionCtx != nil && errors.Is(sessionCtx.Err(), context.DeadlineExceeded):
		return failedResult(ErrorCodeSessionTimeout, sessionCtx.Err())
	case sessionCtx != nil && errors.Is(sessionCtx.Err(), context.Canceled):
		return failedResult(ErrorCodeWorkerShutdown, sessionCtx.Err())
	default:
		return failedResult(ErrorCodeInternal, runErr)
	}
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
		return validateSignPayload(intent.Payload)
	default:
		return nil
	}
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
	if len(payload.Digest) == 0 {
		return fmt.Errorf("%w: digest is required for SIGN", errInvalidIntent)
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

func toCoreDerivationContext(ctx monolith.DerivationContext) coretss.DerivationContext {
	return coretss.DerivationContext{
		ProfileID:         ctx.ProfileID,
		ProfileTemplateID: ctx.ProfileTemplateID,
		Chain:             ctx.Chain,
		Algorithm:         ctx.Algorithm,
		Curve:             ctx.Curve,
		Scheme:            ctx.Scheme,
		PublicKeyFormat:   ctx.PublicKeyFormat,
		AccountPath:       ctx.AccountPath,
		ChildPath:         ctx.ChildPath,
		FullPath:          ctx.FullPath,
		AddressEncoding:   ctx.AddressEncoding,
		ExpectedAddress:   ctx.ExpectedAddress,
		DerivedPublicKey:  ctx.ExpectedPublicKey,
		DescriptorVersion: ctx.DescriptorVersion,
		ProfileVersion:    ctx.ProfileVersion,
		KeyVersion:        ctx.KeyVersion,
	}
}

func derivationContextPtr(ctx monolith.DerivationContext) *coretss.DerivationContext {
	coreCtx := toCoreDerivationContext(ctx)
	return &coreCtx
}

func buildDKGRequest(intent monolith.Intent, localPartyID string, tr coretss.Transport) coretss.DKGSessionRequest {
	return coretss.DKGSessionRequest{
		Session: coretss.SessionDescriptor{
			SessionID: intent.SessionID,
			OrgID:     intent.Payload.OrgID,
			KeyID:     intent.Payload.KeyID,
			Parties:   intent.Payload.Parties,
			Threshold: intent.Payload.Threshold,
			Algorithm: intent.Payload.Algorithm,
			Curve:     intent.Payload.Curve,
			Chain:     "",
		},
		LocalPartyID: localPartyID,
		DerivationMaterial: &coretss.DKGDerivationMaterial{
			ChainCode:        intent.Payload.ChainCode,
			DerivationScheme: intent.Payload.DerivationScheme,
		},
		Transport: tr,
	}
}

func buildSignRequest(intent monolith.Intent, localPartyID string, tr coretss.Transport) coretss.SignSessionRequest {
	return coretss.SignSessionRequest{
		Session: coretss.SessionDescriptor{
			SessionID: intent.SessionID,
			OrgID:     intent.Payload.OrgID,
			KeyID:     intent.Payload.KeyID,
			Parties:   intent.Payload.Parties,
			Threshold: intent.Payload.Threshold,
			Algorithm: intent.Payload.Algorithm,
			Curve:     intent.Payload.Curve,
			Chain:     intent.Payload.Chain,
		},
		LocalPartyID:      localPartyID,
		Digest:            intent.Payload.Digest,
		DerivationContext: derivationContextPtr(*intent.Payload.DerivationContext),
		Transport:         tr,
	}
}

func buildDKGSuccessResult(intent monolith.Intent, localPartyID string, output coretss.DKGOutput) monolith.IntentResult {
	return monolith.IntentResult{
		Status: intentStatusCompleted,
		DkgMaterial: &monolith.DkgParticipantResult{
			PartyID:          localPartyID,
			KeyID:            output.KeyID,
			AccountPublicKey: output.PublicKey,
			ChainCodeHash:    intent.Payload.ChainCodeHash,
			ChainCodePresent: true,
			PublicKeyFormat:  output.PublicKeyFormat,
			DerivationScheme: output.DerivationScheme,
		},
	}
}

func postResult(
	ctx context.Context,
	client sessionClient,
	intentID string,
	result monolith.IntentResult,
	log *slog.Logger,
) {
	postCtx := ctx
	if ctx == nil || ctx.Err() != nil {
		timeoutCtx, cancel := context.WithTimeout(context.Background(), postResultTimeout)
		defer cancel()
		postCtx = timeoutCtx
	}

	if err := client.PostResult(postCtx, intentID, result); err != nil {
		log.Warn("post result failed", "intent_id", intentID, "status", result.Status, "error_code", result.ErrorCode, "err", err)
	}
}

func releaseSem(sem chan struct{}, repollCh chan struct{}) {
	if sem != nil {
		<-sem
	}
	if repollCh != nil {
		select {
		case repollCh <- struct{}{}:
		default:
		}
	}
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

func failedResult(code string, err error) monolith.IntentResult {
	return monolith.IntentResult{
		Status:       intentStatusFailed,
		ErrorCode:    code,
		ErrorMessage: err.Error(),
	}
}

func isProtocolError(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "duplicate frame") ||
		strings.Contains(msg, "unknown party") ||
		strings.Contains(msg, "frame payload too large") ||
		strings.Contains(msg, "queue is full") ||
		strings.Contains(msg, "protocol stalled") ||
		strings.Contains(msg, "mpc protocol") ||
		strings.Contains(msg, "protocol error")
}
