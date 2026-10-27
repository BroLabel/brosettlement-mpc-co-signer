package signingpolicy

import (
	"encoding/hex"
	"encoding/json"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/rlp"
	tron "github.com/fbsobreira/gotron-sdk/pkg/proto/core"
	"google.golang.org/protobuf/proto"
)

func TestEthereumPreparedNumericBounds(t *testing.T) {
	for _, field := range []struct {
		name string
		max  string
	}{
		{"nonce", "9007199254740991"},   // ethers nonce is a JavaScript safe integer.
		{"gas", "18446744073709551615"}, // Ethereum gas is uint64.
	} {
		t.Run(field.name, func(t *testing.T) {
			max, _ := new(big.Int).SetString(field.max, 10)
			for _, delta := range []int64{-1, 0, 1} {
				n := new(big.Int).Add(max, big.NewInt(delta))
				t.Run(n.String(), func(t *testing.T) {
					raw, _ := hex.DecodeString("02e283aa36a78001028252089422222222222222222222222222222222222222220180c0")
					var tx unsignedType2
					if err := rlp.DecodeBytes(raw[1:], &tx); err != nil {
						t.Fatal(err)
					}
					p := &Context{Chain: "ethereum:sepolia", ChainID: "11155111", Nonce: "0", GasLimit: "21000", MaxFeePerGas: "2", MaxPriorityFeePerGas: "1", ToAddress: "0x2222222222222222222222222222222222222222", AmountAtomic: "1", Asset: "ETH"}
					if field.name == "nonce" {
						tx.Nonce, p.Nonce = n, n.String()
					} else {
						tx.Gas, p.GasLimit = n, n.String()
					}
					encoded, err := rlp.EncodeToBytes(tx)
					if err != nil {
						t.Fatal(err)
					}
					_, err = verifyEthereumBytes(append([]byte{2}, encoded...), p)
					if (err == nil) != (delta <= 0) {
						t.Fatalf("accepted=%v want=%v err=%v", err == nil, delta <= 0, err)
					}
				})
			}
		})
	}
}

func TestTronPreparedNumericBounds(t *testing.T) {
	data, err := ReadPreparedCorpus("../../testdata/approval-prepared-v1/corpus.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	type vector struct {
		Valid    bool              `json:"valid"`
		Prepared *PreparedTransfer `json:"prepared"`
		Policy   *Context          `json:"policy"`
		Now      int64             `json:"now"`
	}
	var vectors []vector
	// Decode only positive vectors: negative policies deliberately have hostile schemas.
	var items []json.RawMessage
	if err := json.Unmarshal(data, &items); err != nil {
		t.Fatal(err)
	}
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
		var v vector
		if err := json.Unmarshal(item, &v); err != nil {
			t.Fatal(err)
		}
		vectors = append(vectors, v)
	}
	for _, field := range []string{"native-amount", "fee-limit"} {
		t.Run(field, func(t *testing.T) {
			var found bool
			for _, v := range vectors {
				if v.Policy.Chain != "tron:nile" || (v.Policy.TokenStandard == nil) != (field == "native-amount") {
					continue
				}
				found = true
				for _, n := range []int64{9007199254740990, 9007199254740991, 9007199254740992} {
					t.Run(big.NewInt(n).String(), func(t *testing.T) {
						raw, err := hex.DecodeString(v.Prepared.UnsignedTransaction)
						if err != nil {
							t.Fatal(err)
						}
						var tx tron.TransactionRaw
						if err := proto.Unmarshal(raw, &tx); err != nil {
							t.Fatal(err)
						}
						p := *v.Policy
						if field == "native-amount" {
							var transfer tron.TransferContract
							if err := proto.Unmarshal(tx.Contract[0].Parameter.Value, &transfer); err != nil {
								t.Fatal(err)
							}
							transfer.Amount, p.AmountAtomic = n, big.NewInt(n).String()
							tx.Contract[0].Parameter.Value, err = proto.MarshalOptions{Deterministic: true}.Marshal(&transfer)
							if err != nil {
								t.Fatal(err)
							}
						} else {
							limit := big.NewInt(n).String()
							tx.FeeLimit, p.FeeLimitSun = n, &limit
						}
						raw, err = proto.MarshalOptions{Deterministic: true}.Marshal(&tx)
						if err != nil {
							t.Fatal(err)
						}
						err = verifyTronBytes(raw, &p, time.UnixMilli(v.Now))
						if (err == nil) != (n <= 9007199254740991) {
							t.Fatalf("accepted=%v want=%v err=%v", err == nil, n <= 9007199254740991, err)
						}
					})
				}
				break
			}
			if !found {
				t.Fatal("missing real producer vector")
			}
		})
	}
}
