package worker

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/contract/mpc2of3"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/localrouter"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/monolith"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/sharestore"
	coretss "github.com/BroLabel/brosettlement-mpc-core/tss"
)

var (
	ErrInvalidDKGContext   = errors.New("invalid dual-party dkg context")
	ErrDKGEvidenceMismatch = errors.New("post-runtime dkg evidence mismatch")
)

type PersistenceRunKey struct {
	SessionID    string
	LocalPartyID string
}

type DKGCoordinatorConfig struct {
	PlatformPartyID string
	PrimaryPartyID  string
	RecoveryPartyID string
}

type DKGResult struct {
	Primary  sharestore.ArtifactEvidence
	Recovery sharestore.ArtifactEvidence
}

type dkgSessionRunner interface {
	RunDKGSession(context.Context, coretss.DKGSessionRequest) (coretss.DKGOutput, error)
}

type artifactInspector interface {
	InspectExisting(context.Context, sharestore.ExpectedArtifactContext) (sharestore.ArtifactEvidence, error)
}

type DKGCoordinator struct {
	runner     dkgSessionRunner
	activePair *sharestore.ActivePair
	primary    artifactInspector
	recovery   artifactInspector
	config     DKGCoordinatorConfig
}

func NewDKGCoordinator(
	runner dkgSessionRunner,
	activePair *sharestore.ActivePair,
	primary artifactInspector,
	recovery artifactInspector,
	config DKGCoordinatorConfig,
) (*DKGCoordinator, error) {
	if runner == nil || activePair == nil || primary == nil || recovery == nil {
		return nil, errors.New("dkg coordinator requires one runner, an active pair, and both inspectors")
	}
	if config.PlatformPartyID == "" || config.PrimaryPartyID == "" || config.RecoveryPartyID == "" ||
		config.PlatformPartyID == config.PrimaryPartyID ||
		config.PlatformPartyID == config.RecoveryPartyID ||
		config.PrimaryPartyID == config.RecoveryPartyID {
		return nil, errors.New("dkg coordinator requires three distinct parties")
	}
	return &DKGCoordinator{
		runner:     runner,
		activePair: activePair,
		primary:    primary,
		recovery:   recovery,
		config:     config,
	}, nil
}

func (c *DKGCoordinator) Run(ctx context.Context, intent monolith.Intent, network coretss.Transport) (DKGResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if network == nil {
		return DKGResult{}, fmt.Errorf("%w: network transport is required", ErrInvalidDKGContext)
	}
	runtimeContext, err := c.validateRuntimeContext(intent)
	if err != nil {
		return DKGResult{}, err
	}

	router, err := localrouter.New(network, localrouter.Config{
		SessionID:       intent.SessionID,
		PlatformPartyID: c.config.PlatformPartyID,
		PrimaryPartyID:  c.config.PrimaryPartyID,
		RecoveryPartyID: c.config.RecoveryPartyID,
		Stage:           "dkg",
		Protocol:        runtimeContext.descriptor.Algorithm,
	})
	if err != nil {
		return DKGResult{}, fmt.Errorf("%w: create local router", ErrInvalidDKGContext)
	}
	router.Start(ctx)
	defer router.Close()

	primaryTransport, err := router.Transport(c.config.PrimaryPartyID)
	if err != nil {
		return DKGResult{}, err
	}
	recoveryTransport, err := router.Transport(c.config.RecoveryPartyID)
	if err != nil {
		return DKGResult{}, err
	}

	lease, err := c.activePair.RegisterPair(sharestore.PairRegistration{
		SessionID:       intent.SessionID,
		KeyID:           runtimeContext.descriptor.KeyID,
		PrimaryPartyID:  c.config.PrimaryPartyID,
		RecoveryPartyID: c.config.RecoveryPartyID,
		DescriptorBytes: runtimeContext.descriptorBytes,
	})
	if err != nil {
		return DKGResult{}, err
	}

	runErr := c.runParties(ctx, runtimeContext, router, primaryTransport, recoveryTransport)
	if finishErr := router.Finish(); finishErr != nil {
		runErr = finishErr
	}
	releaseErr := lease.Release()
	if runErr != nil || releaseErr != nil {
		return DKGResult{}, errors.Join(runErr, releaseErr)
	}

	expected := sharestore.ExpectedArtifactContext{
		SessionID:       intent.SessionID,
		KeyID:           runtimeContext.descriptor.KeyID,
		DescriptorBytes: append([]byte(nil), runtimeContext.descriptorBytes...),
	}
	primaryEvidence, err := c.primary.InspectExisting(ctx, expected)
	if err != nil {
		return DKGResult{}, fmt.Errorf("inspect primary dkg artifact: %w", err)
	}
	recoveryEvidence, err := c.recovery.InspectExisting(ctx, expected)
	if err != nil {
		return DKGResult{}, fmt.Errorf("inspect recovery dkg artifact: %w", err)
	}
	if err := c.compareEvidence(runtimeContext, primaryEvidence, recoveryEvidence); err != nil {
		return DKGResult{}, err
	}
	return DKGResult{Primary: primaryEvidence, Recovery: recoveryEvidence}, nil
}

type validatedDKGContext struct {
	intent          monolith.Intent
	descriptor      mpc2of3.KeyDescriptorV1
	fingerprint     mpc2of3.DescriptorFingerprint
	descriptorBytes []byte
	chainCode       string
}

func (c *DKGCoordinator) validateRuntimeContext(intent monolith.Intent) (validatedDKGContext, error) {
	if !strings.EqualFold(strings.TrimSpace(intent.Type), "DKG") ||
		strings.TrimSpace(intent.SessionID) == "" ||
		strings.TrimSpace(intent.Payload.OrgID) == "" ||
		len(intent.Payload.DescriptorBytes) == 0 {
		return validatedDKGContext{}, fmt.Errorf("%w: incomplete intent", ErrInvalidDKGContext)
	}
	descriptorBytes := append([]byte(nil), intent.Payload.DescriptorBytes...)
	descriptor, fingerprint, err := mpc2of3.ParseCanonicalDescriptor(descriptorBytes)
	if err != nil {
		return validatedDKGContext{}, fmt.Errorf("%w: descriptor", ErrInvalidDKGContext)
	}
	if descriptor.KeyID != intent.Payload.KeyID ||
		descriptor.Threshold != 2 ||
		len(descriptor.Parties) != 3 ||
		descriptor.Parties[0].PartyID != c.config.PlatformPartyID ||
		descriptor.Parties[1].PartyID != c.config.PrimaryPartyID ||
		descriptor.Parties[2].PartyID != c.config.RecoveryPartyID {
		return validatedDKGContext{}, fmt.Errorf("%w: descriptor binding", ErrInvalidDKGContext)
	}
	if intent.Payload.DescriptorFingerprint == "" || intent.Payload.DescriptorFingerprint != fingerprint.String() {
		return validatedDKGContext{}, fmt.Errorf("%w: descriptor fingerprint", ErrInvalidDKGContext)
	}
	chainCode, err := hex.DecodeString(intent.Payload.ChainCode)
	if err != nil || len(chainCode) != 32 || hex.EncodeToString(chainCode) != intent.Payload.ChainCode {
		clear(chainCode)
		return validatedDKGContext{}, fmt.Errorf("%w: chain code", ErrInvalidDKGContext)
	}
	defer clear(chainCode)
	if mpc2of3.ChainCodeHashFor(chainCode).String() != descriptor.ChainCodeHash {
		return validatedDKGContext{}, fmt.Errorf("%w: chain code hash", ErrInvalidDKGContext)
	}
	intent.Payload.DescriptorBytes = append([]byte(nil), descriptorBytes...)
	intent.Payload.Parties = append([]string(nil), intent.Payload.Parties...)
	return validatedDKGContext{
		intent:          intent,
		descriptor:      descriptor,
		fingerprint:     fingerprint,
		descriptorBytes: descriptorBytes,
		chainCode:       intent.Payload.ChainCode,
	}, nil
}

func (c *DKGCoordinator) runParties(
	ctx context.Context,
	runtimeContext validatedDKGContext,
	router *localrouter.Router,
	primaryTransport coretss.Transport,
	recoveryTransport coretss.Transport,
) error {
	primaryCtx, cancelPrimary := context.WithCancel(ctx)
	recoveryCtx, cancelRecovery := context.WithCancel(ctx)
	defer cancelPrimary()
	defer cancelRecovery()

	type partyRunResult struct {
		key PersistenceRunKey
		err error
	}
	results := make(chan partyRunResult, 2)
	var group sync.WaitGroup
	group.Add(2)
	run := func(
		runCtx context.Context,
		cancelSibling context.CancelFunc,
		partyID string,
		partyTransport coretss.Transport,
	) {
		defer group.Done()
		_, err := c.runner.RunDKGSession(
			runCtx,
			buildPartyDKGRequest(runtimeContext, partyID, partyTransport),
		)
		if err != nil {
			cancelSibling()
		}
		results <- partyRunResult{
			key: PersistenceRunKey{
				SessionID:    runtimeContext.intent.SessionID,
				LocalPartyID: partyID,
			},
			err: err,
		}
	}
	go run(primaryCtx, cancelRecovery, c.config.PrimaryPartyID, primaryTransport)
	go run(recoveryCtx, cancelPrimary, c.config.RecoveryPartyID, recoveryTransport)

	var failures []error
	var routerErr error
	routerDone := router.Done()
	for completed := 0; completed < 2; {
		select {
		case result := <-results:
			completed++
			if result.err != nil {
				failures = append(failures, fmt.Errorf("dkg party %s: %w", result.key.LocalPartyID, result.err))
			}
		case <-routerDone:
			routerErr = router.Err()
			cancelPrimary()
			cancelRecovery()
			routerDone = nil
		}
	}
	group.Wait()
	if terminalErr := router.Err(); terminalErr != nil {
		routerErr = terminalErr
	}
	if routerErr != nil {
		return routerErr
	}
	return preferredPartyFailure(failures)
}

func buildPartyDKGRequest(
	runtimeContext validatedDKGContext,
	localPartyID string,
	partyTransport coretss.Transport,
) coretss.DKGSessionRequest {
	partyIDs := make([]string, 0, len(runtimeContext.descriptor.Parties))
	for _, party := range runtimeContext.descriptor.Parties {
		partyIDs = append(partyIDs, party.PartyID)
	}
	fingerprint := [32]byte(runtimeContext.fingerprint)
	return coretss.DKGSessionRequest{
		Session: coretss.DKGSessionDescriptor{
			SessionID: runtimeContext.intent.SessionID,
			OrgID:     runtimeContext.intent.Payload.OrgID,
			KeyID:     runtimeContext.descriptor.KeyID,
			Parties:   partyIDs,
			Threshold: uint32(runtimeContext.descriptor.Threshold),
			Algorithm: runtimeContext.descriptor.Algorithm,
			Curve:     runtimeContext.descriptor.Curve,
		},
		LocalPartyID:                localPartyID,
		OpaqueDescriptorFingerprint: append([]byte(nil), fingerprint[:]...),
		DerivationMaterial: &coretss.DKGDerivationMaterial{
			ChainCode:        runtimeContext.chainCode,
			DerivationScheme: runtimeContext.descriptor.DerivationScheme,
		},
		Transport: partyTransport,
	}
}

func preferredPartyFailure(failures []error) error {
	for _, err := range failures {
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			return err
		}
	}
	return errors.Join(failures...)
}

func (c *DKGCoordinator) compareEvidence(
	runtimeContext validatedDKGContext,
	primary sharestore.ArtifactEvidence,
	recovery sharestore.ArtifactEvidence,
) error {
	commonMatches := primary.SessionID == runtimeContext.intent.SessionID &&
		recovery.SessionID == runtimeContext.intent.SessionID &&
		primary.KeyID == runtimeContext.descriptor.KeyID &&
		recovery.KeyID == runtimeContext.descriptor.KeyID &&
		primary.DescriptorFingerprint == runtimeContext.fingerprint &&
		recovery.DescriptorFingerprint == runtimeContext.fingerprint &&
		bytes.Equal(primary.AccountPublicKey, recovery.AccountPublicKey) &&
		primary.ChainCodeHash == recovery.ChainCodeHash &&
		primary.ChainCodeHash.String() == runtimeContext.descriptor.ChainCodeHash &&
		primary.CodecVersion == recovery.CodecVersion
	partyMatches := primary.PartyID == c.config.PrimaryPartyID &&
		primary.Purpose == sharestore.StorePurposePrimary &&
		recovery.PartyID == c.config.RecoveryPartyID &&
		recovery.Purpose == sharestore.StorePurposeRecovery
	if !commonMatches || !partyMatches {
		return ErrDKGEvidenceMismatch
	}
	return nil
}
