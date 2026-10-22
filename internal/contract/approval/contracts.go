// Package approval implements the closed approval-v1 wire boundary, without
// transport, scheduling, preparation, journal, or MPC execution side effects.
package approval

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/contract/mpc2of3"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/strictjson"
	coretss "github.com/BroLabel/brosettlement-mpc-core/tss"
	"github.com/btcsuite/btcd/btcec"
	"github.com/gowebpki/jcs"
)

const DecisionDomain = "brosettlement.co-signer.approval-decision/v1\n"

type Object map[string]json.RawMessage

var hex256 = regexp.MustCompile(`^[0-9a-f]{64}$`)
var uintPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)
var timestampPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`)

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
	validateChain, ok := authorizationValidator(chain)
	if !ok {
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
	if err := validateChain(v, pub.SerializeUncompressed()); err != nil {
		return nil, err
	}
	if str(v, "transferKind") == "INTERNAL_TRANSFER" {
		r := rawObject(v, "internalRecipient")
		if !closed(r, "accountId,walletId") || !bounded(str(r, "accountId"), 128) || !bounded(str(r, "walletId"), 128) {
			return nil, invalid()
		}
	} else if str(v, "transferKind") != "EXTERNAL_WITHDRAWAL" || !isNull(v, "internalRecipient") {
		return nil, invalid()
	}
	return v, nil
}

func ParseDecisionEnvelope(raw []byte) (Object, error) {
	v, e := parse(raw, 4096, true)
	if e != nil {
		return nil, e
	}
	if !closed(v, "version,domain,audience,orgId,requestId,authorizationHash,decision,reason,source,externalDecisionReference,decidedAt,signingKeyId,configRevision") || integer(v, "version") != 1 || str(v, "domain") != "brosettlement.co-signer.approval-decision" || str(v, "audience") != "brosettlement-transfer-approval" {
		return nil, invalid()
	}
	for _, k := range []string{"orgId", "requestId", "signingKeyId", "configRevision"} {
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
	OrgID, RequestID, AuthorizationHash, SigningKeyID string
	PublicKey                                         ed25519.PublicKey
	Eligible                                          bool
}

func VerifyReceipt(raw []byte, trusted TrustedReceipt) (Receipt, error) {
	r, e := ParseReceipt(raw)
	if e != nil {
		return Receipt{}, e
	}
	for k, want := range map[string]string{"orgId": trusted.OrgID, "requestId": trusted.RequestID, "authorizationHash": trusted.AuthorizationHash, "signingKeyId": trusted.SigningKeyID} {
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
	if !isNull(v, "nextCursor") {
		for _, c := range []byte(str(v, "nextCursor")) {
			if c < 0x21 || c > 0x7e {
				return invalid()
			}
		}
	}
	var items []json.RawMessage
	if json.Unmarshal(v["items"], &items) != nil || bytes.Equal(v["items"], []byte("null")) || len(items) > 100 {
		return invalid()
	}
	seen := make(map[string]bool, len(items))
	previousTime, previousID := "", ""
	for _, item := range items {
		row, e := parse(item, 32768, false)
		if e != nil {
			return e
		}
		if !closed(row, "requestId,createdAt,authorization,authorizationHash") || !timestamp(str(row, "createdAt")) {
			return invalid()
		}
		id, created := str(row, "requestId"), str(row, "createdAt")
		if seen[id] || created < previousTime || created == previousTime && id <= previousID {
			return invalid()
		}
		seen[id] = true
		previousTime, previousID = created, id
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

// ValidReference exposes the frozen shared Id/Reference validation for trusted configuration.
func ValidReference(value string) bool { return bounded(value, 128) }

// PendingItem and PendingPage carry the frozen discovery wire shape. Validation
// still belongs to ParsePendingPage, including authorization canonicalization.
type PendingItem struct {
	RequestID         string          `json:"requestId"`
	CreatedAt         string          `json:"createdAt"`
	Authorization     json.RawMessage `json:"authorization"`
	AuthorizationHash string          `json:"authorizationHash"`
}
type PendingPage struct {
	Version    int           `json:"version"`
	Items      []PendingItem `json:"items"`
	NextCursor *string       `json:"nextCursor"`
}

// ValidAuthorizationHash and ValidTimestamp reuse frozen wire primitives for
// durable local records; they do not normalize signed values.
func ValidAuthorizationHash(value string) bool { return hex256.MatchString(value) }
func ValidTimestamp(value string) bool         { return timestamp(value) }
func ValidExecutionDigest(value string) bool   { _, err := standardBase64(value, 32); return err == nil }
