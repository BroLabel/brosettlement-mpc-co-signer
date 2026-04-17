package health_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/health"
)

func TestHealthOK(t *testing.T) {
	dir := t.TempDir()
	h := health.NewHandler("0.1.0", dir)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200", rec.Code)
	}

	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("got status=%q, want ok", body["status"])
	}
	if body["ready"] != true {
		t.Errorf("expected ready=true")
	}
}

func TestHealthFailMissingDir(t *testing.T) {
	h := health.NewHandler("0.1.0", "/nonexistent/path/shares")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("got status %d, want 503", rec.Code)
	}

	var body map[string]any
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if body["status"] != "fail" {
		t.Errorf("got status=%q, want fail", body["status"])
	}
}

func TestHealthOnlyGetAllowed(t *testing.T) {
	dir := t.TempDir()
	h := health.NewHandler("0.1.0", dir)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/health", nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("got status %d, want 405", rec.Code)
	}
}
