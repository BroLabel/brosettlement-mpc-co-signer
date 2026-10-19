package monolith

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestApprovalTransportFreshHTTPAuthenticationAndExactSavedBody(t *testing.T) {
	body := []byte(`{"version":1,"envelopeBase64":"saved","signatureBase64":"exact"}`)
	var nonces []string
	client, pub := newTestClient(t, "")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if r.Header.Get("Authorization") != "" {
			t.Error("dedicated org token forwarded to backend")
		}
		sum := sha256.Sum256(raw)
		hash := ""
		if len(raw) > 0 {
			hash = hex.EncodeToString(sum[:])
		}
		if !verifyRequestSignature(t, r, pub, hash) {
			t.Error("invalid backend auth")
		}
		nonces = append(nonces, r.Header.Get("X-Api-Nonce"))
		if r.Method == "GET" {
			if r.URL.RawQuery != "cursor=opaque%2B%2F%3D&limit=50" || len(raw) > 0 {
				t.Error("wrong discovery query/body")
			}
			io.WriteString(w, `{"version":1,"items":[],"nextCursor":null}`)
			return
		}
		if string(raw) != string(body) {
			t.Error("receipt bytes changed")
		}
		io.WriteString(w, `{"version":1,"requestId":"request","outcome":"EXACT_REPLAY","disposition":"APPLIED"}`)
	}))
	defer server.Close()
	client.baseURL = server.URL
	if _, e := client.PendingApprovals(context.Background(), "opaque+/=", "org-1"); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 2; i++ {
		if _, e := client.SendApprovalDecision(context.Background(), "request", body); e != nil {
			t.Fatal(e)
		}
	}
	if len(nonces) != 3 || nonces[0] == nonces[1] || nonces[1] == nonces[2] {
		t.Fatal("HTTP authentication reused")
	}
}
func TestApprovalApplicationStatusBindingAndBoundedErrors(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		ok     bool
	}{
		{200, `{"version":1,"requestId":"request","outcome":"ACCEPTED","disposition":"APPLIED"}`, true},
		{409, `{"version":1,"requestId":"request","outcome":"TERMINAL_CONFLICT","disposition":"TERMINAL_CONFLICT"}`, true},
		{200, `{"version":1,"requestId":"other","outcome":"ACCEPTED","disposition":"APPLIED"}`, false},
		{200, `{"version":1,"requestId":"request","outcome":"TERMINAL_CONFLICT","disposition":"TERMINAL_CONFLICT"}`, false},
		{409, `{"version":1,"requestId":"request","outcome":"ACCEPTED","disposition":"APPLIED"}`, false},
		{503, `secret provider response`, false}, {302, ``, false}, {200, strings.Repeat("x", 4097), false},
	} {
		t.Run(tc.body[:min(15, len(tc.body))], func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Location", "/redirect")
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}))
			defer server.Close()
			c, _ := newTestClient(t, server.URL)
			_, e := c.SendApprovalDecision(context.Background(), "request", []byte(`{}`))
			if (e == nil) != tc.ok {
				t.Fatalf("unexpected result: %v", e)
			}
			if calls != 1 {
				t.Fatalf("hidden retry: %d", calls)
			}
			if e != nil && strings.Contains(e.Error(), "secret provider response") {
				t.Fatal("unsafe response logged")
			}
		})
	}
}
