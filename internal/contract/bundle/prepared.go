package bundle

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/signingpolicy"
)

const expectedPreparedProducerCommit = "491b2487c023486aa7eee3db72b687967b2d85f3"
const expectedPreparedProducerSHA256 = "0049521c3970db161996d24220605f7e11ff2a62fe2b970d878a268f4ab1aca1"
const expectedPreparedBundleIdentity = "YZUUZADKcVypRx12Mq-FTlsMfV2c_8feM32kiARIL60"

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
