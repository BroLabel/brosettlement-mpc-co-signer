//go:build !linux

package metrics

func processRSS() (uint64, bool) { return 0, false }
