package metrics

func ObserveRelayIntegrityConflict() { Default.Inc("relay_integrity_conflicts_total", nil) }
func ObserveRelayQueueOverflow()     { Default.Inc("relay_queue_overflow_total", nil) }
