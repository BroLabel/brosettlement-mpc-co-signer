//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package main

import (
	"math"

	"golang.org/x/sys/unix"
)

func filesystemFreeBytes(path string) (uint64, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return 0, err
	}
	blocks := uint64(stat.Bavail)
	blockSize := uint64(stat.Bsize)
	if blocks != 0 && blockSize > math.MaxUint64/blocks {
		return math.MaxUint64, nil
	}
	return blocks * blockSize, nil
}
