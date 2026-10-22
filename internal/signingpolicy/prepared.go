package signingpolicy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"reflect"
	"time"

	wire "github.com/BroLabel/brosettlement-mpc-co-signer/internal/contract/approval"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/strictjson"
	"github.com/gowebpki/jcs"
)

// ErrApprovalApplicationPending keeps an owned SIGN waiting for durable decision delivery.
var ErrApprovalApplicationPending = errors.New("approval application confirmation pending")

var ErrPrepared = errors.New("prepared transaction does not match approved authorization")

type ApprovedExecution struct {
	Version           int             `json:"version"`
	RequestID         string          `json:"requestId"`
	OutgoingID        string          `json:"outgoingId"`
	Authorization     json.RawMessage `json:"authorization"`
	AuthorizationHash string          `json:"authorizationHash"`
	DecisionReceipt   json.RawMessage `json:"decisionReceipt"`
}
type PreparedTransfer struct {
	Version                int             `json:"version"`
	Encoding               string          `json:"encoding"`
	UnsignedTransaction    string          `json:"unsignedTransaction"`
	NetworkFeeAsset        string          `json:"networkFeeAsset"`
	NetworkFeeBudgetAtomic string          `json:"networkFeeBudgetAtomic"`
	FeeEvidence            json.RawMessage `json:"feeEvidence"`
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
func authString(a wire.Object, k string) string { var v string; _ = json.Unmarshal(a[k], &v); return v }
func authObject(a wire.Object, k string) wire.Object {
	var v wire.Object
	_ = json.Unmarshal(a[k], &v)
	return v
}
func number(v string) *big.Int                { n, _ := new(big.Int).SetString(v, 10); return n }
func nullEqual(v json.RawMessage, w any) bool { b, _ := json.Marshal(w); return bytes.Equal(v, b) }

// VerifiedPrepared carries validated authorization and any network requirement.
// Its private requirement is derived from the verified transaction bytes.
type VerifiedPrepared struct {
	Authorization wire.Object
	network       *networkRequirement
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
	if !nullEqual(auth["tokenDecimals"], policy.TokenDecimals) || !nullEqual(auth["tokenContractCanonical"], policy.TokenContractCanonical) || number(prepared.NetworkFeeBudgetAtomic).Cmp(number(authString(auth, "maxNetworkFeeAtomic"))) > 0 {
		return nil, ErrPrepared
	}
	raw, e := hex.DecodeString(prepared.UnsignedTransaction)
	if e != nil || len(raw) == 0 || len(raw) > 16384 || hex.EncodeToString(raw) != prepared.UnsignedTransaction {
		return nil, ErrPrepared
	}
	protocol, ok := protocolForPayload(payloadType)
	if !ok || algorithm != protocol.hashAlgorithm || !protocol.supportsChain(policy.Chain) {
		return nil, ErrPrepared
	}
	verified, e := protocol.verifyPrepared(raw, prepared, policy, auth, now)
	if e != nil || verified.budget == "" || verified.budget != prepared.NetworkFeeBudgetAtomic || !bytes.Equal(verified.digest, digest) {
		return nil, ErrPrepared
	}
	return &VerifiedPrepared{Authorization: auth, network: verified.network}, nil
}
