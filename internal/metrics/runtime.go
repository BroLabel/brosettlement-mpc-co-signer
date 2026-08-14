package metrics

// ObserveRSS is intentionally best effort: Go has no portable standard-library
// RSS reader. Linux callers may supply a process value; other platforms retain
// the heap/goroutine measurements without fabricating RSS.
func ObserveRSS(bytes uint64) { Default.Set("process_rss_bytes", nil, float64(bytes)) }

func ObserveGoRuntime() {
	ObserveRuntime()
	if bytes, ok := processRSS(); ok {
		ObserveRSS(bytes)
	}
}
