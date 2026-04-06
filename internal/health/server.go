package health

import (
	"encoding/json"
	"net/http"
	"os"
	"time"
)

type checkResult struct {
	Status   string `json:"status"`
	Critical bool   `json:"critical"`
	Message  string `json:"message,omitempty"`
}

type response struct {
	Status       string                 `json:"status"`
	Ready        bool                   `json:"ready"`
	Version      string                 `json:"version"`
	Timestamp    string                 `json:"timestamp"`
	Capabilities map[string]bool        `json:"capabilities"`
	Checks       map[string]checkResult `json:"checks"`
}

type Handler struct {
	version   string
	sharesDir string
}

func NewHandler(version, sharesDir string) http.Handler {
	return &Handler{
		version:   version,
		sharesDir: sharesDir,
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	sharesDirCheck := checkResult{
		Status:   "ok",
		Critical: true,
	}
	if _, err := os.Stat(h.sharesDir); err != nil {
		sharesDirCheck.Status = "fail"
		sharesDirCheck.Message = err.Error()
	}

	resp := response{
		Status:    "ok",
		Ready:     true,
		Version:   h.version,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Capabilities: map[string]bool{
			"sign": true,
			"dkg":  true,
		},
		Checks: map[string]checkResult{
			"shares_dir": sharesDirCheck,
		},
	}
	if sharesDirCheck.Status == "fail" {
		resp.Status = "fail"
		resp.Ready = false
	}

	w.Header().Set("Content-Type", "application/json")
	statusCode := http.StatusOK
	if !resp.Ready {
		statusCode = http.StatusServiceUnavailable
	}
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(resp)
}
