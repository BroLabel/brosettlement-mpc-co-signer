package worker

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/approval"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/monolith"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/signingpolicy"
	coretss "github.com/BroLabel/brosettlement-mpc-core/tss"
)

type ApprovalGate = approval.SignGate

type trustedTronFunc func(context.Context, string, *signingpolicy.Tapos) error

func (f trustedTronFunc) Verify(c context.Context, s string, p *signingpolicy.Tapos) error {
	return f(c, s, p)
}
func approvalIntent(t *testing.T, tron bool) (monolith.Intent, int64) {
	t.Helper()
	raw, e := signingpolicy.ReadPreparedCorpus("../../testdata/approval-prepared-v1/corpus.json.gz")
	if e != nil {
		t.Fatal(e)
	}
	var vectors []struct {
		Valid         bool                             `json:"valid"`
		Approval      *signingpolicy.ApprovedExecution `json:"approval"`
		Prepared      *signingpolicy.PreparedTransfer  `json:"prepared"`
		Policy        *signingpolicy.Context           `json:"policy"`
		Digest        []byte                           `json:"digest"`
		PayloadType   string                           `json:"payloadType"`
		HashAlgorithm string                           `json:"hashAlgorithm"`
		Now           int64                            `json:"now"`
	}
	if e = json.Unmarshal(raw, &vectors); e != nil {
		t.Fatal(e)
	}
	for _, v := range vectors {
		if !v.Valid || (v.PayloadType == "tron-transaction") != tron {
			continue
		}
		var a struct {
			OrgID                 string                     `json:"orgId"`
			WalletID              string                     `json:"walletId"`
			KeyID                 string                     `json:"keyId"`
			DerivationContextHash string                     `json:"derivationContextHash"`
			DerivationContext     monolith.DerivationContext `json:"derivationContext"`
		}
		if e = json.Unmarshal(v.Approval.Authorization, &a); e != nil {
			t.Fatal(e)
		}
		i := validSignIntent(t)
		i.Version = 2
		i.IntentID = v.Approval.OutgoingID
		i.Approval = v.Approval
		i.Prepared = v.Prepared
		i.Payload = monolith.IntentPayload{Type: "SIGN", OrgID: a.OrgID, WalletID: a.WalletID, KeyID: a.KeyID, ProfileID: a.DerivationContext.ProfileID, ProfileTemplateID: a.DerivationContext.ProfileTemplateID, ProfileVersion: a.DerivationContext.ProfileVersion, Parties: []string{platformPartyID, primaryPartyID}, PartyID: primaryPartyID, Threshold: 2, Algorithm: "ECDSA", Curve: "secp256k1", Chain: v.Policy.Chain, Digest: v.Digest, DigestType: "transaction", HashAlgorithm: v.HashAlgorithm, SigningPayloadType: v.PayloadType, DerivationContextHash: a.DerivationContextHash, DerivationContext: &a.DerivationContext, PolicyContext: v.Policy}
		return i, v.Now
	}
	t.Fatal("missing positive")
	return monolith.Intent{}, 0
}
func approvalJournal(t *testing.T, i monolith.Intent) *approval.Journal {
	j := approvalJournalAwaitingACK(t, i)
	acknowledgeApproval(t, j, i)
	return j
}
func acknowledgeApproval(t *testing.T, j *approval.Journal, i monolith.Intent) {
	t.Helper()
	ack, _ := json.Marshal(map[string]any{"version": 1, "requestId": i.Approval.RequestID, "outcome": "EXACT_REPLAY", "disposition": "APPLIED"})
	if e := j.Acknowledge(i.Payload.OrgID, i.Approval.RequestID, ack); e != nil {
		t.Fatal(e)
	}
}
func approvalJournalAwaitingACK(t *testing.T, i monolith.Intent) *approval.Journal {
	t.Helper()
	dir := t.TempDir()
	if e := os.Chmod(dir, 0700); e != nil {
		t.Fatal(e)
	}
	j, e := approval.OpenJournal(dir)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { j.Close() })
	endpoint := "https://synthetic.example/approval"
	if e = j.Admit(approval.Admission{Version: 1, OrgID: i.Payload.OrgID, RequestID: i.Approval.RequestID, AuthorizationHash: i.Approval.AuthorizationHash, Mode: "external", Environment: "synthetic-test", ConfigRevision: "synthetic-config-v1", Endpoint: &endpoint, AdmittedAt: "2026-10-19T10:00:00.000Z"}); e != nil {
		t.Fatal(e)
	}
	seed := make([]byte, 32)
	for k := range seed {
		seed[k] = 0x42
	}
	if e = j.SaveReceipt(approval.SavedReceipt{Version: 1, OrgID: i.Payload.OrgID, RequestID: i.Approval.RequestID, AuthorizationHash: i.Approval.AuthorizationHash, SigningKeyID: "synthetic-approval-ed25519", PublicKey: ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey), Bytes: i.Approval.DecisionReceipt}); e != nil {
		t.Fatal(e)
	}
	return j
}
func TestApprovalGateDurableBindingAndReplay(t *testing.T) {
	for _, tron := range []bool{false, true} {
		t.Run(map[bool]string{false: "ethereum", true: "tron"}[tron], func(t *testing.T) {
			i, now := approvalIntent(t, tron)
			j := approvalJournal(t, i)
			g := ApprovalGate{OrgID: i.Payload.OrgID, Journal: j, Now: func() time.Time { return time.UnixMilli(now) }, Tron: trustedTronFunc(func(context.Context, string, *signingpolicy.Tapos) error { return nil })}
			if e := g.Authorize(context.Background(), i); e != nil {
				t.Fatal(e)
			}
			state, e := j.Load(i.Payload.OrgID, i.Approval.RequestID)
			if e != nil || state.Execution == nil {
				t.Fatalf("not durable: %v", e)
			}
			if e = g.Authorize(context.Background(), i); e != nil {
				t.Fatal("same byte retry", e)
			}
			i.Approval.OutgoingID = "replacement"
			i.IntentID = "replacement"
			if e = g.Authorize(context.Background(), i); !errors.Is(e, approval.ErrConflict) {
				t.Fatalf("replacement accepted: %v", e)
			}
		})
	}
}
func TestApprovalGateRejectsSubstitutedOutgoingBeforeBinding(t *testing.T) {
	i, now := approvalIntent(t, false)
	j := approvalJournal(t, i)
	g := ApprovalGate{OrgID: i.Payload.OrgID, Journal: j, Now: func() time.Time { return time.UnixMilli(now) }}
	// A different intent cannot borrow an approved outgoing, even on first binding.
	i.IntentID = "substituted-intent"
	if e := g.Authorize(context.Background(), i); e == nil {
		t.Fatal("substituted outgoing accepted")
	}
	i.IntentID = i.Approval.OutgoingID
	if e := g.Authorize(context.Background(), i); e != nil {
		t.Fatal(e)
	}
}
func TestApprovalGateRejectsNullDowngradeAndMissingLocalEvidence(t *testing.T) {
	i, now := approvalIntent(t, false)
	j := approvalJournal(t, i)
	g := ApprovalGate{OrgID: i.Payload.OrgID, Journal: j, Now: func() time.Time { return time.UnixMilli(now) }}
	for _, test := range []string{"null", "one-null", "tenant", "hash", "receipt", "context", "v1"} {
		t.Run(test, func(t *testing.T) {
			v := i
			a := *i.Approval
			v.Approval = &a
			p := *i.Prepared
			v.Prepared = &p
			d := *i.Payload.DerivationContext
			v.Payload.DerivationContext = &d
			switch test {
			case "null":
				v.Approval = nil
				v.Prepared = nil
			case "one-null":
				v.Prepared = nil
			case "tenant":
				v.Payload.OrgID = "other"
			case "hash":
				v.Approval.AuthorizationHash = string(make([]byte, 64))
			case "receipt":
				v.Approval.DecisionReceipt = []byte(`{}`)
			case "context":
				v.Payload.DerivationContext.FullPath += "/1"
			case "v1":
				v.Version = 1
			}
			if e := g.Authorize(context.Background(), v); e == nil {
				t.Fatal("unauthorized accepted")
			}
		})
	}
	legacy := i
	legacy.Approval = nil
	legacy.Prepared = nil
	if e := (ApprovalGate{}).Authorize(context.Background(), legacy); e == nil {
		t.Fatal("unsigned legacy claim accepted without approval configuration")
	}
	if e := (ApprovalGate{}).Authorize(context.Background(), i); e == nil {
		t.Fatal("missing local journal accepted")
	}
	g.OrgID = ""
	if e := g.Authorize(context.Background(), i); e == nil {
		t.Fatal("valid evidence accepted without a configured organization")
	}
}
func TestApprovalRuntimeGateBeforeCoreAndOutageDeadline(t *testing.T) {
	for _, test := range []string{"valid", "hostile", "null", "outage", "missing-checker", "ack-delayed", "ack-timeout"} {
		t.Run(test, func(t *testing.T) {
			tron := test == "outage" || test == "missing-checker"
			i, now := approvalIntent(t, tron)
			var j *approval.Journal
			if test == "ack-delayed" || test == "ack-timeout" {
				j = approvalJournalAwaitingACK(t, i)
			} else {
				j = approvalJournal(t, i)
			}
			g := ApprovalGate{OrgID: i.Payload.OrgID, Journal: j, Now: func() time.Time { return time.UnixMilli(now) }}
			if test == "outage" {
				g.Tron = trustedTronFunc(func(context.Context, string, *signingpolicy.Tapos) error { return approval.ErrNetworkUnavailable })
			}
			if test == "hostile" {
				i.Prepared.NetworkFeeBudgetAtomic = "0"
			}
			if test == "null" {
				i.Approval = nil
				i.Prepared = nil
			}
			claim := claimResultForIntent(i)
			claim.Version = 2
			claim.Approval = i.Approval
			claim.Prepared = i.Prepared
			running := claim.Session
			start := time.Now()
			running.Status = "RUNNING"
			running.StartedAt = &start
			expiry := claim.Deadline
			running.ExecutionExpiresAt = &expiry
			var calls atomic.Int64
			client := &stubClient{claimResult: claim, poll: func(context.Context, string, uint64) (monolith.MessagesResult, error) {
				return monolith.MessagesResult{Session: running}, nil
			}}
			runner := signRunnerFunc(func(context.Context, coretss.SignSessionRequest) error {
				state, e := j.Load(i.Payload.OrgID, i.Approval.RequestID)
				if e != nil || state.Execution == nil {
					t.Error("Core before binding")
				}
				calls.Add(1)
				return nil
			})
			clock := realReadinessClock()
			clock.authorize = g.Authorize
			if test == "ack-delayed" {
				attempts := 0
				clock.authorize = func(ctx context.Context, intent monolith.Intent) error {
					attempts++
					err := g.Authorize(ctx, intent)
					if attempts == 1 {
						if err == nil || calls.Load() != 0 {
							t.Error("MPC admitted before durable ACK")
						}
						// The existing dispatcher recovers the exact saved receipt while SIGN waits.
						acknowledgeApproval(t, j, i)
					}
					return err
				}
			}
			timeout := 5 * time.Second
			if test == "outage" || test == "missing-checker" || test == "ack-timeout" {
				timeout = 100 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			permit := make(chan struct{}, 1)
			permit <- struct{}{}
			runSessionWithExecutorsForTest(ctx, i, client, runner, nil, nil, primaryPartyID, time.Millisecond, permit, nil, slog.Default(), clock)
			want := int64(0)
			if test == "valid" || test == "ack-delayed" {
				want = 1
			}
			if calls.Load() != want {
				t.Fatalf("Core calls=%d want=%d", calls.Load(), want)
			}
			if test == "outage" || test == "missing-checker" || test == "ack-timeout" {
				if client.lastResult.ErrorCode != ErrorCodeSessionTimeout {
					t.Fatalf("outage terminal code=%s", client.lastResult.ErrorCode)
				}
			}
			if test == "hostile" || test == "null" {
				if client.lastResult.ErrorCode != ErrorCodeInvalidIntent {
					t.Fatalf("hostile terminal code=%s", client.lastResult.ErrorCode)
				}
			}
		})
	}
}

type brokenExecutionJournal struct{ binds int }

func (*brokenExecutionJournal) Load(string, string) (approval.State, error) {
	return approval.State{}, approval.ErrIntegrity
}
func (j *brokenExecutionJournal) BindExecution(approval.ExecutionBinding) error {
	j.binds++
	return nil
}
func TestApprovalGateDoesNotRepairLostLocalHistory(t *testing.T) {
	i, now := approvalIntent(t, false)
	j := &brokenExecutionJournal{}
	g := ApprovalGate{OrgID: i.Payload.OrgID, Journal: j, Now: func() time.Time { return time.UnixMilli(now) }}
	if e := g.Authorize(context.Background(), i); !errors.Is(e, approval.ErrIntegrity) || j.binds != 0 {
		t.Fatalf("claim reconstructed missing history: binds=%d err=%v", j.binds, e)
	}
}
func TestApprovalExecutionRaceHasOneDurableOutgoing(t *testing.T) {
	i, now := approvalIntent(t, false)
	j := approvalJournal(t, i)
	g := ApprovalGate{OrgID: i.Payload.OrgID, Journal: j, Now: func() time.Time { return time.UnixMilli(now) }}
	other := i
	a := *i.Approval
	other.Approval = &a
	other.IntentID = "competing-outgoing"
	other.Approval.OutgoingID = other.IntentID
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, v := range []monolith.Intent{i, other} {
		go func(v monolith.Intent) { <-start; results <- g.Authorize(context.Background(), v) }(v)
	}
	close(start)
	aerr, berr := <-results, <-results
	if !((aerr == nil && errors.Is(berr, approval.ErrConflict)) || (berr == nil && errors.Is(aerr, approval.ErrConflict))) {
		t.Fatalf("race results: %v, %v", aerr, berr)
	}
	state, e := j.Load(i.Payload.OrgID, i.Approval.RequestID)
	if e != nil || state.Execution == nil {
		t.Fatal("missing durable winner", e)
	}
}

func TestApprovalTronFreshnessIsRecheckedAfterTrustedRead(t *testing.T) {
	i, now := approvalIntent(t, true)
	j := approvalJournal(t, i)
	g := ApprovalGate{OrgID: i.Payload.OrgID, Journal: j, Now: func() time.Time { return time.UnixMilli(now) }, Tron: trustedTronFunc(func(context.Context, string, *signingpolicy.Tapos) error { now += 86_340_000; return nil })}
	if e := g.Authorize(context.Background(), i); !errors.Is(e, signingpolicy.ErrPrepared) {
		t.Fatalf("expired during trusted read: %v", e)
	}
	state, e := j.Load(i.Payload.OrgID, i.Approval.RequestID)
	if e != nil || state.Execution != nil {
		t.Fatal("stale bytes bound", e)
	}
}
