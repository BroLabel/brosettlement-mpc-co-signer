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

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/monolith"
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

type stubRunner struct{}

func (s *stubRunner) RunDKGSession(context.Context, coretss.DKGSessionRequest) (coretss.DKGOutput, error) {
	return coretss.DKGOutput{
		KeyID:            "key-1",
		PublicKey:        "account-public-key",
		ChainCode:        strings.Repeat("11", 32),
		PublicKeyFormat:  coretss.PublicKeyFormatUncompressedHex,
		DerivationScheme: coretss.DerivationSchemeBIP32Secp256k1,
	}, nil
}

func (s *stubRunner) RunSignSession(context.Context, coretss.SignSessionRequest) error {
	return nil
}

type capturingRunner struct {
	dkgReq coretss.DKGSessionRequest
	dkgOut coretss.DKGOutput
	dkgErr error
}

func (s *capturingRunner) RunDKGSession(_ context.Context, req coretss.DKGSessionRequest) (coretss.DKGOutput, error) {
	s.dkgReq = req
	if s.dkgOut == (coretss.DKGOutput{}) {
		s.dkgOut = coretss.DKGOutput{
			KeyID:            "key-1",
			PublicKey:        "account-public-key",
			ChainCode:        strings.Repeat("11", 32),
			PublicKeyFormat:  coretss.PublicKeyFormatUncompressedHex,
			DerivationScheme: coretss.DerivationSchemeBIP32Secp256k1,
		}
	}
	return s.dkgOut, s.dkgErr
}

func (s *capturingRunner) RunSignSession(context.Context, coretss.SignSessionRequest) error {
	return nil
}

func TestRunSessionRejectsInvalidIntent(t *testing.T) {
	client := &stubClient{}
	runner := &stubRunner{}
	intent := monolith.Intent{
		IntentID:  "intent-1",
		SessionID: "session-1",
		Type:      "SIGN",
		Payload: monolith.IntentPayload{
			Parties:   []string{"party-1", "co-signer"},
			Threshold: 2,
		},
	}

	sem := make(chan struct{}, 1)
	sem <- struct{}{}

	RunSession(
		context.Background(),
		intent,
		client,
		runner,
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

func TestBuildDKGRequestMapsHDPayload(t *testing.T) {
	intent := validDKGIntent()
	req := buildDKGRequest(intent, "co-signer", nil)

	if req.Session.OrgID != "org-1" ||
		req.Session.KeyID != "key-1" ||
		!sameStrings(req.Session.Parties, []string{"party-1", "co-signer"}) ||
		req.Session.Threshold != 2 ||
		req.Session.Algorithm != "ECDSA" ||
		req.Session.Curve != "secp256k1" ||
		req.Session.Chain != "" {
		t.Fatalf("unexpected DKG session = %+v", req.Session)
	}
	if req.DerivationMaterial == nil {
		t.Fatal("DerivationMaterial is nil")
	}
	if req.DerivationMaterial.ChainCode != intent.Payload.ChainCode ||
		req.DerivationMaterial.DerivationScheme != coretss.DerivationSchemeBIP32Secp256k1 {
		t.Fatalf("unexpected derivation material = %+v", req.DerivationMaterial)
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
	client := &stubClient{}
	runner := &capturingRunner{}
	sem := make(chan struct{}, 1)
	sem <- struct{}{}

	RunSession(context.Background(), validDKGIntent(), client, runner, "co-signer", time.Millisecond, sem, nil, slog.Default())

	result := client.lastResult
	if result.Status != intentStatusCompleted {
		t.Fatalf("status = %q, want %q", result.Status, intentStatusCompleted)
	}
	if result.DkgMaterial == nil {
		t.Fatal("DkgMaterial is nil")
	}
	material := result.DkgMaterial
	if material.KeyID != "key-1" ||
		material.AccountPublicKey != "account-public-key" ||
		material.ChainCodeHash != "AtRJox-7JnyPNS6ZaKeePl_JXBu-qlAv1kVOveWkvtw" ||
		!material.ChainCodePresent ||
		material.PublicKeyFormat != coretss.PublicKeyFormatUncompressedHex ||
		material.DerivationScheme != coretss.DerivationSchemeBIP32Secp256k1 {
		t.Fatalf("unexpected DKG material = %+v", material)
	}
}

func TestRunSessionPostsFailedDkgWithoutMaterial(t *testing.T) {
	client := &stubClient{}
	runner := &capturingRunner{dkgErr: coretss.ErrChainCodeMissing}
	sem := make(chan struct{}, 1)
	sem <- struct{}{}

	RunSession(context.Background(), validDKGIntent(), client, runner, "co-signer", time.Millisecond, sem, nil, slog.Default())

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
