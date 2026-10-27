package signingpolicy

import (
	"encoding/hex"
	"testing"
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
	raw, e := ReadPreparedCorpus("../../testdata/approval-prepared-v1/corpus.json.gz")
	if e != nil {
		t.Fatal(e)
	}
	if e = VerifyPreparedCorpus(raw); e != nil {
		t.Fatal(e)
	}
}
