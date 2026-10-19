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
type TronNetworkEvidence interface {
	Verify(context.Context, string, *signingpolicy.Tapos) error
}

// SignGate reuses the application-owned journal and immutable retained keys.
// It never recreates missing admission, decision, ACK or execution artifacts.
type SignGate struct {
	RequiredOrgID string
	Journal       ExecutionJournal
	Tron          TronNetworkEvidence
	Now           func() time.Time
}

func (g SignGate) Authorize(ctx context.Context, i monolith.Intent) error {
	if i.Version != 2 {
		return signingpolicy.ErrPrepared
	}
	a, p := i.Approval, i.Prepared
	if a == nil && p == nil {
		if g.RequiredOrgID != "" {
			return signingpolicy.ErrPrepared
		}
		return nil
	}
	if a == nil || p == nil || a.OutgoingID != i.IntentID || g.Journal == nil || g.RequiredOrgID != "" && i.Payload.OrgID != g.RequiredOrgID {
		return signingpolicy.ErrPrepared
	}
	state, e := g.Journal.Load(i.Payload.OrgID, a.RequestID)
	if e != nil {
		return e
	}
	if state.Admission == nil || state.Receipt == nil || len(state.Application) == 0 || state.Admission.AuthorizationHash != a.AuthorizationHash {
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
	application, e := wire.ParseDecisionApplication(state.Application)
	if e != nil {
		return e
	}
	var decision, disposition string
	_ = json.Unmarshal(receipt.Envelope["decision"], &decision)
	_ = json.Unmarshal(application["disposition"], &disposition)
	if decision != "APPROVED" || disposition != "APPLIED" {
		return ErrIntegrity
	}
	canonical, e := jcs.Transform(a.Authorization)
	if e != nil {
		return signingpolicy.ErrPrepared
	}
	auth, e := wire.ParseAuthorization(canonical)
	if e != nil {
		return e
	}
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
	now := time.Now()
	if g.Now != nil {
		now = g.Now()
	}
	tapos, e := signingpolicy.VerifyPrepared(a, p, i.Payload.PolicyContext, i.Payload.Digest, i.Payload.SigningPayloadType, i.Payload.HashAlgorithm, now)
	if e != nil {
		return e
	}
	binding := ExecutionBinding{Version: 1, OrgID: i.Payload.OrgID, RequestID: a.RequestID, OutgoingID: a.OutgoingID, AuthorizationHash: a.AuthorizationHash, ExecutionDigest: base64.StdEncoding.EncodeToString(i.Payload.Digest), PayloadType: i.Payload.SigningPayloadType, HashAlgorithm: i.Payload.HashAlgorithm}
	// Reject a replacement before any provider read. BindExecution also compares
	// all fields under the journal lock and returns after both fsync barriers.
	if state.Execution != nil && *state.Execution != binding {
		return ErrConflict
	}
	if tapos != nil {
		if g.Tron == nil {
			return ErrNetworkUnavailable
		}
		if e = g.Tron.Verify(ctx, i.Payload.Chain, tapos); e != nil {
			return e
		}
		// A bounded RPC read may consume the remaining freshness margin. Check
		// the unchanged raw expiration again immediately before durable admission.
		checkedAt := time.Now()
		if g.Now != nil {
			checkedAt = g.Now()
		}
		if tapos.Expiration-checkedAt.UnixMilli() <= 60000 {
			return signingpolicy.ErrPrepared
		}
	}
	if e = ctx.Err(); e != nil {
		return e
	}
	return g.Journal.BindExecution(binding)
}
