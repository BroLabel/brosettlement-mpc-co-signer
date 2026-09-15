package worker

import (
	"context"
	"errors"
	"strings"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/monolith"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/sharestore"
	coretss "github.com/BroLabel/brosettlement-mpc-core/tss"
)

const (
	ErrorCodeInvalidIntent       = "INVALID_INTENT"
	ErrorCodeAlreadyExpired      = "ALREADY_EXPIRED"
	ErrorCodeSessionTimeout      = "SESSION_TIMEOUT"
	ErrorCodeWorkerShutdown      = "WORKER_SHUTDOWN"
	ErrorCodeShareNotFound       = "SHARE_NOT_FOUND"
	ErrorCodeInvalidSharePayload = "INVALID_SHARE_PAYLOAD"
	ErrorCodeShareMetadata       = "SHARE_METADATA_MISMATCH"
	ErrorCodeMissingPublicKey    = "DKG_MISSING_PUBLIC_KEY"
	ErrorCodeMissingAddress      = "DKG_MISSING_ADDRESS"
	ErrorCodeProtocol            = "MPC_PROTOCOL_ERROR"
	ErrorCodeInternal            = "INTERNAL_ERROR"
)

func BuildResult(runErr error, sessionCtx context.Context) monolith.IntentResult {
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

func isPrimarySigningArtifactFailure(err error) bool {
	return errors.Is(err, coretss.ErrShareNotFound) ||
		errors.Is(err, sharestore.ErrArtifactBinding) ||
		errors.Is(err, coretss.ErrInvalidSharePayload) ||
		errors.Is(err, coretss.ErrMetadataMismatch)
}
