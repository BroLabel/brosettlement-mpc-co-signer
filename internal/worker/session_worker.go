package worker

import (
	"context"
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
	switch strings.ToUpper(strings.TrimSpace(intent.Type)) {
	case "DKG":
		_, runErr = runner.RunDKGSession(sessionCtx, buildDKGRequest(intent, localPartyID, tr))
	case "SIGN":
		runErr = runner.RunSignSession(sessionCtx, buildSignRequest(intent, localPartyID, tr))
	default:
		runErr = fmt.Errorf("%w: unknown intent type: %s", errInvalidIntent, intent.Type)
	}

	postResult(ctx, client, intent.IntentID, BuildResult(runErr, sessionCtx, intent), log)
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
		errors.Is(runErr, coretss.ErrDigestMissing):
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
	if curve != "" && !strings.EqualFold(curve, "secp256k1") {
		return fmt.Errorf("%w: unsupported ecdsa curve %q", errInvalidIntent, intent.Payload.Curve)
	}

	if intentType == "SIGN" {
		if strings.TrimSpace(intent.Payload.KeyID) == "" {
			return fmt.Errorf("%w: key id is required for SIGN", errInvalidIntent)
		}
		if len(intent.Payload.Digest) == 0 {
			return fmt.Errorf("%w: digest is required for SIGN", errInvalidIntent)
		}
	}

	return nil
}

func buildDKGRequest(intent monolith.Intent, localPartyID string, tr coretss.Transport) coretss.DKGSessionRequest {
	return coretss.DKGSessionRequest{
		Session: coretss.SessionDescriptor{
			SessionID: intent.SessionID,
			KeyID:     intent.Payload.KeyID,
			Parties:   intent.Payload.Parties,
			Threshold: intent.Payload.Threshold,
			Algorithm: intent.Payload.Algorithm,
			Curve:     intent.Payload.Curve,
			Chain:     intent.Payload.Chain,
		},
		LocalPartyID: localPartyID,
		Transport:    tr,
	}
}

func buildSignRequest(intent monolith.Intent, localPartyID string, tr coretss.Transport) coretss.SignSessionRequest {
	return coretss.SignSessionRequest{
		Session: coretss.SessionDescriptor{
			SessionID: intent.SessionID,
			KeyID:     intent.Payload.KeyID,
			Parties:   intent.Payload.Parties,
			Threshold: intent.Payload.Threshold,
			Algorithm: intent.Payload.Algorithm,
			Curve:     intent.Payload.Curve,
			Chain:     intent.Payload.Chain,
		},
		LocalPartyID: localPartyID,
		Digest:       intent.Payload.Digest,
		Transport:    tr,
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
