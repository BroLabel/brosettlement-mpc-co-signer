package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultJournalNeverAbandonsLegacyDirectory(t *testing.T) {
	root := t.TempDir()
	legacy := filepath.Join(root, "old-journal")
	primary := filepath.Join(root, "current", "primary")
	directory, err := defaultJournalDirectory(primary, legacy)
	if err != nil || directory != filepath.Join(root, "current", "approval") {
		t.Fatalf("default did not follow primary storage: %s %v", directory, err)
	}
	if err := os.Mkdir(legacy, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := defaultJournalDirectory(primary, legacy); err == nil {
		t.Fatal("existing legacy journal silently abandoned")
	}
}
