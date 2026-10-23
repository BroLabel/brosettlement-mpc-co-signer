package approval

import (
	"encoding/json"
	"os"
	"path/filepath"

	"testing"
)

func TestJournalRejectsLegacyEnvironmentBoundRecords(t *testing.T) {
	// Captured from the pre-refactor writer with synthetic test identities.
	// Keep the original filenames and exact record bytes as the compatibility boundary.
	raw, err := os.ReadFile("testdata/journal-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var records map[string]string
	if err := json.Unmarshal(raw, &records); err != nil {
		t.Fatal(err)
	}
	if len(records) != 6 {
		t.Fatal("fixture must include all six journal record types")
	}
	dir := privateDir(t)
	for name, record := range records {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(record), 0600); err != nil {
			t.Fatal(err)
		}
	}
	j, err := OpenJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	a := testAdmission()
	if _, err := j.Load(a.OrgID, a.RequestID); err == nil {
		t.Fatal("legacy environment-bound records must require explicit reconciliation")
	}
}
