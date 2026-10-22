package approval

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	wire "github.com/BroLabel/brosettlement-mpc-co-signer/internal/contract/approval"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/lifecycle"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/signingpolicy"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/strictjson"
)

var ErrIntegrity = errors.New("approval journal integrity failure; reconcile durable evidence")
var ErrConflict = errors.New("approval immutable evidence conflict")

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
type State struct {
	Admission   *Admission
	Receipt     *SavedReceipt
	Application []byte
	Execution   *ExecutionBinding
}
type Journal struct {
	directory string
	identity  os.FileInfo
	lock      *lifecycle.LifetimeLock
	scan      *os.File
	mu        sync.Mutex
	ops       journalOperations
	closed    bool
}

func OpenJournal(directory string) (*Journal, error) {
	if !filepath.IsAbs(directory) {
		return nil, ErrIntegrity
	}
	dir, e := filepath.EvalSymlinks(directory)
	if e != nil {
		return nil, e
	}
	info, e := os.Stat(dir)
	if e != nil {
		return nil, e
	}
	if e = privateDirectory(info); e != nil {
		return nil, e
	}
	lockPath := filepath.Join(dir, ".approval.lock")
	if i, e := os.Lstat(lockPath); e == nil {
		if !i.Mode().IsRegular() || i.Mode().Perm() != 0600 {
			return nil, ErrIntegrity
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return nil, e
	}
	lock, e := lifecycle.AcquireLifetimeLock(lockPath)
	if e != nil {
		return nil, e
	}
	j := &Journal{directory: dir, identity: info, lock: lock, ops: realJournalOperations{}}
	if e = j.probe(); e != nil {
		lock.Close()
		return nil, e
	}
	return j, nil
}
func (j *Journal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return nil
	}
	j.closed = true
	var e error
	if j.scan != nil {
		e = j.scan.Close()
		j.scan = nil
	}
	return errors.Join(e, j.lock.Close())
}
func (j *Journal) check() error {
	if j.closed {
		return ErrIntegrity
	}
	i, e := os.Stat(j.directory)
	if e != nil {
		return e
	}
	if !os.SameFile(i, j.identity) {
		return ErrIntegrity
	}
	return privateDirectory(i)
}
func digest(raw []byte) string             { h := sha256.Sum256(raw); return hex.EncodeToString(h[:]) }
func recordKey(org, request string) string { return digest([]byte(org + "\x00" + request)) }
func (j *Journal) path(org, request, kind string) string {
	return filepath.Join(j.directory, recordKey(org, request)+"."+kind+".json")
}
func (j *Journal) probe() error {
	f, e := os.CreateTemp(j.directory, ".approval-probe-*")
	if e != nil {
		return e
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	defer os.Remove(name)
	if e = j.publish(name, []byte("approval-v1")); e != nil {
		return e
	}
	if e = j.publish(name, []byte("changed")); !errors.Is(e, os.ErrExist) {
		return ErrIntegrity
	}
	raw, e := readJournalFile(name)
	if e != nil || string(raw) != "approval-v1" {
		return ErrIntegrity
	}
	if e = os.Remove(name); e != nil {
		return e
	}
	return j.ops.SyncDirectory(j.directory)
}
func (j *Journal) put(path string, value any) error {
	raw, e := json.Marshal(value)
	if e != nil {
		return e
	}
	if len(raw) == 0 || len(raw) > 32768 {
		return ErrIntegrity
	}
	if e = j.publish(path, raw); errors.Is(e, os.ErrExist) {
		old, re := readJournalFile(path)
		if re != nil {
			return re
		}
		if !bytes.Equal(old, raw) {
			return ErrConflict
		}
		return j.ops.SyncDirectory(j.directory)
	}
	return e
}
func decodeRecord(raw []byte, out any, fields string) error {
	if e := strictjson.DecodeClosed(raw, out); e != nil {
		return errors.Join(ErrIntegrity, e)
	}
	var m map[string]json.RawMessage
	if e := json.Unmarshal(raw, &m); e != nil {
		return ErrIntegrity
	}
	keys := strings.Split(fields, ",")
	if len(m) != len(keys) {
		return ErrIntegrity
	}
	for _, k := range keys {
		if _, ok := m[k]; !ok {
			return ErrIntegrity
		}
	}
	return nil
}
func (j *Journal) read(org, req, kind string, out any, fields string) (bool, error) {
	raw, e := readJournalFile(j.path(org, req, kind))
	if errors.Is(e, os.ErrNotExist) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	return true, decodeRecord(raw, out, fields)
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
func (j *Journal) load(org, request string) (State, error) {
	var s State
	if !wire.ValidReference(org) || !wire.ValidReference(request) {
		return s, ErrIntegrity
	}
	if e := j.check(); e != nil {
		return s, e
	}
	a := new(Admission)
	ok, e := j.read(org, request, "admission", a, "version,orgId,requestId,authorizationHash,mode,environment,configRevision,endpoint,admittedAt")
	if e != nil {
		return s, e
	}
	if ok {
		if a.OrgID != org || a.RequestID != request || a.validate() != nil {
			return s, ErrIntegrity
		}
		s.Admission = a
		rev := revisionRecord{1, a.OrgID, a.Mode, a.Environment, a.ConfigRevision, a.Endpoint}
		b, _ := json.Marshal(rev)
		raw, e := readJournalFile(j.revisionPath(a.OrgID, a.ConfigRevision))
		if e != nil || !bytes.Equal(raw, b) {
			return s, ErrIntegrity
		}
	}
	r := new(SavedReceipt)
	ok, e = j.read(org, request, "receipt", r, "version,orgId,requestId,authorizationHash,signingKeyId,publicKey,bytes")
	if e != nil {
		return s, e
	}
	if ok {
		if s.Admission == nil || j.validateReceipt(*a, *r) != nil {
			return s, ErrIntegrity
		}
		s.Receipt = r
	}
	ack := new(deliveryAck)
	ok, e = j.read(org, request, "ack", ack, "version,orgId,requestId,receiptHash,application")
	if e != nil {
		return s, e
	}
	if ok {
		if s.Receipt == nil || ack.Version != 1 || ack.OrgID != org || ack.RequestID != request || ack.ReceiptHash != digest(r.Bytes) {
			return s, ErrIntegrity
		}
		if e = validateApplication(ack.Application, request); e != nil {
			return s, e
		}
		s.Application = ack.Application
	}
	x := new(ExecutionBinding)
	xok, e := j.read(org, request, "execution", x, "version,orgId,requestId,outgoingId,authorizationHash,executionDigest,payloadType,hashAlgorithm")
	if e != nil {
		return s, e
	}
	known := new(executionKnown)
	kok, e := j.read(org, request, "execution-known", known, "version,orgId,requestId,authorizationHash,bindingHash")
	if e != nil {
		return s, e
	}
	if xok != kok {
		return s, ErrIntegrity
	}
	if xok {
		if e = validateExecution(s, *x); e != nil {
			return s, e
		}
		raw, e := readJournalFile(j.path(org, request, "execution"))
		if e != nil {
			return s, e
		}
		if known.Version != 1 || known.OrgID != org || known.RequestID != request || known.AuthorizationHash != s.Admission.AuthorizationHash || known.BindingHash != digest(raw) {
			return s, ErrIntegrity
		}
		s.Execution = x
	}
	// A renamed record with failed directory fsync must be made durable before use.
	if e = j.ops.SyncDirectory(j.directory); e != nil {
		return s, e
	}
	return s, nil
}
func (j *Journal) Load(org, request string) (State, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.load(org, request)
}
func (j *Journal) revisionPath(org, revision string) string {
	return filepath.Join(j.directory, recordKey(org, revision)+".config.json")
}
func (j *Journal) Admit(a Admission) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if e := a.validate(); e != nil {
		return e
	}
	s, e := j.load(a.OrgID, a.RequestID)
	if e != nil {
		return e
	}
	if s.Admission != nil {
		old := *s.Admission
		a.AdmittedAt = old.AdmittedAt
		if !equalJSON(old, a) {
			return ErrConflict
		}
		return nil
	}
	if e = j.put(j.revisionPath(a.OrgID, a.ConfigRevision), revisionRecord{1, a.OrgID, a.Mode, a.Environment, a.ConfigRevision, a.Endpoint}); e != nil {
		return e
	}
	return j.put(j.path(a.OrgID, a.RequestID, "admission"), a)
}
func equalJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}
func (j *Journal) validateReceipt(a Admission, r SavedReceipt) error {
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
func (j *Journal) SaveReceipt(r SavedReceipt) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	s, e := j.load(r.OrgID, r.RequestID)
	if e != nil {
		return e
	}
	if s.Admission == nil {
		return ErrIntegrity
	}
	if e = j.validateReceipt(*s.Admission, r); e != nil {
		return e
	}
	return j.put(j.path(r.OrgID, r.RequestID, "receipt"), r)
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
func (j *Journal) Acknowledge(org, request string, application []byte) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	s, e := j.load(org, request)
	if e != nil {
		return e
	}
	if s.Receipt == nil {
		return ErrIntegrity
	}
	if e = validateApplication(application, request); e != nil {
		return e
	}
	return j.put(j.path(org, request, "ack"), deliveryAck{1, org, request, digest(s.Receipt.Bytes), application})
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
func (j *Journal) BindExecution(x ExecutionBinding) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	s, e := j.load(x.OrgID, x.RequestID)
	if e != nil {
		return e
	}
	if e = validateExecution(s, x); e != nil {
		return e
	}
	raw, e := json.Marshal(x)
	if e != nil {
		return e
	}
	if e = j.put(j.path(x.OrgID, x.RequestID, "execution-known"), executionKnown{1, x.OrgID, x.RequestID, x.AuthorizationHash, digest(raw)}); e != nil {
		return e
	}
	return j.put(j.path(x.OrgID, x.RequestID, "execution"), x)
}

// PendingReceipts streams at most limit filenames and retains at most one receipt body.
// Delivery is performed outside the journal lock, independently of fresh discovery.
func (j *Journal) PendingReceipts(limit int) ([]SavedReceipt, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if limit < 1 || limit > 1000 {
		return nil, ErrIntegrity
	}
	if e := j.check(); e != nil {
		return nil, e
	}
	if j.scan == nil {
		f, e := os.Open(j.directory)
		if e != nil {
			return nil, e
		}
		j.scan = f
	}
	var scanErr error
	var result error
	var receipts []SavedReceipt
	for i := 0; i < limit; i++ {
		var names []string
		names, scanErr = j.scan.Readdirnames(1)
		if scanErr != nil && scanErr != io.EOF {
			return nil, scanErr
		}
		if len(names) == 0 {
			break
		}
		name := names[0]
		if !strings.HasSuffix(name, ".receipt.json") {
			continue
		}
		raw, e := readJournalFile(filepath.Join(j.directory, name))
		if e != nil {
			result = errors.Join(result, e)
			continue
		}
		var r SavedReceipt
		if e = decodeRecord(raw, &r, "version,orgId,requestId,authorizationHash,signingKeyId,publicKey,bytes"); e != nil || name != filepath.Base(j.path(r.OrgID, r.RequestID, "receipt")) {
			result = errors.Join(result, ErrIntegrity)
			continue
		}
		s, e := j.load(r.OrgID, r.RequestID)
		if e != nil {
			result = errors.Join(result, e)
			continue
		}
		if len(s.Application) == 0 {
			receipts = append(receipts, *s.Receipt)
			break
		}
	}
	if scanErr == io.EOF {
		j.scan.Close()
		j.scan = nil
	}
	return receipts, result
}

func field(v wire.Object, k string) string { var s string; _ = json.Unmarshal(v[k], &s); return s }
func validateDigest(s string) error {
	if !wire.ValidExecutionDigest(s) {
		return ErrIntegrity
	}
	return nil
}
