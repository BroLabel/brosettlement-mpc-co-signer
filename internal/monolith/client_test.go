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
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
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
		_, _ = w.Write([]byte(`{"httpStatus":200,"status":"CLAIMED","type":"DKG","expiresAt":"2026-04-16T12:00:00Z"}`))
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
		_, _ = w.Write([]byte(`{
			"httpStatus":200,
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
		_, _ = w.Write(fixture)
	}))
	defer srv.Close()

	client, _ := newTestClient(t, srv.URL)
	claim, err := client.ClaimIntent(context.Background(), "DKG", "intent-123")
	if err != nil {
		t.Fatalf("ClaimIntent() error = %v", err)
	}
	intent := claim.Intent()
	if intent.Type != "DKG" ||
		intent.SessionID != "123e4567-e89b-42d3-a456-426614174123" ||
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
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/api/v1/co-signer/intents/sign/intent-125/claim" {
			t.Fatalf("request path = %q", r.URL.Path)
		}
		_, _ = w.Write(fixture)
	}))
	defer srv.Close()

	client, _ := newTestClient(t, srv.URL)
	first, err := client.ClaimIntent(context.Background(), "SIGN", "intent-125")
	if err != nil {
		t.Fatalf("first ClaimIntent() error = %v", err)
	}
	second, err := client.ClaimIntent(context.Background(), "SIGN", "intent-125")
	if err != nil {
		t.Fatalf("replay ClaimIntent() error = %v", err)
	}
	if requests != 2 || !reflect.DeepEqual(first, second) {
		t.Fatalf("claim replay changed immutable response: first=%+v second=%+v requests=%d", first, second, requests)
	}
	if first.IntentID != "intent-125" || first.SessionID != "sign-125" || first.Type != "SIGN" || first.Status != "CLAIMED" ||
		first.DeadlineRaw != "2026-07-30T00:00:00.000Z" ||
		first.Payload.Type != "SIGN" || first.Payload.OrgID != "org-123" || first.Payload.KeyID != "mpc_key_123e4567-e89b-42d3-a456-426614174004" ||
		!bytes.Equal(first.Payload.Digest, []byte{0xaa, 0xbb, 0xcc}) || !reflect.DeepEqual(first.Payload.Parties, []string{"mpc-signer", "co-signer-primary"}) ||
		first.Payload.DerivationContext == nil || first.Payload.DerivationContext.FullPath != "m/44'/195'/0'/0/0" {
		t.Fatalf("unexpected SIGN claim = %+v", first)
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
		_, _ = w.Write([]byte(`{"httpStatus":409,"code":"INTENT_FOREIGN_CLAIM"}`))
	}))
	defer srv.Close()
	client, _ := newTestClient(t, srv.URL)
	_, err := client.ClaimIntent(context.Background(), "DKG", "intent-125")
	if !errors.Is(err, ErrAlreadyClaimed) {
		t.Fatalf("ClaimIntent() error = %v, want ErrAlreadyClaimed", err)
	}
}

func TestListActionableIntentsDecodesStrictBackendFixture(t *testing.T) {
	fixture, err := os.ReadFile("../../testdata/mpc-co-signer-http/v1/listing-response.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(fixture)
	}))
	defer srv.Close()

	client, _ := newTestClient(t, srv.URL)
	listing, err := client.ListActionableIntents(context.Background())
	if err != nil {
		t.Fatalf("ListActionableIntents() error = %v", err)
	}
	if listing.HTTPStatus != http.StatusOK {
		t.Fatalf("HTTPStatus = %d, want %d", listing.HTTPStatus, http.StatusOK)
	}
	if len(listing.OwnClaimedDKG) != 1 {
		t.Fatalf("len(OwnClaimedDKG) = %d, want 1", len(listing.OwnClaimedDKG))
	}
	claimed := listing.OwnClaimedDKG[0]
	if claimed.IntentID != "intent-122" ||
		claimed.SessionID != "123e4567-e89b-42d3-a456-426614174122" ||
		claimed.KeyID != "mpc_key_123e4567-e89b-42d3-a456-426614174001" ||
		claimed.Type != "DKG" ||
		claimed.Status != "CLAIMED" ||
		claimed.DeadlineRaw != "2026-07-30T00:00:00.000Z" ||
		len(claimed.DescriptorBytes) == 0 {
		t.Fatalf("unexpected own claimed DKG = %+v", claimed)
	}
	if len(listing.Pending) != 2 {
		t.Fatalf("len(Pending) = %d, want 2", len(listing.Pending))
	}
	if listing.Pending[0].Type != "DKG" ||
		listing.Pending[0].Status != "PENDING" ||
		listing.Pending[0].DeadlineRaw != "2026-07-30T00:00:00.000Z" {
		t.Fatalf("unexpected pending DKG = %+v", listing.Pending[0])
	}
	if listing.Pending[1].Type != "SIGN" || listing.Pending[1].Status != "PENDING" {
		t.Fatalf("unexpected pending SIGN = %+v", listing.Pending[1])
	}
	if len(listing.OwnClaimedSign) != 1 {
		t.Fatalf("len(OwnClaimedSign) = %d, want 1", len(listing.OwnClaimedSign))
	}
	rediscovered := listing.OwnClaimedSign[0]
	if rediscovered.IntentID != "intent-125" || rediscovered.SessionID != "sign-125" || rediscovered.Type != "SIGN" ||
		rediscovered.Status != "CLAIMED" ||
		rediscovered.DeadlineRaw != "2026-07-30T00:00:00.000Z" || len(rediscovered.DescriptorBytes) != 0 {
		t.Fatalf("unexpected own claimed SIGN = %+v", rediscovered)
	}
}

func TestGetPendingIntentsExcludesOwnClaimedDKGFromStrictListing(t *testing.T) {
	fixture, err := os.ReadFile("../../testdata/mpc-co-signer-http/v1/listing-response.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(fixture)
	}))
	defer srv.Close()

	client, _ := newTestClient(t, srv.URL)
	pending, err := client.GetPendingIntents(context.Background())
	if err != nil {
		t.Fatalf("GetPendingIntents() error = %v", err)
	}
	if len(pending) != 3 {
		t.Fatalf("len(pending) = %d, want 3", len(pending))
	}
	if pending[0].IntentID != "intent-125" || pending[0].SessionID != "sign-125" || pending[0].Type != "SIGN" ||
		pending[0].Payload.OrgID != "org-123" || pending[0].Payload.KeyID != "mpc_key_123e4567-e89b-42d3-a456-426614174004" ||
		pending[0].ExpiresAt.Format(time.RFC3339Nano) != "2026-07-30T00:00:00Z" || len(pending[0].Payload.Digest) != 0 ||
		pending[1].IntentID != "intent-123" || pending[1].Type != "DKG" ||
		pending[2].IntentID != "intent-124" || pending[2].Type != "SIGN" {
		t.Fatalf("pending = %+v, want own-claimed SIGN then backend pending collection", pending)
	}
	if pending[0].CreatedAt.IsZero() || pending[1].CreatedAt.IsZero() || pending[2].CreatedAt.IsZero() ||
		!pending[0].CreatedAt.Before(pending[1].CreatedAt) || !pending[1].CreatedAt.Before(pending[2].CreatedAt) {
		t.Fatalf("pending createdAt values were not preserved: %+v", pending)
	}
}

func TestListActionableIntentsRejectsInvalidOwnClaimedSignDiscovery(t *testing.T) {
	valid := `{"createdAt":"2026-07-29T00:00:00.500Z","deadline":"2026-07-30T00:00:00.000Z","intentId":"intent-125","keyId":"mpc_key_123e4567-e89b-42d3-a456-426614174004","orgId":"org-123","sessionId":"sign-125","status":"CLAIMED","type":"SIGN"}`
	tests := []struct{ name, item string }{
		{name: "missing deadline", item: strings.Replace(valid, `,"deadline":"2026-07-30T00:00:00.000Z"`, "", 1)},
		{name: "wrong status", item: strings.Replace(valid, `"status":"CLAIMED"`, `"status":"PENDING"`, 1)},
		{name: "wrong field type", item: strings.Replace(valid, `"deadline":"2026-07-30T00:00:00.000Z"`, `"deadline":7`, 1)},
		{name: "unexpected payload", item: strings.Replace(valid, `,"sessionId"`, `,"payload":{},"sessionId"`, 1)},
		{name: "known DKG field is still unexpected", item: strings.Replace(valid, `,"intentId"`, `,"descriptorFingerprint":"","intentId"`, 1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"httpStatus":200,"ownClaimedDkg":[],"ownClaimedSign":[` + tt.item + `],"pending":[]}`))
			}))
			defer srv.Close()
			client, _ := newTestClient(t, srv.URL)
			if _, err := client.ListActionableIntents(context.Background()); err == nil {
				t.Fatal("ListActionableIntents() error = nil")
			}
		})
	}
}

func TestListActionableIntentsRejectsNonStrictListingContract(t *testing.T) {
	validDescriptor := base64.StdEncoding.EncodeToString([]byte(`{"descriptorKind":"test"}`))
	tests := []struct {
		name string
		body string
	}{
		{
			name: "legacy response shape",
			body: `{"intents":[]}`,
		},
		{
			name: "unknown top-level field",
			body: `{"httpStatus":200,"ownClaimedDkg":[],"pending":[],"extra":true}`,
		},
		{
			name: "duplicate top-level field",
			body: `{"httpStatus":200,"httpStatus":200,"ownClaimedDkg":[],"pending":[]}`,
		},
		{
			name: "missing claimed collection",
			body: `{"httpStatus":200,"pending":[]}`,
		},
		{
			name: "body status mismatch",
			body: `{"httpStatus":201,"ownClaimedDkg":[],"pending":[]}`,
		},
		{
			name: "unknown item field",
			body: `{"httpStatus":200,"ownClaimedDkg":[],"pending":[{"createdAt":"2026-07-29T00:00:00Z","intentId":"intent-1","keyId":"key-1","orgId":"org-1","status":"PENDING","type":"SIGN","extra":true}]}`,
		},
		{
			name: "removed deployment identity field",
			body: `{"httpStatus":200,"ownClaimedDkg":[],"ownClaimedSign":[{"coSignerDeploymentId":"legacy-installation","createdAt":"2026-07-29T00:00:00.000Z","deadline":"2026-07-30T00:00:00.000Z","intentId":"intent-1","keyId":"mpc_key_123e4567-e89b-42d3-a456-426614174000","orgId":"org-1","sessionId":"sign-1","status":"CLAIMED","type":"SIGN"}],"pending":[]}`,
		},
		{
			name: "noncanonical descriptor base64",
			body: `{"httpStatus":200,"ownClaimedDkg":[],"pending":[{"createdAt":"2026-07-29T00:00:00Z","deadline":"2026-07-30T00:00:00Z","descriptorBytesBase64":"` + strings.TrimRight(validDescriptor, "=") + `","descriptorFingerprint":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","intentId":"intent-1","keyId":"mpc_key_123e4567-e89b-42d3-a456-426614174000","orgId":"org-1","sessionId":"dkg-1","status":"PENDING","type":"DKG"}]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			client, _ := newTestClient(t, srv.URL)
			if _, err := client.ListActionableIntents(context.Background()); err == nil {
				t.Fatal("ListActionableIntents() error = nil")
			}
		})
	}
}

func TestListActionableIntentsRejectsUnexpectedHTTPStatusWithValidBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"httpStatus":200,"ownClaimedDkg":[],"pending":[]}`))
	}))
	defer srv.Close()

	client, _ := newTestClient(t, srv.URL)
	if _, err := client.ListActionableIntents(context.Background()); err == nil {
		t.Fatal("ListActionableIntents() error = nil")
	}
}

func TestClaimIntentRejectsUnknownBackendFixtureField(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"expiresAt":"2026-04-16T12:00:00Z","unexpected":true}`))
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

func TestPostMessageAddsSigningAndIdempotencyHeaders(t *testing.T) {
	var gotSignature, gotBodyHash, gotIdempotency, gotNonce, gotAPIKeyID string
	var gotBody []byte
	var signatureIsValid bool
	var signatureIsInvalidWithDifferentAPIKeyID bool
	var gotPayload map[string]any
	wantBody, err := os.ReadFile("../../testdata/mpc-co-signer-http/v1/mailbox-frame.json")
	if err != nil {
		t.Fatal(err)
	}
	_, pub := newTestClient(t, "https://example.test")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("ReadAll() error = %v", err)
		}
		gotBody = append([]byte(nil), body...)

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
	err = client.PostMessage(context.Background(), "123e4567-e89b-42d3-a456-426614174123", OutboundFrame{
		AuthenticatedPartyID:  "co-signer-primary",
		Broadcast:             false,
		FromPartyID:           "co-signer-primary",
		IntentID:              "intent-123",
		MessageID:             "msg_0123456789abcdef",
		OrgID:                 "org-123",
		Payload:               []byte{0},
		ProtocolSeq:           1,
		Round:                 1,
		SessionID:             "123e4567-e89b-42d3-a456-426614174123",
		ToPartyID:             "mpc-signer",
		DerivationContextHash: "must-not-be-an-extra-http-field",
	})
	if err != nil {
		t.Fatalf("PostMessage() error = %v", err)
	}
	if gotSignature == "" || gotBodyHash == "" || gotIdempotency != "msg_0123456789abcdef" {
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
	if !bytes.Equal(gotBody, wantBody) {
		t.Fatalf("PostMessage() body = %s, want exact producer fixture %s", gotBody, wantBody)
	}
	if gotPayload["messageId"] != "msg_0123456789abcdef" {
		t.Fatalf("messageId = %v, want %q", gotPayload["messageId"], "msg_0123456789abcdef")
	}
	if gotPayload["protocolSeq"] != float64(1) {
		t.Fatalf("protocolSeq = %v, want %d", gotPayload["protocolSeq"], 1)
	}
	if gotPayload["round"] != float64(1) {
		t.Fatalf("round = %v, want %d", gotPayload["round"], 1)
	}
	if gotPayload["fromPartyId"] != "co-signer-primary" {
		t.Fatalf("fromPartyId = %v, want %q", gotPayload["fromPartyId"], "co-signer-primary")
	}
	if gotPayload["toPartyId"] != "mpc-signer" {
		t.Fatalf("toPartyId = %v, want %q", gotPayload["toPartyId"], "mpc-signer")
	}
	if gotPayload["payload"] != "AA==" {
		t.Fatalf("payload = %v, want %q", gotPayload["payload"], "AA==")
	}
	if _, exists := gotPayload["seq"]; exists {
		t.Fatalf("unexpected legacy seq field in payload: %+v", gotPayload)
	}
}

func TestPostResultAddsIdempotencyHeaderFromIntentID(t *testing.T) {
	var gotIdempotency string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/co-signer/intents/sign/intent-42/result" {
			t.Fatalf("request path = %q", r.URL.Path)
		}
		gotIdempotency = r.Header.Get("X-Idempotency-Key")
		_, _ = w.Write([]byte(`{"authoritativeStatus":"COMPLETED","httpStatus":200,"outcome":"ACCEPTED"}`))
	}))
	defer srv.Close()

	client, _ := newTestClient(t, srv.URL)
	err := client.PostResult(context.Background(), "intent-42", IntentResult{
		Status: "COMPLETED",
	})
	if err != nil {
		t.Fatalf("PostResult() error = %v", err)
	}
	if gotIdempotency != "intent-42" {
		t.Fatalf("X-Idempotency-Key = %q, want %q", gotIdempotency, "intent-42")
	}
}

func TestPostResultPublishesMinimalCompletedAndFailedSignResults(t *testing.T) {
	tests := []struct {
		name, fixture, response string
		result                  IntentResult
	}{
		{name: "completed", fixture: "sign-terminal-completed-request.json", response: `{"authoritativeStatus":"COMPLETED","httpStatus":200,"outcome":"ACCEPTED"}`, result: IntentResult{Status: "COMPLETED"}},
		{name: "failed", fixture: "sign-terminal-failed-request.json", response: `{"authoritativeStatus":"FAILED","httpStatus":200,"outcome":"EXACT_REPLAY"}`, result: IntentResult{Status: "FAILED", ErrorCode: "PRIMARY_SIGN_FAILED"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wantBody, err := os.ReadFile("../../testdata/mpc-co-signer-http/v1/" + tt.fixture)
			if err != nil {
				t.Fatal(err)
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotBody, readErr := io.ReadAll(r.Body)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if !bytes.Equal(gotBody, wantBody) {
					t.Fatalf("result body = %s, want %s", gotBody, wantBody)
				}
				_, _ = w.Write([]byte(tt.response))
			}))
			defer srv.Close()
			client, _ := newTestClient(t, srv.URL)
			if err := client.PostResult(context.Background(), "intent-125", tt.result); err != nil {
				t.Fatalf("PostResult() error = %v", err)
			}
		})
	}
}

func TestPostResultClassifiesAuthoritativeTerminalConflict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"authoritativeStatus":"FAILED","httpStatus":409,"outcome":"TERMINAL_CONFLICT"}`))
	}))
	defer srv.Close()
	client, _ := newTestClient(t, srv.URL)
	err := client.PostResult(context.Background(), "intent-125", IntentResult{Status: "COMPLETED"})
	if !errors.Is(err, ErrTerminalConflict) {
		t.Fatalf("PostResult() error = %v, want ErrTerminalConflict", err)
	}
	var conflict *ResultConflictError
	if !errors.As(err, &conflict) || conflict.AuthoritativeStatus != "FAILED" {
		t.Fatalf("PostResult() conflict = %#v", conflict)
	}
}

func TestPostResultRejectsMalformedAuthoritativeOutcome(t *testing.T) {
	for _, body := range []string{
		`{"authoritativeStatus":"COMPLETED","httpStatus":200,"outcome":"ACCEPTED","unknown":true}`,
		`{"authoritativeStatus":"COMPLETED","httpStatus":200,"outcome":"TERMINAL_CONFLICT"}`,
		`{"authoritativeStatus":"TIMED_OUT","httpStatus":200,"outcome":"ACCEPTED"}`,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
		client, _ := newTestClient(t, srv.URL)
		if err := client.PostResult(context.Background(), "intent-125", IntentResult{Status: "COMPLETED"}); err == nil {
			t.Fatalf("PostResult() accepted malformed outcome %s", body)
		}
		srv.Close()
	}
}

func TestPostResultRejectsDKGResultFamily(t *testing.T) {
	client, _ := newTestClient(t, "https://example.invalid")
	err := client.PostResult(context.Background(), "intent-123", IntentResult{Status: "COMPLETED", DkgMaterial: &DkgParticipantResult{PartyID: "co-signer-primary"}})
	if err == nil {
		t.Fatal("PostResult() accepted DKG result material")
	}
}

func TestPostTerminalResultPerformsOneExactAttemptWithoutGenericRetry(t *testing.T) {
	body := []byte(`{"terminalResult":{"status":"FAILED"},"terminalResultFingerprint":"fingerprint"}`)
	var attempts int
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if r.URL.Path != "/api/v1/co-signer/intents/dkg/intent-42/result" {
			t.Fatalf("request path = %q", r.URL.Path)
		}
		gotBody, _ = io.ReadAll(r.Body)
		if got := r.Header.Get("X-Idempotency-Key"); got != "intent-42" {
			t.Errorf("X-Idempotency-Key = %q", got)
		}
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	client, _ := newTestClient(t, srv.URL)
	response, err := client.PostTerminalResult(context.Background(), "intent-42", body)
	if err != nil {
		t.Fatalf("PostTerminalResult() error = %v", err)
	}
	if attempts != 1 || response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("attempts = %d, response = %+v", attempts, response)
	}
	if !bytes.Equal(gotBody, body) {
		t.Fatalf("body = %q, want exact %q", gotBody, body)
	}
}

func TestPostTerminalResultBoundsOversizedAuthoritativeResponse(t *testing.T) {
	oversized := bytes.Repeat([]byte("x"), 16<<10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(oversized)
	}))
	defer srv.Close()

	client, _ := newTestClient(t, srv.URL)
	response, err := client.PostTerminalResult(context.Background(), "intent-42", []byte(`{}`))
	if err != nil {
		t.Fatalf("PostTerminalResult() error = %v", err)
	}
	if len(response.Body) > 8<<10 {
		t.Fatalf("response body bytes = %d, want bounded to at most 8192", len(response.Body))
	}
	if response.ProtocolViolation == "" {
		t.Fatal("oversized response did not carry protocol classification")
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

func TestClaimIntentDecodesHDIntentPayload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"httpStatus":200,
			"intentId":"intent-1",
			"sessionId":"session-1",
			"type":"SIGN",
			"status":"CLAIMED",
			"deadline":"2027-04-16T12:00:00.000Z",
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
		}`))
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
		payload.DigestType != "transaction_hash" ||
		payload.HashAlgorithm != "sha256" ||
		payload.SigningPayloadType != "ethereum_transaction" ||
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

	_, err := client.ClaimIntent(context.Background(), "DKG", "intent-1")
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
