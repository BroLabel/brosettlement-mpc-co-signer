package approval

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

// These tests run unchanged on Linux and macOS. Linux evidence requires a real
// Linux process and a journal on its native filesystem, not cross-compilation.
func TestJournalCrashReopenPublicationAndACKBoundaries(t *testing.T) {
	t.Logf("actual filesystem crash/reopen process platform=%s/%s temp=%s", runtime.GOOS, runtime.GOARCH, os.TempDir())
	for _, kind := range []string{"receipt", "ack"} {
		for _, stage := range []string{"write", "file-sync", "publish", "directory-sync"} {
			t.Run(kind+"/"+stage, func(t *testing.T) {
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
				if kind == "ack" {
					if e = j.SaveReceipt(r); e != nil {
						t.Fatal(e)
					}
				}
				j.Close()
				cmd := exec.Command(os.Args[0], "-test.run=^TestJournalCrashChild$")
				cmd.Env = append(os.Environ(), "APPROVAL_CRASH_DIR="+dir, "APPROVAL_CRASH_STAGE="+kind+":"+stage)
				out, e := cmd.CombinedOutput()
				var exit *exec.ExitError
				if !errors.As(e, &exit) || exit.ExitCode() != 79 {
					t.Fatalf("child did not stop at crash boundary: %v %s", e, out)
				}
				j, e = OpenJournal(dir)
				if e != nil {
					t.Fatal(e)
				}
				defer j.Close()
				s, e := j.Load(a.OrgID, a.RequestID)
				if e != nil {
					t.Fatal(e)
				}
				published := stage == "publish" || stage == "directory-sync"
				if kind == "receipt" {
					if (s.Receipt != nil) != published {
						t.Fatalf("receipt visibility at %s: %+v", stage, s)
					}
					if s.Receipt != nil && !bytes.Equal(s.Receipt.Bytes, r.Bytes) {
						t.Fatal("crash replaced exact receipt")
					}
				} else {
					if s.Receipt == nil || !bytes.Equal(s.Receipt.Bytes, r.Bytes) {
						t.Fatal("ACK crash lost saved receipt")
					}
					if (len(s.Application) > 0) != published {
						t.Fatal("ACK publication mismatch")
					}
				}
			})
		}
	}
}
func TestJournalCrashChild(t *testing.T) {
	dir := os.Getenv("APPROVAL_CRASH_DIR")
	if dir == "" {
		return
	}
	stage := os.Getenv("APPROVAL_CRASH_STAGE")
	j, e := OpenJournal(dir)
	if e != nil {
		t.Fatal(e)
	}
	a := testAdmission()
	r := testReceipt(t, a)
	parts := strings.Split(stage, ":")
	j.ops = &crashOperations{journalOperations: j.ops, stage: parts[1]}
	if parts[0] == "receipt" {
		e = j.SaveReceipt(r)
	} else {
		ack, _ := json.Marshal(map[string]any{"version": 1, "requestId": a.RequestID, "outcome": "ACCEPTED", "disposition": "APPLIED"})
		e = j.Acknowledge(a.OrgID, a.RequestID, ack)
	}
	t.Fatalf("crash not reached: %v", e)
}

type crashOperations struct {
	journalOperations
	stage     string
	published bool
}

func (c *crashOperations) CreateTemp(d, p string) (journalFile, error) {
	f, e := c.journalOperations.CreateTemp(d, p)
	return crashFile{f, c.stage}, e
}
func (c *crashOperations) Publish(a, b string) error {
	e := c.journalOperations.Publish(a, b)
	if e == nil {
		c.published = true
	}
	if e == nil && c.stage == "publish" {
		os.Exit(79)
	}
	return e
}
func (c *crashOperations) SyncDirectory(d string) error {
	e := c.journalOperations.SyncDirectory(d)
	if e == nil && c.stage == "directory-sync" && c.published {
		os.Exit(79)
	}
	return e
}

type crashFile struct {
	journalFile
	stage string
}

func (c crashFile) Write(b []byte) (int, error) {
	n, e := c.journalFile.Write(b)
	if e == nil && c.stage == "write" {
		os.Exit(79)
	}
	return n, e
}
func (c crashFile) Sync() error {
	e := c.journalFile.Sync()
	if e == nil && c.stage == "file-sync" {
		os.Exit(79)
	}
	return e
}
func TestReceiptSentBeforeACKCrashIsRedeliverable(t *testing.T) {
	dir := privateDir(t)
	j, e := OpenJournal(dir)
	if e != nil {
		t.Fatal(e)
	}
	a := testAdmission()
	j.Admit(a)
	r := testReceipt(t, a)
	j.SaveReceipt(r)
	j.Close()
	var received []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { received, _ = io.ReadAll(req.Body); w.WriteHeader(200) }))
	defer server.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^TestReceiptSendCrashChild$")
	cmd.Env = append(os.Environ(), "APPROVAL_SEND_CRASH_DIR="+dir, "APPROVAL_SEND_CRASH_URL="+server.URL)
	out, e := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(e, &exit) || exit.ExitCode() != 79 {
		t.Fatalf("send crash %v %s", e, out)
	}
	if !bytes.Equal(received, r.Bytes) {
		t.Fatal("sent body differed from saved receipt")
	}
	j, e = OpenJournal(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer j.Close()
	s, e := j.Load(a.OrgID, a.RequestID)
	if e != nil || s.Receipt == nil || len(s.Application) != 0 {
		t.Fatal("lost ACK fabricated application or lost receipt")
	}
	records, e := j.PendingReceipts(50)
	if e != nil || len(records) != 1 || !bytes.Equal(records[0].Bytes, r.Bytes) {
		t.Fatal("receipt not independently recoverable")
	}
}
func TestReceiptSendCrashChild(t *testing.T) {
	dir := os.Getenv("APPROVAL_SEND_CRASH_DIR")
	if dir == "" {
		return
	}
	j, e := OpenJournal(dir)
	if e != nil {
		t.Fatal(e)
	}
	a := testAdmission()
	s, e := j.Load(a.OrgID, a.RequestID)
	if e != nil || s.Receipt == nil {
		t.Fatal("receipt missing before send")
	}
	resp, e := http.Post(os.Getenv("APPROVAL_SEND_CRASH_URL"), "application/json", bytes.NewReader(s.Receipt.Bytes))
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	os.Exit(79)
}
