package approval

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/gowebpki/jcs"
)

func privateDir(t *testing.T) string {
	t.Helper()
	p := t.TempDir()
	if e := os.Chmod(p, 0700); e != nil {
		t.Fatal(e)
	}
	return p
}
func testConfig(t *testing.T) Config {
	return Config{Mode: "auto_approve", OrgID: "org-1", Environment: "test", Revision: "rev-1", JournalDir: privateDir(t), MinInterval: time.Millisecond, MaxInterval: time.Second, Timeout: time.Second, BackoffFactor: 2}
}
func testAdmission() Admission {
	return Admission{1, "org-1", "../../request", string(bytes.Repeat([]byte("a"), 64)), "auto_approve", "test", "rev-1", nil, "2026-10-19T01:00:00.000Z"}
}
func testReceipt(t *testing.T, a Admission) SavedReceipt {
	t.Helper()
	key := ed25519.NewKeyFromSeed(make([]byte, 32))
	env := map[string]any{"version": 1, "domain": "brosettlement.co-signer.approval-decision", "audience": "brosettlement-transfer-approval", "environment": a.Environment, "orgId": a.OrgID, "requestId": a.RequestID, "authorizationHash": a.AuthorizationHash, "decision": "APPROVED", "reason": nil, "source": a.Mode, "externalDecisionReference": nil, "decidedAt": "2026-10-19T02:00:00.000Z", "signingKeyId": "key-1", "configRevision": a.ConfigRevision}
	raw, _ := json.Marshal(env)
	raw, _ = jcs.Transform(raw)
	sig := ed25519.Sign(key, append([]byte("brosettlement.co-signer.approval-decision/v1\n"), raw...))
	body, _ := json.Marshal(map[string]any{"version": 1, "envelopeBase64": base64.StdEncoding.EncodeToString(raw), "signatureBase64": base64.StdEncoding.EncodeToString(sig)})
	return SavedReceipt{1, a.OrgID, a.RequestID, a.AuthorizationHash, "key-1", key.Public().(ed25519.PublicKey), body}
}
func TestJournalReopenPreservesExactReceiptAndRejectsConflict(t *testing.T) {
	dir := privateDir(t)
	j, e := OpenJournal(dir)
	if e != nil {
		t.Fatal(e)
	}
	a := testAdmission()
	if e = j.Admit(a); e != nil {
		t.Fatal(e)
	}
	r := testReceipt(t, a)
	if e = j.SaveReceipt(r); e != nil {
		t.Fatal(e)
	}
	a.AuthorizationHash = string(bytes.Repeat([]byte("b"), 64))
	if e = j.Admit(a); e == nil {
		t.Fatal("changed admission replaced original")
	}
	j.Close()
	j, e = OpenJournal(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer j.Close()
	state, e := j.Load(r.OrgID, r.RequestID)
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(state.Receipt.Bytes, r.Bytes) {
		t.Fatal("receipt bytes changed")
	}
	if e = j.Acknowledge(r.OrgID, r.RequestID, []byte(`{"version":1,"requestId":"../../request","outcome":"ACCEPTED","disposition":"APPLIED"}`)); e != nil {
		t.Fatal(e)
	}
	if e = os.Remove(j.path(r.OrgID, r.RequestID, "receipt")); e != nil {
		t.Fatal(e)
	}
	if _, e = j.Load(r.OrgID, r.RequestID); e == nil {
		t.Fatal("missing known receipt accepted")
	}
}
func TestJournalPrivateExistingDirectoryAndSeparateProcessLock(t *testing.T) {
	dir := privateDir(t)
	if _, e := OpenJournal(filepath.Join(dir, "missing")); !errors.Is(e, os.ErrNotExist) {
		t.Fatalf("missing journal recreated: %v", e)
	}
	j, e := OpenJournal(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer j.Close()
	c := exec.Command(os.Args[0], "-test.run=^TestJournalLockChild$")
	c.Env = append(os.Environ(), "APPROVAL_LOCK_CHILD="+dir)
	if out, e := c.CombinedOutput(); e != nil {
		t.Fatalf("lock child: %s %v", out, e)
	}
	if e = os.Chmod(dir, 0755); e != nil {
		t.Fatal(e)
	}
	if e = j.Admit(testAdmission()); e == nil {
		t.Fatal("unsafe permissions accepted")
	}
}
func TestJournalLockChild(t *testing.T) {
	dir := os.Getenv("APPROVAL_LOCK_CHILD")
	if dir == "" {
		return
	}
	if j, e := OpenJournal(dir); e == nil {
		j.Close()
		t.Fatal("second process acquired journal")
	}
}
func TestJournalDurabilityFailureDoesNotAuthorizeAndPublishedReceiptNeverRepolls(t *testing.T) {
	for _, stage := range []string{"write", "file-sync", "publish", "directory-sync"} {
		t.Run(stage, func(t *testing.T) {
			j, e := OpenJournal(privateDir(t))
			if e != nil {
				t.Fatal(e)
			}
			defer j.Close()
			a := testAdmission()
			if e = j.Admit(a); e != nil {
				t.Fatal(e)
			}
			j.ops = &faultOperations{journalOperations: j.ops, stage: stage}
			if e = j.SaveReceipt(testReceipt(t, a)); e == nil {
				t.Fatal("durability failure accepted")
			}
			j.ops = realJournalOperations{}
			s, e := j.Load(a.OrgID, a.RequestID)
			if e != nil {
				t.Fatal(e)
			}
			if stage == "directory-sync" && s.Receipt == nil {
				t.Fatal("published final lost")
			}
		})
	}
}

type faultOperations struct {
	journalOperations
	stage     string
	published bool
}

func (f *faultOperations) CreateTemp(d, p string) (journalFile, error) {
	v, e := f.journalOperations.CreateTemp(d, p)
	return &faultFile{journalFile: v, stage: f.stage}, e
}
func (f *faultOperations) Publish(a, b string) error {
	if f.stage == "publish" {
		return errors.New("injected publish")
	}
	e := f.journalOperations.Publish(a, b)
	if e == nil {
		f.published = true
	}
	return e
}
func (f *faultOperations) SyncDirectory(d string) error {
	if f.stage == "directory-sync" && f.published {
		return errors.New("injected directory sync")
	}
	return f.journalOperations.SyncDirectory(d)
}

type faultFile struct {
	journalFile
	stage string
}

func (f *faultFile) Write(b []byte) (int, error) {
	if f.stage == "write" {
		return 0, errors.New("injected write")
	}
	return f.journalFile.Write(b)
}
func (f *faultFile) Sync() error {
	if f.stage == "file-sync" {
		return errors.New("injected file sync")
	}
	return f.journalFile.Sync()
}

func TestJournalRejectsMissingAndSubstitutedEvidenceAndRevisionReuse(t *testing.T) {
	j, e := OpenJournal(privateDir(t))
	if e != nil {
		t.Fatal(e)
	}
	defer j.Close()
	a := testAdmission()
	if e = j.Admit(a); e != nil {
		t.Fatal(e)
	}
	changed := a
	changed.RequestID = "other"
	changed.Mode = "external"
	endpoint := "https://org.test"
	changed.Endpoint = &endpoint
	if e = j.Admit(changed); e == nil {
		t.Fatal("revision reused for changed nonsecret config")
	}
	r := testReceipt(t, a)
	if e = j.SaveReceipt(r); e != nil {
		t.Fatal(e)
	}
	r.Bytes = append(r.Bytes, byte(' '))
	if e = j.SaveReceipt(r); e == nil {
		t.Fatal("saved exact wrapper replaced")
	}
	if e = os.Remove(j.path(a.OrgID, a.RequestID, "admission")); e != nil {
		t.Fatal(e)
	}
	if _, e = j.Load(a.OrgID, a.RequestID); e == nil {
		t.Fatal("receipt reconstructed missing admission")
	}
}
func TestJournalExecutionBindingIsCreateOnlyAndRequiresAppliedApprove(t *testing.T) {
	j, e := OpenJournal(privateDir(t))
	if e != nil {
		t.Fatal(e)
	}
	defer j.Close()
	a := testAdmission()
	j.Admit(a)
	r := testReceipt(t, a)
	j.SaveReceipt(r)
	x := ExecutionBinding{1, a.OrgID, a.RequestID, "outgoing-1", a.AuthorizationHash, base64.StdEncoding.EncodeToString(make([]byte, 32)), "ethereum-transaction", "keccak256"}
	if e = j.BindExecution(x); e == nil {
		t.Fatal("execution before applied ACK")
	}
	ack, _ := json.Marshal(map[string]any{"version": 1, "requestId": a.RequestID, "outcome": "ACCEPTED", "disposition": "APPLIED"})
	if e = j.Acknowledge(a.OrgID, a.RequestID, ack); e != nil {
		t.Fatal(e)
	}
	if e = j.BindExecution(x); e != nil {
		t.Fatal(e)
	}
	if e = j.BindExecution(x); e != nil {
		t.Fatal(e)
	}
	x.OutgoingID = "outgoing-2"
	if e = j.BindExecution(x); e == nil {
		t.Fatal("execution replacement allowed")
	}
	s, e := j.Load(a.OrgID, a.RequestID)
	if e != nil || s.Execution == nil || s.Execution.OutgoingID != "outgoing-1" {
		t.Fatal("execution evidence changed")
	}
}
func TestJournalRejectsSymlinkRecordAndDirectoryReplacement(t *testing.T) {
	dir := privateDir(t)
	j, e := OpenJournal(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer j.Close()
	a := testAdmission()
	j.Admit(a)
	path := j.path(a.OrgID, a.RequestID, "admission")
	other := filepath.Join(dir, "other")
	os.Rename(path, other)
	os.Symlink(other, path)
	if _, e = j.Load(a.OrgID, a.RequestID); e == nil {
		t.Fatal("symlink evidence accepted")
	}
	os.Remove(path)
	os.Rename(other, path)
	os.Rename(dir, dir+"-moved")
	defer os.Rename(dir+"-moved", dir)
	os.Mkdir(dir, 0700)
	defer os.Remove(dir)
	if _, e = j.Load(a.OrgID, a.RequestID); e == nil {
		t.Fatal("replacement directory accepted")
	}
}

func TestClosedJournalCannotWriteAfterNewOwnerAcquiresLock(t *testing.T) {
	dir := privateDir(t)
	old, e := OpenJournal(dir)
	if e != nil {
		t.Fatal(e)
	}
	old.Close()
	owner, e := OpenJournal(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer owner.Close()
	if e = old.Admit(testAdmission()); e == nil {
		t.Fatal("closed journal wrote without owning lock")
	}
	if _, e = old.Load("org-1", "request"); e == nil {
		t.Fatal("closed journal read authorized evidence")
	}
	if _, e = old.PendingReceipts(50); e == nil {
		t.Fatal("closed journal reopened scanner")
	}
}

func appliedExecutionFixture(t *testing.T, j *Journal) (Admission, ExecutionBinding) {
	t.Helper()
	a := testAdmission()
	if e := j.Admit(a); e != nil {
		t.Fatal(e)
	}
	if e := j.SaveReceipt(testReceipt(t, a)); e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(map[string]any{"version": 1, "requestId": a.RequestID, "outcome": "ACCEPTED", "disposition": "APPLIED"})
	if e := j.Acknowledge(a.OrgID, a.RequestID, raw); e != nil {
		t.Fatal(e)
	}
	return a, ExecutionBinding{1, a.OrgID, a.RequestID, "outgoing-1", a.AuthorizationHash, base64.StdEncoding.EncodeToString(make([]byte, 32)), "ethereum-transaction", "keccak256"}
}
func TestExecutionKnownMarkerRequiresBothExactArtifactsAfterReopen(t *testing.T) {
	for _, kind := range []string{"execution", "execution-known"} {
		t.Run(kind, func(t *testing.T) {
			dir := privateDir(t)
			j, e := OpenJournal(dir)
			if e != nil {
				t.Fatal(e)
			}
			a, x := appliedExecutionFixture(t, j)
			if e = j.BindExecution(x); e != nil {
				t.Fatal(e)
			}
			os.Remove(j.path(a.OrgID, a.RequestID, kind))
			j.Close()
			j, e = OpenJournal(dir)
			if e != nil {
				t.Fatal(e)
			}
			defer j.Close()
			if _, e = j.Load(a.OrgID, a.RequestID); e == nil {
				t.Fatal("missing known artifact accepted")
			}
			if e = j.BindExecution(x); e == nil {
				t.Fatal("missing artifact reconstructed from claim echo")
			}
		})
	}
}
func TestExecutionPartialPublicationFailsClosedAtEachDurabilityBoundary(t *testing.T) {
	for _, target := range []int{1, 2} {
		for _, stage := range []string{"write", "file-sync", "publish", "directory-sync"} {
			t.Run(fmt.Sprintf("artifact-%d/%s", target, stage), func(t *testing.T) {
				dir := privateDir(t)
				j, e := OpenJournal(dir)
				if e != nil {
					t.Fatal(e)
				}
				a, x := appliedExecutionFixture(t, j)
				j.ops = &nthFaultOperations{journalOperations: j.ops, target: target, stage: stage}
				if e = j.BindExecution(x); e == nil {
					t.Fatal("failed publication authorized execution")
				}
				j.Close()
				j, e = OpenJournal(dir)
				if e != nil {
					t.Fatal(e)
				}
				defer j.Close()
				s, e := j.Load(a.OrgID, a.RequestID)
				if target == 1 && stage != "directory-sync" {
					if e != nil || s.Execution != nil {
						t.Fatal("prepublication failure created execution")
					}
				} else if target == 2 && stage == "directory-sync" {
					if e != nil || s.Execution == nil {
						t.Fatal("durability not reestablished for published pair")
					}
				} else {
					if e == nil {
						t.Fatal("partial execution publication usable")
					}
					if e = j.BindExecution(x); e == nil {
						t.Fatal("partial execution repaired from claim")
					}
				}
			})
		}
	}
}

type nthFaultOperations struct {
	journalOperations
	target, n int
	stage     string
	published bool
}

func (o *nthFaultOperations) CreateTemp(d, p string) (journalFile, error) {
	o.n++
	f, e := o.journalOperations.CreateTemp(d, p)
	stage := ""
	if o.n == o.target {
		stage = o.stage
	}
	return &faultFile{journalFile: f, stage: stage}, e
}
func (o *nthFaultOperations) Publish(a, b string) error {
	if o.n == o.target && o.stage == "publish" {
		return errors.New("injected execution publication")
	}
	e := o.journalOperations.Publish(a, b)
	o.published = e == nil
	return e
}
func (o *nthFaultOperations) SyncDirectory(d string) error {
	if o.n == o.target && o.published && o.stage == "directory-sync" {
		return errors.New("injected execution directory sync")
	}
	return o.journalOperations.SyncDirectory(d)
}
