//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package main

import "errors"

func filesystemFreeBytes(string) (uint64, error) {
	return 0, errors.New("filesystem free-space admission is unsupported on this platform")
}
