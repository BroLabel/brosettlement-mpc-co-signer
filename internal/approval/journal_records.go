package approval

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"

	wire "github.com/BroLabel/brosettlement-mpc-co-signer/internal/contract/approval"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/signingpolicy"
)

const (
	admissionFields       = "version,orgId,requestId,authorizationHash,mode,environment,configRevision,endpoint,admittedAt"
	receiptFields         = "version,orgId,requestId,authorizationHash,signingKeyId,publicKey,bytes"
	acknowledgementFields = "version,orgId,requestId,receiptHash,application"
	executionFields       = "version,orgId,requestId,outgoingId,authorizationHash,executionDigest,payloadType,hashAlgorithm"
	executionKnownFields  = "version,orgId,requestId,authorizationHash,bindingHash"
)

type Admission struct {
	Version           int     `json:"version"`
	OrgID             string  `json:"orgId"`
	RequestID         string  `json:"requestId"`
	AuthorizationHash string  `json:"authorizationHash"`
	Mode              string  `json:"mode"`
	Environment       string  `json:"environment"`
	ConfigRevision    string  `json:"configRevision"`
	Endpoint          *string `json:"endpoint"`
	AdmittedAt        string  `json:"admittedAt"`
}
type SavedReceipt struct {
	Version           int    `json:"version"`
	OrgID             string `json:"orgId"`
	RequestID         string `json:"requestId"`
	AuthorizationHash string `json:"authorizationHash"`
	SigningKeyID      string `json:"signingKeyId"`
	PublicKey         []byte `json:"publicKey"`
	Bytes             []byte `json:"bytes"`
}
type ExecutionBinding struct {
	Version           int    `json:"version"`
	OrgID             string `json:"orgId"`
	RequestID         string `json:"requestId"`
	OutgoingID        string `json:"outgoingId"`
	AuthorizationHash string `json:"authorizationHash"`
	ExecutionDigest   string `json:"executionDigest"`
	PayloadType       string `json:"payloadType"`
	HashAlgorithm     string `json:"hashAlgorithm"`
}

// executionKnown is a narrow guard against single-artifact loss. Partial
// publication is never repaired from a backend claim; restore requires review.
type executionKnown struct {
	Version           int    `json:"version"`
	OrgID             string `json:"orgId"`
	RequestID         string `json:"requestId"`
	AuthorizationHash string `json:"authorizationHash"`
	BindingHash       string `json:"bindingHash"`
}
type deliveryAck struct {
	Version     int    `json:"version"`
	OrgID       string `json:"orgId"`
	RequestID   string `json:"requestId"`
	ReceiptHash string `json:"receiptHash"`
	Application []byte `json:"application"`
}
type revisionRecord struct {
	Version        int     `json:"version"`
	OrgID          string  `json:"orgId"`
	Mode           string  `json:"mode"`
	Environment    string  `json:"environment"`
	ConfigRevision string  `json:"configRevision"`
	Endpoint       *string `json:"endpoint"`
}

func (a Admission) validate() error {
	if a.Version != 1 || !wire.ValidReference(a.OrgID) || !wire.ValidReference(a.RequestID) || !wire.ValidAuthorizationHash(a.AuthorizationHash) || !wire.ValidReference(a.Environment) || !wire.ValidReference(a.ConfigRevision) || !wire.ValidTimestamp(a.AdmittedAt) {
		return ErrIntegrity
	}
	if a.Mode == "auto_approve" && a.Endpoint == nil {
		return nil
	}
	if a.Mode == "external" && a.Endpoint != nil && validEndpoint(*a.Endpoint) {
		return nil
	}
	return ErrIntegrity
}

func (a Admission) revision() revisionRecord {
	return revisionRecord{
		Version:        1,
		OrgID:          a.OrgID,
		Mode:           a.Mode,
		Environment:    a.Environment,
		ConfigRevision: a.ConfigRevision,
		Endpoint:       a.Endpoint,
	}
}

func equalJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}
func validateReceipt(a Admission, r SavedReceipt) error {
	if r.Version != 1 || r.OrgID != a.OrgID || r.RequestID != a.RequestID || r.AuthorizationHash != a.AuthorizationHash {
		return ErrIntegrity
	}
	v, e := wire.VerifyReceipt(r.Bytes, wire.TrustedReceipt{OrgID: a.OrgID, RequestID: a.RequestID, AuthorizationHash: a.AuthorizationHash, Environment: a.Environment, SigningKeyID: r.SigningKeyID, PublicKey: ed25519.PublicKey(r.PublicKey), Eligible: true})
	if e != nil {
		return e
	}
	if field(v.Envelope, "source") != a.Mode || field(v.Envelope, "configRevision") != a.ConfigRevision {
		return ErrIntegrity
	}
	return nil
}

func validateApplication(raw []byte, request string) error {
	v, e := wire.ParseDecisionApplication(raw)
	if e != nil {
		return e
	}
	if field(v, "requestId") != request {
		return ErrIntegrity
	}
	return nil
}

func validateExecution(s State, x ExecutionBinding) error {
	if s.Admission == nil || s.Receipt == nil || len(s.Application) == 0 {
		return ErrIntegrity
	}
	v, e := wire.ParseDecisionApplication(s.Application)
	if e != nil || field(v, "disposition") != "APPLIED" {
		return ErrIntegrity
	}
	r, e := wire.ParseReceipt(s.Receipt.Bytes)
	if e != nil || field(r.Envelope, "decision") != "APPROVED" {
		return ErrIntegrity
	}
	if x.Version != 1 || x.OrgID != s.Admission.OrgID || x.RequestID != s.Admission.RequestID || x.AuthorizationHash != s.Admission.AuthorizationHash || !wire.ValidReference(x.OutgoingID) {
		return ErrIntegrity
	}
	if !signingpolicy.ValidPayloadHash(x.PayloadType, x.HashAlgorithm) {
		return ErrIntegrity
	}
	return validateDigest(x.ExecutionDigest)
}

func field(v wire.Object, k string) string { var s string; _ = json.Unmarshal(v[k], &s); return s }
func validateDigest(s string) error {
	if !wire.ValidExecutionDigest(s) {
		return ErrIntegrity
	}
	return nil
}
