package monolith

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestMessagesRejectsMissingLifecycle(t *testing.T) {
	for _, body := range []string{`{"messages":[]}`, `{"session":null,"messages":[]}`, `[]`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
			defer server.Close()
			client, _ := newTestClient(t, server.URL)
			if _, err := client.GetMessages(context.Background(), "session-1", 0); err == nil {
				t.Fatal("poll accepted missing lifecycle")
			}
		})
	}
}

func TestClaimRequiresDeadlineEvenWithLegacyExpiry(t *testing.T) {
	for _, kind := range []string{"SIGN", "DKG"} {
		t.Run(kind, func(t *testing.T) {
			file := "claim-response.json"
			if kind == "SIGN" {
				file = "sign-claim-response.json"
			}
			raw, err := os.ReadFile("../../testdata/mpc-co-signer-http/v1/" + file)
			if err != nil {
				t.Fatal(err)
			}
			var body map[string]any
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatal(err)
			}
			body["expiresAt"] = body["deadline"]
			delete(body, "deadline")
			raw, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(raw) }))
			defer server.Close()
			client, _ := newTestClient(t, server.URL)
			if _, err := client.ClaimIntent(context.Background(), kind, body["intentId"].(string)); err == nil {
				t.Fatal("legacy expiry replaced mandatory deadline")
			}
		})
	}
}

func TestDKGClaimRequiresTopLevelSessionIdentity(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/mpc-co-signer-http/v1/claim-response.json")
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	delete(body, "sessionId")
	raw, err = json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(raw) }))
	defer server.Close()
	client, _ := newTestClient(t, server.URL)
	if _, err := client.ClaimIntent(context.Background(), "DKG", body["intentId"].(string)); err == nil {
		t.Fatal("DKG claim accepted absent sessionId")
	}
}

func TestClientRejectsMalformedLifecycleAcrossDiscoveryAndClaim(t *testing.T) {
	for _, file := range []string{"listing-response.json", "claim-response.json", "sign-claim-response.json"} {
		for _, mutation := range []string{"missing", "null", "unknown", "deadline mismatch", "start at deadline", "pending with start", "null SIGN expiry"} {
			t.Run(file+"/"+mutation, func(t *testing.T) {
				raw, err := os.ReadFile("../../testdata/mpc-co-signer-http/v1/" + file)
				if err != nil {
					t.Fatal(err)
				}
				var body map[string]any
				if err := json.Unmarshal(raw, &body); err != nil {
					t.Fatal(err)
				}
				target := body
				if file == "listing-response.json" {
					target = body["ownClaimedSign"].([]any)[0].(map[string]any)
				}
				session := target["session"].(map[string]any)
				switch mutation {
				case "missing":
					delete(target, "session")
				case "null":
					target["session"] = nil
				case "unknown":
					session["status"] = "UNKNOWN"
				case "deadline mismatch":
					session["deadline"] = "2099-01-01T00:00:00Z"
				case "start at deadline":
					session["startedAt"] = session["deadline"]
				case "pending with start":
					session["status"] = "PENDING"
					session["startedAt"] = "2026-07-29T00:00:00Z"
				case "null SIGN expiry":
					session["status"] = "RUNNING"
					session["startedAt"] = "2026-07-29T00:00:00Z"
					session["executionExpiresAt"] = nil
				}
				raw, err = json.Marshal(body)
				if err != nil {
					t.Fatal(err)
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(raw) }))
				defer server.Close()
				client, _ := newTestClient(t, server.URL)
				if file == "listing-response.json" {
					_, err = client.ListActionableIntents(context.Background())
				} else {
					kind := "DKG"
					if strings.HasPrefix(file, "sign-") {
						kind = "SIGN"
					}
					_, err = client.ClaimIntent(context.Background(), kind, target["intentId"].(string))
				}
				if file == "claim-response.json" && mutation == "null SIGN expiry" {
					if err != nil {
						t.Fatalf("DKG incorrectly required SIGN execution expiry: %v", err)
					}
					return
				}
				if err == nil {
					t.Fatal("accepted malformed lifecycle")
				}
			})
		}
	}
}

func TestMessagesOneBoundedAttemptAndCancellation(t *testing.T) {
	for _, mode := range []string{"server failure", "cancellation", "request deadline"} {
		t.Run(mode, func(t *testing.T) {
			calls := make(chan struct{}, 4)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls <- struct{}{}
				if mode == "server failure" {
					w.WriteHeader(503)
					return
				}
				<-r.Context().Done()
			}))
			defer server.Close()
			client, _ := newTestClient(t, server.URL)
			client.httpClient.Timeout = 30 * time.Second
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			started := time.Now()
			go func() { _, err := client.GetMessages(ctx, "session-1", 0); done <- err }()
			<-calls
			if mode == "cancellation" {
				cancel()
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("request unexpectedly succeeded")
				}
			case <-time.After(time.Second):
				t.Fatal("poll inherited 30s HTTP retry budget")
			}
			if len(calls) != 0 {
				t.Fatal("poll performed nested retries")
			}
			if time.Since(started) > time.Second {
				t.Fatal("poll exceeded bounded request budget")
			}
		})
	}
}

func TestMessagesRejectsInvalidLifecycle(t *testing.T) {
	for name, mutate := range map[string]func(map[string]any){
		"missing start":        func(s map[string]any) { delete(s, "startedAt") },
		"missing expiry":       func(s map[string]any) { delete(s, "executionExpiresAt") },
		"missing deadline":     func(s map[string]any) { delete(s, "deadline") },
		"null deadline":        func(s map[string]any) { s["deadline"] = nil },
		"zero deadline":        func(s map[string]any) { s["deadline"] = "0001-01-01T00:00:00Z" },
		"malformed start":      func(s map[string]any) { s["startedAt"] = "yesterday" },
		"null running start":   func(s map[string]any) { s["startedAt"] = nil },
		"pending with start":   func(s map[string]any) { s["status"] = "PENDING" },
		"unknown status":       func(s map[string]any) { s["status"] = "CREATED" },
		"wrong session":        func(s map[string]any) { s["sessionId"] = "other" },
		"nonpositive start":    func(s map[string]any) { s["startedAt"] = "1970-01-01T00:00:00Z" },
		"equal start deadline": func(s map[string]any) { s["startedAt"] = s["deadline"] },
		"after deadline":       func(s map[string]any) { s["startedAt"] = "2026-07-31T00:00:00Z" },
		"wrong expiry":         func(s map[string]any) { s["executionExpiresAt"] = "2026-07-29T00:04:59Z" },
		"extra field":          func(s map[string]any) { s["executionMode"] = "legacy" },
	} {
		t.Run(name, func(t *testing.T) {
			s := map[string]any{"sessionId": "session-1", "status": "RUNNING", "startedAt": "2026-07-29T00:00:00Z", "deadline": "2026-07-30T00:00:00Z", "executionExpiresAt": "2026-07-29T00:05:00Z"}
			mutate(s)
			body, _ := json.Marshal(map[string]any{"session": s, "messages": []any{}})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) }))
			defer server.Close()
			client, _ := newTestClient(t, server.URL)
			if _, err := client.GetMessages(context.Background(), "session-1", 0); err == nil {
				t.Fatal("accepted invalid lifecycle")
			}
		})
	}
}
