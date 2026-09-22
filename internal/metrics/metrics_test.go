package metrics

import (
	"strings"
	"testing"
)

func TestContractContainsOnlySafeBoundedMetrics(t *testing.T) {
	contract := Contract()
	want := []string{
		"co_signer_lifecycle_lock_held", "co_signer_reconciliation_duration_seconds", "co_signer_reconciliation_failures_total",
		"co_signer_process_ready", "co_signer_signing_ready", "co_signer_provisioning_ready",
		"dkg_guard_occupied", "dkg_admission_open", "dkg_claim_conflicts_total", "oldest_pending_dkg_age_seconds",
		"oldest_pending_sign_age_seconds", "pending_batch_items", "intent_claims_total", "dkg_terminal_unconfirmed",
		"dkg_terminal_publish_attempts_total", "dkg_terminal_conflicts_total", "preparams_pool_size",
		"preparams_generation_inflight", "preparams_generation_duration_seconds", "preparams_acquired_total",
		"preparams_consumed_total", "preparams_discarded_before_start_total", "preparams_acquire_failed_total",
		"preparams_consume_conflict_total", "dkg_skipped_preparams_total", "artifact_publish_total",
		"artifact_publish_failures_total", "artifact_inspection_failures_total", "artifact_file_count",
		"artifact_temporary_file_count", "artifact_total_bytes", "artifact_oldest_file_age_seconds",
		"artifact_filesystem_free_bytes", "active_dkg_jobs", "active_sign_jobs", "mpc_session_duration_seconds",
		"relay_integrity_conflicts_total", "relay_queue_overflow_total", "process_rss_bytes", "go_heap_bytes", "go_goroutines",
	}
	if len(contract) != len(want) {
		t.Fatalf("contract has %d metrics, want %d", len(contract), len(want))
	}
	for _, name := range want {
		spec, ok := contract[name]
		if !ok {
			t.Fatalf("missing metric %q", name)
		}
		for label, values := range spec.Labels {
			if len(values) == 0 {
				t.Fatalf("metric %q label %q is not bounded", name, label)
			}
			for _, value := range values {
				if strings.ContainsAny(value, "/\\") || strings.Contains(value, "mpc_key_") {
					t.Fatalf("metric %q contains unsafe label value %q", name, value)
				}
			}
		}
	}
}

func TestRegistryRejectsUncontractedLabelsAndRedactsCanaries(t *testing.T) {
	r := NewRegistry()
	r.Inc("intent_claims_total", Labels{"type": "DKG", "outcome": "accepted"})
	r.Inc("intent_claims_total", Labels{"type": "DKG", "outcome": "mpc_key_123/path"})
	r.Inc("not_a_metric", nil)

	snapshot := r.Snapshot()
	if got := snapshot["intent_claims_total"]["outcome=accepted,type=DKG"]; got != 1 {
		t.Fatalf("safe counter = %v, want 1", got)
	}
	for _, rendered := range r.Render() {
		if strings.Contains(rendered, "mpc_key_") || strings.Contains(rendered, "/path") || strings.Contains(rendered, "not_a_metric") {
			t.Fatalf("unsafe metric leaked: %q", rendered)
		}
	}
}
