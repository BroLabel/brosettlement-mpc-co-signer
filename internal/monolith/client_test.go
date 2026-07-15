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

const testAPIKeyID = "11111111-2222-3333-4444-555555555555"

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
	var bodyHashHeaderIsAbsent bool
	var signatureIsValid bool
	var pub ed25519.PublicKey
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentLength = r.ContentLength
		gotIdempotency = r.Header.Get("X-Idempotency-Key")
		bodyHashHeaderIsAbsent = len(r.Header.Values("X-Api-Body-Hash")) == 0
		signatureIsValid = verifyRequestSignature(t, r, pub, "")
		_, _ = w.Write([]byte(`{"expiresAt":"2026-04-16T12:00:00Z"}`))
	}))
	defer srv.Close()

	client, pub := newTestClient(t, srv.URL)
	if _, err := client.ClaimIntent(context.Background(), "intent-1"); err != nil {
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

func TestClaimIntentDecodesExecutableIntentPayload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"intentId":"intent-1",
			"sessionId":"session-1",
			"type":"DKG",
			"status":"CLAIMED",
			"expiresAt":"2026-04-16T12:00:00Z",
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
		}`))
	}))
	defer srv.Close()

	client, _ := newTestClient(t, srv.URL)
	claim, err := client.ClaimIntent(context.Background(), "intent-1")
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

func TestPostMessageAddsSigningAndIdempotencyHeaders(t *testing.T) {
	var gotSignature, gotBodyHash, gotIdempotency, gotNonce, gotAPIKeyID string
	var signatureIsValid bool
	var signatureIsInvalidWithDifferentAPIKeyID bool
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
		gotAPIKeyID = r.Header.Get("X-Api-Key-Id")

		signatureIsValid = verifyRequestSignature(t, r, pub, gotBodyHash)
		signatureIsInvalidWithDifferentAPIKeyID = !verifyRequestSignatureFor(
			t,
			r,
			pub,
			r.URL.RequestURI(),
			gotBodyHash,
			"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		)

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
	if gotAPIKeyID != testAPIKeyID {
		t.Fatalf("X-Api-Key-Id = %q, want %q", gotAPIKeyID, testAPIKeyID)
	}
	if !signatureIsValid {
		t.Fatal("signature validation failed")
	}
	if !signatureIsInvalidWithDifferentAPIKeyID {
		t.Fatal("signature remained valid after changing the API key ID")
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
	var signatureIsValid, changedQuerySignatureIsValid bool
	var pub ed25519.PublicKey
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantRequestTarget := "/api/v1/co-signer/sessions/session-1/messages?afterSeq=10"
		if got := r.URL.RequestURI(); got != wantRequestTarget {
			t.Fatalf("request target = %q, want %q", got, wantRequestTarget)
		}
		signatureIsValid = verifyRequestSignature(t, r, pub, "")
		changedQuerySignatureIsValid = verifyRequestSignatureFor(
			t,
			r,
			pub,
			"/api/v1/co-signer/sessions/session-1/messages?afterSeq=11",
			"",
			testAPIKeyID,
		)
		_, _ = w.Write([]byte(`{"messages":[{"deliverySeq":11,"protocolSeq":7,"messageId":"msg-1","round":2,"fromPartyId":"co-signer","toPartyId":"mpc-signer","payload":"YWJj"}]}`))
	}))
	defer srv.Close()

	client, pub := newTestClient(t, srv.URL)
	msgs, err := client.GetMessages(context.Background(), "session-1", 10)
	if err != nil {
		t.Fatalf("GetMessages() error = %v", err)
	}
	if len(msgs) != 1 || msgs[0].DeliverySeq != 11 || msgs[0].ProtocolSeq != 7 {
		t.Fatalf("unexpected messages = %+v", msgs)
	}
	if !signatureIsValid {
		t.Fatal("query-bearing GET signature validation failed")
	}
	if changedQuerySignatureIsValid {
		t.Fatal("signature remained valid after changing query")
	}
}

func TestNewRequestSignsExactRequestTarget(t *testing.T) {
	client, pub := newTestClient(t, "https://example.test")
	tests := []struct {
		name          string
		requestTarget string
	}{
		{name: "parameter order", requestTarget: "/resource?b=2&a=1"},
		{name: "repeated parameters and empty values", requestTarget: "/resource?tag=one&tag=two&empty=&bare"},
		{name: "percent encoding", requestTarget: "/resource?value=a%2Fb&space=a%20b&plus=a+b"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := client.newRequest(context.Background(), http.MethodGet, tt.requestTarget, nil)
			if err != nil {
				t.Fatalf("newRequest() error = %v", err)
			}
			if got := req.URL.RequestURI(); got != tt.requestTarget {
				t.Fatalf("RequestURI() = %q, want %q", got, tt.requestTarget)
			}
			if !verifyRequestSignature(t, req, pub, "") {
				t.Fatalf("signature validation failed for request target %q", tt.requestTarget)
			}

			if tt.requestTarget == "/resource?b=2&a=1" && verifyRequestSignatureFor(
				t,
				req,
				pub,
				"/resource?a=1&b=2",
				"",
				testAPIKeyID,
			) {
				t.Fatal("signature remained valid after changing query parameter order")
			}
		})
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
	client, pub := newTestClient(t, "https://example.test")
	attempts := 0
	seenNonces := make(map[string]bool)
	seenSignatures := make(map[string]bool)
	client.httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		attempts++
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
	client := New(baseURL, testAPIKeyID, privateKey, time.Second)
	return client, privateKey.Public().(ed25519.PublicKey)
}

func verifyRequestSignature(t *testing.T, r *http.Request, pub ed25519.PublicKey, bodyHash string) bool {
	t.Helper()
	return verifyRequestSignatureFor(
		t,
		r,
		pub,
		r.URL.RequestURI(),
		bodyHash,
		r.Header.Get("X-Api-Key-Id"),
	)
}

func verifyRequestSignatureFor(
	t *testing.T,
	r *http.Request,
	pub ed25519.PublicKey,
	requestTarget string,
	bodyHash string,
	apiKeyID string,
) bool {
	t.Helper()
	signature, err := base64.StdEncoding.DecodeString(r.Header.Get("X-Api-Signature"))
	if err != nil {
		t.Fatalf("DecodeString() error = %v", err)
	}
	canonical := strings.Join([]string{
		strings.ToUpper(r.Method),
		requestTarget,
		bodyHash,
		r.Header.Get("X-Api-Timestamp"),
		r.Header.Get("X-Api-Nonce"),
		apiKeyID,
	}, "\n")
	return ed25519.Verify(pub, []byte(canonical), signature)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
