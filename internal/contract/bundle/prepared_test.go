package bundle

import (
	"bytes"
	"compress/gzip"
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
	for _, test := range []string{"changed", "truncated", "checksum", "oversized", "missing", "unexpected", "producer"} {
		t.Run(test, func(t *testing.T) {
			root := copyBundle(t, "../../../testdata/approval-prepared-v1")
			path := filepath.Join(root, "corpus.json.gz")
			switch test {
			case "changed", "oversized":
				raw := []byte(`[]`)
				if test == "oversized" {
					raw = bytes.Repeat([]byte(" "), 2*1024*1024+1)
				}
				var compressed bytes.Buffer
				writer := gzip.NewWriter(&compressed)
				if _, err := writer.Write(raw); err != nil {
					t.Fatal(err)
				}
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
				write(t, path, compressed.Bytes())
			case "truncated", "checksum":
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if test == "truncated" {
					raw = raw[:len(raw)/2]
				} else {
					raw[len(raw)-8] ^= 1
				}
				write(t, path, raw)
			case "missing":
				if e := os.Remove(path); e != nil {
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
