package bundle

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPreparedBundleExecutesPinnedRealProducerCorpus(t *testing.T) {
	id, e := VerifyPreparedBundle("../../../testdata/approval-prepared-v1")
	if e != nil || id != expectedPreparedBundleIdentity {
		t.Fatalf("identity=%s err=%v", id, e)
	}
}
func TestPreparedBundleRejectsChangedUnpinnedOrMissingEvidence(t *testing.T) {
	for _, test := range []string{"changed", "missing", "unexpected", "producer"} {
		t.Run(test, func(t *testing.T) {
			root := copyBundle(t, "../../../testdata/approval-prepared-v1")
			switch test {
			case "changed":
				write(t, filepath.Join(root, "corpus.json"), []byte(`[]`))
			case "missing":
				if e := os.Remove(filepath.Join(root, "corpus.json")); e != nil {
					t.Fatal(e)
				}
			case "unexpected":
				write(t, filepath.Join(root, "extra.json"), []byte(`{}`))
			case "producer":
				mutateJSONFixture(t, root, "manifest.json", func(v map[string]any) { v["producerCommit"] = "0000000000000000000000000000000000000000" })
			}
			if _, e := VerifyPreparedBundle(root); e == nil {
				t.Fatal("invalid bundle accepted")
			}
		})
	}
}
