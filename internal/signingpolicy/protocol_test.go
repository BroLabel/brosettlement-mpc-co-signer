package signingpolicy

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// These producer fixtures exercise the complete approval-to-bytes path for all
// supported networks and assets, including the fee evidence now decoded by the
// selected protocol rather than the common wire DTO.
func TestPreparedProtocolAndFeeEvidenceIsolation(t *testing.T) {
	raw, err := ReadPreparedCorpus("../../testdata/approval-prepared-v1/corpus.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		t.Fatal(err)
	}
	positives := 0
	for _, item := range items {
		var identity struct {
			Valid bool `json:"valid"`
		}
		if err := json.Unmarshal(item, &identity); err != nil {
			t.Fatal(err)
		}
		if !identity.Valid {
			continue
		}
		var v PreparedVector
		if err := json.Unmarshal(item, &v); err != nil {
			t.Fatal(err)
		}
		positives++
		t.Run(v.ID, func(t *testing.T) {
			verify := func(payload, hash string, evidence json.RawMessage) error {
				prepared := *v.Prepared
				prepared.FeeEvidence = evidence
				_, err := VerifyPrepared(v.Approval, &prepared, v.Policy, v.Digest, payload, hash, time.UnixMilli(v.Now))
				return err
			}
			if err := verify(v.PayloadType, v.HashAlgorithm, v.Prepared.FeeEvidence); err != nil {
				t.Fatal(err)
			}
			otherPayload, otherHash := "tron-transaction", "sha256"
			if v.PayloadType == otherPayload {
				otherPayload, otherHash = "ethereum-transaction", "keccak256"
			}
			for _, test := range []struct{ name, payload, hash string }{
				{"cross-protocol", otherPayload, otherHash},
				{"wrong-hash", v.PayloadType, otherHash},
				{"unknown-protocol", "unknown-transaction", v.HashAlgorithm},
			} {
				t.Run(test.name, func(t *testing.T) {
					if err := verify(test.payload, test.hash, v.Prepared.FeeEvidence); !errors.Is(err, ErrPrepared) {
						t.Fatalf("mismatched protocol accepted: %v", err)
					}
				})
			}
			for _, evidence := range []json.RawMessage{[]byte(`{}`), []byte(`{"unexpected":true}`), []byte(`[]`), []byte(`false`)} {
				if err := verify(v.PayloadType, v.HashAlgorithm, evidence); !errors.Is(err, ErrPrepared) {
					t.Fatalf("invalid fee evidence accepted: %v", err)
				}
			}
			if v.PayloadType == "tron-transaction" {
				if err := verify(v.PayloadType, v.HashAlgorithm, []byte(`null`)); !errors.Is(err, ErrPrepared) {
					t.Fatalf("missing TRON fee evidence accepted: %v", err)
				}
				var quote map[string]json.RawMessage
				if err := json.Unmarshal(v.Prepared.FeeEvidence, &quote); err != nil {
					t.Fatal(err)
				}
				quote["unexpected"] = []byte(`true`)
				extra, err := json.Marshal(quote)
				if err != nil {
					t.Fatal(err)
				}
				if err := verify(v.PayloadType, v.HashAlgorithm, extra); !errors.Is(err, ErrPrepared) {
					t.Fatalf("open fee evidence accepted: %v", err)
				}
			}
		})
	}
	if positives != 8 {
		t.Fatalf("positive vectors=%d", positives)
	}
}
