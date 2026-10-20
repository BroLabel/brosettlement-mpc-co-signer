package monolith

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	wire "github.com/BroLabel/brosettlement-mpc-co-signer/internal/contract/approval"
)

func TestPendingApprovalsRejectsInvalidPagesAtHTTPBoundary(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/approval-v1/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Vectors []wire.Vector `json:"vectors"`
	}
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	type testCase struct {
		name, body, org string
		ok              bool
	}
	var cases []testCase
	for _, v := range corpus.Vectors {
		if v.Kind != "page" {
			continue
		}
		body, err := base64.StdEncoding.DecodeString(v.InputBase64)
		if err != nil {
			t.Fatal(err)
		}
		var binding struct {
			OrgID string `json:"orgId"`
		}
		if err := json.Unmarshal(v.Binding, &binding); err != nil {
			t.Fatal(err)
		}
		cases = append(cases, testCase{v.ID, string(body), binding.OrgID, v.Expected == "ACCEPT"})
		if v.ID == "APPROVAL-V1-010-end-page" {
			cases = append(cases,
				testCase{"unknown field", string(body[:len(body)-1]) + `,"unknown":true}`, binding.OrgID, false},
				testCase{"duplicate key", strings.Replace(string(body), `"version":1`, `"version":1,"version":1`, 1), binding.OrgID, false},
				testCase{"changed hash", strings.Replace(string(body), `"authorizationHash":"`, `"authorizationHash":"0`, 1), binding.OrgID, false},
			)
		}
	}
	if len(cases) != 6 {
		t.Fatalf("expected page boundary coverage, got %d cases", len(cases))
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			client, _ := newTestClient(t, server.URL)
			if _, err := client.PendingApprovals(context.Background(), "", tc.org); (err == nil) != tc.ok {
				t.Fatalf("accepted=%t want=%t err=%v", err == nil, tc.ok, err)
			}
		})
	}
}

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
