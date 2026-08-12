package metrics

func ObservePreparams(pool, inflight float64) {
	Default.Set("preparams_pool_size", nil, pool)
	Default.Set("preparams_generation_inflight", nil, inflight)
}

func ObservePreparamsTransitions(acquired, consumed, discarded, acquireFailed, consumeConflict float64) {
	Default.Set("preparams_acquired_total", nil, acquired)
	Default.Set("preparams_consumed_total", nil, consumed)
	Default.Set("preparams_discarded_before_start_total", nil, discarded)
	Default.Set("preparams_acquire_failed_total", nil, acquireFailed)
	Default.Set("preparams_consume_conflict_total", nil, consumeConflict)
}

func ObservePreparamsGeneration(seconds float64) {
	Default.Observe("preparams_generation_duration_seconds", nil, seconds)
}
