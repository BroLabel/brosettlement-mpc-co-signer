package monolith

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClaimIntentReturnsAlreadyClaimed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
	}))
	defer srv.Close()

	client, _ := newTestClient(t, srv.URL)
	_, err := client.ClaimIntent(context.Background(), "intent-1")
	if !errors.Is(err, ErrAlreadyClaimed) {
		t.Fatalf("expected ErrAlreadyClaimed, got %v", err)
	}
}

func TestClaimIntentSendsNoBodyAndIdempotencyHeader(t *testing.T) {
	var gotContentLength int64
	var gotIdempotency string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentLength = r.ContentLength
		gotIdempotency = r.Header.Get("X-Idempotency-Key")
		_, _ = w.Write([]byte(`{"expiresAt":"2026-04-16T12:00:00Z"}`))
	}))
	defer srv.Close()

	client, _ := newTestClient(t, srv.URL)
	if _, err := client.ClaimIntent(context.Background(), "intent-1"); err != nil {
		t.Fatalf("ClaimIntent() error = %v", err)
	}
	if gotContentLength > 0 {
		t.Fatalf("ClaimIntent() sent unexpected body, ContentLength = %d", gotContentLength)
	}
	if gotIdempotency != "intent-1" {
		t.Fatalf("X-Idempotency-Key = %q, want %q", gotIdempotency, "intent-1")
	}
}

func TestPostMessageAddsSigningAndIdempotencyHeaders(t *testing.T) {
	var gotSignature, gotBodyHash, gotIdempotency, gotNonce string
	var signatureIsValid bool
	var gotPayload map[string]any
	_, pub := newTestClient(t, "https://example.test")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("ReadAll() error = %v", err)
		}

		gotSignature = r.Header.Get("X-Api-Signature")
		gotBodyHash = r.Header.Get("X-Api-Body-Hash")
		gotIdempotency = r.Header.Get("X-Idempotency-Key")
		gotNonce = r.Header.Get("X-Api-Nonce")

		ts := r.Header.Get("X-Api-Timestamp")
		canonical := strings.Join([]string{
			strings.ToUpper(r.Method),
			r.URL.Path,
			gotBodyHash,
			ts,
			gotNonce,
		}, "\n")
		sigBytes, err := base64.StdEncoding.DecodeString(gotSignature)
		if err == nil {
			signatureIsValid = ed25519.Verify(pub, []byte(canonical), sigBytes)
		}

		wantBodyHash := sha256.Sum256(body)
		if gotBodyHash != hex.EncodeToString(wantBodyHash[:]) {
			t.Fatalf("X-Api-Body-Hash = %q, want %q", gotBodyHash, hex.EncodeToString(wantBodyHash[:]))
		}
		if err := json.Unmarshal(body, &gotPayload); err != nil {
			t.Fatalf("json.Unmarshal() error = %v", err)
		}
		_, _ = w.Write([]byte(`{"deliverySeq":17}`))
	}))
	defer srv.Close()

	client, _ := newTestClient(t, srv.URL)
	err := client.PostMessage(context.Background(), "session-1", OutboundFrame{
		MessageID:   "msg-1",
		ProtocolSeq: 9,
		Round:       2,
		ToPartyID:   "mpc-signer",
		Payload:     []byte("abc"),
	})
	if err != nil {
		t.Fatalf("PostMessage() error = %v", err)
	}
	if gotSignature == "" || gotBodyHash == "" || gotIdempotency != "msg-1" {
		t.Fatalf("missing required headers signature=%q bodyHash=%q idempotency=%q", gotSignature, gotBodyHash, gotIdempotency)
	}
	if gotNonce == "" {
		t.Fatal("missing required X-Api-Nonce header")
	}
	if !signatureIsValid {
		t.Fatal("signature validation failed")
	}
	if gotPayload["messageId"] != "msg-1" {
		t.Fatalf("messageId = %v, want %q", gotPayload["messageId"], "msg-1")
	}
	if gotPayload["protocolSeq"] != float64(9) {
		t.Fatalf("protocolSeq = %v, want %d", gotPayload["protocolSeq"], 9)
	}
	if gotPayload["round"] != float64(2) {
		t.Fatalf("round = %v, want %d", gotPayload["round"], 2)
	}
	if gotPayload["toPartyId"] != "mpc-signer" {
		t.Fatalf("toPartyId = %v, want %q", gotPayload["toPartyId"], "mpc-signer")
	}
	if gotPayload["payload"] != base64.StdEncoding.EncodeToString([]byte("abc")) {
		t.Fatalf("payload = %v, want %q", gotPayload["payload"], base64.StdEncoding.EncodeToString([]byte("abc")))
	}
	if _, exists := gotPayload["seq"]; exists {
		t.Fatalf("unexpected legacy seq field in payload: %+v", gotPayload)
	}
}

func TestPostResultAddsIdempotencyHeaderFromIntentID(t *testing.T) {
	var gotIdempotency string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotIdempotency = r.Header.Get("X-Idempotency-Key")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	client, _ := newTestClient(t, srv.URL)
	err := client.PostResult(context.Background(), "intent-42", IntentResult{
		Status: "approved",
	})
	if err != nil {
		t.Fatalf("PostResult() error = %v", err)
	}
	if gotIdempotency != "intent-42" {
		t.Fatalf("X-Idempotency-Key = %q, want %q", gotIdempotency, "intent-42")
	}
}

func TestGetMessagesDecodesDeliverySeqSeparatelyFromProtocolSeq(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("afterSeq") != "10" {
			t.Fatalf("afterSeq query = %q, want 10", r.URL.Query().Get("afterSeq"))
		}
		_, _ = w.Write([]byte(`{"messages":[{"deliverySeq":11,"protocolSeq":7,"messageId":"msg-1","round":2,"fromPartyId":"co-signer","toPartyId":"mpc-signer","payload":"YWJj"}]}`))
	}))
	defer srv.Close()

	client, _ := newTestClient(t, srv.URL)
	msgs, err := client.GetMessages(context.Background(), "session-1", 10)
	if err != nil {
		t.Fatalf("GetMessages() error = %v", err)
	}
	if len(msgs) != 1 || msgs[0].DeliverySeq != 11 || msgs[0].ProtocolSeq != 7 {
		t.Fatalf("unexpected messages = %+v", msgs)
	}
}

func TestGetPendingIntentsDecodesHDIntentPayload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"intents":[{
			"intentId":"intent-1",
			"sessionId":"session-1",
			"type":"SIGN",
			"expiresAt":"2026-04-16T12:00:00Z",
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
				"chain":"ethereum",
				"digest":"AQID",
				"digestType":"transaction_hash",
				"hashAlgorithm":"sha256",
				"signingPayloadType":"ethereum_transaction",
				"chainCode":"1111111111111111111111111111111111111111111111111111111111111111",
				"chainCodeHash":"AtRJox-7JnyPNS6ZaKeePl_JXBu-qlAv1kVOveWkvtw",
				"derivationScheme":"bip32_secp256k1",
				"derivationContextHash":"context-hash",
				"partyId":"co-signer",
				"derivationContext":{
					"profileId":"profile-1",
					"profileTemplateId":"ethereum-default",
					"chain":"ethereum",
					"algorithm":"ecdsa",
					"curve":"secp256k1",
					"scheme":"bip32_secp256k1",
					"accountPath":"m/44'/60'/0'",
					"childPath":"/0/15",
					"fullPath":"m/44'/60'/0'/0/15",
					"expectedPublicKey":"04abcdef",
					"publicKeyFormat":"uncompressed_hex",
					"descriptorVersion":7,
					"profileVersion":3,
					"keyVersion":1
				}
			}
		}]}`))
	}))
	defer srv.Close()

	client, _ := newTestClient(t, srv.URL)
	intents, err := client.GetPendingIntents(context.Background())
	if err != nil {
		t.Fatalf("GetPendingIntents() error = %v", err)
	}
	if len(intents) != 1 {
		t.Fatalf("len(intents) = %d, want 1", len(intents))
	}

	payload := intents[0].Payload
	if payload.Type != "SIGN" ||
		payload.OrgID != "org-1" ||
		payload.WalletID != "wallet-1" ||
		payload.KeyID != "key-1" ||
		payload.ProfileID != "profile-1" ||
		payload.ProfileVersion != 3 ||
		payload.ProfileTemplateID != "ethereum-default" ||
		payload.DigestType != "transaction_hash" ||
		payload.HashAlgorithm != "sha256" ||
		payload.SigningPayloadType != "ethereum_transaction" ||
		payload.ChainCode != "1111111111111111111111111111111111111111111111111111111111111111" ||
		payload.ChainCodeHash != "AtRJox-7JnyPNS6ZaKeePl_JXBu-qlAv1kVOveWkvtw" ||
		payload.DerivationScheme != "bip32_secp256k1" ||
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
		ctx.Chain != "ethereum" ||
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

func TestClaimIntentReturnsOutcomeUnknownAfterAmbiguousRetries(t *testing.T) {
	client, _ := newTestClient(t, "https://example.test")
	attempts := 0
	client.httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		attempts++
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection reset")}
	})

	_, err := client.ClaimIntent(context.Background(), "intent-1")
	if !errors.Is(err, ErrClaimOutcomeUnknown) {
		t.Fatalf("expected ErrClaimOutcomeUnknown, got %v", err)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
}

func newTestClient(t *testing.T, baseURL string) (*Client, ed25519.PublicKey) {
	t.Helper()

	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	privateKey := ed25519.NewKeyFromSeed(seed)
	client := New(baseURL, "key-1", privateKey, time.Second)
	return client, privateKey.Public().(ed25519.PublicKey)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
