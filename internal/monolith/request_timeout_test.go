package monolith

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSessionRequestsAllowResponsesWithinConfiguredTimeout(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		call func(context.Context, *Client) error
	}{
		{
			name: "claim",
			body: `{"httpStatus":200,"status":"CLAIMED","type":"DKG","sessionId":"session-1","deadline":"2026-04-16T12:00:00Z","session":{"sessionId":"session-1","status":"PENDING","startedAt":null,"executionExpiresAt":null,"deadline":"2026-04-16T12:00:00Z"}}`,
			call: func(ctx context.Context, c *Client) error {
				_, err := c.ClaimIntent(ctx, "DKG", "intent-1")
				return err
			},
		},
		{
			name: "poll",
			body: `{"session":{"sessionId":"session-1","status":"PENDING","startedAt":null,"executionExpiresAt":null,"deadline":"2026-04-16T12:00:00Z"},"messages":[]}`,
			call: func(ctx context.Context, c *Client) error {
				_, err := c.GetMessages(ctx, "session-1", 0)
				return err
			},
		},
		{
			name: "result",
			body: `{"authoritativeStatus":"COMPLETED","httpStatus":200,"outcome":"ACCEPTED"}`,
			call: func(ctx context.Context, c *Client) error {
				return c.PostResult(ctx, "intent-1", IntentResult{Status: "COMPLETED"})
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				timer := time.NewTimer(600 * time.Millisecond)
				defer timer.Stop()
				select {
				case <-timer.C:
					_, _ = w.Write([]byte(tc.body))
				case <-r.Context().Done():
				}
			}))
			defer srv.Close()
			client, _ := newTestClient(t, srv.URL)
			client.httpClient.Timeout = 2 * time.Second
			if err := tc.call(context.Background(), client); err != nil {
				t.Fatalf("response within configured HTTP timeout was rejected: %v", err)
			}
		})
	}
}

func TestGetMessagesHonorsEarlierParentDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()
	client, _ := newTestClient(t, srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := client.GetMessages(ctx, "session-1", 0)
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("request did not stop at parent deadline: request=%v parent=%v", err, ctx.Err())
	}
}
