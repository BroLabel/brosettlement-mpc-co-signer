package approval

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/lifecycle"
)

var ErrIntegrity = errors.New("approval journal integrity failure; reconcile durable evidence")
var ErrConflict = errors.New("approval immutable evidence conflict")

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
	dir, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if err = privateDirectory(info); err != nil {
		return nil, err
	}
	lockPath := filepath.Join(dir, ".approval.lock")
	if lockInfo, err := os.Lstat(lockPath); err == nil {
		if !lockInfo.Mode().IsRegular() || lockInfo.Mode().Perm() != 0600 {
			return nil, ErrIntegrity
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	lock, err := lifecycle.AcquireLifetimeLock(lockPath)
	if err != nil {
		return nil, err
	}
	journal := &Journal{directory: dir, identity: info, lock: lock, ops: realJournalOperations{}}
	if err = journal.probe(); err != nil {
		lock.Close()
		return nil, err
	}
	return journal, nil
}

func (j *Journal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return nil
	}
	j.closed = true
	var err error
	if j.scan != nil {
		err = j.scan.Close()
		j.scan = nil
	}
	return errors.Join(err, j.lock.Close())
}

func (j *Journal) Admit(admission Admission) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := admission.validate(); err != nil {
		return err
	}
	state, err := j.loadStateLocked(admission.OrgID, admission.RequestID)
	if err != nil {
		return err
	}
	if state.Admission != nil {
		old := *state.Admission
		admission.AdmittedAt = old.AdmittedAt
		if !equalJSON(old, admission) {
			return ErrConflict
		}
		return nil
	}
	return j.put(j.path(admission.OrgID, admission.RequestID, "admission"), admission)
}

func (j *Journal) SaveReceipt(receipt SavedReceipt) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	state, err := j.loadStateLocked(receipt.OrgID, receipt.RequestID)
	if err != nil {
		return err
	}
	if state.Admission == nil {
		return ErrIntegrity
	}
	if err = validateReceipt(*state.Admission, receipt); err != nil {
		return err
	}
	return j.put(j.path(receipt.OrgID, receipt.RequestID, "receipt"), receipt)
}

func (j *Journal) Acknowledge(org, request string, application []byte) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	state, err := j.loadStateLocked(org, request)
	if err != nil {
		return err
	}
	if state.Receipt == nil {
		return ErrIntegrity
	}
	if err = validateApplication(application, request); err != nil {
		return err
	}
	return j.put(j.path(org, request, "ack"), deliveryAck{
		Version:     1,
		OrgID:       org,
		RequestID:   request,
		ReceiptHash: digest(state.Receipt.Bytes),
		Application: application,
	})
}

func (j *Journal) BindExecution(execution ExecutionBinding) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	state, err := j.loadStateLocked(execution.OrgID, execution.RequestID)
	if err != nil {
		return err
	}
	if err = validateExecution(state, execution); err != nil {
		return err
	}
	raw, err := json.Marshal(execution)
	if err != nil {
		return err
	}
	if err = j.put(j.path(execution.OrgID, execution.RequestID, "execution-known"), executionKnown{
		Version:           1,
		OrgID:             execution.OrgID,
		RequestID:         execution.RequestID,
		AuthorizationHash: execution.AuthorizationHash,
		BindingHash:       digest(raw),
	}); err != nil {
		return err
	}
	return j.put(j.path(execution.OrgID, execution.RequestID, "execution"), execution)
}

func (j *Journal) check() error {
	if j.closed {
		return ErrIntegrity
	}
	info, err := os.Stat(j.directory)
	if err != nil {
		return err
	}
	if !os.SameFile(info, j.identity) {
		return ErrIntegrity
	}
	return privateDirectory(info)
}
