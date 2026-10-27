package approval

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestExternalOnly200BoundFinalResponseAndNoPlatformCredential(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
		body string
		ok   bool
	}{
		{"pending", 200, `"status":"PENDING","reason":null,"decisionReference":null`, true},
		{"approved", 200, `"status":"APPROVED","reason":null,"decisionReference":"reference"`, true},
		{"rejected", 200, `"status":"REJECTED","reason":{"code":"DENIED","text":"Policy denied"},"decisionReference":null`, true},
		{"malformed", 200, `"status":"APPROVED","reason":null,"decisionReference":null,"extra":true`, false},
		{"reject no reason", 200, `"status":"REJECTED","reason":null,"decisionReference":null`, false},
		{"unauthenticated", 401, "", false}, {"server error", 503, "", false}, {"accepted status", 202, "", false}, {"redirect", 302, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item := pendingItem(t, "request")
			var calls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer dedicated-secret" || r.Header.Get("X-Api-Key-Id") != "" || r.Header.Get("Idempotency-Key") != "request:"+item.AuthorizationHash {
					t.Error("wrong outbound credentials/binding")
				}
				raw, _ := io.ReadAll(r.Body)
				var body map[string]json.RawMessage
				if json.Unmarshal(raw, &body) != nil || len(body) != 5 {
					t.Error("wrong immutable request")
				}
				w.Header().Set("Location", "/redirect")
				w.WriteHeader(tc.code)
				io.WriteString(w, `{"version":1,"orgId":"org-1","requestId":"request","authorizationHash":"`+item.AuthorizationHash+`",`+tc.body+`}`)
			}))
			defer server.Close()
			c := testConfig(t)
			c.Mode = "external"
			c.Endpoint = server.URL
			c.BearerToken = "dedicated-secret"
			o, e := NewExternal(c, server.Client())
			if e != nil {
				t.Fatal(e)
			}
			_, e = o.Evaluate(context.Background(), item)
			if (e == nil) != tc.ok {
				t.Fatalf("result %v", e)
			}
			if calls.Load() != 1 {
				t.Fatalf("hidden retry: %d", calls.Load())
			}
		})
	}
}
func TestExternalTransportCannotReplayIdempotentPOSTAfterLostResponse(t *testing.T) {
	item := pendingItem(t, "request")
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		io.Copy(io.Discard, r.Body)
		if n == 2 {
			conn, _, e := w.(http.Hijacker).Hijack()
			if e != nil {
				t.Error(e)
				return
			}
			conn.Close()
			return
		}
		io.WriteString(w, `{"version":1,"orgId":"org-1","requestId":"request","authorizationHash":"`+item.AuthorizationHash+`","status":"PENDING","reason":null,"decisionReference":null}`)
	}))
	defer server.Close()
	c := testConfig(t)
	c.Mode = "external"
	c.Endpoint = server.URL
	c.BearerToken = "dedicated"
	o, e := NewExternal(c, server.Client())
	if e != nil {
		t.Fatal(e)
	}
	if _, e = o.Evaluate(context.Background(), item); e != nil {
		t.Fatal(e)
	}
	if _, e = o.Evaluate(context.Background(), item); e == nil {
		t.Fatal("lost response unexpectedly retried")
	}
	if calls.Load() != 2 {
		t.Fatalf("transport replayed admission: %d", calls.Load())
	}
}
func TestExternalAttemptTimeoutDoesNotCreateFinalDecision(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-time.After(100 * time.Millisecond):
		}
	}))
	defer server.Close()
	c := testConfig(t)
	c.Mode = "external"
	c.Endpoint = server.URL
	c.BearerToken = "dedicated"
	c.Timeout = 20 * time.Millisecond
	o, e := NewExternal(c, server.Client())
	if e != nil {
		t.Fatal(e)
	}
	start := time.Now()
	if _, e = o.Evaluate(context.Background(), pendingItem(t, "request")); e == nil {
		t.Fatal("timeout accepted")
	}
	if time.Since(start) > time.Second {
		t.Fatal("unbounded HTTP attempt")
	}
}

func TestExternalRejectsUnauthenticatedTLSAndOversizedResponse(t *testing.T) {
	var calls atomic.Int32
	item := pendingItem(t, "request")
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.WriteString(w, strings.Repeat("x", 65537))
	}))
	defer server.Close()
	c := testConfig(t)
	c.Mode = "external"
	c.Endpoint = server.URL
	c.BearerToken = "dedicated"
	o, e := NewExternal(c, nil)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = o.Evaluate(context.Background(), item); e == nil || calls.Load() != 0 {
		t.Fatal("untrusted organization TLS accepted")
	}
	o, e = NewExternal(c, server.Client())
	if e != nil {
		t.Fatal(e)
	}
	if _, e = o.Evaluate(context.Background(), item); e == nil {
		t.Fatal("oversized organization response accepted")
	}
}

func TestExternalBindsOrganizationToValidatedAuthorization(t *testing.T) {
	for _, scenario := range []string{"matching", "wrong-response-org", "altered-authorization"} {
		t.Run(scenario, func(t *testing.T) {
			item := pendingItemForOrg(t, "request", "org-2")
			if scenario == "altered-authorization" {
				item.AuthorizationHash = strings.Repeat("0", 64)
			}
			var calls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var body struct {
					OrgID string `json:"orgId"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.OrgID != "org-2" {
					t.Error("outbound organization did not match authorization")
				}
				orgID := "org-2"
				if scenario == "wrong-response-org" {
					orgID = "org-1"
				}
				json.NewEncoder(w).Encode(map[string]any{"version": 1, "orgId": orgID, "requestId": item.RequestID, "authorizationHash": item.AuthorizationHash, "status": "APPROVED", "reason": nil, "decisionReference": nil})
			}))
			defer server.Close()
			cfg := testConfig(t)
			cfg.Mode, cfg.Endpoint, cfg.BearerToken = "external", server.URL, "dedicated"
			organization, err := NewExternal(cfg, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			_, err = organization.Evaluate(context.Background(), item)
			if (err == nil) != (scenario == "matching") {
				t.Fatalf("unexpected decision result: %v", err)
			}
			if scenario == "altered-authorization" && calls.Load() != 0 {
				t.Fatal("sent invalid authorization to organization")
			}
		})
	}
}
