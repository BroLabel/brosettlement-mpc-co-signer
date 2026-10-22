package approval

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestJournalReadsAndReplaysFrozenV1Records(t *testing.T) {
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
	state, err := j.Load(a.OrgID, a.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(state.Admission, &a) {
		t.Fatal("admission changed while reading v1 records")
	}
	r := testReceipt(t, a)
	if !reflect.DeepEqual(state.Receipt, &r) {
		t.Fatal("signed receipt changed while reading v1 records")
	}
	application := []byte(`{"disposition":"APPLIED","outcome":"ACCEPTED","requestId":"../../request","version":1}`)
	if !bytes.Equal(state.Application, application) {
		t.Fatal("application changed while reading v1 records")
	}
	execution := ExecutionBinding{
		Version:           1,
		OrgID:             a.OrgID,
		RequestID:         a.RequestID,
		OutgoingID:        "outgoing-1",
		AuthorizationHash: a.AuthorizationHash,
		ExecutionDigest:   "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
		PayloadType:       "ethereum-transaction",
		HashAlgorithm:     "keccak256",
	}
	if !reflect.DeepEqual(state.Execution, &execution) {
		t.Fatal("execution binding changed while reading v1 records")
	}
	if err := j.Admit(a); err != nil {
		t.Fatal(err)
	}
	if err := j.SaveReceipt(r); err != nil {
		t.Fatal(err)
	}
	if err := j.Acknowledge(a.OrgID, a.RequestID, application); err != nil {
		t.Fatal(err)
	}
	if err := j.BindExecution(execution); err != nil {
		t.Fatal(err)
	}
	pending, err := j.PendingReceipts(100)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatal("acknowledged v1 receipt became pending")
	}
	for name, record := range records {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if string(raw) != record {
			t.Fatalf("replay changed v1 record %s", name)
		}
	}
}
