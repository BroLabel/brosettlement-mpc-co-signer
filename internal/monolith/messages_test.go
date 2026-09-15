package monolith

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

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
	err = client.PostMessage(context.Background(), "20000000-0000-4000-8000-000000000003", OutboundFrame{
		AuthenticatedPartyID:  "co-signer-primary",
		Broadcast:             false,
		FromPartyID:           "co-signer-primary",
		IntentID:              "30000000-0000-4000-8000-000000000003",
		MessageID:             "msg_0123456789abcdef",
		OrgID:                 "org-123",
		Payload:               []byte{0},
		ProtocolSeq:           1,
		Round:                 1,
		SessionID:             "20000000-0000-4000-8000-000000000003",
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
		_, _ = w.Write([]byte(`{"session":{"sessionId":"session-1","status":"PENDING","startedAt":null,"executionExpiresAt":null,"deadline":"2026-04-16T12:00:00Z"},"messages":[{"deliverySeq":11,"protocolSeq":7,"messageId":"msg-1","round":2,"fromPartyId":"co-signer","toPartyId":"mpc-signer","payload":"YWJj"}]}`))
	}))
	defer srv.Close()

	client, pub := newTestClient(t, srv.URL)
	result, err := client.GetMessages(context.Background(), "session-1", 10)
	msgs := result.Messages
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
