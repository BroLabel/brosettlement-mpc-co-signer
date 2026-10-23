package worker

import (
	"context"
	"testing"
	"time"
)

func TestApprovalGateAuthorizesTronWithoutRPC(t *testing.T) {
	i, now := approvalIntent(t, true)
	j := approvalJournal(t, i)
	gate := ApprovalGate{
		Journal: j,
		Now:     func() time.Time { return time.UnixMilli(now) },
	}

	if err := gate.Authorize(context.Background(), i); err != nil {
		t.Fatalf("authorize approved TRON transaction without RPC: %v", err)
	}
	state, err := j.Load(i.Payload.OrgID, i.Approval.RequestID)
	if err != nil || state.Execution == nil {
		t.Fatalf("execution binding was not persisted: %v", err)
	}
}
