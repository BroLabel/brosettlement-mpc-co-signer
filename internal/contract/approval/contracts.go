// Package approval implements the closed approval-v1 wire boundary, without
// transport, scheduling, preparation, journal, or MPC execution side effects.
package approval

import (
	"bytes"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/contract/mpc2of3"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/strictjson"
	coretss "github.com/BroLabel/brosettlement-mpc-core/tss"
	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcutil/base58"
	"github.com/gowebpki/jcs"
	"golang.org/x/crypto/sha3"
)

const DecisionDomain = "brosettlement.co-signer.approval-decision/v1\n"

type Object map[string]json.RawMessage

var hex256 = regexp.MustCompile(`^[0-9a-f]{64}$`)
var uintPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)
var evmAddress = regexp.MustCompile(`^0x[0-9a-f]{40}$`)
var timestampPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`)
var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func invalid() error { return errors.New("invalid approval v1 contract") }
func parse(raw []byte, maximum int, canonical bool) (Object, error) {
	if len(raw) == 0 || len(raw) > maximum || !utf8.Valid(raw) {
		return nil, invalid()
	}
	var fields Object
	if err := strictjson.DecodeClosed(raw, &fields); err != nil {
		return nil, err
	}
	if fields == nil {
		return nil, invalid()
	}
	if canonical {
		if err := mpc2of3.RequireCanonicalJCS(raw); err != nil {
			return nil, err
		}
	}
	return fields, nil
}
func closed(v Object, keys string) bool {
	expected := strings.Split(keys, ",")
	if len(v) != len(expected) {
		return false
	}
	for _, k := range expected {
		if _, ok := v[k]; !ok {
			return false
		}
	}
	return true
}
func str(v Object, k string) string { var s string; _ = json.Unmarshal(v[k], &s); return s }
func integer(v Object, k string) int64 {
	var n int64
	if len(v[k]) == 0 || v[k][0] < '0' || v[k][0] > '9' || json.Unmarshal(v[k], &n) != nil {
		return -1
	}
	return n
}
func isNull(v Object, k string) bool { return bytes.Equal(v[k], []byte("null")) }
func bounded(s string, maximum int) bool {
	if len(s) == 0 || len(s) > maximum || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func timestamp(s string) bool {
	t, e := time.Parse("2006-01-02T15:04:05.000Z", s)
	return e == nil && timestampPattern.MatchString(s) && t.UnixMilli() > 0 && t.UTC().Format("2006-01-02T15:04:05.000Z") == s
}
func rawObject(v Object, k string) Object { var o Object; _ = json.Unmarshal(v[k], &o); return o }
func hash(raw []byte) string              { h := sha256.Sum256(raw); return hex.EncodeToString(h[:]) }
func standardBase64(s string, n int) ([]byte, error) {
	b, e := base64.StdEncoding.DecodeString(s)
	if e != nil || base64.StdEncoding.EncodeToString(b) != s || n >= 0 && len(b) != n {
		return nil, invalid()
	}
	return b, nil
}
func validReason(v Object, key string, rejected bool) bool {
	if !rejected {
		return isNull(v, key)
	}
	r := rawObject(v, key)
	return closed(r, "code,text") && regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`).MatchString(str(r, "code")) && bounded(str(r, "text"), 512)
}
func canonicalAddress(s string, ethereum bool) bool {
	if ethereum {
		return evmAddress.MatchString(s)
	}
	payload, version, e := base58.CheckDecode(s)
	return e == nil && version == 0x41 && len(payload) == 20 && base58.CheckEncode(payload, version) == s
}

// ParseAuthorization validates immutable exact JCS bytes, preserving all signed fields.
func ParseAuthorization(raw []byte) (Object, error) {
	v, e := parse(raw, 16384, true)
	if e != nil {
		return nil, e
	}
	if !closed(v, "version,orgId,requestId,ledgerTransactionId,clientReference,accountId,walletId,keyId,descriptorFingerprint,derivationContextHash,derivationContext,chain,fromAddress,toAddress,asset,tokenStandard,tokenContractCanonical,tokenDecimals,amountAtomic,transferKind,internalRecipient,networkFeeAsset,maxNetworkFeeAtomic,requestedChainParams") || integer(v, "version") != 1 {
		return nil, invalid()
	}
	for _, k := range []string{"orgId", "requestId", "ledgerTransactionId", "accountId", "walletId", "keyId"} {
		if !bounded(str(v, k), 128) {
			return nil, invalid()
		}
	}
	if !isNull(v, "clientReference") && !bounded(str(v, "clientReference"), 128) {
		return nil, invalid()
	}
	if _, e := mpc2of3.ParseDescriptorFingerprint(str(v, "descriptorFingerprint")); e != nil {
		return nil, e
	}
	if !hex256.MatchString(str(v, "derivationContextHash")) {
		return nil, invalid()
	}
	chain := str(v, "chain")
	ethereum := strings.HasPrefix(chain, "ethereum:")
	if chain != "ethereum:mainnet" && chain != "ethereum:sepolia" && chain != "tron:mainnet" && chain != "tron:nile" {
		return nil, invalid()
	}
	d := rawObject(v, "derivationContext")
	if !closed(d, "profileId,profileTemplateId,profileVersion,chain,algorithm,curve,scheme,accountPath,childPath,fullPath,addressEncoding,expectedAddress,expectedPublicKey,publicKeyFormat,descriptorVersion,keyVersion") {
		return nil, invalid()
	}
	for k := range d {
		if k == "profileVersion" || k == "descriptorVersion" || k == "keyVersion" {
			n := integer(d, k)
			if n < 1 || n > 2147483647 {
				return nil, invalid()
			}
		} else if !bounded(str(d, k), 1024) {
			return nil, invalid()
		}
	}
	if str(d, "algorithm") != "ECDSA" || str(d, "curve") != "secp256k1" || str(d, "scheme") != "bip32_secp256k1" || str(d, "publicKeyFormat") != "uncompressed_hex" || str(d, "chain") != chain || str(d, "expectedAddress") != str(v, "fromAddress") {
		return nil, invalid()
	}
	ctx := coretss.DerivationContext{ProfileID: str(d, "profileId"), ProfileTemplateID: str(d, "profileTemplateId"), ProfileVersion: uint32(integer(d, "profileVersion")), Chain: chain, Algorithm: str(d, "algorithm"), Curve: str(d, "curve"), Scheme: str(d, "scheme"), AccountPath: str(d, "accountPath"), ChildPath: str(d, "childPath"), FullPath: str(d, "fullPath"), AddressEncoding: str(d, "addressEncoding"), ExpectedAddress: str(d, "expectedAddress"), DerivedPublicKey: str(d, "expectedPublicKey"), PublicKeyFormat: str(d, "publicKeyFormat"), DescriptorVersion: uint32(integer(d, "descriptorVersion")), KeyVersion: uint32(integer(d, "keyVersion"))}
	got, e := coretss.DerivationContextHashV1(ctx)
	if e != nil || got != str(v, "derivationContextHash") {
		return nil, invalid()
	}
	pubRaw, e := hex.DecodeString(str(d, "expectedPublicKey"))
	if e != nil || len(pubRaw) != 65 || pubRaw[0] != 4 {
		return nil, invalid()
	}
	pub, e := btcec.ParsePubKey(pubRaw, btcec.S256())
	if e != nil {
		return nil, invalid()
	}
	h := sha3.NewLegacyKeccak256()
	_, _ = h.Write(pub.SerializeUncompressed()[1:])
	addr := h.Sum(nil)[12:]
	expected := "0x" + hex.EncodeToString(addr)
	encoding := "evm_hex"
	native := "ETH"
	standard := "erc20"
	if !ethereum {
		expected = base58.CheckEncode(addr, 0x41)
		encoding = "tron_base58"
		native = "TRX"
		standard = "trc20"
	}
	if str(v, "fromAddress") != expected || str(d, "addressEncoding") != encoding || !canonicalAddress(str(v, "toAddress"), ethereum) || !canonicalAddress(str(v, "fromAddress"), ethereum) {
		return nil, invalid()
	}
	amount := str(v, "amountAtomic")
	cap := str(v, "maxNetworkFeeAtomic")
	if !uintPattern.MatchString(amount) || amount == "0" || !uintPattern.MatchString(cap) || !bounded(str(v, "asset"), 128) || str(v, "networkFeeAsset") != native {
		return nil, invalid()
	}
	amountN, _ := new(big.Int).SetString(amount, 10)
	maximum := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))
	if !ethereum && isNull(v, "tokenStandard") {
		maximum = big.NewInt(9007199254740991)
	}
	if amountN.Cmp(maximum) > 0 {
		return nil, invalid()
	}
	if isNull(v, "tokenStandard") {
		if !isNull(v, "tokenContractCanonical") || !isNull(v, "tokenDecimals") || str(v, "asset") != native {
			return nil, invalid()
		}
	} else if str(v, "tokenStandard") != standard || !evmAddress.MatchString(str(v, "tokenContractCanonical")) || integer(v, "tokenDecimals") < 0 || integer(v, "tokenDecimals") > 255 {
		return nil, invalid()
	}
	if str(v, "transferKind") == "INTERNAL_TRANSFER" {
		r := rawObject(v, "internalRecipient")
		if !closed(r, "accountId,walletId") || !bounded(str(r, "accountId"), 128) || !bounded(str(r, "walletId"), 128) {
			return nil, invalid()
		}
	} else if str(v, "transferKind") != "EXTERNAL_WITHDRAWAL" || !isNull(v, "internalRecipient") {
		return nil, invalid()
	}
	if !isNull(v, "requestedChainParams") {
		p := rawObject(v, "requestedChainParams")
		if ethereum {
			q := rawObject(p, "ethereum")
			if !closed(p, "ethereum") || !closed(q, "maxFeePerGas,maxPriorityFeePerGas") || !uintPattern.MatchString(str(q, "maxFeePerGas")) || !uintPattern.MatchString(str(q, "maxPriorityFeePerGas")) {
				return nil, invalid()
			}
			a, _ := new(big.Int).SetString(str(q, "maxFeePerGas"), 10)
			b, _ := new(big.Int).SetString(str(q, "maxPriorityFeePerGas"), 10)
			if b.Cmp(a) > 0 {
				return nil, invalid()
			}
		} else {
			q := rawObject(p, "tron")
			if !closed(p, "tron") || !closed(q, "feeLimitSun") || !isNull(q, "feeLimitSun") && !uintPattern.MatchString(str(q, "feeLimitSun")) {
				return nil, invalid()
			}
		}
	}
	return v, nil
}

func ParseDecisionEnvelope(raw []byte) (Object, error) {
	v, e := parse(raw, 4096, true)
	if e != nil {
		return nil, e
	}
	if !closed(v, "version,domain,audience,environment,orgId,requestId,authorizationHash,decision,reason,source,externalDecisionReference,decidedAt,signingKeyId,configRevision") || integer(v, "version") != 1 || str(v, "domain") != "brosettlement.co-signer.approval-decision" || str(v, "audience") != "brosettlement-transfer-approval" {
		return nil, invalid()
	}
	for _, k := range []string{"environment", "orgId", "requestId", "signingKeyId", "configRevision"} {
		if !bounded(str(v, k), 128) {
			return nil, invalid()
		}
	}
	decision := str(v, "decision")
	source := str(v, "source")
	if !hex256.MatchString(str(v, "authorizationHash")) || !timestamp(str(v, "decidedAt")) || decision != "APPROVED" && decision != "REJECTED" || !validReason(v, "reason", decision == "REJECTED") || source != "external" && source != "auto_approve" || !isNull(v, "externalDecisionReference") && !bounded(str(v, "externalDecisionReference"), 128) || source == "auto_approve" && (decision != "APPROVED" || !isNull(v, "externalDecisionReference")) {
		return nil, invalid()
	}
	return v, nil
}

type Receipt struct {
	Envelope                 Object
	EnvelopeBytes, Signature []byte
}

func ParseReceipt(raw []byte) (Receipt, error) {
	v, e := parse(raw, 8192, false)
	if e != nil {
		return Receipt{}, e
	}
	if !closed(v, "version,envelopeBase64,signatureBase64") || integer(v, "version") != 1 {
		return Receipt{}, invalid()
	}
	envelope, e := standardBase64(str(v, "envelopeBase64"), -1)
	if e != nil {
		return Receipt{}, e
	}
	signature, e := standardBase64(str(v, "signatureBase64"), 64)
	if e != nil {
		return Receipt{}, e
	}
	parsed, e := ParseDecisionEnvelope(envelope)
	if e != nil {
		return Receipt{}, e
	}
	return Receipt{parsed, envelope, signature}, nil
}

type TrustedReceipt struct {
	OrgID, RequestID, AuthorizationHash, Environment, SigningKeyID string
	PublicKey                                                      ed25519.PublicKey
	Eligible                                                       bool
}

func VerifyReceipt(raw []byte, trusted TrustedReceipt) (Receipt, error) {
	r, e := ParseReceipt(raw)
	if e != nil {
		return Receipt{}, e
	}
	for k, want := range map[string]string{"orgId": trusted.OrgID, "requestId": trusted.RequestID, "authorizationHash": trusted.AuthorizationHash, "environment": trusted.Environment, "signingKeyId": trusted.SigningKeyID} {
		if str(r.Envelope, k) != want {
			return Receipt{}, invalid()
		}
	}
	if !trusted.Eligible || len(trusted.PublicKey) != ed25519.PublicKeySize || !ed25519.Verify(trusted.PublicKey, append([]byte(DecisionDomain), r.EnvelopeBytes...), r.Signature) {
		return Receipt{}, invalid()
	}
	return r, nil
}
func SameReceipt(left, right []byte) (bool, error) {
	a, e := ParseReceipt(left)
	if e != nil {
		return false, e
	}
	b, e := ParseReceipt(right)
	if e != nil {
		return false, e
	}
	return bytes.Equal(a.EnvelopeBytes, b.EnvelopeBytes) && bytes.Equal(a.Signature, b.Signature), nil
}
func ParseOrganizationResponse(raw []byte, orgID, requestID, authorizationHash string) (Object, error) {
	v, e := parse(raw, 65536, false)
	if e != nil {
		return nil, e
	}
	status := str(v, "status")
	if !closed(v, "version,orgId,requestId,authorizationHash,status,reason,decisionReference") || integer(v, "version") != 1 || str(v, "orgId") != orgID || str(v, "requestId") != requestID || str(v, "authorizationHash") != authorizationHash || status != "PENDING" && status != "APPROVED" && status != "REJECTED" || !validReason(v, "reason", status == "REJECTED") || !isNull(v, "decisionReference") && !bounded(str(v, "decisionReference"), 128) || status == "PENDING" && !isNull(v, "decisionReference") {
		return nil, invalid()
	}
	return v, nil
}
func ParseDecisionApplication(raw []byte) (Object, error) {
	v, e := parse(raw, 4096, false)
	if e != nil {
		return nil, e
	}
	if !closed(v, "version,requestId,outcome,disposition") || integer(v, "version") != 1 || !bounded(str(v, "requestId"), 128) {
		return nil, invalid()
	}
	outcome, disposition := str(v, "outcome"), str(v, "disposition")
	valid := outcome == "ACCEPTED" && disposition == "APPLIED" || outcome == "EXACT_REPLAY" && (disposition == "APPLIED" || disposition == "NOT_APPLIED_CANCELLED") || outcome == "NOT_APPLIED_CANCELLED" && disposition == outcome || outcome == "TERMINAL_CONFLICT" && disposition == outcome
	if !valid {
		return nil, invalid()
	}
	return v, nil
}

// VerifyPendingCursorFixture independently verifies the backend service's fixed
// binding/domain/MAC contract. Runtime Co-Signer treats cursor bytes as opaque.
func VerifyPendingCursorFixture(token string, binding json.RawMessage, secret []byte) error {
	if len(token) == 0 || len(token) > 4096 {
		return invalid()
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return invalid()
	}
	raw, e := base64.RawURLEncoding.DecodeString(parts[0])
	if e != nil || base64.RawURLEncoding.EncodeToString(raw) != parts[0] {
		return invalid()
	}
	canonicalBinding, e := jcs.Transform(binding)
	if e != nil {
		return e
	}
	mac := func(domain string) string {
		h := hmac.New(sha256.New, secret)
		_, _ = h.Write([]byte(domain))
		return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
	}
	if !hmac.Equal([]byte(parts[1]), []byte(mac("token:v1\n"+parts[0]))) {
		return invalid()
	}
	v, e := parse(raw, 4096, true)
	if e != nil {
		return e
	}
	var b Object
	if json.Unmarshal(binding, &b) != nil {
		return invalid()
	}
	if !closed(v, "v,resource,bindingHash,position") || integer(v, "v") != 1 || str(v, "resource") != str(b, "resource") || str(v, "bindingHash") != mac("binding:v1\n"+string(canonicalBinding)) {
		return invalid()
	}
	return ValidatePendingPosition(v["position"])
}
func ValidatePendingPosition(raw []byte) error {
	v, e := parse(raw, 4096, false)
	if e != nil {
		return e
	}
	if !closed(v, "upper,after") {
		return invalid()
	}
	upper, after := rawObject(v, "upper"), rawObject(v, "after")
	for _, pair := range []Object{upper, after} {
		if !closed(pair, "createdAt,id") || !timestamp(str(pair, "createdAt")) || !uuidPattern.MatchString(str(pair, "id")) {
			return invalid()
		}
	}
	if str(after, "createdAt") > str(upper, "createdAt") || str(after, "createdAt") == str(upper, "createdAt") && str(after, "id") > str(upper, "id") {
		return invalid()
	}
	return nil
}
func ParsePendingPage(raw []byte, orgID string) error {
	v, e := parse(raw, 2*1024*1024, false)
	if e != nil {
		return e
	}
	if !closed(v, "version,items,nextCursor") || integer(v, "version") != 1 {
		return invalid()
	}
	if !isNull(v, "nextCursor") && (len(str(v, "nextCursor")) == 0 || len(str(v, "nextCursor")) > 4096) {
		return invalid()
	}
	var items []json.RawMessage
	if json.Unmarshal(v["items"], &items) != nil || bytes.Equal(v["items"], []byte("null")) {
		return invalid()
	}
	for _, item := range items {
		row, e := parse(item, 32768, false)
		if e != nil {
			return e
		}
		if !closed(row, "requestId,createdAt,authorization,authorizationHash") || !timestamp(str(row, "createdAt")) {
			return invalid()
		}
		auth, e := jcs.Transform(row["authorization"])
		if e != nil {
			return e
		}
		a, e := ParseAuthorization(auth)
		if e != nil {
			return e
		}
		if str(a, "orgId") != orgID || str(row, "requestId") != str(a, "requestId") || hash(auth) != str(row, "authorizationHash") {
			return invalid()
		}
	}
	return nil
}

func hexSeed(value string) ([]byte, error) {
	b, e := hex.DecodeString(value)
	if e != nil || len(b) != 32 {
		return nil, invalid()
	}
	return b, nil
}
func bytesEqual(a, b []byte) bool { return bytes.Equal(a, b) }
