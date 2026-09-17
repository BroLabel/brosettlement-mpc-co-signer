package worker

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/monolith"
	coretss "github.com/BroLabel/brosettlement-mpc-core/tss"
)

const ethereumVectorSourceHash = "reBSo1hUdbEqcU3ZMMxt9xLB08ZIVHlyYR3Cp0ocmEI"

type ethereumVectorFixture struct {
	SourceHash string                 `json:"sourceHash"`
	Vectors    []ethereumIntentVector `json:"vectors"`
}

type ethereumIntentVector struct {
	ID                string `json:"id"`
	Chain             string `json:"chain"`
	Digest            string `json:"digest"`
	ContextHash       string `json:"contextHash"`
	ProfileID         string `json:"profileId"`
	ProfileTemplateID string `json:"profileTemplateId"`
	AccountPath       string `json:"accountPath"`
	ChildPath         string `json:"childPath"`
	FullPath          string `json:"fullPath"`
	DerivedPublicKey  string `json:"derivedPublicKey"`
}

func TestValidateIntentRejectsCrossProtocolSigningTuple(t *testing.T) {
	intent := validEthereumIntent(t, "ethereum:mainnet")
	intent.Payload.HashAlgorithm = "sha256"
	intent.Payload.SigningPayloadType = "tron-transaction"

	if err := validateIntent(intent, coordinatorPrimaryParty); err == nil {
		t.Fatal("validateIntent() accepted an Ethereum chain with a TRON signing tuple")
	}
}

func TestEthereumVectorsValidateAndForwardExactDigestAndContext(t *testing.T) {
	fixture := readEthereumVectorFixture(t)
	if fixture.SourceHash != ethereumVectorSourceHash {
		t.Fatalf("source hash = %q, want %q", fixture.SourceHash, ethereumVectorSourceHash)
	}
	if len(fixture.Vectors) != 2 {
		t.Fatalf("vector count = %d, want 2", len(fixture.Vectors))
	}

	for _, vector := range fixture.Vectors {
		t.Run(vector.ID, func(t *testing.T) {
			intent := ethereumIntentFromVector(t, vector)
			if err := validateIntent(intent, coordinatorPrimaryParty); err != nil {
				t.Fatalf("validateIntent() error = %v", err)
			}

			req := buildSignRequest(intent, coordinatorPrimaryParty, nil)
			if !bytes.Equal(req.Digest, intent.Payload.Digest) {
				t.Fatal("buildSignRequest() changed the transaction digest")
			}
			gotHash, err := coretss.DerivationContextHashV1(*req.DerivationContext)
			if err != nil || gotHash != vector.ContextHash {
				t.Fatalf("context hash = %s, error = %v", gotHash, err)
			}
		})
	}
}

func TestValidateIntentRejectsEthereumTupleMutations(t *testing.T) {
	vector := readEthereumVectorFixture(t).Vectors[0]
	tests := []struct {
		name string
		edit func(*monolith.Intent)
	}{
		{name: "wrong chain", edit: func(intent *monolith.Intent) {
			intent.Payload.Chain = "ethereum:unknown"
			intent.Payload.DerivationContext.Chain = "ethereum:unknown"
			refreshDerivationContextHash(t, intent)
		}},
		{name: "wrong digest type", edit: func(intent *monolith.Intent) { intent.Payload.DigestType = "digest" }},
		{name: "wrong hash algorithm", edit: func(intent *monolith.Intent) { intent.Payload.HashAlgorithm = "sha3-256" }},
		{name: "wrong signing payload type", edit: func(intent *monolith.Intent) { intent.Payload.SigningPayloadType = "tron-transaction" }},
		{name: "wrong address encoding", edit: func(intent *monolith.Intent) { intent.Payload.DerivationContext.AddressEncoding = "base58check" }},
		{name: "wrong algorithm", edit: func(intent *monolith.Intent) { intent.Payload.Algorithm = "eddsa" }},
		{name: "wrong curve", edit: func(intent *monolith.Intent) { intent.Payload.Curve = "ed25519" }},
		{name: "31 byte digest", edit: func(intent *monolith.Intent) { intent.Payload.Digest = intent.Payload.Digest[:31] }},
		{name: "33 byte digest", edit: func(intent *monolith.Intent) { intent.Payload.Digest = append(intent.Payload.Digest, 0) }},
		{name: "mutated derivation context", edit: func(intent *monolith.Intent) { intent.Payload.DerivationContext.FullPath = "m/44'/60'/0'/0/8" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			intent := ethereumIntentFromVector(t, vector)
			tt.edit(&intent)
			if err := validateIntent(intent, coordinatorPrimaryParty); err == nil {
				t.Fatal("validateIntent() error = nil, want rejection")
			}
		})
	}
}

func TestValidateClaimIdentityRejectsChangedSignTenantOrKey(t *testing.T) {
	for _, field := range []string{"tenant", "key"} {
		t.Run(field, func(t *testing.T) {
			intent := validEthereumIntent(t, "ethereum:mainnet")
			claimed := intent
			if field == "tenant" {
				claimed.Payload.OrgID = "other-org"
			} else {
				claimed.Payload.KeyID = "other-key"
			}
			claim := claimResultForIntent(claimed)
			if err := validateClaimIdentity(intent, claimed, claim, intentKindSIGN); err == nil {
				t.Fatal("validateClaimIdentity() error = nil, want rejection")
			}
		})
	}
}

func TestValidateIntentPreservesLegacyTronTuple(t *testing.T) {
	if err := validateIntent(validSignIntent(t), coordinatorPrimaryParty); err != nil {
		t.Fatalf("validateIntent() error = %v", err)
	}
}

func TestValidateIntentPreservesTronChainsAndEstablishedEncodings(t *testing.T) {
	for _, chain := range []string{"tron:mainnet", "tron:nile"} {
		for _, encoding := range []string{"tron_base58", "base58check", "tron_base58check"} {
			t.Run(chain+"/"+encoding, func(t *testing.T) {
				intent := validSignIntent(t)
				intent.Payload.Chain = chain
				intent.Payload.DerivationContext.Chain = chain
				intent.Payload.DerivationContext.AddressEncoding = encoding
				refreshDerivationContextHash(t, &intent)
				if err := validateIntent(intent, coordinatorPrimaryParty); err != nil {
					t.Fatalf("validateIntent() error = %v", err)
				}
			})
		}
	}
}

func TestInvalidTupleNeverStartsCore(t *testing.T) {
	intent := validEthereumIntent(t, "ethereum:mainnet")
	intent.ExpiresAt = time.Now().Add(time.Minute)
	intent.Payload.HashAlgorithm = "sha256"
	client := &stubClient{claimResult: claimResultForIntent(intent)}
	runner := &countingSignRunner{}
	sem := make(chan struct{}, 1)
	sem <- struct{}{}

	runSessionWithExecutorsForTest(
		context.Background(), intent, client, runner, &capturingDKGExecutor{}, nil,
		coordinatorPrimaryParty, time.Millisecond, sem, nil, slog.Default(),
	)

	if runner.calls != 0 {
		t.Fatalf("Core starts for rejected tuple = %d, want 0", runner.calls)
	}
	if client.lastResult.ErrorCode != ErrorCodeInvalidIntent {
		t.Fatalf("result = %+v, want invalid intent", client.lastResult)
	}
}

func validEthereumIntent(t *testing.T, chain string) monolith.Intent {
	t.Helper()

	intent := validSignIntent(t)
	intent.Payload.Chain = chain
	intent.Payload.Digest = make([]byte, 32)
	intent.Payload.DigestType = "transaction"
	intent.Payload.HashAlgorithm = "keccak256"
	intent.Payload.SigningPayloadType = "ethereum-transaction"
	intent.Payload.ProfileID = "ethereum-bip44-account-0"
	intent.Payload.ProfileTemplateID = "ethereum-bip44-account"
	intent.Payload.DerivationContext.ProfileID = intent.Payload.ProfileID
	intent.Payload.DerivationContext.ProfileTemplateID = intent.Payload.ProfileTemplateID
	intent.Payload.DerivationContext.Chain = chain
	intent.Payload.DerivationContext.AddressEncoding = "evm_hex"

	hash, err := coretss.DerivationContextHashV1(toCoreDerivationContext(*intent.Payload.DerivationContext))
	if err != nil {
		t.Fatalf("DerivationContextHashV1() error = %v", err)
	}
	intent.Payload.DerivationContextHash = hash
	return intent
}

func readEthereumVectorFixture(t *testing.T) ethereumVectorFixture {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/ethereum-wallet-v1/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture ethereumVectorFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func ethereumIntentFromVector(t *testing.T, vector ethereumIntentVector) monolith.Intent {
	t.Helper()
	digest, err := hex.DecodeString(vector.Digest)
	if err != nil {
		t.Fatal(err)
	}
	intent := validEthereumIntent(t, vector.Chain)
	intent.Payload.Digest = digest
	intent.Payload.ProfileID = vector.ProfileID
	intent.Payload.ProfileTemplateID = vector.ProfileTemplateID
	intent.Payload.DerivationContext.ProfileID = vector.ProfileID
	intent.Payload.DerivationContext.ProfileTemplateID = vector.ProfileTemplateID
	intent.Payload.DerivationContext.AccountPath = vector.AccountPath
	intent.Payload.DerivationContext.ChildPath = vector.ChildPath
	intent.Payload.DerivationContext.FullPath = vector.FullPath
	intent.Payload.DerivationContext.ExpectedPublicKey = vector.DerivedPublicKey
	intent.Payload.ProfileVersion = 1
	intent.Payload.DerivationContext.DescriptorVersion = 1
	intent.Payload.DerivationContext.ProfileVersion = 1
	intent.Payload.DerivationContext.KeyVersion = 1
	intent.Payload.DerivationContextHash = vector.ContextHash
	return intent
}

func refreshDerivationContextHash(t *testing.T, intent *monolith.Intent) {
	t.Helper()
	hash, err := coretss.DerivationContextHashV1(toCoreDerivationContext(*intent.Payload.DerivationContext))
	if err != nil {
		t.Fatalf("DerivationContextHashV1() error = %v", err)
	}
	intent.Payload.DerivationContextHash = hash
}
