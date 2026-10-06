package worker

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/monolith"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/transport"
	coretss "github.com/BroLabel/brosettlement-mpc-core/tss"
)

type readinessClock struct {
	// Scheduling at this boundary cannot authorize execution; Dispatch always
	// arbitrates against transport stop after the scheduling callback returns.
	beforeDispatch func(*transport.HTTPTransport)
	sample         func() (time.Time, time.Time)
	arm            func(context.Context, time.Time) (context.Context, context.CancelFunc)
}

func realReadinessClock() readinessClock {
	return readinessClock{sample: func() (time.Time, time.Time) { now := time.Now(); return now, now }, arm: context.WithDeadline}
}

func signExecutionBudget(expiresAt, wall time.Time) time.Duration {
	return min(300*time.Second, expiresAt.Add(-2*time.Second).Sub(wall))
}

func waitForSessionReadiness(ctx context.Context, client sessionClient, intent monolith.Intent, interval time.Duration, clock readinessClock) (context.Context, context.CancelFunc, monolith.SessionLifecycle, error) {
	kind := strings.ToUpper(strings.TrimSpace(intent.Type))
	for {
		if err := ctx.Err(); err != nil {
			return ctx, nil, monolith.SessionLifecycle{}, err
		}
		result, err := client.GetMessages(ctx, intent.SessionID, 0)
		if errors.Is(err, monolith.ErrInvalidLifecycle) {
			return ctx, nil, result.Session, err
		}
		if err == nil {
			if err := result.Session.Validate(kind, intent.SessionID, intent.ExpiresAt); err != nil {
				return ctx, nil, result.Session, err
			}
			switch result.Session.Status {
			case "PENDING":
			case "RUNNING":
				if intent.Session.StartedAt != nil && (!result.Session.StartedAt.Equal(*intent.Session.StartedAt) || !sameOptionalTime(result.Session.ExecutionExpiresAt, intent.Session.ExecutionExpiresAt)) {
					return ctx, nil, result.Session, monolith.ErrInvalidLifecycle
				}
				if kind == "DKG" {
					active, stop := context.WithCancel(ctx)
					return active, stop, result.Session, nil
				}
				wall, mono := clock.sample()
				budget := signExecutionBudget(*result.Session.ExecutionExpiresAt, wall)
				if budget <= 0 || !result.Session.Deadline.After(wall) {
					return ctx, nil, result.Session, errAlreadyExpired
				}
				active, stop := clock.arm(ctx, mono.Add(budget))
				if err := active.Err(); err != nil {
					stop()
					return ctx, nil, result.Session, err
				}
				return active, stop, result.Session, nil
			default:
				return ctx, nil, result.Session, monolith.ErrInvalidLifecycle
			}
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx, nil, monolith.SessionLifecycle{}, ctx.Err()
		case <-timer.C:
		}
	}
}

func sameOptionalTime(left, right *time.Time) bool {
	return left == nil && right == nil || left != nil && right != nil && left.Equal(*right)
}

// runSignProtocol joins Core before returning, including after transport stop or cancellation.
func runSignProtocol(sessionCtx context.Context, intent monolith.Intent, signRunner signSessionRunner, localPartyID string, tr *transport.HTTPTransport, clock readinessClock) error {
	if err := sessionCtx.Err(); err != nil {
		return err
	}
	var runErr error
	runnerCtx, stopRunner := context.WithCancel(sessionCtx)
	finished := make(chan error, 1)
	go func() {
		if err := runnerCtx.Err(); err != nil {
			finished <- err
			return
		}
		if clock.beforeDispatch != nil {
			clock.beforeDispatch(tr)
		}
		finished <- tr.Dispatch(runnerCtx, func(dispatchCtx context.Context) error {
			return signRunner.RunSignSession(dispatchCtx, buildSignRequest(intent, localPartyID, tr))
		})
	}()
	select {
	case runErr = <-finished:
	case <-tr.Done():
		stopRunner()
		runErr = <-finished
		if tr.Err() != nil {
			runErr = tr.Err()
		}
	case <-sessionCtx.Done():
		stopRunner()
		runErr = <-finished
		if runErr == nil {
			runErr = sessionCtx.Err()
		}
	}
	stopRunner()
	return runErr
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
