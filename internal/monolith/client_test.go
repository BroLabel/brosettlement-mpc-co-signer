package monolith

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
	"time"
)

const testAPIKeyID = "11111111-2222-3333-4444-555555555555"

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
