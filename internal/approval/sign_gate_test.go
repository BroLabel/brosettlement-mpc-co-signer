package approval

import (
	"context"
	"errors"
	"testing"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/monolith"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/signingpolicy"
)

func TestSignGateRequiresApprovalWithoutConfiguredOrganization(t *testing.T) {
	if err := (SignGate{}).Authorize(context.Background(), monolith.Intent{Version: 2}); !errors.Is(err, signingpolicy.ErrPrepared) {
		t.Fatalf("SIGN without approval: %v", err)
	}
}
