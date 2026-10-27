package signingpolicy

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/strictjson"
)

// ReadPreparedCorpus expands the stored fixture without changing producer bytes.
// The bundle verifier checks the original manifest hash after decompression.
func ReadPreparedCorpus(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	reader, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	const maximum = 2 * 1024 * 1024
	raw, err := io.ReadAll(io.LimitReader(reader, maximum+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maximum {
		return nil, fmt.Errorf("prepared corpus exceeds size limit")
	}
	return raw, nil
}

type PreparedVector struct {
	ID            string             `json:"id"`
	Valid         bool               `json:"valid"`
	Approval      *ApprovedExecution `json:"approval"`
	Prepared      *PreparedTransfer  `json:"prepared"`
	Policy        *Context           `json:"policy"`
	Digest        []byte             `json:"digest"`
	PayloadType   string             `json:"payloadType"`
	HashAlgorithm string             `json:"hashAlgorithm"`
	Now           int64              `json:"now"`
}

// VerifyPreparedCorpus executes the exact shared real-producer/hostile corpus.
func VerifyPreparedCorpus(raw []byte) error {
	var items []json.RawMessage
	if strictjson.DecodeClosed(raw, &items) != nil || len(items) == 0 {
		return ErrPrepared
	}
	seen := map[string]bool{}
	positives := 0
	for _, item := range items {
		var identity struct {
			ID    string `json:"id"`
			Valid bool   `json:"valid"`
		}
		if json.Unmarshal(item, &identity) != nil || identity.ID == "" || seen[identity.ID] {
			return ErrPrepared
		}
		seen[identity.ID] = true
		var v PreparedVector
		e := strictjson.DecodeClosed(item, &v)
		if e == nil {
			_, e = VerifyPrepared(v.Approval, v.Prepared, v.Policy, v.Digest, v.PayloadType, v.HashAlgorithm, time.UnixMilli(v.Now))
		}
		if (e == nil) != identity.Valid {
			return fmt.Errorf("prepared corpus %s acceptance mismatch", identity.ID)
		}
		if identity.Valid {
			positives++
		}
	}
	if positives != 8 {
		return ErrPrepared
	}
	return nil
}
