package monolith

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestPostResultAddsIdempotencyHeaderFromIntentID(t *testing.T) {
	var gotIdempotency string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/co-signer/intents/sign/intent-42/result" {
			t.Fatalf("request path = %q", r.URL.Path)
		}
		gotIdempotency = r.Header.Get("X-Idempotency-Key")
		_, _ = w.Write([]byte(`{"authoritativeStatus":"COMPLETED","httpStatus":200,"outcome":"ACCEPTED"}`))
	}))
	defer srv.Close()

	client, _ := newTestClient(t, srv.URL)
	err := client.PostResult(context.Background(), "intent-42", IntentResult{
		Status: "COMPLETED",
	})
	if err != nil {
		t.Fatalf("PostResult() error = %v", err)
	}
	if gotIdempotency != "intent-42" {
		t.Fatalf("X-Idempotency-Key = %q, want %q", gotIdempotency, "intent-42")
	}
}

func TestPostResultPublishesMinimalCompletedAndFailedSignResults(t *testing.T) {
	tests := []struct {
		name, fixture, response string
		result                  IntentResult
	}{
		{name: "completed", fixture: "sign-terminal-completed-request.json", response: `{"authoritativeStatus":"COMPLETED","httpStatus":200,"outcome":"ACCEPTED"}`, result: IntentResult{Status: "COMPLETED"}},
		{name: "failed", fixture: "sign-terminal-failed-request.json", response: `{"authoritativeStatus":"FAILED","httpStatus":200,"outcome":"EXACT_REPLAY"}`, result: IntentResult{Status: "FAILED", ErrorCode: "PRIMARY_SIGN_FAILED"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wantBody, err := os.ReadFile("../../testdata/mpc-co-signer-http/v1/" + tt.fixture)
			if err != nil {
				t.Fatal(err)
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotBody, readErr := io.ReadAll(r.Body)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if !bytes.Equal(gotBody, wantBody) {
					t.Fatalf("result body = %s, want %s", gotBody, wantBody)
				}
				_, _ = w.Write([]byte(tt.response))
			}))
			defer srv.Close()
			client, _ := newTestClient(t, srv.URL)
			if err := client.PostResult(context.Background(), "intent-125", tt.result); err != nil {
				t.Fatalf("PostResult() error = %v", err)
			}
		})
	}
}

func TestPostResultClassifiesAuthoritativeTerminalConflict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"authoritativeStatus":"FAILED","httpStatus":409,"outcome":"TERMINAL_CONFLICT"}`))
	}))
	defer srv.Close()
	client, _ := newTestClient(t, srv.URL)
	err := client.PostResult(context.Background(), "intent-125", IntentResult{Status: "COMPLETED"})
	if !errors.Is(err, ErrTerminalConflict) {
		t.Fatalf("PostResult() error = %v, want ErrTerminalConflict", err)
	}
	var conflict *ResultConflictError
	if !errors.As(err, &conflict) || conflict.AuthoritativeStatus != "FAILED" {
		t.Fatalf("PostResult() conflict = %#v", conflict)
	}
}

func TestPostResultRejectsMalformedAuthoritativeOutcome(t *testing.T) {
	for _, body := range []string{
		`{"authoritativeStatus":"COMPLETED","httpStatus":200,"outcome":"ACCEPTED","unknown":true}`,
		`{"authoritativeStatus":"COMPLETED","httpStatus":200,"outcome":"TERMINAL_CONFLICT"}`,
		`{"authoritativeStatus":"TIMED_OUT","httpStatus":200,"outcome":"ACCEPTED"}`,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
		client, _ := newTestClient(t, srv.URL)
		if err := client.PostResult(context.Background(), "intent-125", IntentResult{Status: "COMPLETED"}); err == nil {
			t.Fatalf("PostResult() accepted malformed outcome %s", body)
		}
		srv.Close()
	}
}

func TestPostTerminalResultPerformsOneExactAttemptWithoutGenericRetry(t *testing.T) {
	body := []byte(`{"terminalResult":{"status":"FAILED"},"terminalResultFingerprint":"fingerprint"}`)
	var attempts int
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if r.URL.Path != "/api/v1/co-signer/intents/dkg/intent-42/result" {
			t.Fatalf("request path = %q", r.URL.Path)
		}
		gotBody, _ = io.ReadAll(r.Body)
		if got := r.Header.Get("X-Idempotency-Key"); got != "intent-42" {
			t.Errorf("X-Idempotency-Key = %q", got)
		}
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	client, _ := newTestClient(t, srv.URL)
	response, err := client.PostTerminalResult(context.Background(), "intent-42", body)
	if err != nil {
		t.Fatalf("PostTerminalResult() error = %v", err)
	}
	if attempts != 1 || response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("attempts = %d, response = %+v", attempts, response)
	}
	if !bytes.Equal(gotBody, body) {
		t.Fatalf("body = %q, want exact %q", gotBody, body)
	}
}

func TestPostTerminalResultBoundsOversizedAuthoritativeResponse(t *testing.T) {
	oversized := bytes.Repeat([]byte("x"), 16<<10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(oversized)
	}))
	defer srv.Close()

	client, _ := newTestClient(t, srv.URL)
	response, err := client.PostTerminalResult(context.Background(), "intent-42", []byte(`{}`))
	if err != nil {
		t.Fatalf("PostTerminalResult() error = %v", err)
	}
	if len(response.Body) > 8<<10 {
		t.Fatalf("response body bytes = %d, want bounded to at most 8192", len(response.Body))
	}
	if response.ProtocolViolation == "" {
		t.Fatal("oversized response did not carry protocol classification")
	}
}

func TestSignResultRetriesFrozenExactBytesWithFreshAuthentication(t *testing.T) {
	client, _ := newTestClient(t, "http://localhost")
	original := IntentResult{Status: "FAILED", ErrorCode: "WORKER_SHUTDOWN", ErrorMessage: "same failure"}
	request, err := NewSignResultRequest(original)
	if err != nil {
		t.Fatal(err)
	}
	original.ErrorMessage = "changed after serialization"
	const expected = `{"errorCode":"WORKER_SHUTDOWN","errorMessage":"same failure","status":"FAILED"}`
	var bodies, nonces []string
	client.httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, string(body))
		nonces = append(nonces, r.Header.Get("X-Api-Nonce"))
		if r.Header.Get("X-Idempotency-Key") != "intent-1" {
			t.Fatal("result request identity changed")
		}
		if deadline, ok := r.Context().Deadline(); !ok || time.Until(deadline) > time.Second {
			t.Fatal("result request lacks short budget")
		}
		if len(bodies) == 1 {
			return nil, io.ErrUnexpectedEOF
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"authoritativeStatus":"FAILED","httpStatus":200,"outcome":"EXACT_REPLAY"}`)), Header: make(http.Header)}, nil
	})
	if err := client.PostSignResult(context.Background(), "intent-1", request); err == nil {
		t.Fatal("lost response was not ambiguous")
	}
	if len(bodies) != 1 {
		t.Fatal("HTTP client hid additional retries")
	}
	if err := client.PostSignResult(context.Background(), "intent-1", request); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 || bodies[0] != expected || bodies[1] != expected {
		t.Fatalf("request bodies=%q", bodies)
	}
	if nonces[0] == nonces[1] {
		t.Fatal("authentication nonce replayed")
	}
}
