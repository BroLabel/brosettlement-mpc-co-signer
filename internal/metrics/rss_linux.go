//go:build linux

package metrics

import (
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

func processRSS() (uint64, bool) {
	raw, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(raw))
	if len(fields) < 2 {
		return 0, false
	}
	pages, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0, false
	}
	return pages * uint64(unix.Getpagesize()), true
}
