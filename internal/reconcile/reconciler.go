package reconcile

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/contract/mpc2of3"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/metrics"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/monolith"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/sharestore"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/terminal"
)

type Disposition string

const (
	DispositionEligible                    Disposition = "eligible"
	DispositionTerminalPublicationRequired Disposition = "terminal_publication_required"
	DispositionCapabilityDeferred          Disposition = "capability_deferred"
	DispositionFreshPollRequired           Disposition = "fresh_poll_required"
	DispositionProtocolIntegrity           Disposition = "protocol_integrity"
)

type CleanupDecision string

const (
	CleanupPreserve           CleanupDecision = "preserve"
	CleanupAddressedArtifacts CleanupDecision = "cleanup_addressed_artifacts"
)

type Result struct {
	Disposition Disposition
	Job         *terminal.Job
	DeadlineRaw string
	Cause       error
}

type ProtocolIntegrityError struct {
	Reason string
}

func (e *ProtocolIntegrityError) Error() string {
	return "actionable DKG protocol integrity failure: " + e.Reason
}

type CapabilityDeferredError struct {
	Cause error
}

func (e *CapabilityDeferredError) Error() string {
	return "actionable DKG reconciliation capability deferred"
}

func (e *CapabilityDeferredError) Unwrap() error {
	return e.Cause
}

type Backend interface {
	ListActionableIntents(context.Context) (monolith.ActionableListing, error)
	ClaimIntent(context.Context, string) (monolith.ClaimResult, error)
}

// InspectionPreflight validates the recovery-store and strict inspection
// capability before reconciliation addresses any artifact path or claims an
// intent.
type InspectionPreflight interface {
	Check(context.Context) error
}

// ArtifactStore deliberately exposes only exact key-addressed existence and
// strict evidence inspection. It cannot enumerate a directory.
type ArtifactStore interface {
	Exists(context.Context, string) (bool, error)
	InspectExisting(context.Context, sharestore.ExpectedArtifactContext) (sharestore.ArtifactEvidence, error)
}

type Config struct {
	CoSignerDeploymentID string
	Now                  func() time.Time
}

type Reconciler struct {
	config    Config
	backend   Backend
	preflight InspectionPreflight
	primary   ArtifactStore
	recovery  ArtifactStore
}

func New(
	config Config,
	backend Backend,
	preflight InspectionPreflight,
	primary ArtifactStore,
	recovery ArtifactStore,
) (*Reconciler, error) {
	if config.CoSignerDeploymentID == "" {
		return nil, errors.New("reconciler deployment ID is required")
	}
	if config.Now == nil {
		return nil, errors.New("reconciler clock is required")
	}
	if backend == nil || preflight == nil || primary == nil || recovery == nil {
		return nil, errors.New("reconciler dependencies are required")
	}
	return &Reconciler{
		config:    config,
		backend:   backend,
		preflight: preflight,
		primary:   primary,
		recovery:  recovery,
	}, nil
}

func (r *Reconciler) Reconcile(ctx context.Context) (result Result, returnErr error) {
	started := time.Now()
	defer func() {
		metrics.ObserveReconciliation(time.Since(started).Seconds(), returnErr != nil || result.Disposition == DispositionProtocolIntegrity)
	}()
	if ctx == nil {
		ctx = context.Background()
	}
	listing, err := r.backend.ListActionableIntents(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("list actionable intents: %w", err)
	}
	if result := r.validateListing(listing); result != nil {
		return *result, nil
	}
	if err := r.preflight.Check(ctx); err != nil {
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		deferred := &CapabilityDeferredError{Cause: err}
		return Result{
			Disposition: DispositionCapabilityDeferred,
			Cause:       deferred,
		}, nil
	}

	type classifiedIntent struct {
		intent         monolith.ActionableIntent
		primaryExists  bool
		recoveryExists bool
		claimed        bool
	}
	actionable := make([]classifiedIntent, 0, len(listing.OwnClaimedDKG)+len(listing.Pending))
	for _, intent := range listing.OwnClaimedDKG {
		actionable = append(actionable, classifiedIntent{intent: intent, claimed: true})
	}
	for _, intent := range listing.Pending {
		if intent.Type == "DKG" {
			actionable = append(actionable, classifiedIntent{intent: intent})
		}
	}

	terminalCandidates := 0
	for index := range actionable {
		item := &actionable[index]
		item.primaryExists, err = r.primary.Exists(ctx, item.intent.KeyID)
		if err != nil {
			return capabilityDeferred(err, item.intent.DeadlineRaw), nil
		}
		item.recoveryExists, err = r.recovery.Exists(ctx, item.intent.KeyID)
		if err != nil {
			return capabilityDeferred(err, item.intent.DeadlineRaw), nil
		}
		if item.claimed || item.primaryExists || item.recoveryExists {
			terminalCandidates++
		}
	}
	if terminalCandidates > 1 {
		return protocolIntegrity("listing requires more than one startup terminal publication"), nil
	}
	if terminalCandidates == 0 {
		deadlineRaw := ""
		for _, item := range actionable {
			if !item.claimed {
				deadlineRaw = item.intent.DeadlineRaw
				break
			}
		}
		return Result{Disposition: DispositionEligible, DeadlineRaw: deadlineRaw}, nil
	}

	for _, item := range actionable {
		if item.claimed {
			job := r.reconstructClaimed(ctx, item.intent, item.primaryExists, item.recoveryExists)
			return Result{
				Disposition: DispositionTerminalPublicationRequired,
				Job:         &job,
				DeadlineRaw: item.intent.DeadlineRaw,
			}, nil
		}
		if !item.primaryExists && !item.recoveryExists {
			continue
		}
		claim, err := r.backend.ClaimIntent(ctx, item.intent.IntentID)
		if err != nil {
			switch {
			case errors.Is(err, monolith.ErrAlreadyClaimed),
				errors.Is(err, monolith.ErrNotFound),
				errors.Is(err, monolith.ErrClaimOutcomeUnknown):
				return Result{
					Disposition: DispositionFreshPollRequired,
					DeadlineRaw: item.intent.DeadlineRaw,
					Cause:       err,
				}, nil
			default:
				return Result{}, fmt.Errorf("claim actionable DKG intent: %w", err)
			}
		}
		if reason := validateClaim(item.intent, claim, r.config.CoSignerDeploymentID); reason != "" {
			result := protocolIntegrity(reason)
			result.DeadlineRaw = item.intent.DeadlineRaw
			return result, nil
		}
		job, err := terminal.NewFailedJob(claim.IntentID, claim.SessionID, claim.KeyID)
		if err != nil {
			return Result{}, fmt.Errorf("construct canonical failed reconciliation job: %w", err)
		}
		return Result{
			Disposition: DispositionTerminalPublicationRequired,
			Job:         &job,
			DeadlineRaw: item.intent.DeadlineRaw,
		}, nil
	}

	return Result{Disposition: DispositionEligible}, nil
}

func (r *Reconciler) validateListing(listing monolith.ActionableListing) *Result {
	if listing.HTTPStatus != 200 {
		result := protocolIntegrity("listing HTTP status contract mismatch")
		return &result
	}
	if len(listing.OwnClaimedDKG) > 1 {
		result := protocolIntegrity("multiple deployment-owned CLAIMED DKG intents")
		return &result
	}

	now := r.config.Now()
	seen := make(map[string]struct{}, len(listing.OwnClaimedDKG)+len(listing.Pending))
	for _, intent := range listing.OwnClaimedDKG {
		if intent.Type != "DKG" || intent.Status != "CLAIMED" {
			result := protocolIntegrity("own claimed listing contains a non-CLAIMED DKG intent")
			return &result
		}
		if intent.CoSignerDeploymentID != r.config.CoSignerDeploymentID {
			result := protocolIntegrity("own claimed listing contains a foreign deployment claim")
			return &result
		}
		if reason := validateDKGIntent(intent); reason != "" {
			result := protocolIntegrity(reason)
			return &result
		}
		if _, exists := seen[intent.IntentID]; exists {
			result := protocolIntegrity("listing contains duplicate intent IDs")
			return &result
		}
		seen[intent.IntentID] = struct{}{}
	}
	for _, intent := range listing.Pending {
		if intent.Status != "PENDING" {
			result := protocolIntegrity("pending listing contains a non-PENDING intent")
			return &result
		}
		switch intent.Type {
		case "SIGN":
		case "DKG":
			if reason := validateDKGIntent(intent); reason != "" {
				result := protocolIntegrity(reason)
				return &result
			}
			if !intent.Deadline.After(now) {
				result := protocolIntegrity("pending DKG is expired")
				return &result
			}
		default:
			result := protocolIntegrity("pending listing contains an unsupported intent type")
			return &result
		}
		if _, exists := seen[intent.IntentID]; exists {
			result := protocolIntegrity("listing contains duplicate intent IDs")
			return &result
		}
		seen[intent.IntentID] = struct{}{}
	}
	if !ordered(listing.Pending) {
		result := protocolIntegrity("pending listing order is not deterministic")
		return &result
	}
	return nil
}

func validateDKGIntent(intent monolith.ActionableIntent) string {
	if intent.IntentID == "" || intent.SessionID == "" || intent.KeyID == "" ||
		intent.OrgID == "" || intent.Deadline.IsZero() || intent.DeadlineRaw == "" {
		return "DKG listing identity is incomplete"
	}
	if _, err := terminal.NewFailedJob(intent.IntentID, intent.SessionID, intent.KeyID); err != nil {
		return "DKG listing identity cannot produce a canonical terminal result"
	}
	exactDeadline, err := time.Parse(time.RFC3339Nano, intent.DeadlineRaw)
	if err != nil || !exactDeadline.Equal(intent.Deadline) {
		return "DKG listing deadline bytes do not match the parsed deadline"
	}
	descriptor, fingerprint, err := mpc2of3.ParseCanonicalDescriptor(intent.DescriptorBytes)
	if err != nil {
		return "DKG listing descriptor is invalid"
	}
	submittedFingerprint, err := mpc2of3.ParseDescriptorFingerprint(intent.DescriptorFingerprint)
	if err != nil || submittedFingerprint != fingerprint {
		return "DKG listing descriptor fingerprint mismatch"
	}
	if descriptor.KeyID != intent.KeyID {
		return "DKG listing key does not match descriptor"
	}
	return ""
}

func validateClaim(
	listed monolith.ActionableIntent,
	claim monolith.ClaimResult,
	deploymentID string,
) string {
	if claim.HTTPStatus != 200 || claim.Status != "CLAIMED" || claim.Type != "DKG" {
		return "claim response is not a typed CLAIMED DKG"
	}
	if claim.CoSignerDeploymentID != deploymentID ||
		claim.IntentID != listed.IntentID ||
		claim.SessionID != listed.SessionID ||
		claim.KeyID != listed.KeyID ||
		claim.OrgID != listed.OrgID {
		return "claim response identity mismatch"
	}
	if claim.DeadlineRaw != listed.DeadlineRaw || !claim.Deadline.Equal(listed.Deadline) {
		return "claim response changed the absolute deadline"
	}
	if claim.DescriptorFingerprint != listed.DescriptorFingerprint ||
		!bytes.Equal(claim.DescriptorBytes, listed.DescriptorBytes) {
		return "claim response changed the canonical descriptor"
	}
	return ""
}

func (r *Reconciler) reconstructClaimed(
	ctx context.Context,
	intent monolith.ActionableIntent,
	primaryExists bool,
	recoveryExists bool,
) terminal.Job {
	failed := func() terminal.Job {
		job, _ := terminal.NewFailedJob(intent.IntentID, intent.SessionID, intent.KeyID)
		return job
	}
	if !primaryExists || !recoveryExists {
		return failed()
	}
	expected := sharestore.ExpectedArtifactContext{
		SessionID:       intent.SessionID,
		KeyID:           intent.KeyID,
		DescriptorBytes: append([]byte(nil), intent.DescriptorBytes...),
	}
	primary, err := r.primary.InspectExisting(ctx, expected)
	if err != nil {
		return failed()
	}
	recovery, err := r.recovery.InspectExisting(ctx, expected)
	if err != nil || !evidenceMatches(intent, primary, recovery) {
		return failed()
	}
	job, err := terminal.NewCompletedJob(terminal.CompletedInput{
		IntentID:              intent.IntentID,
		SessionID:             intent.SessionID,
		KeyID:                 intent.KeyID,
		DescriptorFingerprint: primary.DescriptorFingerprint,
		AccountPublicKey:      primary.AccountPublicKey,
		ChainCodeHash:         primary.ChainCodeHash,
		Primary: terminal.ArtifactInput{
			PartyID:     primary.PartyID,
			Purpose:     string(primary.Purpose),
			Fingerprint: primary.ArtifactFingerprint,
		},
		Recovery: terminal.ArtifactInput{
			PartyID:     recovery.PartyID,
			Purpose:     string(recovery.Purpose),
			Fingerprint: recovery.ArtifactFingerprint,
		},
	})
	if err != nil {
		return failed()
	}
	return job
}

func evidenceMatches(
	intent monolith.ActionableIntent,
	primary sharestore.ArtifactEvidence,
	recovery sharestore.ArtifactEvidence,
) bool {
	expectedFingerprint, err := mpc2of3.ParseDescriptorFingerprint(intent.DescriptorFingerprint)
	if err != nil {
		return false
	}
	if primary.SessionID != intent.SessionID || recovery.SessionID != intent.SessionID ||
		primary.KeyID != intent.KeyID || recovery.KeyID != intent.KeyID ||
		primary.PartyID != "co-signer-primary" ||
		recovery.PartyID != "co-signer-recovery" ||
		primary.Purpose != sharestore.StorePurposePrimary ||
		recovery.Purpose != sharestore.StorePurposeRecovery ||
		primary.DescriptorFingerprint != expectedFingerprint ||
		recovery.DescriptorFingerprint != expectedFingerprint ||
		primary.ChainCodeHash != recovery.ChainCodeHash ||
		primary.CodecVersion != recovery.CodecVersion ||
		!bytes.Equal(primary.AccountPublicKey, recovery.AccountPublicKey) {
		return false
	}
	return true
}

func ordered(intents []monolith.ActionableIntent) bool {
	return slices.IsSortedFunc(intents, func(left, right monolith.ActionableIntent) int {
		if comparison := left.CreatedAt.Compare(right.CreatedAt); comparison != 0 {
			return comparison
		}
		switch {
		case left.IntentID < right.IntentID:
			return -1
		case left.IntentID > right.IntentID:
			return 1
		default:
			return 0
		}
	})
}

func capabilityDeferred(cause error, deadlineRaw string) Result {
	return Result{
		Disposition: DispositionCapabilityDeferred,
		DeadlineRaw: deadlineRaw,
		Cause:       &CapabilityDeferredError{Cause: cause},
	}
}

func protocolIntegrity(reason string) Result {
	return Result{
		Disposition: DispositionProtocolIntegrity,
		Cause:       &ProtocolIntegrityError{Reason: reason},
	}
}

// DecideCleanup exposes only the post-confirmation authorization boundary. The
// reconciler never executes cleanup and never mutates artifact bytes.
func DecideCleanup(outcome terminal.Outcome) CleanupDecision {
	if outcome.AuthoritativeFingerprint == (mpc2of3.TerminalResultFingerprint{}) {
		return CleanupPreserve
	}
	switch outcome.AuthoritativeStatus {
	case mpc2of3.TerminalStatusFailed:
		switch outcome.Kind {
		case terminal.OutcomeAccepted, terminal.OutcomeExactReplay, terminal.OutcomeTerminalConflict:
			return CleanupAddressedArtifacts
		}
	case mpc2of3.TerminalStatusTimedOut:
		if outcome.Kind == terminal.OutcomeTerminalConflict {
			return CleanupAddressedArtifacts
		}
	}
	return CleanupPreserve
}
