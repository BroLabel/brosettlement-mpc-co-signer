package health_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/health"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/metrics"
)

func TestHealthOK(t *testing.T) {
	dir := t.TempDir()
	h := newReadyHandler("0.1.0", dir)
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

func TestReadinessPublishesOnlyCapabilityGauges(t *testing.T) {
	metrics.Default = metrics.NewRegistry()
	state := health.NewReadiness()
	state.Set(health.Snapshot{ProcessReady: true, SigningReady: true})
	snapshot := metrics.Default.Snapshot()
	if snapshot["co_signer_process_ready"][""] != 1 || snapshot["co_signer_signing_ready"][""] != 1 || snapshot["co_signer_provisioning_ready"][""] != 0 {
		t.Fatalf("readiness gauges = %#v", snapshot)
	}
}

func TestHealthFailMissingDir(t *testing.T) {
	metrics.Default = metrics.NewRegistry()
	h := newReadyHandler("0.1.0", "/nonexistent/path/shares")
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
	metricsSnapshot := metrics.Default.Snapshot()
	if metricsSnapshot["co_signer_process_ready"][""] != 0 ||
		metricsSnapshot["co_signer_signing_ready"][""] != 0 ||
		metricsSnapshot["co_signer_provisioning_ready"][""] != 0 {
		t.Fatalf("readiness gauges do not reflect 503 response: %#v", metricsSnapshot)
	}
}

func TestHealthOnlyGetAllowed(t *testing.T) {
	dir := t.TempDir()
	h := newReadyHandler("0.1.0", dir)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/health", nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("got status %d, want 405", rec.Code)
	}
}

func TestMetricsEndpointRendersOnlySafeRegistryValues(t *testing.T) {
	metrics.Default = metrics.NewRegistry()
	metrics.Default.Inc("intent_claims_total", metrics.Labels{"type": "DKG", "outcome": "accepted"})
	rec := httptest.NewRecorder()
	newReadyHandler("0.1.0", t.TempDir()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "intent_claims_total") || strings.Contains(rec.Body.String(), "mpc_key_") {
		t.Fatalf("metrics response = status:%d body:%q", rec.Code, rec.Body.String())
	}
}

func TestHealthReportsSplitLifecycleReadiness(t *testing.T) {
	dir := t.TempDir()
	state := health.NewReadiness()
	state.Set(health.Snapshot{
		ProcessReady:       true,
		SigningReady:       true,
		ProvisioningReady:  false,
		ProvisioningReason: health.ReasonDKGTerminalUnconfirmed,
	})
	h := health.NewLifecycleHandlerWithReadinessProbes("0.1.0", dir, state, nil, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200 for signing-ready process", rec.Code)
	}
	var body struct {
		Ready              bool            `json:"ready"`
		ProcessReady       bool            `json:"processReady"`
		SigningReady       bool            `json:"signingReady"`
		ProvisioningReady  bool            `json:"provisioningReady"`
		ProvisioningReason health.Reason   `json:"provisioningReason"`
		Capabilities       map[string]bool `json:"capabilities"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.Ready || !body.ProcessReady || !body.SigningReady {
		t.Fatalf("readiness = ready:%v process:%v signing:%v, want all true", body.Ready, body.ProcessReady, body.SigningReady)
	}
	if body.ProvisioningReady || body.ProvisioningReason != health.ReasonDKGTerminalUnconfirmed {
		t.Fatalf("provisioning = ready:%v reason:%q", body.ProvisioningReady, body.ProvisioningReason)
	}
	if !body.Capabilities["sign"] || body.Capabilities["dkg"] {
		t.Fatalf("capabilities = %#v, want sign only", body.Capabilities)
	}
}

func TestHealthClosesProcessAndSigningForSystemicPrimaryProbeLoss(t *testing.T) {
	state := health.NewReadiness()
	state.Set(health.Snapshot{ProcessReady: true, SigningReady: true, ProvisioningReady: true})
	available := true
	h := health.NewLifecycleHandlerWithReadinessProbes("0.1.0", t.TempDir(), state, func() bool { return available }, nil)
	available = false
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want 503", rec.Code)
	}
	if got := state.Snapshot(); !got.ProcessReady || !got.SigningReady || !got.ProvisioningReady {
		t.Fatalf("probe incorrectly latched lifecycle state=%#v", got)
	}
	available = true
	restored := httptest.NewRecorder()
	h.ServeHTTP(restored, httptest.NewRequest(http.MethodGet, "/health", nil))
	if restored.Code != http.StatusOK {
		t.Fatalf("restored status=%d", restored.Code)
	}
}

func TestHealthRefreshesProvisioningCapabilityAfterStartup(t *testing.T) {
	state := health.NewReadiness()
	state.Set(health.Snapshot{
		ProcessReady:       true,
		SigningReady:       true,
		ProvisioningReady:  false,
		ProvisioningReason: health.ReasonProvisioningUnavailable,
	})
	provisioningReady := false
	h := health.NewLifecycleHandlerWithReadinessProbes(
		"0.1.0",
		t.TempDir(),
		state,
		func() bool { return true },
		func() bool { return provisioningReady },
	)

	provisioningReady = true
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body readinessResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.ProcessReady || !body.SigningReady || !body.ProvisioningReady {
		t.Fatalf("readiness = process:%v signing:%v provisioning:%v, want all true", body.ProcessReady, body.SigningReady, body.ProvisioningReady)
	}
	if body.ProvisioningReason != health.ReasonNone || !body.Capabilities["dkg"] {
		t.Fatalf("provisioning = reason:%q capabilities:%#v, want ready", body.ProvisioningReason, body.Capabilities)
	}
}

func TestHealthClosesOnlyProvisioningWhenDynamicCapabilityFalls(t *testing.T) {
	state := health.NewReadiness()
	state.Set(health.Snapshot{ProcessReady: true, SigningReady: true, ProvisioningReady: true})
	provisioningReady := true
	h := health.NewLifecycleHandlerWithReadinessProbes(
		"0.1.0",
		t.TempDir(),
		state,
		func() bool { return true },
		func() bool { return provisioningReady },
	)

	initial := serveReadiness(t, h)
	if !initial.ProvisioningReady {
		t.Fatal("initial provisioning readiness = false, want true")
	}
	provisioningReady = false
	got := serveReadiness(t, h)

	if !got.Ready || !got.ProcessReady || !got.SigningReady {
		t.Fatalf("readiness = ready:%v process:%v signing:%v, want true", got.Ready, got.ProcessReady, got.SigningReady)
	}
	if got.ProvisioningReady || got.ProvisioningReason != health.ReasonProvisioningUnavailable {
		t.Fatalf("provisioning = ready:%v reason:%q, want capability unavailable", got.ProvisioningReady, got.ProvisioningReason)
	}
	if !got.Capabilities["sign"] || got.Capabilities["dkg"] {
		t.Fatalf("capabilities = %#v, want sign only", got.Capabilities)
	}
}

func TestHealthDynamicProvisioningProbeCannotOverrideLifecycleGates(t *testing.T) {
	tests := []struct {
		name   string
		reason health.Reason
	}{
		{name: "terminal unconfirmed", reason: health.ReasonDKGTerminalUnconfirmed},
		{name: "capability deferred", reason: health.ReasonCapabilityDeferred},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := health.NewReadiness()
			state.Set(health.Snapshot{
				ProcessReady:       true,
				SigningReady:       true,
				ProvisioningReason: tt.reason,
			})
			probeCalls := 0
			h := health.NewLifecycleHandlerWithReadinessProbes(
				"0.1.0",
				t.TempDir(),
				state,
				func() bool { return true },
				func() bool { probeCalls++; return true },
			)

			got := serveReadiness(t, h)

			if !got.Ready || !got.ProcessReady || !got.SigningReady || got.ProvisioningReady {
				t.Fatalf("readiness = %#v, want signing-only", got)
			}
			if got.ProvisioningReason != tt.reason {
				t.Fatalf("provisioning reason = %q, want %q", got.ProvisioningReason, tt.reason)
			}
			if probeCalls != 0 {
				t.Fatalf("provisioning probe calls = %d, want lifecycle gate to bypass probe", probeCalls)
			}
		})
	}
}

func TestHealthSigningProbeOverridesDynamicProvisioningCapability(t *testing.T) {
	state := health.NewReadiness()
	state.Set(health.Snapshot{ProcessReady: true, SigningReady: true, ProvisioningReady: true})
	h := health.NewLifecycleHandlerWithReadinessProbes(
		"0.1.0",
		t.TempDir(),
		state,
		func() bool { return false },
		func() bool { return true },
	)

	got := serveReadiness(t, h)

	if got.Ready || got.ProcessReady || got.SigningReady || got.ProvisioningReady {
		t.Fatalf("readiness = %#v, want all closed", got)
	}
	if got.Capabilities["sign"] || got.Capabilities["dkg"] {
		t.Fatalf("capabilities = %#v, want all closed", got.Capabilities)
	}
}

func TestHealthDynamicProbeCannotReopenReadinessDuringShutdown(t *testing.T) {
	state := health.NewReadiness()
	state.Set(health.Snapshot{
		ProcessReady:       true,
		SigningReady:       true,
		ProvisioningReason: health.ReasonProvisioningUnavailable,
	})
	probeEntered := make(chan struct{})
	releaseProbe := make(chan struct{})
	h := health.NewLifecycleHandlerWithReadinessProbes(
		"0.1.0",
		t.TempDir(),
		state,
		func() bool { return true },
		func() bool {
			close(probeEntered)
			<-releaseProbe
			return true
		},
	)
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
		result <- rec
	}()

	select {
	case <-probeEntered:
	case rec := <-result:
		t.Fatalf("request returned before dynamic probe: %s", rec.Body.String())
	}
	state.Set(health.Snapshot{})
	close(releaseProbe)
	got := decodeReadiness(t, <-result)

	if got.Ready || got.ProcessReady || got.SigningReady || got.ProvisioningReady {
		t.Fatalf("readiness reopened during shutdown: %#v", got)
	}
}

type readinessResponse struct {
	Status             string          `json:"status"`
	Ready              bool            `json:"ready"`
	ProcessReady       bool            `json:"processReady"`
	SigningReady       bool            `json:"signingReady"`
	ProvisioningReady  bool            `json:"provisioningReady"`
	ProvisioningReason health.Reason   `json:"provisioningReason"`
	Capabilities       map[string]bool `json:"capabilities"`
}

func serveReadiness(t *testing.T, h http.Handler) readinessResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	return decodeReadiness(t, rec)
}

func decodeReadiness(t *testing.T, rec *httptest.ResponseRecorder) readinessResponse {
	t.Helper()
	var body readinessResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode health response: %v", err)
	}
	return body
}

func TestHealthProcessReadinessClosesDuringShutdown(t *testing.T) {
	dir := t.TempDir()
	state := health.NewReadiness()
	state.Set(health.Snapshot{ProcessReady: true, SigningReady: true, ProvisioningReady: true})
	state.Set(health.Snapshot{})

	rec := httptest.NewRecorder()
	health.NewLifecycleHandlerWithReadinessProbes("0.1.0", dir, state, nil, nil).
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("got status %d, want 503", rec.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "fail" {
		t.Fatalf("status = %q, want fail", body["status"])
	}
}

func newReadyHandler(version, sharesDir string) http.Handler {
	readiness := health.NewReadiness()
	readiness.Set(health.Snapshot{ProcessReady: true, SigningReady: true, ProvisioningReady: true})
	return health.NewLifecycleHandlerWithReadinessProbes(version, sharesDir, readiness, nil, nil)
}
