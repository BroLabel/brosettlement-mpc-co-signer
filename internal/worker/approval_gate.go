package worker

import (
	"context"
	"errors"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/monolith"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/signingpolicy"
	"log/slog"
	"time"
)

func authorizeSign(ctx context.Context, i monolith.Intent, gate func(context.Context, monolith.Intent) error, log *slog.Logger) error {
	if gate == nil {
		if i.Approval != nil || i.Prepared != nil {
			return signingpolicy.ErrPrepared
		}
		return nil
	}
	logged := false
	for {
		err := gate(ctx, i)
		if !errors.Is(err, signingpolicy.ErrNetworkUnavailable) && !errors.Is(err, signingpolicy.ErrApprovalApplicationPending) {
			return err
		}
		if !logged && log != nil {
			log.Warn("SIGN authorization evidence pending; owned SIGN suspended until bounded expiry", "intent_id", i.IntentID, "session_id", i.SessionID, "stage", "pre_core_authorization")
			logged = true
		}
		// Only this already-owned live SIGN attempt may retry identical evidence;
		// shutdown, operation deadline and execution TTL still end the attempt.
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
