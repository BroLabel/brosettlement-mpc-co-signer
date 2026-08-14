package metrics

func SetDKGAdmission(open bool) { Default.Set("dkg_admission_open", nil, boolFloat(open)) }
func SetDKGGuard(occupied bool) { Default.Set("dkg_guard_occupied", nil, boolFloat(occupied)) }
func ObservePending(kind string, ageSeconds, items float64) {
	if kind == "DKG" {
		Default.Observe("oldest_pending_dkg_age_seconds", nil, ageSeconds)
	}
	if kind == "SIGN" {
		Default.Observe("oldest_pending_sign_age_seconds", nil, ageSeconds)
	}
	Default.Set("pending_batch_items", Labels{"type": kind}, items)
}
func ObserveClaim(kind, outcome string) {
	Default.Inc("intent_claims_total", Labels{"type": kind, "outcome": outcome})
}
func ObserveClaimConflict()       { Default.Inc("dkg_claim_conflicts_total", nil) }
func ObserveDKGSkippedPreparams() { Default.Inc("dkg_skipped_preparams_total", nil) }
func boolFloat(value bool) float64 {
	if value {
		return 1
	}
	return 0
}
