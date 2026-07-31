package metrics

func ObservePreparams(pool, inflight float64) {
	Default.Set("preparams_pool_size", nil, pool)
	Default.Set("preparams_generation_inflight", nil, inflight)
}
func ObservePreparamsGeneration(seconds float64) {
	Default.Observe("preparams_generation_duration_seconds", nil, seconds)
}
func ObservePreparamsTransition(transition string) {
	switch transition {
	case "acquired":
		Default.Inc("preparams_acquired_total", nil)
	case "consumed":
		Default.Inc("preparams_consumed_total", nil)
	case "discarded_before_start":
		Default.Inc("preparams_discarded_before_start_total", nil)
	case "acquire_failed":
		Default.Inc("preparams_acquire_failed_total", nil)
	case "consume_conflict":
		Default.Inc("preparams_consume_conflict_total", nil)
	}
}
