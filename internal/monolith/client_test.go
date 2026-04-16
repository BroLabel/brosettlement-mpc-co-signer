package monolith

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
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

func TestClaimIntentSendsNoBody(t *testing.T) {
	var gotContentLength int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentLength = r.ContentLength
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
}

func TestPostMessageAddsSigningAndIdempotencyHeaders(t *testing.T) {
	var gotSignature, gotBodyHash, gotIdempotency string
	var signatureIsValid bool
	_, pub := newTestClient(t, "https://example.test")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("ReadAll() error = %v", err)
		}

		gotSignature = r.Header.Get("X-Api-Signature")
		gotBodyHash = r.Header.Get("X-Api-Body-Hash")
		gotIdempotency = r.Header.Get("X-Idempotency-Key")

		ts := r.Header.Get("X-Api-Timestamp")
		canonical := strings.Join([]string{
			strings.ToUpper(r.Method),
			r.URL.Path,
			gotBodyHash,
			ts,
		}, "\n")
		sigBytes, err := base64.StdEncoding.DecodeString(gotSignature)
		if err == nil {
			signatureIsValid = ed25519.Verify(pub, []byte(canonical), sigBytes)
		}

		wantBodyHash := sha256.Sum256(body)
		if gotBodyHash != hex.EncodeToString(wantBodyHash[:]) {
			t.Fatalf("X-Api-Body-Hash = %q, want %q", gotBodyHash, hex.EncodeToString(wantBodyHash[:]))
		}
		_, _ = w.Write([]byte(`{"deliverySeq":17}`))
	}))
	defer srv.Close()

	client, _ := newTestClient(t, srv.URL)
	err := client.PostMessage(context.Background(), "session-1", OutboundFrame{
		MessageID: "msg-1",
		Seq:       9,
		Round:     2,
		ToPartyID: "party-2",
		Payload:   []byte("abc"),
	})
	if err != nil {
		t.Fatalf("PostMessage() error = %v", err)
	}
	if gotSignature == "" || gotBodyHash == "" || gotIdempotency != "msg-1" {
		t.Fatalf("missing required headers signature=%q bodyHash=%q idempotency=%q", gotSignature, gotBodyHash, gotIdempotency)
	}
	if !signatureIsValid {
		t.Fatal("signature validation failed")
	}
}

func TestGetMessagesDecodesDeliverySeqSeparatelyFromProtocolSeq(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("afterSeq") != "10" {
			t.Fatalf("afterSeq query = %q, want 10", r.URL.Query().Get("afterSeq"))
		}
		_, _ = w.Write([]byte(`{"messages":[{"deliverySeq":11,"seq":7,"messageId":"msg-1","round":2,"fromPartyId":"party-2","toPartyId":"party-1","payload":"YWJj"}]}`))
	}))
	defer srv.Close()

	client, _ := newTestClient(t, srv.URL)
	msgs, err := client.GetMessages(context.Background(), "session-1", 10)
	if err != nil {
		t.Fatalf("GetMessages() error = %v", err)
	}
	if len(msgs) != 1 || msgs[0].DeliverySeq != 11 || msgs[0].Seq != 7 {
		t.Fatalf("unexpected messages = %+v", msgs)
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
