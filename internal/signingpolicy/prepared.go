package signingpolicy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"reflect"
	"strings"
	"time"

	wire "github.com/BroLabel/brosettlement-mpc-co-signer/internal/contract/approval"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/strictjson"
	"github.com/btcsuite/btcutil/base58"
	"github.com/ethereum/go-ethereum/rlp"
	tron "github.com/fbsobreira/gotron-sdk/pkg/proto/core"
	"github.com/gowebpki/jcs"
	"golang.org/x/crypto/sha3"
	"google.golang.org/protobuf/proto"
)

// ErrApprovalApplicationPending keeps an owned SIGN waiting for durable decision delivery.
var ErrApprovalApplicationPending = errors.New("approval application confirmation pending")

var ErrNetworkUnavailable = errors.New("trusted TRON network evidence unavailable")
var ErrNetworkMismatch = errors.New("trusted TRON network evidence mismatch")

var ErrPrepared = errors.New("prepared transaction does not match approved authorization")

// Existing ethers nonce and TRON request builders use JavaScript safe integers.
const maxProducerSafeInteger int64 = 1<<53 - 1

type ApprovedExecution struct {
	Version           int             `json:"version"`
	RequestID         string          `json:"requestId"`
	OutgoingID        string          `json:"outgoingId"`
	Authorization     json.RawMessage `json:"authorization"`
	AuthorizationHash string          `json:"authorizationHash"`
	DecisionReceipt   json.RawMessage `json:"decisionReceipt"`
}
type PreparedTransfer struct {
	Version                int           `json:"version"`
	Encoding               string        `json:"encoding"`
	UnsignedTransaction    string        `json:"unsignedTransaction"`
	NetworkFeeAsset        string        `json:"networkFeeAsset"`
	NetworkFeeBudgetAtomic string        `json:"networkFeeBudgetAtomic"`
	FeeEvidence            *TronFeeQuote `json:"feeEvidence"`
}
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

func closedStruct(raw []byte, target any) error {
	if err := strictjson.DecodeClosed(raw, target); err != nil {
		return err
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return ErrPrepared
	}
	typ := reflect.TypeOf(target).Elem()
	if len(m) != typ.NumField() {
		return ErrPrepared
	}
	for i := 0; i < typ.NumField(); i++ {
		k := typ.Field(i).Tag.Get("json")
		v, ok := m[k]
		if !ok || string(v) == "null" && k != "feeEvidence" {
			return ErrPrepared
		}
	}
	return nil
}
func (v *ApprovedExecution) UnmarshalJSON(raw []byte) error {
	type alias ApprovedExecution
	return closedStruct(raw, (*alias)(v))
}
func (v *PreparedTransfer) UnmarshalJSON(raw []byte) error {
	type alias PreparedTransfer
	return closedStruct(raw, (*alias)(v))
}
func (v *TronFeeQuote) UnmarshalJSON(raw []byte) error {
	type alias TronFeeQuote
	return closedStruct(raw, (*alias)(v))
}
func (v *TronFeeParameters) UnmarshalJSON(raw []byte) error {
	type alias TronFeeParameters
	return closedStruct(raw, (*alias)(v))
}
func authString(a wire.Object, k string) string { var v string; _ = json.Unmarshal(a[k], &v); return v }
func authObject(a wire.Object, k string) wire.Object {
	var v wire.Object
	_ = json.Unmarshal(a[k], &v)
	return v
}
func number(v string) *big.Int                { n, _ := new(big.Int).SetString(v, 10); return n }
func nullEqual(v json.RawMessage, w any) bool { b, _ := json.Marshal(w); return bytes.Equal(v, b) }

// VerifiedPrepared carries validated authorization for the caller's claim identity
// checks. TRON network membership still requires a separate trusted gate.
type VerifiedPrepared struct {
	Authorization wire.Object
	Tapos         *Tapos
}

// VerifyPrepared verifies exact transaction bytes and their approved parameters.
// Authorization parsing also verifies the sender key, address and derivation hash.
func VerifyPrepared(a *ApprovedExecution, prepared *PreparedTransfer, policy *Context, digest []byte, payloadType, algorithm string, now time.Time) (*VerifiedPrepared, error) {
	if a == nil || prepared == nil || policy == nil || a.Version != 1 || !wire.ValidReference(a.OutgoingID) || a.RequestID == "" || prepared.Version != 1 || prepared.Encoding != "hex" || !canonicalUint(prepared.NetworkFeeBudgetAtomic) {
		return nil, ErrPrepared
	}
	canonical, e := jcs.Transform(a.Authorization)
	if e != nil {
		return nil, ErrPrepared
	}
	auth, e := wire.ParseAuthorization(canonical)
	if e != nil {
		return nil, ErrPrepared
	}
	h := sha256.Sum256(canonical)
	if hex.EncodeToString(h[:]) != a.AuthorizationHash || authString(auth, "requestId") != a.RequestID {
		return nil, ErrPrepared
	}
	for k, w := range map[string]string{"chain": policy.Chain, "asset": policy.Asset, "fromAddress": policy.FromAddress, "toAddress": policy.ToAddress, "amountAtomic": policy.AmountAtomic, "networkFeeAsset": prepared.NetworkFeeAsset} {
		if authString(auth, k) != w {
			return nil, ErrPrepared
		}
	}
	if !nullEqual(auth["tokenStandard"], canonicalPolicyToken(policy)) || !nullEqual(auth["tokenDecimals"], policy.TokenDecimals) || !nullEqual(auth["tokenContractCanonical"], policy.TokenContractCanonical) || number(prepared.NetworkFeeBudgetAtomic).Cmp(number(authString(auth, "maxNetworkFeeAtomic"))) > 0 {
		return nil, ErrPrepared
	}
	raw, e := hex.DecodeString(prepared.UnsignedTransaction)
	if e != nil || len(raw) == 0 || len(raw) > 16384 || hex.EncodeToString(raw) != prepared.UnsignedTransaction {
		return nil, ErrPrepared
	}
	var got []byte
	var tapos *Tapos
	var budget string
	switch {
	case payloadType == ethereumPayloadType && algorithm == "keccak256" && prepared.NetworkFeeAsset == "ETH" && prepared.FeeEvidence == nil:
		budget, e = verifyEthereumBytes(raw, policy)
		h := sha3.NewLegacyKeccak256()
		_, _ = h.Write(raw)
		got = h.Sum(nil)
		params := authObject(auth, "requestedChainParams")
		if len(params) > 0 {
			p := authObject(params, "ethereum")
			if authString(p, "maxFeePerGas") != policy.MaxFeePerGas || authString(p, "maxPriorityFeePerGas") != policy.MaxPriorityFeePerGas {
				return nil, ErrPrepared
			}
		}
	case payloadType == tronPayloadType && algorithm == "sha256" && prepared.NetworkFeeAsset == "TRX" && prepared.FeeEvidence != nil:
		tapos, e = verifyTronBytes(raw, policy, now)
		budget = tronBudget(raw, policy, prepared.FeeEvidence)
		h := sha256.Sum256(raw)
		got = h[:]
		params := authObject(auth, "requestedChainParams")
		if len(params) > 0 {
			p := authObject(params, "tron")
			if string(p["feeLimitSun"]) != "null" && !nullEqual(p["feeLimitSun"], policy.FeeLimitSun) {
				return nil, ErrPrepared
			}
		}
	default:
		return nil, ErrPrepared
	}
	if e != nil || budget == "" || budget != prepared.NetworkFeeBudgetAtomic || !bytes.Equal(got, digest) {
		return nil, ErrPrepared
	}
	return &VerifiedPrepared{Authorization: auth, Tapos: tapos}, nil
}

type unsignedType2 struct {
	ChainID    *big.Int
	Nonce      *big.Int
	Priority   *big.Int
	MaxFee     *big.Int
	Gas        *big.Int
	To         []byte
	Value      *big.Int
	Data       []byte
	AccessList []rlp.RawValue
}

func verifyEthereumBytes(raw []byte, p *Context) (string, error) {
	var tx unsignedType2
	if len(raw) < 2 || raw[0] != 2 || rlp.DecodeBytes(raw[1:], &tx) != nil {
		return "", ErrPrepared
	}
	encoded, e := rlp.EncodeToBytes(tx)
	if e != nil || !bytes.Equal(encoded, raw[1:]) || len(tx.AccessList) != 0 || len(tx.To) != 20 || tx.ChainID.String() != ethereumChainID(p.Chain) || tx.ChainID.String() != p.ChainID || tx.Nonce.String() != p.Nonce || tx.Gas.String() != p.GasLimit || tx.MaxFee.String() != p.MaxFeePerGas || tx.Priority.String() != p.MaxPriorityFeePerGas || tx.Gas.Sign() <= 0 || tx.MaxFee.Sign() <= 0 || tx.Priority.Cmp(tx.MaxFee) > 0 || tx.Value.BitLen() > 256 || tx.MaxFee.BitLen() > 256 || tx.Priority.BitLen() > 256 || tx.Gas.BitLen() > 64 || tx.Nonce.Cmp(big.NewInt(maxProducerSafeInteger)) > 0 {
		return "", ErrPrepared
	}
	to := "0x" + hex.EncodeToString(tx.To)
	if p.TokenStandard == nil {
		if p.Asset != "ETH" || to != p.ToAddress || tx.Value.String() != p.AmountAtomic || len(tx.Data) != 0 {
			return "", ErrPrepared
		}
	} else {
		if *p.TokenStandard != "erc20" || p.TokenContractCanonical == nil || to != *p.TokenContractCanonical || tx.Value.Sign() != 0 || !bytes.Equal(tx.Data, transferCall(p.ToAddress, p.AmountAtomic)) {
			return "", ErrPrepared
		}
	}
	return new(big.Int).Mul(tx.Gas, tx.MaxFee).String(), nil
}
func transferCall(address, amount string) []byte {
	addr, e := hex.DecodeString(strings.TrimPrefix(address, "0x"))
	n := number(amount)
	if e != nil || len(addr) != 20 || n == nil || n.Sign() < 0 || n.BitLen() > 256 {
		return nil
	}
	b := make([]byte, 68)
	copy(b, []byte{0xa9, 0x05, 0x9c, 0xbb})
	copy(b[16:36], addr)
	n.FillBytes(b[36:])
	return b
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

type Tapos struct {
	RefBytes, RefHash []byte
	Expiration        int64
}

func tronAddress(address string) []byte {
	p, v, e := base58.CheckDecode(address)
	if e != nil || v != 0x41 || len(p) != 20 {
		return nil
	}
	return append([]byte{v}, p...)
}
func verifyTronBytes(raw []byte, p *Context, now time.Time) (*Tapos, error) {
	tx := new(tron.TransactionRaw)
	if strictProto(raw, tx) != nil || len(tx.Contract) != 1 || len(tx.RefBlockBytes) != 2 || len(tx.RefBlockHash) != 8 || tx.RefBlockNum != 0 || len(tx.Auths) != 0 || len(tx.Data) != 0 || len(tx.Scripts) != 0 || tx.Timestamp <= 0 || tx.Timestamp > now.UnixMilli()+1000 || tx.Expiration-now.UnixMilli() <= 60000 || tx.Expiration <= tx.Timestamp || tx.Expiration-tx.Timestamp > 86340000 || tx.FeeLimit < 0 || tx.FeeLimit > maxProducerSafeInteger {
		return nil, ErrPrepared
	}
	c := tx.Contract[0]
	if len(c.ProtoReflect().GetUnknown()) != 0 || c.PermissionId != 0 || len(c.Provider) != 0 || len(c.ContractName) != 0 || c.Parameter == nil || len(c.Parameter.ProtoReflect().GetUnknown()) != 0 {
		return nil, ErrPrepared
	}
	if p.TokenStandard == nil {
		v := new(tron.TransferContract)
		if c.Type != tron.Transaction_Contract_TransferContract || c.Parameter.TypeUrl != "type.googleapis.com/protocol.TransferContract" || strictProto(c.Parameter.Value, v) != nil || !bytes.Equal(v.OwnerAddress, tronAddress(p.FromAddress)) || !bytes.Equal(v.ToAddress, tronAddress(p.ToAddress)) || v.Amount <= 0 || v.Amount > maxProducerSafeInteger || big.NewInt(v.Amount).String() != p.AmountAtomic || tx.FeeLimit != 0 || p.FeeLimitSun != nil || p.Asset != "TRX" {
			return nil, ErrPrepared
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
			return nil, ErrPrepared
		}
	}
	return &Tapos{tx.RefBlockBytes, tx.RefBlockHash, tx.Expiration}, nil
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
func canonicalPolicyToken(p *Context) *string {
	if p.Chain == "tron:mainnet" || p.Chain == "tron:nile" {
		if p.TokenStandard != nil && *p.TokenStandard == "TRC20" {
			v := "trc20"
			return &v
		}
	}
	return p.TokenStandard
}
