package bundle

import (
	"os"
	"path/filepath"
	"testing"
)

func TestApprovalVerifierAcceptsBackendProducerAndExecutesCorpus(t *testing.T) {
	identity, err := VerifyApprovalBundle(filepath.Join("..", "..", "..", "testdata", "approval-v1"))
	if err != nil {
		t.Fatal(err)
	}
	if identity != expectedApprovalBundleIdentity {
		t.Fatalf("wrong approval identity %s", identity)
	}
}
func TestApprovalVerifierRejectsMissingChangedUnexpectedAndUnpinnedFiles(t *testing.T) {
	for _, name := range []string{"missing", "changed", "unexpected", "producer"} {
		t.Run(name, func(t *testing.T) {
			root := copyBundle(t, filepath.Join("..", "..", "..", "testdata", "approval-v1"))
			switch name {
			case "missing":
				if err := os.Remove(filepath.Join(root, "vectors.json")); err != nil {
					t.Fatal(err)
				}
			case "changed":
				write(t, filepath.Join(root, "vectors.json"), []byte(`{}`))
			case "unexpected":
				write(t, filepath.Join(root, "extra.json"), []byte(`{}`))
			case "producer":
				mutateJSONFixture(t, root, "manifest.json", func(v map[string]any) { v["producerCommit"] = "0000000000000000000000000000000000000000" })
			}
			if _, err := VerifyApprovalBundle(root); err == nil {
				t.Fatal("accepted invalid approval bundle")
			}
		})
	}
}
