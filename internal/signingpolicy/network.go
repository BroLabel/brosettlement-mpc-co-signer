package signingpolicy

import (
	"context"
	"errors"
	"time"
)

var ErrNetworkUnavailable = errors.New("trusted network evidence unavailable")
var ErrNetworkMismatch = errors.New("trusted network evidence mismatch")

// NetworkEvidence verifies membership of the exact validated bytes using an
// operator-configured provider. The implementation owns its chain's RPC protocol.
type NetworkEvidence interface {
	Verify(context.Context, string, []byte) error
}

type networkRequirement struct {
	chain      string
	raw        []byte
	validUntil time.Time
}

// VerifyNetwork runs only after the caller has checked identity and any existing
// execution binding. A provider read must not consume the validity margin chosen
// by the protocol validator. Missing evidence fails closed.
func (v *VerifiedPrepared) VerifyNetwork(ctx context.Context, provider NetworkEvidence, now func() time.Time) error {
	if v.network == nil {
		return nil
	}
	if provider == nil {
		return ErrNetworkUnavailable
	}
	if err := provider.Verify(ctx, v.network.chain, v.network.raw); err != nil {
		return err
	}
	if !now().Before(v.network.validUntil) {
		return ErrPrepared
	}
	return nil
}
