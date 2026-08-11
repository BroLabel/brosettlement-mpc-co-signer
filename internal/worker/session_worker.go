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

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/metrics"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/monolith"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/sharestore"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/terminal"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/transport"
	coretss "github.com/BroLabel/brosettlement-mpc-core/tss"
)

const (
	intentStatusCompleted = "COMPLETED"
	intentStatusFailed    = "FAILED"
	postResultTimeout     = 5 * time.Second
	platformPartyID       = "mpc-signer"
	primaryPartyID        = "co-signer-primary"
)

var (
	errInvalidIntent  = errors.New("invalid intent")
	errAlreadyExpired = errors.New("claimed intent already expired")
	errMPCProtocol    = errors.New("mpc protocol error")
)

type sessionClient interface {
	ClaimIntent(ctx context.Context, intentType, intentID string) (monolith.ClaimResult, error)
	PostResult(ctx context.Context, intentID string, result monolith.IntentResult) error
	PostMessage(ctx context.Context, sessionID string, frame monolith.OutboundFrame) error
	GetMessages(ctx context.Context, sessionID string, afterSeq uint64) ([]monolith.InboundMessage, error)
}

type signSessionRunner interface {
	RunSignSession(ctx context.Context, req coretss.SignSessionRequest) error
}

type dkgExecutor interface {
	Run(ctx context.Context, intent monolith.Intent, network coretss.Transport) (DKGResult, error)
}

type DKGTerminalPublisher interface {
	Publish(context.Context, terminal.Job) (terminal.Outcome, error)
}

type intentKind uint8

const (
	intentKindDKG intentKind = iota + 1
	intentKindSIGN
)

func classifyIntentKind(raw string) (intentKind, bool) {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case "DKG":
		return intentKindDKG, true
	case "SIGN":
		return intentKindSIGN, true
	default:
		return 0, false
	}
}

func RunSessionWithExecutors(
	ctx context.Context,
	intent monolith.Intent,
	client sessionClient,
	signRunner signSessionRunner,
	dkgRunner dkgExecutor,
	terminalPublisher DKGTerminalPublisher,
	localPartyID string,
	framePollInterval time.Duration,
	sem chan struct{},
	repollCh chan struct{},
	log *slog.Logger,
) {
	runSessionWithPermits(
		ctx,
		intent,
		client,
		signRunner,
		dkgRunner,
		terminalPublisher,
		localPartyID,
		framePollInterval,
		newLegacyGeneralLease(sem, repollCh),
		log,
		nil,
	)
}

func runSessionWithPermits(
	ctx context.Context,
	intent monolith.Intent,
	client sessionClient,
	signRunner signSessionRunner,
	dkgRunner dkgExecutor,
	terminalPublisher DKGTerminalPublisher,
	localPartyID string,
	framePollInterval time.Duration,
	permits *jobPermitLease,
	log *slog.Logger,
	claimDispatched func(),
) {
	if ctx == nil {
		ctx = context.Background()
	}
	if log == nil {
		log = slog.Default()
	}
	defer permits.Release()

	admittedKind, ok := classifyIntentKind(intent.Type)
	if !ok {
		log.Error("unsupported admitted intent type")
		return
	}
	metricKind := "SIGN"
	if admittedKind == intentKindDKG {
		metricKind = "DKG"
	}
	metrics.JobStarted(metricKind)
	started := time.Now()
	defer func() {
		metrics.JobFinished(metricKind)
		metrics.ObserveSessionDuration(metricKind, time.Since(started).Seconds())
	}()

	claim, err := client.ClaimIntent(ctx, intent.Type, intent.IntentID)
	if err != nil {
		outcome := "failed"
		if errors.Is(err, monolith.ErrAlreadyClaimed) {
			outcome = "conflict"
		}
		if errors.Is(err, monolith.ErrClaimOutcomeUnknown) {
			outcome = "unknown"
		}
		metrics.ObserveClaim(metricKind, outcome)
		if admittedKind == intentKindDKG && errors.Is(err, monolith.ErrAlreadyClaimed) {
			metrics.ObserveClaimConflict()
		}
		permits.Release()
		if claimDispatched != nil {
			claimDispatched()
		}
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
	metrics.ObserveClaim(metricKind, "accepted")
	if claimDispatched != nil {
		claimDispatched()
	}
	claimedIntent := claim.Intent()
	claimedKind, claimedKindOK := classifyIntentKind(claimedIntent.Type)
	if !claimedKindOK || claimedKind != admittedKind {
		if admittedKind == intentKindDKG {
			metrics.ObserveClaimConflict()
		}
		log.Error("claimed intent kind mismatch")
		if admittedKind == intentKindDKG {
			if !publishClaimedDKGFailure(ctx, terminalPublisher, claimedIntent, log) && ctx.Err() == nil {
				<-ctx.Done()
			}
		} else if ctx.Err() == nil {
			<-ctx.Done()
		}
		return
	}
	if admittedKind == intentKindSIGN && intent.DiscoveryStatus == "CLAIMED" {
		if err := validateRediscoveredSignClaim(intent, claim); err != nil {
			log.Error("rediscovered SIGN claim replay mismatch", "intent_id", intent.IntentID, "err", err)
			return
		}
	}
	intent = claimedIntent

	if err := validateIntent(intent, localPartyID); err != nil {
		if admittedKind == intentKindDKG {
			if !publishClaimedDKGFailure(ctx, terminalPublisher, intent, log) && ctx.Err() == nil {
				<-ctx.Done()
			}
			return
		}
		postResult(ctx, client, intent.IntentID, monolith.IntentResult{
			Status:       intentStatusFailed,
			ErrorCode:    ErrorCodeInvalidIntent,
			ErrorMessage: err.Error(),
		}, log)
		return
	}

	deadline := claim.DeadlineTime()
	if !deadline.After(time.Now()) {
		if admittedKind == intentKindDKG {
			if !publishClaimedDKGFailure(ctx, terminalPublisher, intent, log) && ctx.Err() == nil {
				<-ctx.Done()
			}
			return
		}
		postResult(ctx, client, intent.IntentID, monolith.IntentResult{
			Status:    intentStatusFailed,
			ErrorCode: ErrorCodeAlreadyExpired,
		}, log)
		return
	}

	sessionCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	frameCtx := transport.FrameContext{
		IntentID:  intent.IntentID,
		OrgID:     intent.Payload.OrgID,
		SessionID: intent.SessionID,
		Stage:     strings.ToLower(intent.Type),
		Protocol:  intent.Payload.Algorithm,
	}

	tr := transport.NewHTTPTransport(client, frameCtx, framePollInterval, log)
	defer tr.Close()
	tr.Start(sessionCtx)

	var runErr error
	var dkgResult DKGResult
	switch strings.ToUpper(strings.TrimSpace(intent.Type)) {
	case "DKG":
		if dkgRunner == nil {
			runErr = errors.New("dkg coordinator is required")
			break
		}
		dkgResult, runErr = dkgRunner.Run(sessionCtx, intent, tr)
	case "SIGN":
		runErr = signRunner.RunSignSession(sessionCtx, buildSignRequest(intent, localPartyID, tr))
		if isPrimarySigningArtifactFailure(runErr) {
			log.Error("critical primary signing material failure", "alert_class", "primary_material_unavailable")
		}
	default:
		runErr = fmt.Errorf("%w: unknown intent type: %s", errInvalidIntent, intent.Type)
	}

	if strings.EqualFold(strings.TrimSpace(intent.Type), "DKG") {
		if terminalPublisher == nil {
			log.Error("dkg terminal publisher is unavailable", "intent_id", intent.IntentID)
			<-ctx.Done()
			return
		}
		job, err := buildDKGTerminalJob(intent, dkgResult, runErr)
		if err != nil {
			log.Error("construct canonical dkg terminal result failed", "intent_id", intent.IntentID, "err", err)
			<-ctx.Done()
			return
		}
		outcome, err := terminalPublisher.Publish(ctx, job)
		if err != nil {
			log.Warn("publish dkg terminal result stopped", "intent_id", intent.IntentID, "err", err)
			if ctx.Err() == nil {
				<-ctx.Done()
			}
			return
		}
		if outcome.Kind == terminal.OutcomeTerminalConflict {
			log.Error(
				"dkg terminal result conflict",
				"intent_id", intent.IntentID,
				"authoritative_status", outcome.AuthoritativeStatus,
			)
		}
		return
	}

	result := BuildResult(runErr, sessionCtx, intent)
	postResult(ctx, client, intent.IntentID, result, log)
}

func validateRediscoveredSignClaim(discovery monolith.Intent, claim monolith.ClaimResult) error {
	if discovery.DiscoveryStatus != "CLAIMED" || discovery.Type != "SIGN" || discovery.IntentID == "" || discovery.SessionID == "" ||
		discovery.Payload.OrgID == "" || discovery.Payload.KeyID == "" || discovery.DeadlineRaw == "" || discovery.ExpiresAt.IsZero() {
		return errors.New("rediscovered SIGN metadata is incomplete")
	}
	claimed := claim.Intent()
	if claim.Status != "CLAIMED" ||
		claimed.IntentID != discovery.IntentID || claimed.SessionID != discovery.SessionID || claimed.Type != discovery.Type ||
		claimed.Payload.OrgID != discovery.Payload.OrgID || claimed.Payload.KeyID != discovery.Payload.KeyID ||
		claim.DeadlineRaw != discovery.DeadlineRaw || !claim.DeadlineTime().Equal(discovery.ExpiresAt) {
		return errors.New("claim replay differs from SIGN discovery metadata")
	}
	return nil
}

func isPrimarySigningArtifactFailure(err error) bool {
	return errors.Is(err, coretss.ErrShareNotFound) ||
		errors.Is(err, sharestore.ErrArtifactBinding) ||
		errors.Is(err, coretss.ErrInvalidSharePayload) ||
		errors.Is(err, coretss.ErrMetadataMismatch)
}

func publishClaimedDKGFailure(
	ctx context.Context,
	publisher DKGTerminalPublisher,
	intent monolith.Intent,
	log *slog.Logger,
) bool {
	if publisher == nil {
		return false
	}
	job, err := terminal.NewFailedJob(intent.IntentID, intent.SessionID, intent.Payload.KeyID)
	if err != nil {
		log.Error("construct canonical failed dkg terminal result failed", "intent_id", intent.IntentID, "err", err)
		if ctx != nil && ctx.Err() == nil {
			<-ctx.Done()
		}
		return true
	}
	outcome, err := publisher.Publish(ctx, job)
	if err != nil {
		log.Warn("publish failed dkg terminal result stopped", "intent_id", intent.IntentID, "err", err)
		if ctx != nil && ctx.Err() == nil {
			<-ctx.Done()
		}
		return true
	}
	if outcome.Kind == terminal.OutcomeTerminalConflict {
		log.Error(
			"dkg terminal result conflict",
			"intent_id", intent.IntentID,
			"authoritative_status", outcome.AuthoritativeStatus,
		)
	}
	return true
}

func buildDKGTerminalJob(intent monolith.Intent, output DKGResult, runErr error) (terminal.Job, error) {
	if runErr != nil {
		return terminal.NewFailedJob(intent.IntentID, intent.SessionID, intent.Payload.KeyID)
	}
	return terminal.NewCompletedJob(terminal.CompletedInput{
		IntentID:              intent.IntentID,
		SessionID:             intent.SessionID,
		KeyID:                 intent.Payload.KeyID,
		DescriptorFingerprint: output.Primary.DescriptorFingerprint,
		AccountPublicKey:      output.Primary.AccountPublicKey,
		ChainCodeHash:         output.Primary.ChainCodeHash,
		Primary: terminal.ArtifactInput{
			PartyID:     output.Primary.PartyID,
			Purpose:     string(output.Primary.Purpose),
			Fingerprint: output.Primary.ArtifactFingerprint,
		},
		Recovery: terminal.ArtifactInput{
			PartyID:     output.Recovery.PartyID,
			Purpose:     string(output.Recovery.Purpose),
			Fingerprint: output.Recovery.ArtifactFingerprint,
		},
	})
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
		errors.Is(runErr, coretss.ErrUnsupportedAlgorithmCurve),
		errors.Is(runErr, ErrInvalidDKGContext):
		return failedResult(ErrorCodeInvalidIntent, runErr)
	case errors.Is(runErr, errAlreadyExpired):
		return failedResult(ErrorCodeAlreadyExpired, runErr)
	case errors.Is(runErr, coretss.ErrShareNotFound):
		return failedResult(ErrorCodeShareNotFound, runErr)
	case errors.Is(runErr, coretss.ErrInvalidSharePayload):
		return failedResult(ErrorCodeInvalidSharePayload, runErr)
	case errors.Is(runErr, sharestore.ErrArtifactBinding), errors.Is(runErr, coretss.ErrMetadataMismatch):
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

func buildSignRequest(intent monolith.Intent, localPartyID string, tr coretss.Transport) coretss.SignSessionRequest {
	return coretss.SignSessionRequest{
		Session: coretss.SignSessionDescriptor{
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
