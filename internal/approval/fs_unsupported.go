//go:build !linux && !darwin

package approval

import (
	"errors"
	"os"
)

var errUnsupportedFilesystem = errors.New("durable approval publication requires Linux or macOS")

func validateJournalDirectory(os.FileInfo) error           { return errUnsupportedFilesystem }
func readJournalFile(string) ([]byte, error)               { return nil, errUnsupportedFilesystem }
func (realJournalOperations) Publish(string, string) error { return errUnsupportedFilesystem }

type syncedJournalFile struct{ *os.File }

func (syncedJournalFile) Sync() error { return errUnsupportedFilesystem }
