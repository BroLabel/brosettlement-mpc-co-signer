package worker

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/monolith"
)

// Exercise the HTTP claim decoder and worker together: a policy rejection must
// reach terminal delivery without re-claiming or entering the MPC protocol.
func TestClaimPolicyRejectionDeliversWithoutReclaim(t *testing.T) {
	for _, fixture := range []string{"sign-claim-eth.json", "sign-claim-erc20.json"} {
		t.Run(fixture, func(t *testing.T) {
			raw, err := os.ReadFile("../../testdata/ethereum-wallet-v1/" + fixture)
			if err != nil {
				t.Fatal(err)
			}
			var wire map[string]any
			if err := json.Unmarshal(raw, &wire); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
			wire["deadline"] = deadline.Format(time.RFC3339)
			wire["session"].(map[string]any)["deadline"] = wire["deadline"]
			wire["payload"].(map[string]any)["policyContext"].(map[string]any)["toAddress"] = "0x222222222222222222222222222222222222222A"
			raw, _ = json.Marshal(wire)
			var claim monolith.ClaimResult
			if err := json.Unmarshal(raw, &claim); err != nil {
				t.Fatal(err)
			}
			claim.DeadlineRaw = wire["deadline"].(string)
			intent := claim.Intent()
			intent.Payload = monolith.IntentPayload{OrgID: intent.Payload.OrgID, KeyID: intent.Payload.KeyID}
			var claims, posts atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				claims.Add(1)
				_, _ = w.Write(raw)
			}))
			defer srv.Close()
			_, privateKey, _ := ed25519.GenerateKey(nil)
			httpClient := monolith.New(srv.URL, "test-key", privateKey, time.Second)
			handler := &alertCapturingHandler{}
			permits := newSchedulerPermits(1, nil)
			lease := permits.tryAcquireSIGN()
			client := &stubPendingClient{claimFunc: httpClient.ClaimIntent}
			client.pollFunc = func(context.Context, string, uint64) (monolith.MessagesResult, error) {
				return monolith.MessagesResult{Session: claim.Session}, nil
			}
			client.postFunc = func(_ context.Context, id string, result monolith.IntentResult) error {
				if id != intent.IntentID || result.Status != "FAILED" || result.ErrorCode != ErrorCodeInvalidIntent {
					t.Errorf("unexpected result: id=%s result=%+v", id, result)
				}
				if len(permits.general.slots) != 1 {
					t.Error("permit released before delivery")
				}
				found := false
				for _, record := range handler.Records() {
					attrs := map[string]string{}
					for _, attr := range record.attrs {
						attrs[attr.Key] = attr.Value.String()
					}
					if attrs["stage"] == "claim_validation" {
						found = attrs["event"] == "claim_validation_rejected" && attrs["intent_id"] == intent.IntentID && attrs["session_id"] == intent.SessionID && attrs["field"] == "payload.policyContext.toAddress" && attrs["reason"] == "noncanonical_address" && attrs["error_code"] == "INVALID_INTENT"
						if strings.Contains(record.message, "222222") {
							t.Error("diagnostic exposes address")
						}
					}
				}
				if !found {
					t.Error("safe structured rejection missing before POST")
				}
				if posts.Add(1) == 1 {
					return errors.New("terminal response lost")
				}
				return nil
			}
			runner := &countingSignRunner{}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			var dispatched atomic.Int32
			runSessionWithPermits(ctx, intent, client, runner, nil, nil, primaryPartyID, time.Millisecond, lease, slog.New(handler), func() { dispatched.Add(1) })
			if claims.Load() != 1 || posts.Load() != 2 || runner.calls != 0 {
				t.Fatalf("claims=%d posts=%d MPC=%d; want 1, 2, 0", claims.Load(), posts.Load(), runner.calls)
			}
			if dispatched.Load() != 1 || len(permits.general.slots) != 0 {
				t.Fatalf("dispatch=%d retained permits=%d", dispatched.Load(), len(permits.general.slots))
			}
			rejected := 0
			for _, record := range handler.Records() {
				for _, attr := range record.attrs {
					if attr.Key == "stage" && attr.Value.String() == "claim_validation" {
						rejected++
					}
				}
			}
			if rejected != 1 {
				t.Fatalf("rejection logs=%d, want one", rejected)
			}
		})
	}
}

func TestClaimRejectionDoesNotAdoptUnverifiedIdentity(t *testing.T) {
	for _, mismatch := range []string{"intent", "session", "deadline", "org", "key", "lifecycle", "status"} {
		t.Run(mismatch, func(t *testing.T) {
			raw, err := os.ReadFile("../../testdata/ethereum-wallet-v1/sign-claim-eth.json")
			if err != nil {
				t.Fatal(err)
			}
			var wire map[string]any
			if err := json.Unmarshal(raw, &wire); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
			wire["deadline"] = deadline.Format(time.RFC3339)
			wire["session"].(map[string]any)["deadline"] = wire["deadline"]
			raw, _ = json.Marshal(wire)
			var original monolith.ClaimResult
			if err := json.Unmarshal(raw, &original); err != nil {
				t.Fatal(err)
			}
			intent := original.Intent()
			intent.Payload = monolith.IntentPayload{OrgID: intent.Payload.OrgID, KeyID: intent.Payload.KeyID}
			wire["payload"].(map[string]any)["policyContext"].(map[string]any)["toAddress"] = "0x222222222222222222222222222222222222222A"
			switch mismatch {
			case "intent":
				wire["intentId"] = "unrelated-intent"
			case "session":
				wire["sessionId"] = "unrelated-session"
				wire["session"].(map[string]any)["sessionId"] = "unrelated-session"
			case "deadline":
				wire["deadline"] = deadline.Add(time.Second).Format(time.RFC3339)
				wire["session"].(map[string]any)["deadline"] = wire["deadline"]
			case "org":
				wire["payload"].(map[string]any)["orgId"] = "unrelated-org"
			case "key":
				wire["payload"].(map[string]any)["keyId"] = "unrelated-key"
			case "lifecycle":
				wire["session"].(map[string]any)["sessionId"] = "unrelated-session"
			case "status":
				wire["status"] = "PENDING"
			}
			raw, _ = json.Marshal(wire)
			var claims, posts atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { claims.Add(1); _, _ = w.Write(raw) }))
			defer srv.Close()
			_, privateKey, _ := ed25519.GenerateKey(nil)
			httpClient := monolith.New(srv.URL, "test-key", privateKey, time.Second)
			handler := &alertCapturingHandler{}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			client := &stubPendingClient{claimFunc: func(c context.Context, kind, id string) (monolith.ClaimResult, error) {
				result, err := httpClient.ClaimIntent(c, kind, id)
				if claims.Load() >= 2 {
					cancel()
				}
				return result, err
			}}
			client.postFunc = func(_ context.Context, id string, _ monolith.IntentResult) error {
				posts.Add(1)
				t.Errorf("unverified claim posted to %s", id)
				return nil
			}
			runner := &countingSignRunner{}
			runSessionWithPermits(ctx, intent, client, runner, nil, nil, primaryPartyID, time.Millisecond, nil, slog.New(handler), nil)
			if posts.Load() != 0 || runner.calls != 0 {
				t.Fatalf("unverified binding posted or signed: posts=%d MPC=%d", posts.Load(), runner.calls)
			}
			if claims.Load() < 2 {
				t.Fatalf("unverified claim lost uncertainty recovery: claims=%d", claims.Load())
			}
			found := false
			rejected := 0
			for _, record := range handler.Records() {
				attrs := map[string]string{}
				for _, attr := range record.attrs {
					attrs[attr.Key] = attr.Value.String()
				}
				if attrs["stage"] == "claim_validation" && attrs["intent_id"] == intent.IntentID && attrs["session_id"] == intent.SessionID {
					found = true
					rejected++
				}
			}
			if !found {
				t.Fatal("unverified identity lacks immediate diagnostic using original correlation")
			}
			if rejected != 1 {
				t.Fatalf("unverified identity rejection logs=%d, want one", rejected)
			}
		})
	}
}
