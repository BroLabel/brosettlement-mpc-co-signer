package worker

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/contract/mpc2of3"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/monolith"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/sharestore"
	coretss "github.com/BroLabel/brosettlement-mpc-core/tss"
)

type stubClient struct {
	claimResult monolith.ClaimResult
	claimErr    error
	lastResult  monolith.IntentResult
}

func (s *stubClient) ClaimIntent(_ context.Context, _ string) (monolith.ClaimResult, error) {
	if s.claimResult.ExpiresAt.IsZero() {
		s.claimResult.ExpiresAt = time.Now().Add(time.Minute)
	}
	return s.claimResult, s.claimErr
}

func (s *stubClient) PostResult(_ context.Context, _ string, result monolith.IntentResult) error {
	s.lastResult = result
	return nil
}

func (s *stubClient) PostMessage(context.Context, string, monolith.OutboundFrame) error {
	return nil
}

func (s *stubClient) GetMessages(context.Context, string, uint64) ([]monolith.InboundMessage, error) {
	return nil, nil
}

func claimResultForIntent(intent monolith.Intent) monolith.ClaimResult {
	return monolith.ClaimResult{
		IntentID:  intent.IntentID,
		SessionID: intent.SessionID,
		Type:      intent.Type,
		Payload:   intent.Payload,
		Status:    "CLAIMED",
		ExpiresAt: time.Now().Add(time.Minute),
	}
}

type stubRunner struct{}

func (s *stubRunner) RunSignSession(context.Context, coretss.SignSessionRequest) error {
	return nil
}

type capturingRunner struct{}

func (s *capturingRunner) RunSignSession(context.Context, coretss.SignSessionRequest) error {
	return nil
}

type capturingDKGExecutor struct {
	intent monolith.Intent
	calls  int
	result DKGResult
	err    error
}

func (e *capturingDKGExecutor) Run(_ context.Context, intent monolith.Intent, _ coretss.Transport) (DKGResult, error) {
	e.calls++
	e.intent = intent
	return e.result, e.err
}

func TestRunSessionWithExecutorsRoutesDKGOnlyThroughCoordinator(t *testing.T) {
	intent := validDKGIntent()
	client := &stubClient{claimResult: claimResultForIntent(intent)}
	signRunner := &capturingRunner{}
	dkgExecutor := &capturingDKGExecutor{result: DKGResult{
		Primary: sharestore.ArtifactEvidence{
			PartyID:          coordinatorPrimaryParty,
			KeyID:            intent.Payload.KeyID,
			AccountPublicKey: []byte{0x02, 0x01},
			ChainCodeHash:    mpc2of3.ChainCodeHashFor(tMustDecodeHex(intent.Payload.ChainCode)),
		},
	}}
	sem := make(chan struct{}, 1)
	sem <- struct{}{}

	RunSessionWithExecutors(
		context.Background(),
		intent,
		client,
		signRunner,
		dkgExecutor,
		"co-signer",
		time.Millisecond,
		sem,
		nil,
		slog.Default(),
	)

	if dkgExecutor.calls != 1 {
		t.Fatalf("DKG coordinator calls = %d, want 1", dkgExecutor.calls)
	}
	if client.lastResult.Status != intentStatusCompleted || client.lastResult.DkgMaterial == nil {
		t.Fatalf("unexpected DKG result = %+v", client.lastResult)
	}
}

func TestRunSessionRejectsInvalidIntent(t *testing.T) {
	intent := monolith.Intent{
		IntentID:  "intent-1",
		SessionID: "session-1",
		Type:      "SIGN",
		Payload: monolith.IntentPayload{
			Parties:   []string{"party-1", "co-signer"},
			Threshold: 2,
		},
	}
	client := &stubClient{claimResult: claimResultForIntent(intent)}
	runner := &stubRunner{}

	sem := make(chan struct{}, 1)
	sem <- struct{}{}

	RunSessionWithExecutors(
		context.Background(),
		intent,
		client,
		runner,
		&capturingDKGExecutor{},
		"party-1",
		time.Millisecond,
		sem,
		make(chan struct{}, 1),
		slog.Default(),
	)

	if client.lastResult.ErrorCode != ErrorCodeInvalidIntent {
		t.Fatalf("error code = %q, want %q", client.lastResult.ErrorCode, ErrorCodeInvalidIntent)
	}
}

func TestBuildResultMapsShareNotFound(t *testing.T) {
	result := BuildResult(coretss.ErrShareNotFound, context.Background(), monolith.Intent{Type: "SIGN"})
	if result.ErrorCode != ErrorCodeShareNotFound {
		t.Fatalf("error code = %q, want %q", result.ErrorCode, ErrorCodeShareNotFound)
	}
}

func TestBuildResultMapsKnownProtocolErrors(t *testing.T) {
	result := BuildResult(errors.New("duplicate frame"), context.Background(), monolith.Intent{Type: "SIGN"})
	if result.ErrorCode != ErrorCodeProtocol {
		t.Fatalf("error code = %q, want %q", result.ErrorCode, ErrorCodeProtocol)
	}
}

func TestBuildResultMapsDerivationErrorsToInvalidIntent(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "chain code missing", err: coretss.ErrChainCodeMissing},
		{name: "chain code invalid", err: coretss.ErrChainCodeInvalid},
		{name: "derivation context required", err: coretss.ErrDerivationContextRequired},
		{name: "invalid derivation context", err: coretss.ErrInvalidDerivationContext},
		{name: "unsupported derivation scheme", err: coretss.ErrUnsupportedDerivationScheme},
		{name: "derived signing unsupported", err: coretss.ErrDerivedSigningUnsupported},
		{name: "derivation path invalid", err: coretss.ErrDerivationPathInvalid},
		{name: "derivation context mismatch", err: coretss.ErrDerivationContextMismatch},
		{name: "unsupported algorithm curve", err: coretss.ErrUnsupportedAlgorithmCurve},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wrappedErr := fmt.Errorf("wrapped core derivation error: %w", tt.err)
			result := BuildResult(wrappedErr, context.Background(), monolith.Intent{Type: "SIGN"})
			if result.ErrorCode != ErrorCodeInvalidIntent {
				t.Fatalf("error code = %q, want %q", result.ErrorCode, ErrorCodeInvalidIntent)
			}
		})
	}
}

func TestBuildResultUsesCanceledSessionContext(t *testing.T) {
	sessionCtx, cancel := context.WithCancel(context.Background())
	cancel()

	result := BuildResult(errors.New("transport closed"), sessionCtx, monolith.Intent{Type: "SIGN"})
	if result.ErrorCode != ErrorCodeWorkerShutdown {
		t.Fatalf("error code = %q, want %q", result.ErrorCode, ErrorCodeWorkerShutdown)
	}
}

func TestBuildResultUsesExpiredSessionContext(t *testing.T) {
	sessionCtx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	result := BuildResult(errors.New("transport closed"), sessionCtx, monolith.Intent{Type: "SIGN"})
	if result.ErrorCode != ErrorCodeSessionTimeout {
		t.Fatalf("error code = %q, want %q", result.ErrorCode, ErrorCodeSessionTimeout)
	}
}

func TestValidateIntentDKGContract(t *testing.T) {
	tests := []struct {
		name string
		edit func(*monolith.Intent)
	}{
		{name: "missing org id", edit: func(intent *monolith.Intent) { intent.Payload.OrgID = "" }},
		{name: "missing key id", edit: func(intent *monolith.Intent) { intent.Payload.KeyID = "" }},
		{name: "missing curve", edit: func(intent *monolith.Intent) { intent.Payload.Curve = "" }},
		{name: "non-empty chain", edit: func(intent *monolith.Intent) { intent.Payload.Chain = "ethereum" }},
		{name: "missing chain code", edit: func(intent *monolith.Intent) { intent.Payload.ChainCode = "" }},
		{name: "malformed chain code", edit: func(intent *monolith.Intent) { intent.Payload.ChainCode = "0x11" }},
		{name: "uppercase chain code", edit: func(intent *monolith.Intent) { intent.Payload.ChainCode = strings.Repeat("A", 64) }},
		{name: "non-hex chain code", edit: func(intent *monolith.Intent) { intent.Payload.ChainCode = strings.Repeat("g", 64) }},
		{name: "short chain code", edit: func(intent *monolith.Intent) { intent.Payload.ChainCode = strings.Repeat("1", 63) }},
		{name: "missing chain code hash", edit: func(intent *monolith.Intent) { intent.Payload.ChainCodeHash = "" }},
		{name: "mismatched chain code hash", edit: func(intent *monolith.Intent) { intent.Payload.ChainCodeHash = "wrong" }},
		{name: "missing derivation scheme", edit: func(intent *monolith.Intent) { intent.Payload.DerivationScheme = "" }},
		{name: "unsupported derivation scheme", edit: func(intent *monolith.Intent) { intent.Payload.DerivationScheme = "bip32_public" }},
		{name: "derivation context present", edit: func(intent *monolith.Intent) { intent.Payload.DerivationContext = &monolith.DerivationContext{} }},
		{name: "digest present", edit: func(intent *monolith.Intent) { intent.Payload.Digest = []byte{1} }},
		{name: "conflicting payload type", edit: func(intent *monolith.Intent) { intent.Payload.Type = "SIGN" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			intent := validDKGIntent()
			tt.edit(&intent)
			if err := validateIntent(intent, "co-signer"); err == nil {
				t.Fatal("validateIntent() error = nil, want error")
			}
		})
	}
}

func TestValidateIntentDKGAcceptsNormalizedPayloadTypeAndExpectedHash(t *testing.T) {
	intent := validDKGIntent()
	intent.Payload.Type = " dkg "
	intent.Payload.ChainCode = strings.Repeat("11", 32)
	intent.Payload.ChainCodeHash = "AtRJox-7JnyPNS6ZaKeePl_JXBu-qlAv1kVOveWkvtw"

	if err := validateIntent(intent, "co-signer"); err != nil {
		t.Fatalf("validateIntent() error = %v", err)
	}
}

func TestValidateIntentSignContract(t *testing.T) {
	tests := []struct {
		name string
		edit func(*monolith.Intent)
	}{
		{name: "missing org id", edit: func(intent *monolith.Intent) { intent.Payload.OrgID = "" }},
		{name: "missing key id", edit: func(intent *monolith.Intent) { intent.Payload.KeyID = "" }},
		{name: "missing wallet id", edit: func(intent *monolith.Intent) { intent.Payload.WalletID = "" }},
		{name: "missing profile id", edit: func(intent *monolith.Intent) { intent.Payload.ProfileID = "" }},
		{name: "zero profile version", edit: func(intent *monolith.Intent) { intent.Payload.ProfileVersion = 0 }},
		{name: "missing profile template id", edit: func(intent *monolith.Intent) { intent.Payload.ProfileTemplateID = "" }},
		{name: "missing digest type", edit: func(intent *monolith.Intent) { intent.Payload.DigestType = "" }},
		{name: "missing hash algorithm", edit: func(intent *monolith.Intent) { intent.Payload.HashAlgorithm = "" }},
		{name: "missing signing payload type", edit: func(intent *monolith.Intent) { intent.Payload.SigningPayloadType = "" }},
		{name: "missing derivation context hash", edit: func(intent *monolith.Intent) { intent.Payload.DerivationContextHash = "" }},
		{name: "missing party id", edit: func(intent *monolith.Intent) { intent.Payload.PartyID = "" }},
		{name: "missing chain", edit: func(intent *monolith.Intent) { intent.Payload.Chain = "" }},
		{name: "missing digest", edit: func(intent *monolith.Intent) { intent.Payload.Digest = nil }},
		{name: "missing derivation context", edit: func(intent *monolith.Intent) { intent.Payload.DerivationContext = nil }},
		{name: "invalid derivation context", edit: func(intent *monolith.Intent) { intent.Payload.DerivationContext.ProfileID = "" }},
		{name: "zero nested profile version", edit: func(intent *monolith.Intent) { intent.Payload.DerivationContext.ProfileVersion = 0 }},
		{name: "zero descriptor version", edit: func(intent *monolith.Intent) { intent.Payload.DerivationContext.DescriptorVersion = 0 }},
		{name: "zero key version", edit: func(intent *monolith.Intent) { intent.Payload.DerivationContext.KeyVersion = 0 }},
		{name: "chain conflict", edit: func(intent *monolith.Intent) { intent.Payload.Chain = "bitcoin" }},
		{name: "algorithm conflict", edit: func(intent *monolith.Intent) { intent.Payload.Algorithm = "EdDSA" }},
		{name: "curve conflict", edit: func(intent *monolith.Intent) { intent.Payload.Curve = "ed25519" }},
		{name: "profile id conflict", edit: func(intent *monolith.Intent) { intent.Payload.ProfileID = "profile-2" }},
		{name: "profile template conflict", edit: func(intent *monolith.Intent) { intent.Payload.ProfileTemplateID = "bitcoin-default" }},
		{name: "profile version conflict", edit: func(intent *monolith.Intent) { intent.Payload.ProfileVersion = 4 }},
		{name: "hash mismatch", edit: func(intent *monolith.Intent) { intent.Payload.DerivationContextHash = "wrong" }},
		{name: "chain code present", edit: func(intent *monolith.Intent) { intent.Payload.ChainCode = strings.Repeat("11", 32) }},
		{name: "derivation scheme present", edit: func(intent *monolith.Intent) {
			intent.Payload.DerivationScheme = coretss.DerivationSchemeBIP32Secp256k1
		}},
		{name: "conflicting payload type", edit: func(intent *monolith.Intent) { intent.Payload.Type = "DKG" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			intent := validSignIntent(t)
			tt.edit(&intent)
			if err := validateIntent(intent, "co-signer"); err == nil {
				t.Fatal("validateIntent() error = nil, want error")
			}
		})
	}
}

func TestValidateIntentSignAcceptsEmptyDKGFieldsAndNormalizedPayloadType(t *testing.T) {
	intent := validSignIntent(t)
	intent.Payload.Type = " sign "
	intent.Payload.ChainCode = ""
	intent.Payload.DerivationScheme = ""

	if err := validateIntent(intent, "co-signer"); err != nil {
		t.Fatalf("validateIntent() error = %v", err)
	}
}

func TestBuildSignRequestMapsHDPayload(t *testing.T) {
	intent := validSignIntent(t)
	req := buildSignRequest(intent, "co-signer", nil)

	if req.Session.OrgID != "org-1" ||
		req.Session.KeyID != "key-1" ||
		req.Session.Chain != "ethereum" ||
		!sameBytes(req.Digest, []byte{1, 2, 3}) {
		t.Fatalf("unexpected sign request = %+v", req)
	}
	if req.DerivationContext == nil {
		t.Fatal("DerivationContext is nil")
	}
	ctx := req.DerivationContext
	if ctx.ProfileID != "profile-1" ||
		ctx.ProfileTemplateID != "ethereum-default" ||
		ctx.Chain != "ethereum" ||
		ctx.Algorithm != "ecdsa" ||
		ctx.Curve != "secp256k1" ||
		ctx.Scheme != coretss.DerivationSchemeBIP32Secp256k1 ||
		ctx.PublicKeyFormat != coretss.PublicKeyFormatUncompressedHex ||
		ctx.FullPath != "m/44'/60'/0'/0/15" ||
		ctx.DerivedPublicKey != intent.Payload.DerivationContext.ExpectedPublicKey ||
		ctx.DescriptorVersion != 7 ||
		ctx.ProfileVersion != 3 ||
		ctx.KeyVersion != 1 {
		t.Fatalf("unexpected derivation context = %+v", ctx)
	}
}

func TestRunSessionPostsDkgMaterial(t *testing.T) {
	intent := validDKGIntent()
	client := &stubClient{claimResult: claimResultForIntent(intent)}
	runner := &capturingRunner{}
	dkgExecutor := &capturingDKGExecutor{result: successfulDKGResult(intent)}
	sem := make(chan struct{}, 1)
	sem <- struct{}{}

	RunSessionWithExecutors(context.Background(), intent, client, runner, dkgExecutor, "co-signer", time.Millisecond, sem, nil, slog.Default())

	result := client.lastResult
	if result.Status != intentStatusCompleted {
		t.Fatalf("status = %q, want %q", result.Status, intentStatusCompleted)
	}
	if result.DkgMaterial == nil {
		t.Fatal("DkgMaterial is nil")
	}
	material := result.DkgMaterial
	if material.PartyID != coordinatorPrimaryParty ||
		material.KeyID != "key-1" ||
		material.AccountPublicKey != "0201" ||
		material.ChainCodeHash != "AtRJox-7JnyPNS6ZaKeePl_JXBu-qlAv1kVOveWkvtw" ||
		!material.ChainCodePresent ||
		material.PublicKeyFormat != "compressed_sec1" ||
		material.DerivationScheme != coretss.DerivationSchemeBIP32Secp256k1 {
		t.Fatalf("unexpected DKG material = %+v", material)
	}
}

func TestRunSessionUsesClaimedPayloadForDkgExecution(t *testing.T) {
	claimedIntent := validDKGIntent()
	pendingIntent := claimedIntent
	pendingIntent.Payload.ChainCode = ""
	pendingIntent.Payload.ChainCodeHash = claimedIntent.Payload.ChainCodeHash
	client := &stubClient{
		claimResult: monolith.ClaimResult{
			IntentID:  claimedIntent.IntentID,
			SessionID: claimedIntent.SessionID,
			Type:      claimedIntent.Type,
			Payload:   claimedIntent.Payload,
			Status:    "CLAIMED",
			ExpiresAt: time.Now().Add(time.Minute),
		},
	}
	runner := &capturingRunner{}
	dkgExecutor := &capturingDKGExecutor{result: successfulDKGResult(claimedIntent)}
	sem := make(chan struct{}, 1)
	sem <- struct{}{}

	RunSessionWithExecutors(context.Background(), pendingIntent, client, runner, dkgExecutor, "co-signer", time.Millisecond, sem, nil, slog.Default())

	if client.lastResult.Status != intentStatusCompleted {
		t.Fatalf("status = %q, want %q result=%+v", client.lastResult.Status, intentStatusCompleted, client.lastResult)
	}
	if dkgExecutor.intent.Payload.ChainCode != claimedIntent.Payload.ChainCode {
		t.Fatalf("unexpected DKG derivation material = %q", dkgExecutor.intent.Payload.ChainCode)
	}
}

func TestRunSessionRejectsIncompleteClaimResponse(t *testing.T) {
	pendingIntent := validDKGIntent()
	client := &stubClient{
		claimResult: monolith.ClaimResult{
			IntentID:  pendingIntent.IntentID,
			SessionID: pendingIntent.SessionID,
			Type:      pendingIntent.Type,
			ExpiresAt: time.Now().Add(time.Minute),
		},
	}
	runner := &capturingRunner{}
	dkgExecutor := &capturingDKGExecutor{}
	sem := make(chan struct{}, 1)
	sem <- struct{}{}

	RunSessionWithExecutors(context.Background(), pendingIntent, client, runner, dkgExecutor, "co-signer", time.Millisecond, sem, nil, slog.Default())

	if client.lastResult.Status != intentStatusFailed ||
		client.lastResult.ErrorCode != ErrorCodeInvalidIntent ||
		client.lastResult.ErrorMessage == "" {
		t.Fatalf("unexpected result = %+v", client.lastResult)
	}
	if dkgExecutor.calls != 0 {
		t.Fatalf("DKG should not run for incomplete claim response, calls = %d", dkgExecutor.calls)
	}
}

func TestRunSessionPostsFailedDkgWithoutMaterial(t *testing.T) {
	intent := validDKGIntent()
	client := &stubClient{claimResult: claimResultForIntent(intent)}
	runner := &capturingRunner{}
	dkgExecutor := &capturingDKGExecutor{err: coretss.ErrChainCodeMissing}
	sem := make(chan struct{}, 1)
	sem <- struct{}{}

	RunSessionWithExecutors(context.Background(), intent, client, runner, dkgExecutor, "co-signer", time.Millisecond, sem, nil, slog.Default())

	result := client.lastResult
	if result.Status != intentStatusFailed ||
		result.ErrorCode != ErrorCodeInvalidIntent ||
		result.ErrorMessage == "" {
		t.Fatalf("unexpected failed result = %+v", result)
	}
	if result.DkgMaterial != nil {
		t.Fatalf("DkgMaterial = %+v, want nil", result.DkgMaterial)
	}
}

func successfulDKGResult(intent monolith.Intent) DKGResult {
	return DKGResult{
		Primary: sharestore.ArtifactEvidence{
			PartyID:          coordinatorPrimaryParty,
			KeyID:            intent.Payload.KeyID,
			AccountPublicKey: []byte{0x02, 0x01},
			ChainCodeHash:    mpc2of3.ChainCodeHashFor(tMustDecodeHex(intent.Payload.ChainCode)),
		},
	}
}

func validDKGIntent() monolith.Intent {
	chainCode := strings.Repeat("11", 32)
	return monolith.Intent{
		IntentID:  "intent-1",
		SessionID: "session-1",
		Type:      "DKG",
		Payload: monolith.IntentPayload{
			Type:             "DKG",
			OrgID:            "org-1",
			KeyID:            "key-1",
			Parties:          []string{"party-1", "co-signer"},
			Threshold:        2,
			Algorithm:        "ECDSA",
			Curve:            "secp256k1",
			ChainCode:        chainCode,
			ChainCodeHash:    chainCodeHash(tMustDecodeHex(chainCode)),
			DerivationScheme: coretss.DerivationSchemeBIP32Secp256k1,
		},
	}
}

func validSignIntent(t *testing.T) monolith.Intent {
	t.Helper()

	ctx := monolith.DerivationContext{
		ProfileID:         "profile-1",
		ProfileTemplateID: "ethereum-default",
		Chain:             "ethereum",
		Algorithm:         "ecdsa",
		Curve:             "secp256k1",
		Scheme:            coretss.DerivationSchemeBIP32Secp256k1,
		AccountPath:       "m/44'/60'/0'",
		ChildPath:         "/0/15",
		FullPath:          "m/44'/60'/0'/0/15",
		ExpectedPublicKey: "042f8bde4d1a07209355b4a7250a5c5128e88b84bddc619ab7cba8d569b240efe4d8ac222636e5e3d6d4dba9dda6c9c426f788271bab0d6840dca87d3aa6ac62d6",
		PublicKeyFormat:   coretss.PublicKeyFormatUncompressedHex,
		DescriptorVersion: 7,
		ProfileVersion:    3,
		KeyVersion:        1,
	}
	hash, err := coretss.DerivationContextHashV1(toCoreDerivationContext(ctx))
	if err != nil {
		t.Fatalf("DerivationContextHashV1() error = %v", err)
	}

	return monolith.Intent{
		IntentID:  "intent-1",
		SessionID: "session-1",
		Type:      "SIGN",
		Payload: monolith.IntentPayload{
			Type:                  "SIGN",
			OrgID:                 "org-1",
			WalletID:              "wallet-1",
			KeyID:                 "key-1",
			ProfileID:             "profile-1",
			ProfileVersion:        3,
			ProfileTemplateID:     "ethereum-default",
			Parties:               []string{"party-1", "co-signer"},
			Threshold:             2,
			Algorithm:             "ECDSA",
			Curve:                 "secp256k1",
			Chain:                 "ethereum",
			Digest:                []byte{1, 2, 3},
			DigestType:            "transaction_hash",
			HashAlgorithm:         "sha256",
			SigningPayloadType:    "ethereum_transaction",
			DerivationContextHash: hash,
			PartyID:               "co-signer",
			DerivationContext:     &ctx,
		},
	}
}

func chainCodeHash(chainCode []byte) string {
	sum := sha256.Sum256(chainCode)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func tMustDecodeHex(input string) []byte {
	out, err := hex.DecodeString(input)
	if err != nil {
		panic(err)
	}
	return out
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sameBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
