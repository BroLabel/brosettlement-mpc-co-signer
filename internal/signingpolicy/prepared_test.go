package signingpolicy

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestEthereumPreparedStrictType2(t *testing.T) {
	raw, _ := hex.DecodeString("02e283aa36a78001028252089422222222222222222222222222222222222222220180c0")
	p := &Context{Version: 1, TransactionType: 2, Chain: "ethereum:sepolia", ChainID: "11155111", Nonce: "0", GasLimit: "21000", MaxFeePerGas: "2", MaxPriorityFeePerGas: "1", ToAddress: "0x2222222222222222222222222222222222222222", AmountAtomic: "1", Asset: "ETH"}
	if _, err := verifyEthereumBytes(raw, p); err != nil {
		t.Fatalf("valid producer: %v", err)
	}
	for _, b := range [][]byte{append(append([]byte{}, raw...), 0), append([]byte{1}, raw[1:]...)} {
		if _, err := verifyEthereumBytes(b, p); err == nil {
			t.Fatal("hostile bytes accepted")
		}
	}
}

func TestSharedPreparedProducerCorpus(t *testing.T) {
	raw, e := os.ReadFile("../../testdata/approval-prepared-v1/corpus.json")
	if e != nil {
		t.Fatal(e)
	}
	var vectors []struct {
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
	// Hostile policy schemas must reject during decoding, just as HTTP claims do.
	var items []json.RawMessage
	if e = json.Unmarshal(raw, &items); e != nil {
		t.Fatal(e)
	}
	for _, item := range items {
		var identity struct {
			ID    string `json:"id"`
			Valid bool   `json:"valid"`
		}
		_ = json.Unmarshal(item, &identity)
		t.Run(identity.ID, func(t *testing.T) {
			vectors = nil
			if e = json.Unmarshal(append(append([]byte{'['}, item...), ']'), &vectors); e != nil {
				if identity.Valid {
					t.Fatal(e)
				}
				return
			}
			v := vectors[0]
			_, e := VerifyPrepared(v.Approval, v.Prepared, v.Policy, v.Digest, v.PayloadType, v.HashAlgorithm, time.UnixMilli(v.Now))
			if (e == nil) != v.Valid {
				t.Fatalf("accepted=%v want=%v err=%v", e == nil, v.Valid, e)
			}
		})
	}
}
