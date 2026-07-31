package metrics

func SetLockHeld(held bool) { Default.Set("co_signer_lifecycle_lock_held", nil, boolFloat(held)) }
func SetReadiness(process, signing, provisioning bool) {
	Default.Set("co_signer_process_ready", nil, boolFloat(process))
	Default.Set("co_signer_signing_ready", nil, boolFloat(signing))
	Default.Set("co_signer_provisioning_ready", nil, boolFloat(provisioning))
}
func ObserveReconciliation(seconds float64, failed bool) {
	Default.Observe("co_signer_reconciliation_duration_seconds", nil, seconds)
	if failed {
		Default.Inc("co_signer_reconciliation_failures_total", nil)
	}
}
