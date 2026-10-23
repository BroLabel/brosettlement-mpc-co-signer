package approval

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	wire "github.com/BroLabel/brosettlement-mpc-co-signer/internal/contract/approval"
)

func (j *Journal) Load(org, request string) (State, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.loadStateLocked(org, request)
}

// loadStateLocked and its helpers require the caller to hold j.mu.
func (j *Journal) loadStateLocked(org, request string) (State, error) {
	var state State
	if !wire.ValidReference(org) || !wire.ValidReference(request) {
		return state, ErrIntegrity
	}
	if err := j.check(); err != nil {
		return state, err
	}
	var err error
	state.Admission, err = j.loadAdmission(org, request)
	if err != nil {
		return state, err
	}
	state.Receipt, err = j.loadReceipt(org, request, state.Admission)
	if err != nil {
		return state, err
	}
	state.Application, err = j.loadAcknowledgement(org, request, state.Receipt)
	if err != nil {
		return state, err
	}
	state.Execution, err = j.loadExecution(org, request, state)
	if err != nil {
		return state, err
	}
	// A renamed record with failed directory fsync must be made durable before use.
	if err := j.ops.SyncDirectory(j.directory); err != nil {
		return state, err
	}
	return state, nil
}

func (j *Journal) loadAdmission(org, request string) (*Admission, error) {
	admission := new(Admission)
	found, err := j.read(org, request, "admission", admission, admissionFields)
	if err != nil || !found {
		return nil, err
	}
	if admission.OrgID != org || admission.RequestID != request || admission.validate() != nil {
		return nil, ErrIntegrity
	}
	return admission, nil
}

func (j *Journal) loadReceipt(org, request string, admission *Admission) (*SavedReceipt, error) {
	receipt := new(SavedReceipt)
	found, err := j.read(org, request, "receipt", receipt, receiptFields)
	if err != nil || !found {
		return nil, err
	}
	if admission == nil || validateReceipt(*admission, *receipt) != nil {
		return nil, ErrIntegrity
	}
	return receipt, nil
}

func (j *Journal) loadAcknowledgement(org, request string, receipt *SavedReceipt) ([]byte, error) {
	ack := new(deliveryAck)
	found, err := j.read(org, request, "ack", ack, acknowledgementFields)
	if err != nil || !found {
		return nil, err
	}
	if receipt == nil {
		return nil, ErrIntegrity
	}
	if ack.Version != 1 || ack.OrgID != org || ack.RequestID != request ||
		ack.ReceiptHash != digest(receipt.Bytes) {
		return nil, ErrIntegrity
	}
	if err := validateApplication(ack.Application, request); err != nil {
		return nil, err
	}
	return ack.Application, nil
}

func (j *Journal) loadExecution(org, request string, state State) (*ExecutionBinding, error) {
	execution := new(ExecutionBinding)
	hasExecution, err := j.read(org, request, "execution", execution, executionFields)
	if err != nil {
		return nil, err
	}
	known := new(executionKnown)
	hasKnown, err := j.read(org, request, "execution-known", known, executionKnownFields)
	if err != nil {
		return nil, err
	}
	if hasExecution != hasKnown {
		return nil, ErrIntegrity
	}
	if !hasExecution {
		return nil, nil
	}
	if err := validateExecution(state, *execution); err != nil {
		return nil, err
	}
	raw, err := readJournalFile(j.path(org, request, "execution"))
	if err != nil {
		return nil, err
	}
	if known.Version != 1 || known.OrgID != org || known.RequestID != request ||
		known.AuthorizationHash != state.Admission.AuthorizationHash {
		return nil, ErrIntegrity
	}
	if known.BindingHash != digest(raw) {
		return nil, ErrIntegrity
	}
	return execution, nil
}

// PendingReceipts streams at most limit filenames and retains at most one receipt body.
// Delivery is performed outside the journal lock, independently of fresh discovery.
func (j *Journal) PendingReceipts(limit int) ([]SavedReceipt, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if limit < 1 || limit > 1000 {
		return nil, ErrIntegrity
	}
	if err := j.check(); err != nil {
		return nil, err
	}
	if j.scan == nil {
		f, err := os.Open(j.directory)
		if err != nil {
			return nil, err
		}
		j.scan = f
	}
	var scanErr error
	var recordErr error
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
		raw, err := readJournalFile(filepath.Join(j.directory, name))
		if err != nil {
			recordErr = errors.Join(recordErr, err)
			continue
		}
		var receipt SavedReceipt
		if err = decodeRecord(raw, &receipt, receiptFields); err != nil || name != filepath.Base(j.path(receipt.OrgID, receipt.RequestID, "receipt")) {
			recordErr = errors.Join(recordErr, ErrIntegrity)
			continue
		}
		state, err := j.loadStateLocked(receipt.OrgID, receipt.RequestID)
		if err != nil {
			recordErr = errors.Join(recordErr, err)
			continue
		}
		if len(state.Application) == 0 {
			receipts = append(receipts, *state.Receipt)
			break
		}
	}
	if scanErr == io.EOF {
		j.scan.Close()
		j.scan = nil
	}
	return receipts, recordErr
}
