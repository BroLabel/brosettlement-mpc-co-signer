package metrics

// Contract is the exhaustive allowlist. Keep its values literal and bounded;
// identifiers, paths, public keys, artifact material and diagnostics are not
// labels in this package.
func Contract() map[string]Spec {
	plain := func() Spec { return Spec{} }
	return map[string]Spec{
		"co_signer_lifecycle_lock_held": plain(), "co_signer_reconciliation_duration_seconds": plain(), "co_signer_reconciliation_failures_total": plain(),
		"co_signer_process_ready": plain(), "co_signer_signing_ready": plain(), "co_signer_provisioning_ready": plain(),
		"dkg_guard_occupied": plain(), "dkg_admission_open": plain(), "dkg_claim_conflicts_total": plain(),
		"oldest_pending_dkg_age_seconds": plain(), "oldest_pending_sign_age_seconds": plain(),
		"pending_batch_items":      {Labels: map[string][]string{"type": {"DKG", "SIGN"}}},
		"intent_claims_total":      {Labels: map[string][]string{"type": {"DKG", "SIGN"}, "outcome": {"accepted", "conflict", "failed", "unknown"}}},
		"dkg_terminal_unconfirmed": plain(), "dkg_terminal_publish_attempts_total": plain(), "dkg_terminal_conflicts_total": plain(),
		"preparams_pool_size": plain(), "preparams_generation_inflight": plain(), "preparams_generation_duration_seconds": plain(),
		"preparams_acquired_total": plain(), "preparams_consumed_total": plain(), "preparams_discarded_before_start_total": plain(), "preparams_acquire_failed_total": plain(), "preparams_consume_conflict_total": plain(), "dkg_skipped_preparams_total": plain(),
		"artifact_publish_total": plain(), "artifact_publish_failures_total": plain(), "artifact_inspection_failures_total": plain(),
		"artifact_file_count": plain(), "artifact_temporary_file_count": plain(), "artifact_total_bytes": plain(), "artifact_oldest_file_age_seconds": plain(), "artifact_filesystem_free_bytes": plain(),
		"active_dkg_jobs": plain(), "active_sign_jobs": plain(), "mpc_session_duration_seconds": {Labels: map[string][]string{"type": {"DKG", "SIGN"}}},
		"relay_integrity_conflicts_total": plain(), "relay_queue_overflow_total": plain(), "process_rss_bytes": plain(), "go_heap_bytes": plain(), "go_goroutines": plain(),
	}
}
