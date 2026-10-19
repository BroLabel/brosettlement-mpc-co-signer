package approval

import (
	"io"
	"os"
	"path/filepath"
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
