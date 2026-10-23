package bundle

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/signingpolicy"
)

const expectedPreparedProducerCommit = "1cbdee66c09492098c81dd2d8e320f469400776e"
const expectedPreparedProducerSHA256 = "e8ac4e4ae7329393957118a3772465ace9951b2e85a758a889cd004f50911773"
const expectedPreparedBundleIdentity = "nTWG-LUo9SEyoB5G0LbxpmLMEf1Jxf60VSrRuxMhp_k"

type preparedManifest struct {
	BundleVersion      int            `json:"bundleVersion"`
	ProducerRepository string         `json:"producerRepository"`
	ProducerCommit     string         `json:"producerCommit"`
	ProducerPath       string         `json:"producerPath"`
	ProducerSHA256     string         `json:"producerSHA256"`
	ClaimVersion       int            `json:"claimVersion"`
	PreparedVersion    int            `json:"preparedVersion"`
	Files              []manifestFile `json:"files"`
}

// VerifyPreparedBundle preserves the independent legacy trust bundle pins.
func VerifyPreparedBundle(root string) (string, error) {
	// The manifest pins the uncompressed backend export. Gzip is local storage
	// only, so producer and bundle identities remain unchanged.
	var content []byte
	manifest, _, e := verifyManifest(root, []string{"corpus.json.gz"}, func(raw []byte) error {
		var m preparedManifest
		if e := decodeClosed(raw, &m, []string{"bundleVersion", "producerRepository", "producerCommit", "producerPath", "producerSHA256", "claimVersion", "preparedVersion", "files"}); e != nil {
			return e
		}
		if m.BundleVersion != 1 || m.ProducerRepository != "https://github.com/BroLabel/BroSettlement" || m.ProducerCommit != expectedPreparedProducerCommit || m.ProducerPath != "apps/client-api/test/contracts/approval/prepared-sign.producer.ts" || m.ProducerSHA256 != expectedPreparedProducerSHA256 || m.ClaimVersion != 2 || m.PreparedVersion != 1 || len(m.Files) != 1 || m.Files[0].Path != "corpus.json" {
			return fmt.Errorf("invalid prepared bundle provenance")
		}
		var e error
		content, e = signingpolicy.ReadPreparedCorpus(filepath.Join(root, "corpus.json.gz"))
		if e != nil {
			return e
		}
		sum := sha256.Sum256(content)
		if m.Files[0].SHA256 != hex.EncodeToString(sum[:]) {
			return fmt.Errorf("prepared corpus hash mismatch")
		}
		return nil
	})
	if e != nil {
		return "", e
	}
	if digest(manifest) != expectedPreparedBundleIdentity {
		return "", fmt.Errorf("unexpected prepared bundle identity")
	}
	if e = signingpolicy.VerifyPreparedCorpus(content); e != nil {
		return "", e
	}
	return expectedPreparedBundleIdentity, nil
}
