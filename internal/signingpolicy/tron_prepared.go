package signingpolicy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"strings"
	"time"

	wire "github.com/BroLabel/brosettlement-mpc-co-signer/internal/contract/approval"
	"github.com/btcsuite/btcutil/base58"
	tron "github.com/fbsobreira/gotron-sdk/pkg/proto/core"
	"google.golang.org/protobuf/proto"
)

type TronFeeQuote struct {
	Version                  int               `json:"version"`
	DestinationAccountExists bool              `json:"destinationAccountExists"`
	ChainParameters          TronFeeParameters `json:"chainParameters"`
	AmountAtomic             string            `json:"amountAtomic"`
	SignedTransactionBytes   int               `json:"signedTransactionBytes"`
	BandwidthBytes           int               `json:"bandwidthBytes"`
}
type TronFeeParameters struct {
	TransactionFeeSunPerBandwidth          string `json:"transactionFeeSunPerBandwidth"`
	CreateAccountFeeSun                    string `json:"createAccountFeeSun"`
	CreateNewAccountFeeInSystemContractSun string `json:"createNewAccountFeeInSystemContractSun"`
	CreateNewAccountBandwidthRate          string `json:"createNewAccountBandwidthRate"`
}

func (v *TronFeeQuote) UnmarshalJSON(raw []byte) error {
	type alias TronFeeQuote
	return closedStruct(raw, (*alias)(v))
}
func (v *TronFeeParameters) UnmarshalJSON(raw []byte) error {
	type alias TronFeeParameters
	return closedStruct(raw, (*alias)(v))
}

func verifyTronPrepared(raw []byte, prepared *PreparedTransfer, policy *Context, auth wire.Object, now time.Time) (preparedVerification, error) {
	var quote *TronFeeQuote
	if prepared.NetworkFeeAsset != "TRX" || json.Unmarshal(prepared.FeeEvidence, &quote) != nil || quote == nil || !nullEqual(auth["tokenStandard"], canonicalTronPolicyToken(policy)) {
		return preparedVerification{}, ErrPrepared
	}
	validUntil, err := verifyTronBytes(raw, policy, now)
	if err != nil {
		return preparedVerification{}, err
	}
	params := authObject(auth, "requestedChainParams")
	if len(params) > 0 {
		p := authObject(params, "tron")
		if string(p["feeLimitSun"]) != "null" && !nullEqual(p["feeLimitSun"], policy.FeeLimitSun) {
			return preparedVerification{}, ErrPrepared
		}
	}
	h := sha256.Sum256(raw)
	return preparedVerification{
		digest: h[:], budget: tronBudget(raw, policy, quote),
		network: &networkRequirement{chain: policy.Chain, raw: raw, validUntil: validUntil},
	}, nil
}

// strictProto rejects unknown fields and duplicate/nonminimal/reordered encodings
// by comparing each supported message with its deterministic protocol encoding.
func strictProto(raw []byte, m proto.Message) error {
	if e := proto.Unmarshal(raw, m); e != nil {
		return ErrPrepared
	}
	if len(m.ProtoReflect().GetUnknown()) != 0 {
		return ErrPrepared
	}
	b, e := proto.MarshalOptions{Deterministic: true}.Marshal(m)
	if e != nil || !bytes.Equal(raw, b) {
		return ErrPrepared
	}
	return nil
}

func tronAddress(address string) []byte {
	p, v, e := base58.CheckDecode(address)
	if e != nil || v != 0x41 || len(p) != 20 {
		return nil
	}
	return append([]byte{v}, p...)
}
func verifyTronBytes(raw []byte, p *Context, now time.Time) (time.Time, error) {
	tx := new(tron.TransactionRaw)
	if strictProto(raw, tx) != nil || len(tx.Contract) != 1 || len(tx.RefBlockBytes) != 2 || len(tx.RefBlockHash) != 8 || tx.RefBlockNum != 0 || len(tx.Auths) != 0 || len(tx.Data) != 0 || len(tx.Scripts) != 0 || tx.Timestamp <= 0 || tx.Timestamp > now.UnixMilli()+1000 || tx.Expiration-now.UnixMilli() <= 60000 || tx.Expiration <= tx.Timestamp || tx.Expiration-tx.Timestamp > 86340000 || tx.FeeLimit < 0 || tx.FeeLimit > maxProducerSafeInteger {
		return time.Time{}, ErrPrepared
	}
	c := tx.Contract[0]
	if len(c.ProtoReflect().GetUnknown()) != 0 || c.PermissionId != 0 || len(c.Provider) != 0 || len(c.ContractName) != 0 || c.Parameter == nil || len(c.Parameter.ProtoReflect().GetUnknown()) != 0 {
		return time.Time{}, ErrPrepared
	}
	if p.TokenStandard == nil {
		v := new(tron.TransferContract)
		if c.Type != tron.Transaction_Contract_TransferContract || c.Parameter.TypeUrl != "type.googleapis.com/protocol.TransferContract" || strictProto(c.Parameter.Value, v) != nil || !bytes.Equal(v.OwnerAddress, tronAddress(p.FromAddress)) || !bytes.Equal(v.ToAddress, tronAddress(p.ToAddress)) || v.Amount <= 0 || v.Amount > maxProducerSafeInteger || big.NewInt(v.Amount).String() != p.AmountAtomic || tx.FeeLimit != 0 || p.FeeLimitSun != nil || p.Asset != "TRX" {
			return time.Time{}, ErrPrepared
		}
	} else {
		v := new(tron.TriggerSmartContract)
		addr := tronAddress(p.ToAddress)
		contract := []byte(nil)
		if p.TokenContractCanonical != nil {
			h, _ := hex.DecodeString(strings.TrimPrefix(*p.TokenContractCanonical, "0x"))
			contract = append([]byte{0x41}, h...)
		}
		if (*p.TokenStandard != "trc20" && *p.TokenStandard != "TRC20") || c.Type != tron.Transaction_Contract_TriggerSmartContract || c.Parameter.TypeUrl != "type.googleapis.com/protocol.TriggerSmartContract" || strictProto(c.Parameter.Value, v) != nil || !bytes.Equal(v.OwnerAddress, tronAddress(p.FromAddress)) || !bytes.Equal(v.ContractAddress, contract) || len(addr) != 21 || !bytes.Equal(v.Data, transferCall("0x"+hex.EncodeToString(addr[1:]), p.AmountAtomic)) || v.CallValue != 0 || v.CallTokenValue != 0 || v.TokenId != 0 || p.FeeLimitSun == nil || big.NewInt(tx.FeeLimit).String() != *p.FeeLimitSun || tx.FeeLimit <= 0 {
			return time.Time{}, ErrPrepared
		}
	}
	return time.UnixMilli(tx.Expiration - 60000), nil
}
func tronBudget(raw []byte, p *Context, q *TronFeeQuote) string {
	if q == nil || q.Version != 1 {
		return ""
	}
	params := q.ChainParameters
	for _, v := range []string{params.TransactionFeeSunPerBandwidth, params.CreateAccountFeeSun, params.CreateNewAccountFeeInSystemContractSun, params.CreateNewAccountBandwidthRate, q.AmountAtomic} {
		if !canonicalUint(v) {
			return ""
		}
	}
	signed := proto.Size(&tron.Transaction{RawData: &tron.TransactionRaw{}, Signature: [][]byte{make([]byte, 65)}})
	// Replace the zero-length raw message with the exact length-delimited raw bytes.
	signed += len(raw) + varintSize(len(raw)) - 1
	bandwidth := signed + 64
	if q.SignedTransactionBytes != signed || q.BandwidthBytes != bandwidth {
		return ""
	}
	budget := big.NewInt(int64(bandwidth))
	budget.Mul(budget, number(params.TransactionFeeSunPerBandwidth))
	if p.FeeLimitSun == nil && !q.DestinationAccountExists {
		budget.Mul(budget, number(params.CreateNewAccountBandwidthRate))
		budget.Add(budget, number(params.CreateAccountFeeSun))
		budget.Add(budget, number(params.CreateNewAccountFeeInSystemContractSun))
	}
	if p.FeeLimitSun != nil {
		if !canonicalUint(*p.FeeLimitSun) {
			return ""
		}
		budget.Add(budget, number(*p.FeeLimitSun))
	}
	if budget.String() != q.AmountAtomic {
		return ""
	}
	return budget.String()
}
func varintSize(n int) int {
	s := 1
	for n >= 128 {
		s++
		n >>= 7
	}
	return s
}

// The existing unsigned TRON policy uses catalog spelling TRC20. Signed
// authorization remains exactly lowercase trc20; no other normalization occurs.
func canonicalTronPolicyToken(p *Context) *string {
	if isTronChain(p.Chain) {
		if p.TokenStandard != nil && *p.TokenStandard == "TRC20" {
			v := "trc20"
			return &v
		}
	}
	return p.TokenStandard
}
