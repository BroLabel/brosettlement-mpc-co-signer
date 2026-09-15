package worker

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/monolith"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/terminal"
)

const postResultTimeout = 5 * time.Second

func publishClaimedDKGFailure(
	ctx context.Context,
	publisher DKGTerminalPublisher,
	intent monolith.Intent,
	log *slog.Logger,
) {
	if publisher == nil {
		// An unresolved publication must retain the caller's permit until shutdown.
		<-ctx.Done()
		return
	}
	job, err := terminal.NewFailedJob(intent.IntentID, intent.SessionID, intent.Payload.KeyID)
	if err != nil {
		log.Error("construct canonical failed dkg terminal result failed", "intent_id", intent.IntentID, "err", err)
		if ctx != nil && ctx.Err() == nil {
			<-ctx.Done()
		}
		return
	}
	outcome, err := publisher.Publish(ctx, job)
	if err != nil {
		log.Warn("publish failed dkg terminal result stopped", "intent_id", intent.IntentID, "err", err)
		if ctx != nil && ctx.Err() == nil {
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

func publishDKGResult(ctx context.Context, terminalPublisher DKGTerminalPublisher, intent monolith.Intent, dkgResult DKGResult, runErr error, log *slog.Logger) {
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

func deliverSignResult(
	ctx context.Context,
	client sessionClient,
	intent monolith.Intent,
	result monolith.IntentResult,
	log *slog.Logger,
) {
	intentID := intent.IntentID
	postCtx := ctx
	request, err := monolith.NewSignResultRequest(result)
	if err != nil {
		log.Error("serialize SIGN result failed", "err", err)
		if ctx != nil {
			<-ctx.Done()
		}
		return
	}
	if ctx == nil || ctx.Err() != nil {
		timeoutCtx, cancel := context.WithTimeout(context.Background(), postResultTimeout)
		defer cancel()
		postCtx = timeoutCtx
	}

	for {
		requestCtx, cancel := context.WithTimeout(postCtx, 400*time.Millisecond)
		err := client.PostSignResult(requestCtx, intentID, request)
		cancel()
		if err == nil || errors.Is(err, monolith.ErrTerminalConflict) {
			return
		}
		pollCtx, stopPoll := context.WithTimeout(postCtx, 400*time.Millisecond)
		observed, pollErr := client.GetMessages(pollCtx, intent.SessionID, 0)
		stopPoll()
		if pollErr == nil && observed.Session.Validate("SIGN", intent.SessionID, intent.ExpiresAt) == nil &&
			(intent.Session.StartedAt == nil || observed.Session.StartedAt != nil && observed.Session.ExecutionExpiresAt != nil &&
				observed.Session.StartedAt.Equal(*intent.Session.StartedAt) && intent.Session.ExecutionExpiresAt != nil &&
				observed.Session.ExecutionExpiresAt.Equal(*intent.Session.ExecutionExpiresAt)) {
			switch observed.Session.Status {
			case "COMPLETED", "FAILED", "TIMED_OUT":
				return
			}
		}
		log.Warn("post result unresolved", "intent_id", intentID, "status", result.Status, "error_code", result.ErrorCode, "err", err)
		if !waitDeliveryRetry(postCtx) {
			return
		}
	}
}
