package approval

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"time"

	wire "github.com/BroLabel/brosettlement-mpc-co-signer/internal/contract/approval"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/monolith"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/signingpolicy"
	"github.com/gowebpki/jcs"
)

type ExecutionJournal interface {
	Load(string, string) (State, error)
	BindExecution(ExecutionBinding) error
}

// SignGate reuses the application-owned journal and immutable retained keys.
// It never recreates missing admission, decision, ACK or execution artifacts.
type SignGate struct {
	Journal ExecutionJournal
	Now     func() time.Time
}

func (g SignGate) Authorize(ctx context.Context, i monolith.Intent) error {
	if i.Version != 2 {
		return signingpolicy.ErrPrepared
	}
	a, p := i.Approval, i.Prepared
	if a == nil || p == nil || a.OutgoingID != i.IntentID || g.Journal == nil {
		return signingpolicy.ErrPrepared
	}
	state, e := g.Journal.Load(i.Payload.OrgID, a.RequestID)
	if e != nil {
		return e
	}
	if state.Admission == nil || state.Receipt == nil || state.Admission.AuthorizationHash != a.AuthorizationHash {
		return ErrIntegrity
	}
	same, e := wire.SameReceipt(state.Receipt.Bytes, a.DecisionReceipt)
	if e != nil || !same {
		return ErrIntegrity
	}
	// A backend echo can only select already-saved APPLIED APPROVED evidence.
	receipt, e := wire.ParseReceipt(state.Receipt.Bytes)
	if e != nil {
		return e
	}
	var decision string
	_ = json.Unmarshal(receipt.Envelope["decision"], &decision)
	if decision != "APPROVED" {
		return ErrIntegrity
	}
	// Receipt recovery belongs to Dispatcher. A claim cannot supply the missing ACK.
	// Journal.Load already rejects missing ACKs when execution evidence exists.
	if len(state.Application) == 0 {
		return signingpolicy.ErrApprovalApplicationPending
	}
	application, e := wire.ParseDecisionApplication(state.Application)
	if e != nil {
		return e
	}
	var disposition string
	_ = json.Unmarshal(application["disposition"], &disposition)
	if disposition != "APPLIED" {
		return ErrIntegrity
	}
	now := g.Now
	if now == nil {
		now = time.Now
	}
	verified, e := signingpolicy.VerifyPrepared(a, p, i.Payload.PolicyContext, i.Payload.Digest, i.Payload.SigningPayloadType, i.Payload.HashAlgorithm, now())
	if e != nil {
		return e
	}
	auth := verified.Authorization
	for k, w := range map[string]string{"orgId": i.Payload.OrgID, "walletId": i.Payload.WalletID, "keyId": i.Payload.KeyID, "derivationContextHash": i.Payload.DerivationContextHash} {
		var got string
		_ = json.Unmarshal(auth[k], &got)
		if got != w {
			return signingpolicy.ErrPrepared
		}
	}
	contextBytes, _ := json.Marshal(i.Payload.DerivationContext)
	contextBytes, e = jcs.Transform(contextBytes)
	if e != nil {
		return signingpolicy.ErrPrepared
	}
	if !bytes.Equal(contextBytes, auth["derivationContext"]) {
		return signingpolicy.ErrPrepared
	}
	binding := ExecutionBinding{Version: 1, OrgID: i.Payload.OrgID, RequestID: a.RequestID, OutgoingID: a.OutgoingID, AuthorizationHash: a.AuthorizationHash, ExecutionDigest: base64.StdEncoding.EncodeToString(i.Payload.Digest), PayloadType: i.Payload.SigningPayloadType, HashAlgorithm: i.Payload.HashAlgorithm}
	// Reject a replacement before binding. BindExecution also compares
	// all fields under the journal lock and returns after both fsync barriers.
	if state.Execution != nil && *state.Execution != binding {
		return ErrConflict
	}
	if e = ctx.Err(); e != nil {
		return e
	}
	return g.Journal.BindExecution(binding)
}
