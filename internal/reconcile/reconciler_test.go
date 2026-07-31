package reconcile

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/contract/mpc2of3"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/monolith"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/sharestore"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/terminal"
)

const (
	testDeploymentID = "co-signer-deployment-1"
	completedFixture = "terminal-completed-request.json"
	failedFixture    = "terminal-failed-request.json"
)

func TestReconcilerClassifiesFullActionableArtifactMatrix(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	base := fixturePendingDKG(t)
	matchingPrimary, matchingRecovery := fixtureEvidencePair(t, base)
	mismatchedRecovery := matchingRecovery
	mismatchedRecovery.AccountPublicKey = append([]byte(nil), matchingRecovery.AccountPublicKey...)
	mismatchedRecovery.AccountPublicKey[len(mismatchedRecovery.AccountPublicKey)-1] ^= 1

	tests := []struct {
		name              string
		status            string
		primaryExists     bool
		recoveryExists    bool
		primaryEvidence   sharestore.ArtifactEvidence
		recoveryEvidence  sharestore.ArtifactEvidence
		wantDisposition   Disposition
		wantFixture       string
		wantClaims        int
		wantPrimaryReads  int
		wantRecoveryReads int
	}{
		{
			name:            "pending with zero artifacts remains eligible",
			status:          "PENDING",
			wantDisposition: DispositionEligible,
		},
		{
			name:            "pending with only primary claims then fails",
			status:          "PENDING",
			primaryExists:   true,
			wantDisposition: DispositionTerminalPublicationRequired,
			wantFixture:     failedFixture,
			wantClaims:      1,
		},
		{
			name:            "pending with only recovery claims then fails",
			status:          "PENDING",
			recoveryExists:  true,
			wantDisposition: DispositionTerminalPublicationRequired,
			wantFixture:     failedFixture,
			wantClaims:      1,
		},
		{
			name:              "pending with two matching artifacts claims then fails without content reads",
			status:            "PENDING",
			primaryExists:     true,
			recoveryExists:    true,
			primaryEvidence:   matchingPrimary,
			recoveryEvidence:  matchingRecovery,
			wantDisposition:   DispositionTerminalPublicationRequired,
			wantFixture:       failedFixture,
			wantClaims:        1,
			wantPrimaryReads:  0,
			wantRecoveryReads: 0,
		},
		{
			name:              "pending with two mismatched artifacts claims then fails without content reads",
			status:            "PENDING",
			primaryExists:     true,
			recoveryExists:    true,
			primaryEvidence:   matchingPrimary,
			recoveryEvidence:  mismatchedRecovery,
			wantDisposition:   DispositionTerminalPublicationRequired,
			wantFixture:       failedFixture,
			wantClaims:        1,
			wantPrimaryReads:  0,
			wantRecoveryReads: 0,
		},
		{
			name:            "own claimed with zero artifacts fails",
			status:          "CLAIMED",
			wantDisposition: DispositionTerminalPublicationRequired,
			wantFixture:     failedFixture,
		},
		{
			name:            "own claimed with only primary fails",
			status:          "CLAIMED",
			primaryExists:   true,
			wantDisposition: DispositionTerminalPublicationRequired,
			wantFixture:     failedFixture,
		},
		{
			name:            "own claimed with only recovery fails",
			status:          "CLAIMED",
			recoveryExists:  true,
			wantDisposition: DispositionTerminalPublicationRequired,
			wantFixture:     failedFixture,
		},
		{
			name:              "own claimed with matching pair completes",
			status:            "CLAIMED",
			primaryExists:     true,
			recoveryExists:    true,
			primaryEvidence:   matchingPrimary,
			recoveryEvidence:  matchingRecovery,
			wantDisposition:   DispositionTerminalPublicationRequired,
			wantFixture:       completedFixture,
			wantPrimaryReads:  1,
			wantRecoveryReads: 1,
		},
		{
			name:              "own claimed with mismatched pair fails",
			status:            "CLAIMED",
			primaryExists:     true,
			recoveryExists:    true,
			primaryEvidence:   matchingPrimary,
			recoveryEvidence:  mismatchedRecovery,
			wantDisposition:   DispositionTerminalPublicationRequired,
			wantFixture:       failedFixture,
			wantPrimaryReads:  1,
			wantRecoveryReads: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			unrelatedPath, before := createUnrelatedSnapshot(t)
			intent := base
			intent.Status = tt.status
			if tt.status == "CLAIMED" {
				intent.CoSignerDeploymentID = testDeploymentID
			}
			listing := monolith.ActionableListing{HTTPStatus: 200}
			if tt.status == "CLAIMED" {
				listing.OwnClaimedDKG = []monolith.ActionableIntent{intent}
			} else {
				listing.Pending = []monolith.ActionableIntent{intent}
			}

			events := []string{}
			backend := &recordingBackend{
				listing: listing,
				claim:   claimedResult(intent),
				events:  &events,
			}
			preflight := &recordingPreflight{events: &events}
			primary := &recordingStore{
				name:     "primary",
				exists:   tt.primaryExists,
				evidence: tt.primaryEvidence,
				events:   &events,
			}
			recovery := &recordingStore{
				name:     "recovery",
				exists:   tt.recoveryExists,
				evidence: tt.recoveryEvidence,
				events:   &events,
			}

			reconciler := mustReconciler(t, now, backend, preflight, primary, recovery)
			result, err := reconciler.Reconcile(context.Background())
			if err != nil {
				t.Fatalf("Reconcile() error = %v", err)
			}
			if result.Disposition != tt.wantDisposition {
				t.Fatalf("Disposition = %q, want %q", result.Disposition, tt.wantDisposition)
			}
			assertJobFixture(t, result.Job, tt.wantFixture)
			if backend.claimCalls != tt.wantClaims {
				t.Fatalf("claim calls = %d, want %d", backend.claimCalls, tt.wantClaims)
			}
			if primary.inspectCalls != tt.wantPrimaryReads {
				t.Fatalf("primary InspectExisting calls = %d, want %d", primary.inspectCalls, tt.wantPrimaryReads)
			}
			if recovery.inspectCalls != tt.wantRecoveryReads {
				t.Fatalf("recovery InspectExisting calls = %d, want %d", recovery.inspectCalls, tt.wantRecoveryReads)
			}
			assertOnlyExactActionableKey(t, primary.existsKeys, intent.KeyID)
			assertOnlyExactActionableKey(t, recovery.existsKeys, intent.KeyID)
			if result.DeadlineRaw != intent.DeadlineRaw {
				t.Fatalf("DeadlineRaw = %q, want exact %q", result.DeadlineRaw, intent.DeadlineRaw)
			}
			assertUnrelatedSnapshot(t, unrelatedPath, before)
			assertPreflightPrecedesArtifactAndClaim(t, events)
		})
	}
}

func TestReconcilerOwnClaimedRestartIsStableAcrossAbsoluteDeadline(t *testing.T) {
	base := fixturePendingDKG(t)
	primaryEvidence, recoveryEvidence := fixtureEvidencePair(t, base)
	deadline := base.Deadline
	var bodies [][]byte

	for _, now := range []time.Time{deadline.Add(-time.Nanosecond), deadline, deadline.Add(time.Nanosecond)} {
		intent := base
		intent.Status = "CLAIMED"
		intent.CoSignerDeploymentID = testDeploymentID
		backend := &recordingBackend{
			listing: monolith.ActionableListing{
				HTTPStatus:    200,
				OwnClaimedDKG: []monolith.ActionableIntent{intent},
			},
		}
		primary := &recordingStore{name: "primary", exists: true, evidence: primaryEvidence}
		recovery := &recordingStore{name: "recovery", exists: true, evidence: recoveryEvidence}
		result, err := mustReconciler(t, now, backend, &recordingPreflight{}, primary, recovery).
			Reconcile(context.Background())
		if err != nil {
			t.Fatalf("Reconcile(now=%s) error = %v", now, err)
		}
		if result.Job == nil {
			t.Fatalf("Reconcile(now=%s) Job = nil", now)
		}
		if result.DeadlineRaw != "2026-07-30T00:00:00.000Z" {
			t.Fatalf("DeadlineRaw = %q", result.DeadlineRaw)
		}
		bodies = append(bodies, result.Job.Body())
	}

	if !bytes.Equal(bodies[0], bodies[1]) || !bytes.Equal(bodies[0], bodies[2]) {
		t.Fatal("own CLAIMED reconstruction changed across the absolute deadline")
	}
}

func TestReconcilerPendingArtifactClaimLossOrExpiryChangesNothing(t *testing.T) {
	intent := fixturePendingDKG(t)
	now := intent.Deadline.Add(-time.Nanosecond)
	tests := []struct {
		name     string
		claimErr error
	}{
		{name: "claim conflict", claimErr: monolith.ErrAlreadyClaimed},
		{name: "expired or no longer actionable", claimErr: monolith.ErrNotFound},
		{name: "ambiguous claim outcome", claimErr: monolith.ErrClaimOutcomeUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			unrelatedPath, before := createUnrelatedSnapshot(t)
			backend := &recordingBackend{
				listing:  monolith.ActionableListing{HTTPStatus: 200, Pending: []monolith.ActionableIntent{intent}},
				claimErr: tt.claimErr,
			}
			primary := &recordingStore{name: "primary", exists: true}
			recovery := &recordingStore{name: "recovery"}
			result, err := mustReconciler(t, now, backend, &recordingPreflight{}, primary, recovery).
				Reconcile(context.Background())
			if err != nil {
				t.Fatalf("Reconcile() error = %v", err)
			}
			if result.Disposition != DispositionFreshPollRequired || result.Job != nil {
				t.Fatalf("result = %+v, want fresh poll without job", result)
			}
			if primary.inspectCalls != 0 || recovery.inspectCalls != 0 {
				t.Fatal("claim loss inspected artifact content")
			}
			assertUnrelatedSnapshot(t, unrelatedPath, before)
		})
	}
}

func TestReconcilerRefusesMoreThanOneStartupTerminalCandidate(t *testing.T) {
	first := fixturePendingDKG(t)
	second := secondPendingDKG(t, first)
	backend := &recordingBackend{
		listing: monolith.ActionableListing{
			HTTPStatus: 200,
			Pending:    []monolith.ActionableIntent{first, second},
		},
	}
	primary := &recordingStore{name: "primary", exists: true}
	recovery := &recordingStore{name: "recovery", exists: true}

	result, err := mustReconciler(
		t,
		first.Deadline.Add(-time.Second),
		backend,
		&recordingPreflight{},
		primary,
		recovery,
	).Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	var integrity *ProtocolIntegrityError
	if result.Disposition != DispositionProtocolIntegrity || !errors.As(result.Cause, &integrity) || result.Job != nil {
		t.Fatalf("result = %+v, want one-job fail-closed disposition", result)
	}
	if backend.claimCalls != 0 {
		t.Fatalf("claim calls = %d, want zero before impossible multi-job matrix", backend.claimCalls)
	}
	if !slices.Equal(primary.existsKeys, []string{first.KeyID, second.KeyID}) ||
		!slices.Equal(recovery.existsKeys, []string{first.KeyID, second.KeyID}) {
		t.Fatalf("exact-key lookups primary=%v recovery=%v", primary.existsKeys, recovery.existsKeys)
	}
}

func TestReconcilerRejectsClaimThatChangesAbsoluteDeadline(t *testing.T) {
	intent := fixturePendingDKG(t)
	claim := claimedResult(intent)
	claim.DeadlineRaw = "2026-07-30T00:00:00Z"
	backend := &recordingBackend{
		listing: monolith.ActionableListing{HTTPStatus: 200, Pending: []monolith.ActionableIntent{intent}},
		claim:   claim,
	}
	primary := &recordingStore{name: "primary", exists: true}
	recovery := &recordingStore{name: "recovery"}

	result, err := mustReconciler(t, intent.Deadline.Add(-time.Second), backend, &recordingPreflight{}, primary, recovery).
		Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	var integrity *ProtocolIntegrityError
	if result.Disposition != DispositionProtocolIntegrity || !errors.As(result.Cause, &integrity) || result.Job != nil {
		t.Fatalf("result = %+v, want protocol integrity without job", result)
	}
}

func TestReconcilerDefersBeforeAnyArtifactAccessOrClaimWhenCapabilityUnavailable(t *testing.T) {
	intent := fixturePendingDKG(t)
	events := []string{}
	backend := &recordingBackend{
		listing: monolith.ActionableListing{HTTPStatus: 200, Pending: []monolith.ActionableIntent{intent}},
		events:  &events,
	}
	preflight := &recordingPreflight{err: errors.New("recovery inspection unavailable"), events: &events}
	primary := &recordingStore{name: "primary", exists: true, events: &events}
	recovery := &recordingStore{name: "recovery", exists: true, events: &events}

	result, err := mustReconciler(t, intent.Deadline.Add(-time.Second), backend, preflight, primary, recovery).
		Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	var deferred *CapabilityDeferredError
	if result.Disposition != DispositionCapabilityDeferred || !errors.As(result.Cause, &deferred) || result.Job != nil {
		t.Fatalf("result = %+v, want typed capability-deferred without job", result)
	}
	if backend.claimCalls != 0 || len(primary.existsKeys) != 0 || len(recovery.existsKeys) != 0 ||
		primary.inspectCalls != 0 || recovery.inspectCalls != 0 {
		t.Fatal("capability-deferred reconciliation touched an actionable artifact or claim")
	}
	if !slices.Equal(events, []string{"list", "preflight"}) {
		t.Fatalf("events = %v, want list then preflight only", events)
	}
}

func TestReconcilerDefersOwnClaimedExistenceFailuresWithoutRetiringKey(t *testing.T) {
	intent := fixturePendingDKG(t)
	intent.Status = "CLAIMED"
	intent.CoSignerDeploymentID = testDeploymentID
	existsErr := errors.New("exact-path existence unavailable")
	tests := []struct {
		name              string
		primaryExists     bool
		primaryExistsErr  error
		recoveryExistsErr error
		wantEvents        []string
	}{
		{
			name:             "primary existence failure",
			primaryExistsErr: existsErr,
			wantEvents:       []string{"list", "preflight", "primary.exists"},
		},
		{
			name:              "recovery existence failure",
			primaryExists:     true,
			recoveryExistsErr: existsErr,
			wantEvents:        []string{"list", "preflight", "primary.exists", "recovery.exists"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events := []string{}
			backend := &recordingBackend{
				listing: monolith.ActionableListing{
					HTTPStatus:    200,
					OwnClaimedDKG: []monolith.ActionableIntent{intent},
				},
				events: &events,
			}
			primary := &recordingStore{
				name:      "primary",
				exists:    tt.primaryExists,
				existsErr: tt.primaryExistsErr,
				events:    &events,
			}
			recovery := &recordingStore{
				name:      "recovery",
				existsErr: tt.recoveryExistsErr,
				events:    &events,
			}

			result, err := mustReconciler(
				t,
				intent.Deadline.Add(-time.Second),
				backend,
				&recordingPreflight{events: &events},
				primary,
				recovery,
			).Reconcile(context.Background())
			if err != nil {
				t.Fatalf("Reconcile() error = %v", err)
			}
			var deferred *CapabilityDeferredError
			if result.Disposition != DispositionCapabilityDeferred ||
				!errors.As(result.Cause, &deferred) ||
				!errors.Is(result.Cause, existsErr) {
				t.Fatalf("result = %+v, want typed capability-deferred", result)
			}
			if result.DeadlineRaw != intent.DeadlineRaw {
				t.Fatalf("DeadlineRaw = %q, want exact %q", result.DeadlineRaw, intent.DeadlineRaw)
			}
			if result.Job != nil {
				t.Fatalf("Job = %q, want nil", result.Job.Body())
			}
			if backend.claimCalls != 0 || primary.inspectCalls != 0 || recovery.inspectCalls != 0 {
				t.Fatalf(
					"claim/inspection calls = %d/%d/%d, want zero",
					backend.claimCalls,
					primary.inspectCalls,
					recovery.inspectCalls,
				)
			}
			if !slices.Equal(events, tt.wantEvents) {
				t.Fatalf("events = %v, want %v", events, tt.wantEvents)
			}
		})
	}
}

func TestReconcilerReturnsAbortReadyProtocolIntegrityWithoutArtifactAccess(t *testing.T) {
	base := fixturePendingDKG(t)
	claimed := base
	claimed.Status = "CLAIMED"
	claimed.CoSignerDeploymentID = testDeploymentID
	foreign := claimed
	foreign.CoSignerDeploymentID = "foreign-deployment"

	tests := []struct {
		name    string
		listing monolith.ActionableListing
	}{
		{
			name:    "foreign claimed DKG",
			listing: monolith.ActionableListing{HTTPStatus: 200, OwnClaimedDKG: []monolith.ActionableIntent{foreign}},
		},
		{
			name: "multiple own claimed DKG",
			listing: monolith.ActionableListing{
				HTTPStatus:    200,
				OwnClaimedDKG: []monolith.ActionableIntent{claimed, claimed},
			},
		},
		{
			name: "terminal status in pending",
			listing: monolith.ActionableListing{
				HTTPStatus: 200,
				Pending:    []monolith.ActionableIntent{withStatus(base, "FAILED")},
			},
		},
		{
			name: "terminal status in own claimed",
			listing: monolith.ActionableListing{
				HTTPStatus:    200,
				OwnClaimedDKG: []monolith.ActionableIntent{withStatus(claimed, "COMPLETED")},
			},
		},
		{
			name: "pending collection contains CLAIMED",
			listing: monolith.ActionableListing{
				HTTPStatus: 200,
				Pending:    []monolith.ActionableIntent{withStatus(base, "CLAIMED")},
			},
		},
		{
			name: "own claimed collection contains PENDING",
			listing: monolith.ActionableListing{
				HTTPStatus:    200,
				OwnClaimedDKG: []monolith.ActionableIntent{withStatus(claimed, "PENDING")},
			},
		},
		{
			name: "expired pending DKG",
			listing: monolith.ActionableListing{
				HTTPStatus: 200,
				Pending:    []monolith.ActionableIntent{base},
			},
		},
		{
			name: "own claimed collection contains SIGN",
			listing: monolith.ActionableListing{
				HTTPStatus:    200,
				OwnClaimedDKG: []monolith.ActionableIntent{withType(claimed, "SIGN")},
			},
		},
		{
			name: "terminal identity cannot produce canonical job",
			listing: monolith.ActionableListing{
				HTTPStatus:    200,
				OwnClaimedDKG: []monolith.ActionableIntent{withSessionID(claimed, strings.Repeat("s", 256))},
			},
		},
		{
			name: "parsed deadline differs from exact deadline bytes",
			listing: monolith.ActionableListing{
				HTTPStatus:    200,
				OwnClaimedDKG: []monolith.ActionableIntent{withDeadlineRaw(claimed, "2026-07-31T00:00:00.000Z")},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := base.Deadline.Add(-time.Second)
			if tt.name == "expired pending DKG" {
				now = base.Deadline
			}
			events := []string{}
			backend := &recordingBackend{listing: tt.listing, events: &events}
			primary := &recordingStore{name: "primary", exists: true, events: &events}
			recovery := &recordingStore{name: "recovery", exists: true, events: &events}
			result, err := mustReconciler(t, now, backend, &recordingPreflight{events: &events}, primary, recovery).
				Reconcile(context.Background())
			if err != nil {
				t.Fatalf("Reconcile() error = %v", err)
			}
			var integrity *ProtocolIntegrityError
			if result.Disposition != DispositionProtocolIntegrity || !errors.As(result.Cause, &integrity) || result.Job != nil {
				t.Fatalf("result = %+v, want abort-ready protocol integrity", result)
			}
			if backend.claimCalls != 0 || len(primary.existsKeys) != 0 || len(recovery.existsKeys) != 0 ||
				primary.inspectCalls != 0 || recovery.inspectCalls != 0 {
				t.Fatal("protocol-integrity result touched an artifact or claim")
			}
			if !slices.Equal(events, []string{"list"}) {
				t.Fatalf("events = %v, want listing validation only", events)
			}
		})
	}
}

func TestReconcilerIgnoresPendingSIGNArtifactInventory(t *testing.T) {
	sign := fixturePendingDKG(t)
	sign.Type = "SIGN"
	sign.SessionID = ""
	sign.Deadline = time.Time{}
	sign.DeadlineRaw = ""
	sign.DescriptorBytes = nil
	sign.DescriptorFingerprint = ""
	backend := &recordingBackend{
		listing: monolith.ActionableListing{HTTPStatus: 200, Pending: []monolith.ActionableIntent{sign}},
	}
	primary := &recordingStore{name: "primary", exists: true}
	recovery := &recordingStore{name: "recovery", exists: true}

	result, err := mustReconciler(t, time.Now(), backend, &recordingPreflight{}, primary, recovery).
		Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.Disposition != DispositionEligible || result.Job != nil {
		t.Fatalf("result = %+v", result)
	}
	if len(primary.existsKeys) != 0 || len(recovery.existsKeys) != 0 {
		t.Fatal("pending SIGN caused artifact inspection")
	}
}

func TestCleanupDecisionRequiresTypedAuthoritativeFailedOrTimedOutEvidence(t *testing.T) {
	failedFingerprint := mustTerminalFingerprint(t, "xx0XKjmRzBHaiRDVPNRz6qA07rRLru9u0PPoqd9GMSo")
	completedFingerprint := mustTerminalFingerprint(t, "ofDGx6fYlS706EETY7HPJYE1XqXCk2qwwdpkGpz5-JU")
	tests := []struct {
		name    string
		outcome terminal.Outcome
		want    CleanupDecision
	}{
		{
			name: "accepted failed",
			outcome: terminal.Outcome{
				Kind:                     terminal.OutcomeAccepted,
				AuthoritativeStatus:      mpc2of3.TerminalStatusFailed,
				AuthoritativeFingerprint: failedFingerprint,
			},
			want: CleanupAddressedArtifacts,
		},
		{
			name: "exact replay failed",
			outcome: terminal.Outcome{
				Kind:                     terminal.OutcomeExactReplay,
				AuthoritativeStatus:      mpc2of3.TerminalStatusFailed,
				AuthoritativeFingerprint: failedFingerprint,
			},
			want: CleanupAddressedArtifacts,
		},
		{
			name: "conflict authoritative failed",
			outcome: terminal.Outcome{
				Kind:                     terminal.OutcomeTerminalConflict,
				AuthoritativeStatus:      mpc2of3.TerminalStatusFailed,
				AuthoritativeFingerprint: failedFingerprint,
			},
			want: CleanupAddressedArtifacts,
		},
		{
			name: "conflict authoritative timed out",
			outcome: terminal.Outcome{
				Kind:                     terminal.OutcomeTerminalConflict,
				AuthoritativeStatus:      mpc2of3.TerminalStatusTimedOut,
				AuthoritativeFingerprint: failedFingerprint,
			},
			want: CleanupAddressedArtifacts,
		},
		{
			name: "accepted completed preserves",
			outcome: terminal.Outcome{
				Kind:                     terminal.OutcomeAccepted,
				AuthoritativeStatus:      mpc2of3.TerminalStatusCompleted,
				AuthoritativeFingerprint: completedFingerprint,
			},
			want: CleanupPreserve,
		},
		{
			name: "conflict authoritative completed preserves",
			outcome: terminal.Outcome{
				Kind:                     terminal.OutcomeTerminalConflict,
				AuthoritativeStatus:      mpc2of3.TerminalStatusCompleted,
				AuthoritativeFingerprint: completedFingerprint,
			},
			want: CleanupPreserve,
		},
		{
			name: "untyped zero outcome preserves",
			want: CleanupPreserve,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DecideCleanup(tt.outcome); got != tt.want {
				t.Fatalf("DecideCleanup() = %q, want %q", got, tt.want)
			}
		})
	}
}

type recordingBackend struct {
	listing    monolith.ActionableListing
	listErr    error
	claim      monolith.ClaimResult
	claimErr   error
	claimCalls int
	events     *[]string
}

func (b *recordingBackend) ListActionableIntents(context.Context) (monolith.ActionableListing, error) {
	appendEvent(b.events, "list")
	return b.listing, b.listErr
}

func (b *recordingBackend) ClaimIntent(_ context.Context, _ string) (monolith.ClaimResult, error) {
	b.claimCalls++
	appendEvent(b.events, "claim")
	return b.claim, b.claimErr
}

type recordingPreflight struct {
	err    error
	events *[]string
}

func (p *recordingPreflight) Check(context.Context) error {
	appendEvent(p.events, "preflight")
	return p.err
}

type recordingStore struct {
	name         string
	exists       bool
	existsErr    error
	evidence     sharestore.ArtifactEvidence
	inspectErr   error
	existsKeys   []string
	inspectCalls int
	events       *[]string
}

func (s *recordingStore) Exists(_ context.Context, keyID string) (bool, error) {
	s.existsKeys = append(s.existsKeys, keyID)
	appendEvent(s.events, s.name+".exists")
	return s.exists, s.existsErr
}

func (s *recordingStore) InspectExisting(_ context.Context, expected sharestore.ExpectedArtifactContext) (sharestore.ArtifactEvidence, error) {
	s.inspectCalls++
	appendEvent(s.events, s.name+".inspect")
	if s.evidence.KeyID != "" &&
		(s.evidence.KeyID != expected.KeyID || s.evidence.SessionID != expected.SessionID) {
		return sharestore.ArtifactEvidence{}, errors.New("test evidence does not match expected context")
	}
	return s.evidence, s.inspectErr
}

func mustReconciler(
	t *testing.T,
	now time.Time,
	backend Backend,
	preflight InspectionPreflight,
	primary ArtifactStore,
	recovery ArtifactStore,
) *Reconciler {
	t.Helper()
	reconciler, err := New(Config{
		CoSignerDeploymentID: testDeploymentID,
		Now:                  func() time.Time { return now },
	}, backend, preflight, primary, recovery)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return reconciler
}

func fixturePendingDKG(t *testing.T) monolith.ActionableIntent {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/mpc-co-signer-http/v1/listing-response.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Pending []struct {
			CreatedAt             string `json:"createdAt"`
			Deadline              string `json:"deadline"`
			DescriptorBytes       string `json:"descriptorBytesBase64"`
			DescriptorFingerprint string `json:"descriptorFingerprint"`
			IntentID              string `json:"intentId"`
			KeyID                 string `json:"keyId"`
			OrgID                 string `json:"orgId"`
			SessionID             string `json:"sessionId"`
			Status                string `json:"status"`
			Type                  string `json:"type"`
		} `json:"pending"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	wire := fixture.Pending[0]
	descriptor, err := base64.StdEncoding.Strict().DecodeString(wire.DescriptorBytes)
	if err != nil {
		t.Fatal(err)
	}
	createdAt, err := time.Parse(time.RFC3339Nano, wire.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	deadline, err := time.Parse(time.RFC3339Nano, wire.Deadline)
	if err != nil {
		t.Fatal(err)
	}
	return monolith.ActionableIntent{
		CreatedAt:             createdAt,
		CreatedAtRaw:          wire.CreatedAt,
		Deadline:              deadline,
		DeadlineRaw:           wire.Deadline,
		DescriptorBytes:       descriptor,
		DescriptorFingerprint: wire.DescriptorFingerprint,
		IntentID:              wire.IntentID,
		KeyID:                 wire.KeyID,
		OrgID:                 wire.OrgID,
		SessionID:             wire.SessionID,
		Status:                wire.Status,
		Type:                  wire.Type,
	}
}

func claimedResult(intent monolith.ActionableIntent) monolith.ClaimResult {
	return monolith.ClaimResult{
		HTTPStatus:            200,
		IntentID:              intent.IntentID,
		SessionID:             intent.SessionID,
		Type:                  intent.Type,
		Status:                "CLAIMED",
		Deadline:              intent.Deadline,
		DeadlineRaw:           intent.DeadlineRaw,
		OrgID:                 intent.OrgID,
		KeyID:                 intent.KeyID,
		CoSignerDeploymentID:  testDeploymentID,
		DescriptorBytes:       append([]byte(nil), intent.DescriptorBytes...),
		DescriptorFingerprint: intent.DescriptorFingerprint,
	}
}

func fixtureEvidencePair(
	t *testing.T,
	intent monolith.ActionableIntent,
) (sharestore.ArtifactEvidence, sharestore.ArtifactEvidence) {
	t.Helper()
	descriptor, fingerprint, err := mpc2of3.ParseCanonicalDescriptor(intent.DescriptorBytes)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, err := hex.DecodeString("0279be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798")
	if err != nil {
		t.Fatal(err)
	}
	chainCodeHash, err := mpc2of3.ParseChainCodeHash(descriptor.ChainCodeHash)
	if err != nil {
		t.Fatal(err)
	}
	artifactFingerprint, err := mpc2of3.ParseArtifactFingerprint("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	if err != nil {
		t.Fatal(err)
	}
	common := sharestore.ArtifactEvidence{
		SessionID:             intent.SessionID,
		KeyID:                 intent.KeyID,
		DescriptorFingerprint: fingerprint,
		AccountPublicKey:      publicKey,
		ChainCodeHash:         chainCodeHash,
		CodecVersion:          2,
		ArtifactFingerprint:   artifactFingerprint,
	}
	primary := common
	primary.PartyID = "co-signer-primary"
	primary.Purpose = sharestore.StorePurposePrimary
	recovery := common
	recovery.AccountPublicKey = append([]byte(nil), publicKey...)
	recovery.PartyID = "co-signer-recovery"
	recovery.Purpose = sharestore.StorePurposeRecovery
	return primary, recovery
}

func secondPendingDKG(t *testing.T, first monolith.ActionableIntent) monolith.ActionableIntent {
	t.Helper()
	const secondKeyID = "mpc_key_123e4567-e89b-42d3-a456-426614174003"
	descriptorBytes := bytes.ReplaceAll(first.DescriptorBytes, []byte(first.KeyID), []byte(secondKeyID))
	_, fingerprint, err := mpc2of3.ParseCanonicalDescriptor(descriptorBytes)
	if err != nil {
		t.Fatal(err)
	}
	second := first
	second.CreatedAt = first.CreatedAt.Add(time.Second)
	second.CreatedAtRaw = second.CreatedAt.Format(time.RFC3339Nano)
	second.DescriptorBytes = descriptorBytes
	second.DescriptorFingerprint = fingerprint.String()
	second.IntentID = "intent-124"
	second.KeyID = secondKeyID
	second.SessionID = "dkg-124"
	return second
}

func assertJobFixture(t *testing.T, job *terminal.Job, fixture string) {
	t.Helper()
	if fixture == "" {
		if job != nil {
			t.Fatalf("Job = %q, want nil", job.Body())
		}
		return
	}
	if job == nil {
		t.Fatalf("Job = nil, want %s", fixture)
	}
	want, err := os.ReadFile(filepath.Join("../../testdata/mpc-co-signer-http/v1", fixture))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(job.Body(), want) {
		t.Fatalf("Job body = %s, want fixture %s", job.Body(), fixture)
	}
}

type fileSnapshot struct {
	bytes   []byte
	mode    os.FileMode
	modTime time.Time
}

func createUnrelatedSnapshot(t *testing.T) (string, fileSnapshot) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "unrelated.bin")
	if err := os.WriteFile(path, []byte("unrelated-artifact-canary"), 0o640); err != nil {
		t.Fatal(err)
	}
	modTime := time.Date(2024, 4, 5, 6, 7, 8, 0, time.UTC)
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatal(err)
	}
	return path, snapshotFile(t, path)
}

func snapshotFile(t *testing.T, path string) fileSnapshot {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fileSnapshot{bytes: raw, mode: info.Mode(), modTime: info.ModTime()}
}

func assertUnrelatedSnapshot(t *testing.T, path string, before fileSnapshot) {
	t.Helper()
	after := snapshotFile(t, path)
	if !bytes.Equal(after.bytes, before.bytes) || after.mode != before.mode || !after.modTime.Equal(before.modTime) {
		t.Fatalf("unrelated file changed: before=%+v after=%+v", before, after)
	}
}

func assertOnlyExactActionableKey(t *testing.T, got []string, want string) {
	t.Helper()
	for _, keyID := range got {
		if keyID != want {
			t.Fatalf("artifact path lookup used key %q outside actionable key %q", keyID, want)
		}
	}
}

func assertPreflightPrecedesArtifactAndClaim(t *testing.T, events []string) {
	t.Helper()
	preflight := slices.Index(events, "preflight")
	if preflight < 0 {
		t.Fatalf("events = %v, missing preflight", events)
	}
	for index, event := range events {
		if event == "claim" || event == "primary.exists" || event == "recovery.exists" ||
			event == "primary.inspect" || event == "recovery.inspect" {
			if index < preflight {
				t.Fatalf("events = %v, %s preceded capability preflight", events, event)
			}
		}
	}
}

func appendEvent(events *[]string, event string) {
	if events != nil {
		*events = append(*events, event)
	}
}

func withStatus(intent monolith.ActionableIntent, status string) monolith.ActionableIntent {
	intent.Status = status
	return intent
}

func withType(intent monolith.ActionableIntent, intentType string) monolith.ActionableIntent {
	intent.Type = intentType
	return intent
}

func withSessionID(intent monolith.ActionableIntent, sessionID string) monolith.ActionableIntent {
	intent.SessionID = sessionID
	return intent
}

func withDeadlineRaw(intent monolith.ActionableIntent, deadlineRaw string) monolith.ActionableIntent {
	intent.DeadlineRaw = deadlineRaw
	return intent
}

func mustTerminalFingerprint(t *testing.T, raw string) mpc2of3.TerminalResultFingerprint {
	t.Helper()
	fingerprint, err := mpc2of3.ParseTerminalResultFingerprint(raw)
	if err != nil {
		t.Fatal(err)
	}
	return fingerprint
}
