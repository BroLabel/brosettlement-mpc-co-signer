package approval

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/strictjson"
)

type journalFile interface {
	Name() string
	Chmod(os.FileMode) error
	Write([]byte) (int, error)
	Sync() error
	Close() error
}
type journalOperations interface {
	CreateTemp(string, string) (journalFile, error)
	Publish(string, string) error
	SyncDirectory(string) error
}
type realJournalOperations struct{}

func (realJournalOperations) CreateTemp(d, p string) (journalFile, error) {
	f, e := os.CreateTemp(d, p)
	if e != nil {
		return nil, e
	}
	return syncedJournalFile{f}, nil
}
func (realJournalOperations) SyncDirectory(d string) error {
	f, e := os.Open(d)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
func (j *Journal) publish(path string, raw []byte) error {
	if e := j.check(); e != nil {
		return e
	}
	f, e := j.ops.CreateTemp(j.directory, ".approval-tmp-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if e = f.Chmod(0600); e != nil {
		return e
	}
	n, e := f.Write(raw)
	if e != nil {
		return e
	}
	if n != len(raw) {
		return io.ErrShortWrite
	}
	if e = f.Sync(); e != nil {
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	if e = j.ops.Publish(f.Name(), path); e != nil {
		return e
	}
	return j.ops.SyncDirectory(filepath.Dir(path))
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
