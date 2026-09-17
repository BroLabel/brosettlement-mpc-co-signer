package monolith

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

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
	if claimed.IntentID != "30000000-0000-4000-8000-000000000002" ||
		claimed.SessionID != "20000000-0000-4000-8000-000000000002" ||
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
	if rediscovered.IntentID != "30000000-0000-4000-8000-000000000005" || rediscovered.SessionID != "20000000-0000-4000-8000-000000000005" || rediscovered.Type != "SIGN" ||
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
	if pending[0].IntentID != "30000000-0000-4000-8000-000000000005" || pending[0].SessionID != "20000000-0000-4000-8000-000000000005" || pending[0].Type != "SIGN" ||
		pending[0].Payload.OrgID != "org-123" || pending[0].Payload.KeyID != "mpc_key_123e4567-e89b-42d3-a456-426614174004" ||
		pending[0].ExpiresAt.Format(time.RFC3339Nano) != "2026-07-30T00:00:00Z" || len(pending[0].Payload.Digest) != 0 ||
		pending[1].IntentID != "30000000-0000-4000-8000-000000000003" || pending[1].Type != "DKG" ||
		pending[2].IntentID != "30000000-0000-4000-8000-000000000004" || pending[2].Type != "SIGN" {
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
