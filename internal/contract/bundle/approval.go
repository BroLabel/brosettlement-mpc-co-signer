package bundle

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/contract/approval"
)

const expectedApprovalProducerCommit = "3ce3e8bbf3e2140e651e67f5fbd33603f7985463"
const expectedApprovalProducerSHA256 = "cdf529d72e50a857e5c9db4f5823675cc95e09eb7c7492bf059c97542a39eabc"
const expectedApprovalBundleIdentity = "swh_m0-Yrvbi7HnDxQ8GoXAha21AZpkco7g2_tfLaC8"

type approvalManifest struct {
	BundleVersion      int    `json:"bundleVersion"`
	ProducerRepository string `json:"producerRepository"`
	ProducerCommit     string `json:"producerCommit"`
	ProducerPath       string `json:"producerPath"`
	ProducerSHA256     string `json:"producerSHA256"`
	SchemaVersions     struct {
		Authorization int `json:"authorization"`
		Receipt       int `json:"receipt"`
		PendingCursor int `json:"pendingCursor"`
	} `json:"schemaVersions"`
	Files []manifestFile `json:"files"`
}

// VerifyApprovalBundle pins the backend producer and executes all approval-v1 vectors.
func VerifyApprovalBundle(root string) (string, error) {
	paths := []string{"vectors.json"}
	manifestBytes, _, err := verifyManifest(root, paths, func(raw []byte) error {
		var m approvalManifest
		if err := decodeClosed(raw, &m, []string{"bundleVersion", "producerRepository", "producerCommit", "producerPath", "producerSHA256", "schemaVersions", "files"}); err != nil {
			return err
		}
		if m.BundleVersion != 1 || m.ProducerRepository != "https://github.com/BroLabel/BroSettlement" || m.ProducerCommit != expectedApprovalProducerCommit || !gitCommitPattern.MatchString(m.ProducerCommit) || m.ProducerPath != "apps/client-api/src/transactions/approval/approval-corpus.ts" || m.ProducerSHA256 != expectedApprovalProducerSHA256 || m.SchemaVersions.Authorization != 1 || m.SchemaVersions.Receipt != 1 || m.SchemaVersions.PendingCursor != 1 {
			return fmt.Errorf("invalid approval manifest provenance")
		}
		if len(m.Files) != 1 || m.Files[0].Path != "vectors.json" {
			return fmt.Errorf("invalid approval file list")
		}
		content, err := os.ReadFile(filepath.Join(root, "vectors.json"))
		if err != nil {
			return err
		}
		sum := sha256.Sum256(content)
		if m.Files[0].SHA256 != hex.EncodeToString(sum[:]) {
			return fmt.Errorf("approval vector content hash mismatch")
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if identity := digest(manifestBytes); identity != expectedApprovalBundleIdentity {
		return "", fmt.Errorf("unexpected approval bundle identity %q", identity)
	}
	raw, err := os.ReadFile(filepath.Join(root, "vectors.json"))
	if err != nil {
		return "", err
	}
	if err := approval.VerifyCorpus(raw); err != nil {
		return "", err
	}
	return expectedApprovalBundleIdentity, nil
}
