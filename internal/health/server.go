package health

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/metrics"
)

type Reason string

const (
	ReasonNone                    Reason = ""
	ReasonCapabilityDeferred      Reason = "dkg_capability_deferred"
	ReasonDKGTerminalUnconfirmed  Reason = "dkg_terminal_unconfirmed"
	ReasonProvisioningUnavailable Reason = "dkg_capability_unavailable"
)

type Snapshot struct {
	ProcessReady       bool
	SigningReady       bool
	ProvisioningReady  bool
	ProvisioningReason Reason
}

type Readiness struct {
	value atomic.Value
}

func NewReadiness() *Readiness {
	readiness := &Readiness{}
	readiness.value.Store(Snapshot{})
	return readiness
}

func (r *Readiness) Set(snapshot Snapshot) {
	if r != nil {
		r.value.Store(snapshot)
		metrics.SetReadiness(snapshot.ProcessReady, snapshot.SigningReady, snapshot.ProvisioningReady)
	}
}

func (r *Readiness) Snapshot() Snapshot {
	if r == nil {
		return Snapshot{}
	}
	return r.value.Load().(Snapshot)
}

type checkResult struct {
	Status   string `json:"status"`
	Critical bool   `json:"critical"`
	Message  string `json:"message,omitempty"`
}

type response struct {
	Status             string                 `json:"status"`
	Ready              bool                   `json:"ready"`
	ProcessReady       bool                   `json:"processReady"`
	SigningReady       bool                   `json:"signingReady"`
	ProvisioningReady  bool                   `json:"provisioningReady"`
	ProvisioningReason Reason                 `json:"provisioningReason,omitempty"`
	Version            string                 `json:"version"`
	Timestamp          string                 `json:"timestamp"`
	Capabilities       map[string]bool        `json:"capabilities"`
	Checks             map[string]checkResult `json:"checks"`
}

type Handler struct {
	version      string
	sharesDir    string
	readiness    *Readiness
	signingProbe func() bool
}

func NewHandler(version, sharesDir string) http.Handler {
	readiness := NewReadiness()
	readiness.Set(Snapshot{ProcessReady: true, SigningReady: true, ProvisioningReady: true})
	return NewLifecycleHandler(version, sharesDir, readiness)
}

func NewLifecycleHandler(version, sharesDir string, readiness *Readiness) http.Handler {
	return NewLifecycleHandlerWithSigningProbe(version, sharesDir, readiness, nil)
}

func NewLifecycleHandlerWithSigningProbe(version, sharesDir string, readiness *Readiness, signingProbe func() bool) http.Handler {
	return &Handler{
		version:      version,
		sharesDir:    sharesDir,
		readiness:    readiness,
		signingProbe: signingProbe,
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path == "/metrics" {
		metrics.ObserveGoRuntime()
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(strings.Join(metrics.Default.Render(), "\n") + "\n"))
		return
	}
	metrics.ObserveGoRuntime()

	sharesDirCheck := checkResult{
		Status:   "ok",
		Critical: true,
	}
	if _, err := os.Stat(h.sharesDir); err != nil {
		sharesDirCheck.Status = "fail"
		sharesDirCheck.Message = err.Error()
	}

	snapshot := h.readiness.Snapshot()
	if h.signingProbe != nil && !h.signingProbe() {
		snapshot.ProcessReady = false
		snapshot.SigningReady = false
		snapshot.ProvisioningReady = false
		snapshot.ProvisioningReason = ReasonProvisioningUnavailable
	}
	resp := response{
		Status:             "ok",
		Ready:              snapshot.ProcessReady,
		ProcessReady:       snapshot.ProcessReady,
		SigningReady:       snapshot.SigningReady,
		ProvisioningReady:  snapshot.ProvisioningReady,
		ProvisioningReason: snapshot.ProvisioningReason,
		Version:            h.version,
		Timestamp:          time.Now().UTC().Format(time.RFC3339),
		Capabilities: map[string]bool{
			"sign": snapshot.SigningReady,
			"dkg":  snapshot.ProvisioningReady,
		},
		Checks: map[string]checkResult{
			"shares_dir": sharesDirCheck,
		},
	}
	if !resp.Ready {
		resp.Status = "fail"
	}
	if sharesDirCheck.Status == "fail" {
		resp.Status = "fail"
		resp.Ready = false
		resp.ProcessReady = false
		resp.SigningReady = false
		resp.ProvisioningReady = false
	}
	metrics.SetReadiness(resp.ProcessReady, resp.SigningReady, resp.ProvisioningReady)

	w.Header().Set("Content-Type", "application/json")
	statusCode := http.StatusOK
	if !resp.Ready {
		statusCode = http.StatusServiceUnavailable
	}
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(resp)
}
