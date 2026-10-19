package signingpolicy

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/strictjson"
)

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
