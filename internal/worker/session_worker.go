package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/metrics"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/monolith"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/terminal"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/transport"
	coretss "github.com/BroLabel/brosettlement-mpc-core/tss"
)

const (
	intentStatusCompleted = "COMPLETED"
	intentStatusFailed    = "FAILED"
	platformPartyID       = "mpc-signer"
	primaryPartyID        = "co-signer-primary"
)

var (
	errInvalidIntent  = errors.New("invalid intent")
	errAlreadyExpired = errors.New("claimed intent already expired")
	errMPCProtocol    = errors.New("mpc protocol error")
	errClaimCleanup   = errors.New("claim delivery requires cleanup")
)

type sessionClient interface {
	ClaimIntent(ctx context.Context, intentType, intentID string) (monolith.ClaimResult, error)
	PostSignResult(ctx context.Context, intentID string, result monolith.SignResultRequest) error
	PostMessage(ctx context.Context, sessionID string, frame monolith.OutboundFrame) error
	GetMessages(ctx context.Context, sessionID string, afterSeq uint64) (monolith.MessagesResult, error)
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
	runSessionWithClock(ctx, intent, client, signRunner, dkgRunner, terminalPublisher, localPartyID, framePollInterval, permits, log, claimDispatched, realReadinessClock())
}

func runSessionWithClock(
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
	clock readinessClock,
) {
	if ctx == nil {
		ctx = context.Background()
	}
	if log == nil {
		log = slog.Default()
	}
	defer permits.Release()
	var dispatchOnce sync.Once
	notifyDispatch := func() {
		dispatchOnce.Do(func() {
			if claimDispatched != nil {
				claimDispatched()
			}
		})
	}
	defer notifyDispatch()

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

	// Discovery without a live reserved worker is cleanup only. Exact claim
	// recovery below belongs exclusively to this newly admitted live attempt.
	if admittedKind == intentKindSIGN && intent.DiscoveryStatus == "CLAIMED" {
		notifyDispatch()
		deliverSignResult(ctx, client, intent, failedResult(ErrorCodeWorkerShutdown, errors.New("orphaned SIGN claim")), log)
		return
	}
	claim, err := claimWithRecovery(ctx, client, intent, notifyDispatch)
	if err != nil {
		if (errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errClaimCleanup)) && ctx.Err() == nil {
			if admittedKind == intentKindSIGN {
				code := ErrorCodeInternal
				if errors.Is(err, context.DeadlineExceeded) {
					code = ErrorCodeAlreadyExpired
				}
				deliverSignResult(ctx, client, intent, failedResult(code, err), log)
			} else {
				publishClaimedDKGFailure(ctx, terminalPublisher, intent, log)
			}
		}
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
		notifyDispatch()
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
	claimedIntent := claim.Intent()
	claimedKind, claimedKindOK := classifyIntentKind(claimedIntent.Type)
	if !claimedKindOK || claimedKind != admittedKind {
		if admittedKind == intentKindDKG {
			metrics.ObserveClaimConflict()
		}
		log.Error("claimed intent kind mismatch")
		if admittedKind == intentKindDKG {
			publishClaimedDKGFailure(ctx, terminalPublisher, claimedIntent, log)
		} else if ctx.Err() == nil {
			<-ctx.Done()
		}
		return
	}
	if err := validateClaimIdentity(intent, claimedIntent, claim, admittedKind); err != nil {
		if admittedKind == intentKindDKG {
			publishClaimedDKGFailure(ctx, terminalPublisher, claimedIntent, log)
		} else {
			deliverSignResult(ctx, client, intent, failedResult(ErrorCodeInvalidIntent, monolith.ErrInvalidLifecycle), log)
		}
		return
	}
	intent = claimedIntent

	if err := validateIntent(intent, localPartyID); err != nil {
		if admittedKind == intentKindDKG {
			publishClaimedDKGFailure(ctx, terminalPublisher, intent, log)
			return
		}
		deliverSignResult(ctx, client, intent, monolith.IntentResult{
			Status:       intentStatusFailed,
			ErrorCode:    ErrorCodeInvalidIntent,
			ErrorMessage: err.Error(),
		}, log)
		return
	}

	deadline := claim.DeadlineTime()
	if !deadline.After(time.Now()) {
		if admittedKind == intentKindDKG {
			publishClaimedDKGFailure(ctx, terminalPublisher, intent, log)
			return
		}
		deliverSignResult(ctx, client, intent, monolith.IntentResult{
			Status:    intentStatusFailed,
			ErrorCode: ErrorCodeAlreadyExpired,
		}, log)
		return
	}

	operationCtx, cancel := context.WithDeadline(ctx, deadline)
	executionCtx := operationCtx
	defer cancel()
	if admittedKind == intentKindSIGN {
		var readyCancel context.CancelFunc
		var readyLifecycle monolith.SessionLifecycle
		var err error
		executionCtx, readyCancel, readyLifecycle, err = waitForSignReadiness(operationCtx, client, intent, framePollInterval, clock)
		if err != nil {
			deliverSignResult(ctx, client, intent, BuildResult(err, executionCtx), log)
			return
		}
		// A rejected observation must never replace the trusted claim identity
		// used to reconcile an ambiguous failure publication.
		intent.Session = readyLifecycle
		defer readyCancel()
	}

	frameCtx := transport.FrameContext{
		Session:   intent.Session,
		IntentID:  intent.IntentID,
		OrgID:     intent.Payload.OrgID,
		SessionID: intent.SessionID,
		Stage:     strings.ToLower(intent.Type),
		Protocol:  intent.Payload.Algorithm,
	}

	tr := transport.NewHTTPTransport(client, frameCtx, framePollInterval, log)
	defer tr.Close()
	tr.Start(executionCtx)

	var runErr error
	var dkgResult DKGResult
	switch admittedKind {
	case intentKindDKG:
		if dkgRunner == nil {
			runErr = errors.New("dkg coordinator is required")
			break
		}
		dkgResult, runErr = dkgRunner.Run(executionCtx, intent, tr)
	case intentKindSIGN:
		runErr = runSignProtocol(executionCtx, intent, signRunner, localPartyID, tr, clock)
		if isPrimarySigningArtifactFailure(runErr) {
			log.Error("critical primary signing material failure", "alert_class", "primary_material_unavailable")
		}
	default:
		runErr = fmt.Errorf("%w: unknown intent type: %s", errInvalidIntent, intent.Type)
	}
	// Both completion barriers precede terminal publication and permit release.
	tr.Close()
	if admittedKind == intentKindSIGN && tr.Err() != nil {
		runErr = tr.Err()
	}

	if admittedKind == intentKindDKG {
		publishDKGResult(ctx, terminalPublisher, intent, dkgResult, runErr, log)
		return
	}

	result := BuildResult(runErr, executionCtx)
	deliverSignResult(ctx, client, intent, result, log)
}

func claimWithRecovery(ctx context.Context, client sessionClient, intent monolith.Intent, dispatched func()) (monolith.ClaimResult, error) {
	claimCtx := ctx
	if !intent.ExpiresAt.IsZero() {
		var cancel context.CancelFunc
		claimCtx, cancel = context.WithDeadline(ctx, intent.ExpiresAt)
		defer cancel()
	}
	first := true
	uncertain := false
	for {
		if err := claimCtx.Err(); err != nil {
			return monolith.ClaimResult{}, err
		}
		requestCtx, cancel := context.WithTimeout(claimCtx, 400*time.Millisecond)
		claim, err := client.ClaimIntent(requestCtx, intent.Type, intent.IntentID)
		cancel()
		if first {
			first = false
			if (err == nil || errors.Is(err, monolith.ErrClaimOutcomeUnknown) || errors.Is(err, context.DeadlineExceeded)) && dispatched != nil {
				dispatched()
			}
		}
		if err == nil || errors.Is(err, monolith.ErrAlreadyClaimed) || errors.Is(err, monolith.ErrNotFound) {
			if uncertain && errors.Is(err, monolith.ErrAlreadyClaimed) {
				return claim, errors.Join(errClaimCleanup, err)
			}
			return claim, err
		}
		if !errors.Is(err, monolith.ErrClaimOutcomeUnknown) && !errors.Is(err, context.DeadlineExceeded) {
			if uncertain {
				return claim, errors.Join(errClaimCleanup, err)
			}
			return claim, err
		}
		uncertain = true
		if !waitDeliveryRetry(claimCtx) {
			return monolith.ClaimResult{}, claimCtx.Err()
		}
	}
}

func waitDeliveryRetry(ctx context.Context) bool {
	timer := time.NewTimer(25 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
