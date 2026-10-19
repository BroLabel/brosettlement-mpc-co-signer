package monolith

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestClaimIntentDecodesClosedEthereumPolicyContexts(t *testing.T) {
	tests := []struct {
		name          string
		fixture       string
		chain         string
		chainID       string
		tokenStandard *string
	}{
		{name: "native ETH", fixture: "sign-claim-eth.json", chain: "ethereum:mainnet", chainID: "1"},
		{name: "ERC20", fixture: "sign-claim-erc20.json", chain: "ethereum:sepolia", chainID: "11155111", tokenStandard: stringPointer("erc20")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := os.ReadFile("../../testdata/ethereum-wallet-v1/" + tt.fixture)
			if err != nil {
				t.Fatal(err)
			}
			var identity struct {
				IntentID string `json:"intentId"`
			}
			if err := json.Unmarshal(raw, &identity); err != nil {
				t.Fatal(err)
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(signV2Fixture(raw)) }))
			defer srv.Close()
			client, _ := newTestClient(t, srv.URL)
			claim, err := client.ClaimIntent(context.Background(), "SIGN", identity.IntentID)
			if err != nil {
				t.Fatalf("ClaimIntent() error = %v", err)
			}
			policy := claim.Payload.PolicyContext
			if policy == nil || policy.Chain != tt.chain || policy.ChainID != tt.chainID || policy.Version != 1 || policy.TransactionType != 2 ||
				policy.Nonce == "" || policy.GasLimit == "" || policy.MaxFeePerGas == "" || policy.MaxPriorityFeePerGas == "" ||
				!reflect.DeepEqual(policy.TokenStandard, tt.tokenStandard) {
				t.Fatalf("unexpected Ethereum policy = %+v", policy)
			}
		})
	}
}

func TestClaimIntentRejectsUnknownOrMixedEthereumPolicyFields(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/ethereum-wallet-v1/sign-claim-eth.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture map[string]any
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	policy := fixture["payload"].(map[string]any)["policyContext"].(map[string]any)
	policy["feeLimitSun"] = nil
	mutated, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(mutated) }))
	defer srv.Close()
	client, _ := newTestClient(t, srv.URL)
	if _, err := client.ClaimIntent(context.Background(), "SIGN", fixture["intentId"].(string)); err == nil {
		t.Fatal("ClaimIntent() accepted a mixed TRON/Ethereum policy context")
	}
}

func TestClaimIntentRejectsInvalidEthereumPolicyContexts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "missing field", mutate: func(policy map[string]any) { delete(policy, "nonce") }},
		{name: "unknown field", mutate: func(policy map[string]any) { policy["unexpected"] = true }},
		{name: "wrong version", mutate: func(policy map[string]any) { policy["version"] = float64(2) }},
		{name: "wrong transaction type", mutate: func(policy map[string]any) { policy["transactionType"] = float64(1) }},
		{name: "wrong network chain id", mutate: func(policy map[string]any) { policy["chainId"] = "11155111" }},
		{name: "leading zero nonce", mutate: func(policy map[string]any) { policy["nonce"] = "01" }},
		{name: "negative gas", mutate: func(policy map[string]any) { policy["gasLimit"] = "-1" }},
		{name: "zero gas", mutate: func(policy map[string]any) { policy["gasLimit"] = "0" }},
		{name: "priority exceeds total fee", mutate: func(policy map[string]any) { policy["maxPriorityFeePerGas"] = "30000000001" }},
		{name: "noncanonical from", mutate: func(policy map[string]any) { policy["fromAddress"] = "0xE4ecb326ebcad4ad192bd2aec57bcf541e96948f" }},
		{name: "native token standard", mutate: func(policy map[string]any) { policy["tokenStandard"] = "erc20" }},
		{name: "native token contract", mutate: func(policy map[string]any) {
			policy["tokenContractCanonical"] = "0x2222222222222222222222222222222222222222"
		}},
		{name: "native token decimals", mutate: func(policy map[string]any) { policy["tokenDecimals"] = float64(18) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := os.ReadFile("../../testdata/ethereum-wallet-v1/sign-claim-eth.json")
			if err != nil {
				t.Fatal(err)
			}
			var fixture map[string]any
			if err := json.Unmarshal(raw, &fixture); err != nil {
				t.Fatal(err)
			}
			tt.mutate(fixture["payload"].(map[string]any)["policyContext"].(map[string]any))
			mutated, err := json.Marshal(fixture)
			if err != nil {
				t.Fatal(err)
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(mutated) }))
			defer srv.Close()
			client, _ := newTestClient(t, srv.URL)
			if _, err := client.ClaimIntent(context.Background(), "SIGN", fixture["intentId"].(string)); err == nil {
				t.Fatal("ClaimIntent() accepted invalid Ethereum policy context")
			}
		})
	}
}

func TestClaimIntentRejectsInvalidERC20PolicyShapes(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "missing standard", mutate: func(policy map[string]any) { policy["tokenStandard"] = nil }},
		{name: "wrong standard", mutate: func(policy map[string]any) { policy["tokenStandard"] = "ERC20" }},
		{name: "missing contract", mutate: func(policy map[string]any) { policy["tokenContractCanonical"] = nil }},
		{name: "noncanonical contract", mutate: func(policy map[string]any) {
			policy["tokenContractCanonical"] = "0x222222222222222222222222222222222222222A"
		}},
		{name: "missing decimals", mutate: func(policy map[string]any) { policy["tokenDecimals"] = nil }},
		{name: "negative decimals", mutate: func(policy map[string]any) { policy["tokenDecimals"] = float64(-1) }},
		{name: "native asset with token shape", mutate: func(policy map[string]any) { policy["asset"] = "ETH" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := os.ReadFile("../../testdata/ethereum-wallet-v1/sign-claim-erc20.json")
			if err != nil {
				t.Fatal(err)
			}
			var fixture map[string]any
			if err := json.Unmarshal(raw, &fixture); err != nil {
				t.Fatal(err)
			}
			tt.mutate(fixture["payload"].(map[string]any)["policyContext"].(map[string]any))
			mutated, err := json.Marshal(fixture)
			if err != nil {
				t.Fatal(err)
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(mutated) }))
			defer srv.Close()
			client, _ := newTestClient(t, srv.URL)
			if _, err := client.ClaimIntent(context.Background(), "SIGN", fixture["intentId"].(string)); err == nil {
				t.Fatal("ClaimIntent() accepted invalid ERC20 policy context")
			}
		})
	}
}

func TestLegacyTronSignClaimFixtureBytesRemainPinned(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/mpc-co-signer-http/v1/sign-claim-response.json")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != "c17b9be97868e472cee747e9e992d9da2bedfb05e4140f2c272705b05f8343e5" {
		t.Fatalf("TRON SIGN fixture hash = %s", got)
	}
}

func stringPointer(value string) *string { return &value }

func TestClaimIntentReturnsAlreadyClaimed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
	}))
	defer srv.Close()

	client, _ := newTestClient(t, srv.URL)
	_, err := client.ClaimIntent(context.Background(), "DKG", "intent-1")
	if !errors.Is(err, ErrAlreadyClaimed) {
		t.Fatalf("expected ErrAlreadyClaimed, got %v", err)
	}
}

func TestClaimIntentSendsNoBodyAndIdempotencyHeader(t *testing.T) {
	var gotContentLength int64
	var gotIdempotency string
	var bodyHashHeaderIsAbsent bool
	var signatureIsValid bool
	var pub ed25519.PublicKey
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentLength = r.ContentLength
		gotIdempotency = r.Header.Get("X-Idempotency-Key")
		bodyHashHeaderIsAbsent = len(r.Header.Values("X-Api-Body-Hash")) == 0
		signatureIsValid = verifyRequestSignature(t, r, pub, "")
		_, _ = w.Write(signV2Fixture([]byte(`{"httpStatus":200,"status":"CLAIMED","type":"DKG","sessionId":"session-1","deadline":"2026-04-16T12:00:00Z","session":{"sessionId":"session-1","status":"PENDING","startedAt":null,"executionExpiresAt":null,"deadline":"2026-04-16T12:00:00Z"}}`)))
	}))
	defer srv.Close()

	client, pub := newTestClient(t, srv.URL)
	if _, err := client.ClaimIntent(context.Background(), "DKG", "intent-1"); err != nil {
		t.Fatalf("ClaimIntent() error = %v", err)
	}
	if gotContentLength > 0 {
		t.Fatalf("ClaimIntent() sent unexpected body, ContentLength = %d", gotContentLength)
	}
	if gotIdempotency != "intent-1" {
		t.Fatalf("X-Idempotency-Key = %q, want %q", gotIdempotency, "intent-1")
	}
	if !bodyHashHeaderIsAbsent {
		t.Fatal("bodyless claim sent unexpected X-Api-Body-Hash header")
	}
	if !signatureIsValid {
		t.Fatal("bodyless claim signature validation failed")
	}
}

func TestClaimIntentRejectsUnsupportedTypeBeforeHTTP(t *testing.T) {
	client, _ := newTestClient(t, "https://example.invalid")
	if _, err := client.ClaimIntent(context.Background(), "UNKNOWN", "intent-1"); err == nil {
		t.Fatal("ClaimIntent() error = nil")
	}
}

func TestClaimIntentDecodesExecutableIntentPayload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(signV2Fixture([]byte(`{
			"httpStatus":200,
			"intentId":"intent-1",
			"sessionId":"session-1",
			"type":"DKG",
			"status":"CLAIMED",
			"deadline":"2026-04-16T12:00:00Z",
 "session":{"sessionId":"session-1","status":"PENDING","startedAt":null,"executionExpiresAt":null,"deadline":"2026-04-16T12:00:00Z"},
			"payload":{
				"type":"DKG",
				"orgId":"org-1",
				"keyId":"key-1",
				"parties":["party-1","co-signer"],
				"threshold":2,
				"algorithm":"ECDSA",
				"curve":"secp256k1",
				"chainCode":"1111111111111111111111111111111111111111111111111111111111111111",
				"chainCodeHash":"AtRJox-7JnyPNS6ZaKeePl_JXBu-qlAv1kVOveWkvtw",
				"derivationScheme":"bip32_secp256k1"
			}
		}`)))
	}))
	defer srv.Close()

	client, _ := newTestClient(t, srv.URL)
	claim, err := client.ClaimIntent(context.Background(), "DKG", "intent-1")
	if err != nil {
		t.Fatalf("ClaimIntent() error = %v", err)
	}

	intent := claim.Intent()
	if intent.IntentID != "intent-1" ||
		intent.SessionID != "session-1" ||
		intent.Type != "DKG" ||
		intent.Payload.ChainCode != strings.Repeat("11", 32) ||
		intent.Payload.ChainCodeHash != "AtRJox-7JnyPNS6ZaKeePl_JXBu-qlAv1kVOveWkvtw" ||
		intent.Payload.DerivationScheme != "bip32_secp256k1" {
		t.Fatalf("unexpected claimed intent = %+v", intent)
	}
}

func TestClaimIntentDecodesDualPartyContractFixture(t *testing.T) {
	fixture, err := os.ReadFile("../../testdata/mpc-co-signer-http/v1/claim-response.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(signV2Fixture(fixture))
	}))
	defer srv.Close()

	client, _ := newTestClient(t, srv.URL)
	claim, err := client.ClaimIntent(context.Background(), "DKG", "30000000-0000-4000-8000-000000000003")
	if err != nil {
		t.Fatalf("ClaimIntent() error = %v", err)
	}
	intent := claim.Intent()
	if intent.Type != "DKG" ||
		intent.SessionID != "20000000-0000-4000-8000-000000000003" ||
		intent.Payload.OrgID != "org-123" ||
		intent.Payload.KeyID != "mpc_key_123e4567-e89b-42d3-a456-426614174002" ||
		len(intent.Payload.DescriptorBytes) == 0 ||
		intent.Payload.DescriptorFingerprint != "owXeRUkKctags_JkTP2Xq7uiGEFz6riO1ZhW5jsg9tQ" ||
		intent.Payload.ChainCode != strings.Repeat("00", 32) ||
		intent.Payload.Threshold != 2 ||
		len(intent.Payload.Parties) != 3 {
		t.Fatalf("unexpected dual-party claimed intent = %+v", intent)
	}
	if claim.DeadlineRaw != "2026-07-30T00:00:00.000Z" {
		t.Fatalf("DeadlineRaw = %q, want exact backend value", claim.DeadlineRaw)
	}
}

func TestClaimIntentPreservesSignClaimReplayPayloadAndDeadline(t *testing.T) {
	fixture, err := os.ReadFile("../../testdata/mpc-co-signer-http/v1/sign-claim-response.json")
	if err != nil {
		t.Fatal(err)
	}
	const (
		intentID  = "30000000-0000-4000-8000-000000000005"
		sessionID = "20000000-0000-4000-8000-000000000005"
	)
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/api/v2/co-signer/intents/sign/"+intentID+"/claim" {
			t.Fatalf("request path = %q", r.URL.Path)
		}
		_, _ = w.Write(signV2Fixture(fixture))
	}))
	defer srv.Close()

	client, _ := newTestClient(t, srv.URL)
	first, err := client.ClaimIntent(context.Background(), "SIGN", intentID)
	if err != nil {
		t.Fatalf("first ClaimIntent() error = %v", err)
	}
	second, err := client.ClaimIntent(context.Background(), "SIGN", intentID)
	if err != nil {
		t.Fatalf("replay ClaimIntent() error = %v", err)
	}
	if requests != 2 || !reflect.DeepEqual(first, second) {
		t.Fatalf("claim replay changed immutable response: first=%+v second=%+v requests=%d", first, second, requests)
	}
	if first.IntentID != intentID || first.SessionID != sessionID || first.Type != "SIGN" || first.Status != "CLAIMED" ||
		first.DeadlineRaw != "2026-07-30T00:00:00.000Z" ||
		first.Payload.Type != "SIGN" || first.Payload.OrgID != "org-123" || first.Payload.KeyID != "mpc_key_123e4567-e89b-42d3-a456-426614174004" ||
		!bytes.Equal(first.Payload.Digest, append([]byte{0xaa, 0xbb, 0xcc}, make([]byte, 29)...)) || !reflect.DeepEqual(first.Payload.Parties, []string{"mpc-signer", "co-signer-primary"}) ||
		first.Payload.DerivationContext == nil || first.Payload.DerivationContext.FullPath != "m/44'/195'/0'/0/0" ||
		first.Payload.PolicyContext == nil || first.Payload.PolicyContext.Asset != "TRX" || first.Payload.PolicyContext.AmountAtomic != "100" ||
		first.Payload.PolicyContext.FromAddress != "TAddress" || first.Payload.PolicyContext.ToAddress != "TDestination" ||
		first.Payload.PolicyContext.Chain != "tron:mainnet" || first.Payload.PolicyContext.TokenStandard != nil ||
		first.Payload.PolicyContext.TokenContractCanonical != nil || first.Payload.PolicyContext.TokenDecimals != nil || first.Payload.PolicyContext.FeeLimitSun != nil {
		t.Fatalf("unexpected SIGN claim = %+v", first)
	}
}

func TestClaimIntentRejectsUnboundSignPolicyContext(t *testing.T) {
	fixture, err := os.ReadFile("../../testdata/mpc-co-signer-http/v1/sign-claim-response.json")
	if err != nil {
		t.Fatal(err)
	}
	malformed := bytes.Replace(fixture, []byte(`"chain":"tron:mainnet","feeLimitSun"`), []byte(`"chain":"tron:nile","feeLimitSun"`), 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(malformed) }))
	defer srv.Close()

	client, _ := newTestClient(t, srv.URL)
	if _, err := client.ClaimIntent(context.Background(), "SIGN", "30000000-0000-4000-8000-000000000005"); err == nil {
		t.Fatal("ClaimIntent() error = nil")
	}
}

func TestClaimIntentRejectsUnknownSignPolicyContextField(t *testing.T) {
	fixture, err := os.ReadFile("../../testdata/mpc-co-signer-http/v1/sign-claim-response.json")
	if err != nil {
		t.Fatal(err)
	}
	malformed := bytes.Replace(fixture, []byte(`"amountAtomic":"100"`), []byte(`"amountAtomic":"100","unexpected":true`), 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(malformed) }))
	defer srv.Close()

	client, _ := newTestClient(t, srv.URL)
	if _, err := client.ClaimIntent(context.Background(), "SIGN", "30000000-0000-4000-8000-000000000005"); err == nil {
		t.Fatal("ClaimIntent() error = nil")
	}
}

func TestClaimIntentRejectsWrongKindSignClaim(t *testing.T) {
	fixture, err := os.ReadFile("../../testdata/mpc-co-signer-http/v1/sign-claim-response.json")
	if err != nil {
		t.Fatal(err)
	}
	wrongKind := bytes.Replace(fixture, []byte(`"type":"SIGN"`), []byte(`"type":"DKG"`), 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(wrongKind)
	}))
	defer srv.Close()
	client, _ := newTestClient(t, srv.URL)
	if _, err := client.ClaimIntent(context.Background(), "SIGN", "intent-125"); err == nil {
		t.Fatal("ClaimIntent() error = nil")
	}
}

func TestClaimIntentRejectsUnknownSignClaimPayloadField(t *testing.T) {
	fixture, err := os.ReadFile("../../testdata/mpc-co-signer-http/v1/sign-claim-response.json")
	if err != nil {
		t.Fatal(err)
	}
	malformed := bytes.Replace(fixture, []byte(`"algorithm":"ECDSA"`), []byte(`"algorithm":"ECDSA","unexpected":true`), 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(malformed) }))
	defer srv.Close()
	client, _ := newTestClient(t, srv.URL)
	if _, err := client.ClaimIntent(context.Background(), "SIGN", "intent-125"); err == nil {
		t.Fatal("ClaimIntent() error = nil")
	}
}

func TestClaimIntentClassifiesTypedForeignConflict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write(signV2Fixture([]byte(`{"httpStatus":409,"code":"INTENT_FOREIGN_CLAIM"}`)))
	}))
	defer srv.Close()
	client, _ := newTestClient(t, srv.URL)
	_, err := client.ClaimIntent(context.Background(), "DKG", "intent-125")
	if !errors.Is(err, ErrAlreadyClaimed) {
		t.Fatalf("ClaimIntent() error = %v, want ErrAlreadyClaimed", err)
	}
}

func TestClaimIntentRejectsUnknownBackendFixtureField(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(signV2Fixture([]byte(`{"deadline":"2026-04-16T12:00:00Z","unexpected":true}`)))
	}))
	defer srv.Close()

	client, _ := newTestClient(t, srv.URL)
	if _, err := client.ClaimIntent(context.Background(), "DKG", "intent-1"); err == nil {
		t.Fatal("ClaimIntent() error = nil")
	}
}

func TestClaimIntentRequiresMatchingHTTPStatusContract(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
	}{
		{
			name:       "missing body status",
			statusCode: http.StatusOK,
			body:       `{"status":"CLAIMED","deadline":"2026-07-30T00:00:00Z"}`,
		},
		{
			name:       "unexpected HTTP status",
			statusCode: http.StatusCreated,
			body:       `{"httpStatus":200,"status":"CLAIMED","deadline":"2026-07-30T00:00:00Z"}`,
		},
		{
			name:       "body status mismatch",
			statusCode: http.StatusOK,
			body:       `{"httpStatus":201,"status":"CLAIMED","deadline":"2026-07-30T00:00:00Z"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			client, _ := newTestClient(t, srv.URL)
			if _, err := client.ClaimIntent(context.Background(), "DKG", "intent-1"); err == nil {
				t.Fatal("ClaimIntent() error = nil")
			}
		})
	}
}

func TestClaimIntentDecodesHDIntentPayload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(signV2Fixture([]byte(`{
			"httpStatus":200,
			"intentId":"intent-1",
			"sessionId":"session-1",
			"type":"SIGN",
			"status":"CLAIMED",
			"deadline":"2027-04-16T12:00:00.000Z",
			"session":{"sessionId":"session-1","status":"PENDING","startedAt":null,"executionExpiresAt":null,"deadline":"2027-04-16T12:00:00.000Z"},
			"payload":{
				"type":"SIGN",
				"orgId":"org-1",
				"walletId":"wallet-1",
				"keyId":"key-1",
				"profileId":"profile-1",
				"profileVersion":3,
				"profileTemplateId":"ethereum-default",
				"parties":["party-1","co-signer"],
				"threshold":2,
				"algorithm":"ECDSA",
				"curve":"secp256k1",
				"chain":"ethereum:mainnet",
				"digest":"AQID",
				"digestType":"transaction",
				"hashAlgorithm":"keccak256",
				"signingPayloadType":"ethereum-transaction",
				"policyContext":{
					"amountAtomic":"1",
					"asset":"ETH",
					"chain":"ethereum:mainnet",
					"version":1,
                    "transactionType":2,
                    "chainId":"1",
                    "nonce":"0",
                    "gasLimit":"21000",
                    "maxFeePerGas":"30000000000",
                    "maxPriorityFeePerGas":"1000000000",
					"fromAddress":"0x1111111111111111111111111111111111111111",
					"toAddress":"0x2222222222222222222222222222222222222222",
					"tokenContractCanonical":null,
					"tokenDecimals":null,
					"tokenStandard":null
				},
				"derivationContextHash":"context-hash",
				"partyId":"co-signer",
				"derivationContext":{
					"profileId":"profile-1",
					"profileTemplateId":"ethereum-default",
					"chain":"ethereum:mainnet",
					"algorithm":"ecdsa",
					"curve":"secp256k1",
					"scheme":"bip32_secp256k1",
					"accountPath":"m/44'/60'/0'",
					"childPath":"/0/15",
					"fullPath":"m/44'/60'/0'/0/15",
					"expectedAddress":"0x1111111111111111111111111111111111111111",
					"expectedPublicKey":"04abcdef",
					"publicKeyFormat":"uncompressed_hex",
					"descriptorVersion":7,
					"profileVersion":3,
					"keyVersion":1
				}
			}
		}`)))
	}))
	defer srv.Close()

	client, _ := newTestClient(t, srv.URL)
	claim, err := client.ClaimIntent(context.Background(), "SIGN", "intent-1")
	if err != nil {
		t.Fatalf("ClaimIntent() error = %v", err)
	}

	payload := claim.Payload
	if payload.Type != "SIGN" ||
		payload.OrgID != "org-1" ||
		payload.WalletID != "wallet-1" ||
		payload.KeyID != "key-1" ||
		payload.ProfileID != "profile-1" ||
		payload.ProfileVersion != 3 ||
		payload.ProfileTemplateID != "ethereum-default" ||
		payload.DigestType != "transaction" ||
		payload.HashAlgorithm != "keccak256" ||
		payload.SigningPayloadType != "ethereum-transaction" ||
		payload.DerivationContextHash != "context-hash" ||
		payload.PartyID != "co-signer" {
		t.Fatalf("unexpected HD payload = %+v", payload)
	}
	if payload.DerivationContext == nil {
		t.Fatal("DerivationContext is nil")
	}
	ctx := payload.DerivationContext
	if ctx.ProfileID != "profile-1" ||
		ctx.ProfileTemplateID != "ethereum-default" ||
		ctx.Chain != "ethereum:mainnet" ||
		ctx.Algorithm != "ecdsa" ||
		ctx.Curve != "secp256k1" ||
		ctx.Scheme != "bip32_secp256k1" ||
		ctx.AccountPath != "m/44'/60'/0'" ||
		ctx.ChildPath != "/0/15" ||
		ctx.FullPath != "m/44'/60'/0'/0/15" ||
		ctx.ExpectedPublicKey != "04abcdef" ||
		ctx.PublicKeyFormat != "uncompressed_hex" ||
		ctx.DescriptorVersion != 7 ||
		ctx.ProfileVersion != 3 ||
		ctx.KeyVersion != 1 {
		t.Fatalf("unexpected derivation context = %+v", ctx)
	}
}

func TestClaimIntentReturnsOutcomeUnknownAfterOneBoundedAttempt(t *testing.T) {
	client, pub := newTestClient(t, "https://example.test")
	attempts := 0
	seenNonces := make(map[string]bool)
	seenSignatures := make(map[string]bool)
	client.httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		attempts++
		if deadline, ok := r.Context().Deadline(); !ok || time.Until(deadline) > client.httpClient.Timeout {
			t.Error("claim transport lacks configured HTTP deadline")
		}
		if r.Header.Get("X-Api-Timestamp") == "" {
			t.Fatal("retry request is missing X-Api-Timestamp")
		}
		nonce := r.Header.Get("X-Api-Nonce")
		if nonce == "" || seenNonces[nonce] {
			t.Fatalf("retry request has missing or reused nonce %q", nonce)
		}
		seenNonces[nonce] = true
		signature := r.Header.Get("X-Api-Signature")
		if signature == "" || seenSignatures[signature] {
			t.Fatalf("retry request has missing or reused signature %q", signature)
		}
		seenSignatures[signature] = true
		if !verifyRequestSignature(t, r, pub, "") {
			t.Fatal("retry request signature validation failed")
		}
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection reset")}
	})

	_, err := client.ClaimIntent(context.Background(), "DKG", "intent-1")
	if !errors.Is(err, ErrClaimOutcomeUnknown) {
		t.Fatalf("expected ErrClaimOutcomeUnknown, got %v", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want one attempt owned by worker", attempts)
	}
}

func TestClaimIntentRejectsUnknownSigningPayloadType(t *testing.T) {
	fixture, err := os.ReadFile("../../testdata/mpc-co-signer-http/v1/sign-claim-response.json")
	if err != nil {
		t.Fatal(err)
	}
	malformed := bytes.Replace(fixture, []byte(`"signingPayloadType":"tron-transaction"`), []byte(`"signingPayloadType":"unknown-transaction"`), 1)
	if bytes.Equal(malformed, fixture) {
		t.Fatal("fixture did not contain the expected signing payload type")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(malformed) }))
	defer srv.Close()
	client, _ := newTestClient(t, srv.URL)
	if _, err := client.ClaimIntent(context.Background(), "SIGN", "30000000-0000-4000-8000-000000000005"); err == nil {
		t.Fatal("ClaimIntent() accepted an unsupported signing payload type")
	}
}

func TestClaimIntentRetainsDeterministicSignPolicyRejection(t *testing.T) {
	for _, field := range []string{"fromAddress", "toAddress", "tokenContractCanonical"} {
		t.Run(field, func(t *testing.T) {
			fixture := "sign-claim-eth.json"
			if field == "tokenContractCanonical" {
				fixture = "sign-claim-erc20.json"
			}
			raw, err := os.ReadFile("../../testdata/ethereum-wallet-v1/" + fixture)
			if err != nil {
				t.Fatal(err)
			}
			var wire map[string]any
			if err := json.Unmarshal(raw, &wire); err != nil {
				t.Fatal(err)
			}
			payload := wire["payload"].(map[string]any)
			payload["policyContext"].(map[string]any)[field] = "0x222222222222222222222222222222222222222A"
			raw, _ = json.Marshal(wire)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(signV2Fixture(raw)) }))
			defer srv.Close()
			client, _ := newTestClient(t, srv.URL)
			claim, err := client.ClaimIntent(context.Background(), "SIGN", wire["intentId"].(string))
			if err == nil || errors.Is(err, ErrClaimOutcomeUnknown) || claim.IntentID != wire["intentId"] || claim.SessionID == "" {
				t.Fatalf("deterministic rejection lost: claim identity=%s/%s err=%v", claim.IntentID, claim.SessionID, err)
			}
			// Diagnostics contain a field and a stable reason, never the policy value.
			if !strings.Contains(err.Error(), "payload.policyContext."+field) || !strings.Contains(err.Error(), "noncanonical_address") || strings.Contains(err.Error(), "222222") {
				t.Fatalf("unsafe or imprecise diagnostic: %v", err)
			}
		})
	}
}

func signV2Fixture(raw []byte) []byte {
	var v map[string]json.RawMessage
	if json.Unmarshal(raw, &v) != nil || string(v["type"]) != `"SIGN"` {
		return raw
	}
	var payload map[string]json.RawMessage
	_ = json.Unmarshal(v["payload"], &payload)
	var encoded string
	_ = json.Unmarshal(payload["digest"], &encoded)
	decoded, _ := base64.StdEncoding.DecodeString(encoded)
	if len(decoded) != 32 {
		replacement := make([]byte, 32)
		copy(replacement, decoded)
		payload["digest"], _ = json.Marshal(base64.StdEncoding.EncodeToString(replacement))
		v["payload"], _ = json.Marshal(payload)
	}
	v["version"] = json.RawMessage(`2`)
	v["approval"] = json.RawMessage(`null`)
	v["prepared"] = json.RawMessage(`null`)
	b, _ := json.Marshal(v)
	return b
}

func TestV2InvalidEnvelopeRetainsUnknownClaimOwnership(t *testing.T) {
	fixture, e := os.ReadFile("../../testdata/ethereum-wallet-v1/sign-claim-eth.json")
	if e != nil {
		t.Fatal(e)
	}
	good := signV2Fixture(fixture)
	var identity struct {
		IntentID string `json:"intentId"`
	}
	_ = json.Unmarshal(good, &identity)
	for _, test := range []string{"old-v1", "missing", "unknown", "duplicate", "invalid-utf8", "oversized", "missing-prepared-field", "partial-approval"} {
		t.Run(test, func(t *testing.T) {
			var v map[string]any
			_ = json.Unmarshal(good, &v)
			switch test {
			case "missing":
				delete(v, "approval")
			case "unknown":
				v["unknown"] = true
			case "missing-prepared-field":
				v["prepared"] = map[string]any{"version": 1}
			case "partial-approval":
				v["approval"] = map[string]any{"version": 1}
			}
			raw, _ := json.Marshal(v)
			switch test {
			case "old-v1":
				raw = fixture
			case "duplicate":
				raw = append([]byte(`{"version":2,`), good[1:]...)
			case "invalid-utf8":
				raw = []byte{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'}
			case "oversized":
				raw = bytes.Repeat([]byte(" "), 128*1024+1)
			}
			requests := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if !strings.HasPrefix(r.URL.Path, "/api/v2/") {
					t.Error("legacy fallback")
				}
				_, _ = w.Write(raw)
			}))
			defer srv.Close()
			client, _ := newTestClient(t, srv.URL)
			_, e = client.ClaimIntent(context.Background(), "SIGN", identity.IntentID)
			if !errors.Is(e, ErrClaimOutcomeUnknown) || !errors.Is(e, ErrClaimEnvelopeInvalid) || requests != 1 {
				t.Fatalf("unsafe ownership classification: requests=%d err=%v", requests, e)
			}
		})
	}
}

func TestDkgV1RejectsSignV2Fields(t *testing.T) {
	raw, e := os.ReadFile("../../testdata/mpc-co-signer-http/v1/claim-response.json")
	if e != nil {
		t.Fatal(e)
	}
	for _, field := range []string{"version", "approval", "prepared"} {
		t.Run(field, func(t *testing.T) {
			var v map[string]any
			_ = json.Unmarshal(raw, &v)
			v[field] = nil
			if field == "version" {
				v[field] = 0
			}
			poisoned, _ := json.Marshal(v)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasPrefix(r.URL.Path, "/api/v1/") {
					t.Error("DKG endpoint changed")
				}
				_, _ = w.Write(poisoned)
			}))
			defer srv.Close()
			c, _ := newTestClient(t, srv.URL)
			if _, e := c.ClaimIntent(context.Background(), "DKG", "synthetic-dkg"); e == nil {
				t.Fatal("SIGN-only field accepted on DKG v1")
			}
		})
	}
}

func TestV2ApprovedClaimDecodesSharedProducerBindings(t *testing.T) {
	raw, e := os.ReadFile("../../testdata/approval-prepared-v1/corpus.json")
	if e != nil {
		t.Fatal(e)
	}
	var items []map[string]json.RawMessage
	if e = json.Unmarshal(raw, &items); e != nil {
		t.Fatal(e)
	}
	for _, v := range items {
		if string(v["valid"]) != "true" {
			continue
		}
		var id string
		_ = json.Unmarshal(v["id"], &id)
		t.Run(id, func(t *testing.T) {
			var a map[string]json.RawMessage
			_ = json.Unmarshal(v["approval"], &a)
			var auth map[string]json.RawMessage
			_ = json.Unmarshal(a["authorization"], &auth)
			var d map[string]json.RawMessage
			_ = json.Unmarshal(auth["derivationContext"], &d)
			payload := map[string]any{"type": "SIGN", "orgId": auth["orgId"], "walletId": auth["walletId"], "keyId": auth["keyId"], "profileId": d["profileId"], "profileTemplateId": d["profileTemplateId"], "profileVersion": d["profileVersion"], "algorithm": "ECDSA", "curve": "secp256k1", "chain": auth["chain"], "partyId": "co-signer-primary", "parties": []string{"mpc-signer", "co-signer-primary"}, "threshold": 2, "digest": v["digest"], "digestType": "transaction", "hashAlgorithm": v["hashAlgorithm"], "signingPayloadType": v["payloadType"], "policyContext": v["policy"], "derivationContext": auth["derivationContext"], "derivationContextHash": auth["derivationContextHash"]}
			var outgoing string
			_ = json.Unmarshal(a["outgoingId"], &outgoing)
			response := map[string]any{"httpStatus": 200, "intentId": outgoing, "sessionId": "synthetic-session", "type": "SIGN", "payload": payload, "status": "CLAIMED", "deadline": "2027-01-01T00:00:00.000Z", "version": 2, "approval": v["approval"], "prepared": v["prepared"], "session": map[string]any{"sessionId": "synthetic-session", "status": "PENDING", "startedAt": nil, "deadline": "2027-01-01T00:00:00.000Z", "executionExpiresAt": nil}}
			encoded, _ := json.Marshal(response)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasPrefix(r.URL.Path, "/api/v2/") {
					t.Error("legacy path")
				}
				_, _ = w.Write(encoded)
			}))
			defer srv.Close()
			c, _ := newTestClient(t, srv.URL)
			claim, e := c.ClaimIntent(context.Background(), "SIGN", outgoing)
			if e != nil {
				t.Fatal(e)
			}
			if claim.Version != 2 || claim.Approval == nil || claim.Prepared == nil || claim.Intent().Approval != claim.Approval || claim.Intent().Prepared != claim.Prepared {
				t.Fatal("approved binding lost across HTTP and worker intent")
			}
		})
	}
}
