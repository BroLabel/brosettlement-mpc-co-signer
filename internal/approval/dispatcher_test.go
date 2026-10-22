package approval

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	wire "github.com/BroLabel/brosettlement-mpc-co-signer/internal/contract/approval"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/monolith"
	"github.com/gowebpki/jcs"
)

func pendingItem(t *testing.T, id string) Item {
	t.Helper()
	return pendingItemForOrg(t, id, "org-1")
}

func pendingItemForOrg(t *testing.T, id, orgID string) Item {
	t.Helper()
	raw, e := os.ReadFile("../../testdata/approval-v1/vectors.json")
	if e != nil {
		t.Fatal(e)
	}
	var corpus struct {
		Vectors []struct {
			ID    string `json:"id"`
			Input string `json:"inputBase64"`
		}
	}
	json.Unmarshal(raw, &corpus)
	for _, v := range corpus.Vectors {
		if v.ID == "APPROVAL-V1-001" {
			b, _ := base64.StdEncoding.DecodeString(v.Input)
			var a map[string]any
			json.Unmarshal(b, &a)
			a["orgId"] = orgID
			a["requestId"] = id
			b, _ = json.Marshal(a)
			b, _ = jcs.Transform(b)
			return Item{RequestID: id, CreatedAt: "2026-10-19T01:00:00.000Z", Authorization: b, AuthorizationHash: digest(b)}
		}
	}
	t.Fatal("missing fixture")
	return Item{}
}

type fakeBackend struct {
	page         func(string) (Page, error)
	delivered    [][]byte
	failDelivery bool
	mu           sync.Mutex
}

func (b *fakeBackend) PendingApprovals(ctx context.Context, cursor string) (Page, error) {
	return b.page(cursor)
}
func (b *fakeBackend) SendApprovalDecision(ctx context.Context, id string, raw []byte) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.delivered = append(b.delivered, append([]byte(nil), raw...))
	if b.failDelivery {
		return nil, errors.New("lost ACK")
	}
	return json.Marshal(map[string]any{"version": 1, "requestId": id, "outcome": "ACCEPTED", "disposition": "APPLIED"})
}

type fakeOrganization struct {
	calls  int
	status string
	err    error
}

func (o *fakeOrganization) Evaluate(ctx context.Context, item Item) (wire.Object, error) {
	o.calls++
	if o.err != nil {
		return nil, o.err
	}
	raw, _ := json.Marshal(map[string]any{"version": 1, "orgId": "org-1", "requestId": item.RequestID, "authorizationHash": item.AuthorizationHash, "status": o.status, "reason": nil, "decisionReference": nil})
	return wire.ParseOrganizationResponse(raw, "org-1", item.RequestID, item.AuthorizationHash)
}
func makeDispatcher(t *testing.T, c Config, b Backend, o Organization) (*Dispatcher, *Journal) {
	t.Helper()
	j, e := OpenJournal(c.JournalDir)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { j.Close() })
	d, e := NewDispatcher(c, j, b, o, "key-1", ed25519.NewKeyFromSeed(make([]byte, 32)))
	if e != nil {
		t.Fatal(e)
	}
	d.jitter = func(ceiling time.Duration) time.Duration { return ceiling }
	return d, j
}

func TestDispatcherUsesOrganizationFromAuthorizationAndRecoversItAfterRestart(t *testing.T) {
	c := testConfig(t)
	item := pendingItemForOrg(t, "request-org-2", "org-2")
	b := &fakeBackend{page: func(string) (Page, error) { return Page{Version: 1, Items: []Item{item}}, nil }, failDelivery: true}
	d, j := makeDispatcher(t, c, b, nil)
	if err := d.Step(context.Background()); err == nil {
		t.Fatal("expected delivery outage")
	}
	s, err := j.Load("org-2", item.RequestID)
	if err != nil || s.Receipt == nil {
		t.Fatalf("request organization was not retained: %v", err)
	}
	if other, err := j.Load("org-1", item.RequestID); err != nil || other.Admission != nil {
		t.Fatal("request written under a different organization")
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	b.failDelivery = false
	b.page = func(string) (Page, error) { return Page{Version: 1, Items: []Item{}}, nil }
	restarted, reopened := makeDispatcher(t, c, b, nil)
	if err := restarted.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	s, err = reopened.Load("org-2", item.RequestID)
	if err != nil || len(s.Application) == 0 || len(b.delivered) != 2 || !bytes.Equal(b.delivered[0], b.delivered[1]) {
		t.Fatal("restart did not deliver the exact saved decision with its original organization")
	}
}
func TestDispatcherIdlePendingFinalIdleAndLostACK(t *testing.T) {
	c := testConfig(t)
	c.Mode = "external"
	c.Endpoint = "https://org.test/decision"
	c.BearerToken = "dedicated"
	item := pendingItem(t, "request-1")
	var page Page
	page.Version = 1
	page.Items = []Item{}
	b := &fakeBackend{page: func(string) (Page, error) { return page, nil }}
	o := &fakeOrganization{status: "PENDING"}
	d, j := makeDispatcher(t, c, b, o)
	if e := d.Step(context.Background()); e != nil {
		t.Fatal(e)
	}
	if o.calls != 0 {
		t.Fatal("idle organization call")
	}
	page.Items = []Item{item}
	if e := d.Step(context.Background()); e != nil {
		t.Fatal(e)
	}
	if o.calls != 1 {
		t.Fatal("pending not evaluated")
	}
	s, e := j.Load("org-1", item.RequestID)
	if e != nil || s.Admission == nil || s.Receipt != nil {
		t.Fatalf("pending evidence: %+v %v", s, e)
	}
	d.now = func() time.Time { return time.Now().Add(time.Hour) }
	o.status = "APPROVED"
	b.failDelivery = true
	_ = d.Step(context.Background())
	s, e = j.Load("org-1", item.RequestID)
	if e != nil || s.Receipt == nil {
		t.Fatal("final not saved")
	}
	exact := append([]byte(nil), s.Receipt.Bytes...)
	page.Items = []Item{}
	b.page = func(string) (Page, error) { return Page{}, errors.New("discovery unavailable") }
	b.failDelivery = false
	d.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	for i := 0; i < 8; i++ {
		_ = d.Step(context.Background())
	}
	if o.calls != 2 {
		t.Fatal("saved final repolled organization")
	}
	if len(b.delivered) < 2 || !bytes.Equal(b.delivered[len(b.delivered)-1], exact) {
		t.Fatal("saved receipt not redelivered independently")
	}
	s, e = j.Load("org-1", item.RequestID)
	if e != nil || len(s.Application) == 0 {
		t.Fatal("ACK not saved")
	}
}
func TestDispatcherAutoAndPinnedAdmissionAcrossRestart(t *testing.T) {
	c := testConfig(t)
	item := pendingItem(t, "request-1")
	b := &fakeBackend{page: func(string) (Page, error) { return Page{Version: 1, Items: []Item{item}}, nil }}
	o := &fakeOrganization{status: "APPROVED"}
	d, j := makeDispatcher(t, c, b, o)
	if e := d.Step(context.Background()); e != nil {
		t.Fatal(e)
	}
	if o.calls != 0 {
		t.Fatal("auto called organization")
	}
	s, e := j.Load("org-1", item.RequestID)
	if e != nil || s.Receipt == nil || len(s.Application) == 0 {
		t.Fatal("auto did not use durable receipt path")
	}
	c.Mode = "external"
	c.Endpoint = "https://org.test"
	c.BearerToken = "secret"
	d2, e := NewDispatcher(c, j, b, o, "rotated-key", ed25519.NewKeyFromSeed(bytes.Repeat([]byte{1}, 32)))
	if e != nil {
		t.Fatal(e)
	}
	if e = d2.Step(context.Background()); e != nil {
		t.Fatal(e)
	}
	if o.calls != 0 {
		t.Fatal("historical final reevaluated")
	}
	// An unresolved admission stays pinned even when absent from a cancellation page.
	item2 := pendingItem(t, "request-2")
	a := Admission{1, "org-1", item2.RequestID, item2.AuthorizationHash, "auto_approve", nil, "2026-10-19T01:00:00.000Z"}
	if e = j.Admit(a); e != nil {
		t.Fatal(e)
	}
	b.page = func(string) (Page, error) { return Page{Version: 1, Items: []Item{}}, nil }
	if e = d2.Step(context.Background()); e != nil {
		t.Fatal(e)
	}
	b.page = func(string) (Page, error) { return Page{Version: 1, Items: []Item{item2}}, nil }
	if e = d2.Step(context.Background()); e == nil {
		t.Fatal("changed configuration reevaluated pinned pending admission")
	}
	if o.calls != 0 {
		t.Fatal("pinned admission called external")
	}
}
func TestDispatcherPagesProgressPastForeverPendingAndNewArrivals(t *testing.T) {
	c := testConfig(t)
	c.Mode = "external"
	c.Endpoint = "https://org.test"
	c.BearerToken = "secret"
	calls := []string{}
	sweeps := 0
	b := &fakeBackend{page: func(cursor string) (Page, error) {
		calls = append(calls, cursor)
		p := Page{Version: 1, Items: []Item{}}
		var n int
		if cursor == "" {
			sweeps++
			n = 0
		} else {
			fmt.Sscanf(cursor, "page-%d", &n)
		}
		p.Items = []Item{pendingItem(t, fmt.Sprintf("old-%d", n))}
		if n < 3 {
			x := fmt.Sprintf("page-%d", n+1)
			p.NextCursor = &x
		}
		if n == 3 && sweeps > 1 {
			newItem := pendingItem(t, fmt.Sprintf("new-%d", sweeps))
			newItem.CreatedAt = "2026-10-19T02:00:00.000Z"
			p.Items = append(p.Items, newItem)
		}
		return p, nil
	}}
	o := &fakeOrganization{status: "PENDING"}
	d, _ := makeDispatcher(t, c, b, o)
	for i := 0; i < 12; i++ {
		d.now = func() time.Time { return time.Now().Add(time.Duration(i) * time.Hour) }
		if e := d.Step(context.Background()); e != nil {
			t.Fatal(e)
		}
	}
	if sweeps != 3 || len(calls) != 12 || calls[3] != "page-3" || calls[4] != "" || o.calls != 14 {
		t.Fatalf("page starvation sweeps=%d pages=%v calls=%d", sweeps, calls, o.calls)
	}
	// Failure retains cursor and schedules no cached-only work.
	before := o.calls
	b.page = func(string) (Page, error) { return Page{}, errors.New("outage") }
	for i := 0; i < 3; i++ {
		_ = d.Step(context.Background())
	}
	if o.calls != before {
		t.Fatal("outage scheduled cached organization call")
	}
}
func TestDispatcherBackoffMemoryBound(t *testing.T) {
	c := testConfig(t)
	d, _ := makeDispatcher(t, c, &fakeBackend{page: func(string) (Page, error) { return Page{Version: 1, Items: []Item{}}, nil }}, nil)
	for i := 0; i < 1500; i++ {
		d.suppress(suppressionKey{"org-1", fmt.Sprint(i), false})
	}
	if len(d.backoff) != 1000 {
		t.Fatalf("unbounded backoff: %d", len(d.backoff))
	}
	if _, retained := d.backoff[suppressionKey{"org-1", "0", false}]; retained {
		t.Fatal("oldest organization suppression hint was not evicted")
	}
	if _, retained := d.backoff[suppressionKey{"org-1", "1499", false}]; !retained {
		t.Fatal("newest organization suppression hint was not retained")
	}
}

func TestSavedDeliveryProgressWithContinuousArrivalsAndOneLostACK(t *testing.T) {
	c := testConfig(t)
	b := &fairBackend{delivered: map[string]int{}}
	d, j := makeDispatcher(t, c, b, nil)
	save := func(id string) {
		a := testAdmission()
		a.RequestID = id
		if e := j.Admit(a); e != nil {
			t.Fatal(e)
		}
		if e := j.SaveReceipt(testReceipt(t, a)); e != nil {
			t.Fatal(e)
		}
	}
	for i := 0; i < 8; i++ {
		save(fmt.Sprintf("old-%d", i))
	}
	for i := 0; i < 40; i++ {
		save(fmt.Sprintf("arrival-%d", i))
		_ = d.Step(context.Background())
	}
	for i := 0; i < 8; i++ {
		id := fmt.Sprintf("old-%d", i)
		if b.delivered[id] == 0 {
			t.Fatalf("saved old receipt starved: %s", id)
		}
	}
	if b.pages != 40 {
		t.Fatalf("delivery outage blocked discovery: %d", b.pages)
	}
}

type fairBackend struct {
	delivered map[string]int
	pages     int
}

func (b *fairBackend) PendingApprovals(context.Context, string) (Page, error) {
	b.pages++
	return Page{Version: 1, Items: []Item{}}, nil
}
func (b *fairBackend) SendApprovalDecision(ctx context.Context, id string, raw []byte) ([]byte, error) {
	b.delivered[id]++
	if id == "old-0" {
		return nil, errors.New("permanent lost ACK")
	}
	return json.Marshal(map[string]any{"version": 1, "requestId": id, "outcome": "ACCEPTED", "disposition": "APPLIED"})
}
func TestPendingRestartCancellationAndFreshDiscoveryBackoff(t *testing.T) {
	c := testConfig(t)
	c.Mode = "external"
	c.Endpoint = "https://org.test"
	c.BearerToken = "secret"
	item := pendingItem(t, "request")
	p := Page{Version: 1, Items: []Item{item}}
	b := &fakeBackend{page: func(string) (Page, error) { return p, nil }}
	o := &fakeOrganization{status: "PENDING"}
	d, j := makeDispatcher(t, c, b, o)
	fixed := time.Now()
	d.now = func() time.Time { return fixed }
	if e := d.Step(context.Background()); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 3; i++ {
		if e := d.Step(context.Background()); e != nil {
			t.Fatal(e)
		}
	}
	if o.calls != 1 {
		t.Fatal("due backoff ignored")
	}
	j.Close()
	j, e := OpenJournal(c.JournalDir)
	if e != nil {
		t.Fatal(e)
	}
	defer j.Close()
	d, e = NewDispatcher(c, j, b, o, "key-1", ed25519.NewKeyFromSeed(make([]byte, 32)))
	if e != nil {
		t.Fatal(e)
	}
	p.Items = []Item{}
	for i := 0; i < 3; i++ {
		if e := d.Step(context.Background()); e != nil {
			t.Fatal(e)
		}
	}
	if o.calls != 1 {
		t.Fatal("cancelled local admission scheduled organization")
	}
	p.Items = []Item{item}
	if e := d.Step(context.Background()); e != nil {
		t.Fatal(e)
	}
	if o.calls != 2 {
		t.Fatal("restored pending admission not freshly evaluated")
	}
}

func TestSuppressedSavedReceiptRetriesWithoutWaitingForGrowingDirectoryEOF(t *testing.T) {
	c := testConfig(t)
	b := &fairBackend{delivered: map[string]int{}}
	d, j := makeDispatcher(t, c, b, nil)
	now := time.Now()
	d.now = func() time.Time { return now }
	a := testAdmission()
	a.RequestID = "old-0"
	j.Admit(a)
	j.SaveReceipt(testReceipt(t, a))
	_ = d.Step(context.Background())
	if b.delivered[a.RequestID] != 1 {
		t.Fatal("initial receipt not sent")
	}
	for i := 0; i < 40; i++ {
		arrival := testAdmission()
		arrival.RequestID = fmt.Sprintf("new-%d", i)
		j.Admit(arrival)
		j.SaveReceipt(testReceipt(t, arrival))
	}
	now = now.Add(time.Hour)
	for i := 0; i < 2; i++ {
		_ = d.Step(context.Background())
	}
	if b.delivered[a.RequestID] < 2 {
		t.Fatal("saved retry required growing directory EOF")
	}
}
func TestDeliveryRetrySaturationPreservesEvidenceHintsAndResumesEvaluation(t *testing.T) {
	c := testConfig(t)
	item := pendingItem(t, "new-pending")
	b := &fakeBackend{page: func(string) (Page, error) { return Page{Version: 1, Items: []Item{item}}, nil }}
	d, j := makeDispatcher(t, c, b, nil)
	fixed := time.Now()
	d.now = func() time.Time { return fixed }
	for i := 0; i < 1000; i++ {
		d.suppress(suppressionKey{"org-1", fmt.Sprint(i), true})
	}
	d.suppress(suppressionKey{"org-1", "must-not-evict", false})
	if d.deliveryHints() != 1000 || len(d.backoff) != 1000 {
		t.Fatal("organization hint evicted saved retry")
	}
	if e := d.Step(context.Background()); e == nil {
		t.Fatal("saturated delivery did not suspend new auto evaluation")
	}
	s, e := j.Load("org-1", item.RequestID)
	if e != nil || s.Admission != nil {
		t.Fatal("saturation created new local work")
	}
	delete(d.backoff, suppressionKey{"org-1", "0", true})
	if e = d.Step(context.Background()); e != nil {
		t.Fatal(e)
	}
	s, e = j.Load("org-1", item.RequestID)
	if e != nil || s.Receipt == nil || len(s.Application) == 0 {
		t.Fatal("evaluation did not resume after capacity released")
	}
}

func TestPublishedFinalAfterDirectorySyncFailureNeverRepollsOrganization(t *testing.T) {
	c := testConfig(t)
	c.Mode = "external"
	c.Endpoint = "https://org.test"
	c.BearerToken = "secret"
	item := pendingItem(t, "request")
	b := &fakeBackend{page: func(string) (Page, error) { return Page{Version: 1, Items: []Item{item}}, nil }}
	o := &fakeOrganization{status: "APPROVED"}
	d, j := makeDispatcher(t, c, b, o)
	endpoint := c.Endpoint
	if e := j.Admit(Admission{1, "org-1", item.RequestID, item.AuthorizationHash, c.Mode, &endpoint, "2026-10-19T01:00:00.000Z"}); e != nil {
		t.Fatal(e)
	}
	j.ops = &faultOperations{journalOperations: j.ops, stage: "directory-sync"}
	if e := d.Step(context.Background()); e == nil {
		t.Fatal("failed directory sync authorized decision")
	}
	if o.calls != 1 || len(b.delivered) != 0 {
		t.Fatal("decision delivered before durable publication")
	}
	_ = d.Step(context.Background())
	if o.calls != 1 || len(b.delivered) != 0 {
		t.Fatal("published but nondurable decision repolled/delivered")
	}
	j.ops = realJournalOperations{}
	if e := d.Step(context.Background()); e != nil {
		t.Fatal(e)
	}
	if o.calls != 1 || len(b.delivered) == 0 {
		t.Fatal("durable final not independently recovered")
	}
}

type blockingOrganization struct {
	start       chan struct{}
	release     chan struct{}
	active, max atomic.Int32
}

func (o *blockingOrganization) Evaluate(ctx context.Context, item Item) (wire.Object, error) {
	n := o.active.Add(1)
	defer o.active.Add(-1)
	for {
		old := o.max.Load()
		if n <= old || o.max.CompareAndSwap(old, n) {
			break
		}
	}
	select {
	case o.start <- struct{}{}:
	default:
	}
	select {
	case <-o.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	raw, _ := json.Marshal(map[string]any{"version": 1, "orgId": "org-1", "requestId": item.RequestID, "authorizationHash": item.AuthorizationHash, "status": "PENDING", "reason": nil, "decisionReference": nil})
	return wire.ParseOrganizationResponse(raw, "org-1", item.RequestID, item.AuthorizationHash)
}
func TestDispatcherConcurrentTicksUseOneOrganizationAttemptAndNoSignPermit(t *testing.T) {
	c := testConfig(t)
	c.Mode = "external"
	c.Endpoint = "https://org.test"
	c.BearerToken = "secret"
	item := pendingItem(t, "request")
	b := &fakeBackend{page: func(string) (Page, error) { return Page{Version: 1, Items: []Item{item}}, nil }}
	o := &blockingOrganization{start: make(chan struct{}, 2), release: make(chan struct{})}
	d, _ := makeDispatcher(t, c, b, o)
	fixed := time.Now()
	d.now = func() time.Time { return fixed }
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = d.Step(context.Background()) }()
	select {
	case <-o.start:
	case <-time.After(time.Second):
		t.Fatal("first organization attempt did not start")
	}
	go func() { defer wg.Done(); _ = d.Step(context.Background()) }()
	select {
	case <-o.start:
		close(o.release)
		wg.Wait()
		t.Fatal("more than one organization call in flight")
	case <-time.After(30 * time.Millisecond):
	}
	close(o.release)
	wg.Wait()
	if o.max.Load() != 1 {
		t.Fatal("organization concurrency exceeded one")
	}
}
func TestPendingAndErrorsHaveNoTotalWaitingDeadline(t *testing.T) {
	c := testConfig(t)
	c.Mode = "external"
	c.Endpoint = "https://org.test"
	c.BearerToken = "secret"
	item := pendingItem(t, "old-request")
	item.CreatedAt = "2001-01-01T01:00:00.000Z"
	b := &fakeBackend{page: func(string) (Page, error) { return Page{Version: 1, Items: []Item{item}}, nil }}
	o := &fakeOrganization{status: "PENDING"}
	d, j := makeDispatcher(t, c, b, o)
	for i := 0; i < 8; i++ {
		year := 2026 + i
		d.now = func() time.Time { return time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC) }
		if i%2 == 1 {
			o.err = errors.New("synthetic provider 5xx")
		} else {
			o.err = nil
		}
		_ = d.Step(context.Background())
		s, e := j.Load("org-1", item.RequestID)
		if e != nil || s.Receipt != nil {
			t.Fatal("waiting/error created final receipt")
		}
	}
	o.err = nil
	o.status = "APPROVED"
	d.now = func() time.Time { return time.Date(2040, 1, 1, 0, 0, 0, 0, time.UTC) }
	if e := d.Step(context.Background()); e != nil {
		t.Fatal(e)
	}
	s, e := j.Load("org-1", item.RequestID)
	if e != nil || s.Receipt == nil || len(s.Application) == 0 {
		t.Fatal("unlimited waiting lost eventual decision")
	}
}

// Logical time advances only inside the dispatcher; HTTP auth keeps trusted real time.
func TestThirtyAnd365DayPendingSurvivesJournalReopen(t *testing.T) {
	for _, days := range []int{30, 365} {
		t.Run(fmt.Sprintf("days_%d", days), func(t *testing.T) {
			c := testConfig(t)
			c.Mode, c.Endpoint, c.BearerToken = "external", "https://org.test", "dedicated"
			item := pendingItem(t, "aged-request")
			start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			item.CreatedAt = start.Format("2006-01-02T15:04:05.000Z")
			b := &fakeBackend{page: func(string) (Page, error) { return Page{Version: 1, Items: []Item{item}}, nil }}
			o := &fakeOrganization{status: "PENDING"}
			d, j := makeDispatcher(t, c, b, o)
			d.now = func() time.Time { return start }
			if err := d.Step(context.Background()); err != nil {
				t.Fatal(err)
			}
			if o.calls != 1 || len(b.delivered) != 0 {
				t.Fatal("pending must not deliver a final decision")
			}
			if err := j.Close(); err != nil {
				t.Fatal(err)
			}
			d, j = makeDispatcher(t, c, b, o)
			d.now = func() time.Time { return start.AddDate(0, 0, days) }
			if err := d.Step(context.Background()); err != nil {
				t.Fatal(err)
			}
			s, err := j.Load("org-1", item.RequestID)
			if err != nil || s.Admission == nil || s.Receipt != nil || o.calls != 2 || len(b.delivered) != 0 {
				t.Fatal("age/reopen changed pending approval")
			}
			o.status = "APPROVED"
			d.now = func() time.Time { return start.AddDate(0, 0, days).Add(time.Hour) }
			if err := d.Step(context.Background()); err != nil {
				t.Fatal(err)
			}
			s, err = j.Load("org-1", item.RequestID)
			if err != nil || s.Receipt == nil || len(s.Application) == 0 || o.calls != 3 || len(b.delivered) != 1 {
				t.Fatal("aged approval did not resume exactly once")
			}
		})
	}
}

func TestDispatcherRecoversInvalidCursorWithoutReevaluatingSavedDecisions(t *testing.T) {
	for _, reset := range []bool{true, false} {
		t.Run(fmt.Sprint(reset), func(t *testing.T) {
			c := testConfig(t)
			item := pendingItem(t, "saved")
			fresh, stale := 0, 0
			cursor := "old-key-cursor"
			b := &fakeBackend{page: func(got string) (Page, error) {
				if got != "" {
					stale++
					if reset {
						return Page{}, monolith.ErrInvalidApprovalCursor
					}
					return Page{}, errors.New("temporary outage")
				}
				fresh++
				return Page{Version: 1, Items: []Item{item}, NextCursor: &cursor}, nil
			}}
			d, j := makeDispatcher(t, c, b, nil)
			if err := d.Step(context.Background()); err != nil {
				t.Fatal(err)
			}
			saved, _ := j.Load(c.OrgID, item.RequestID)
			if err := d.Step(context.Background()); err == nil {
				t.Fatal("missing discovery error")
			}
			_ = d.Step(context.Background())
			want := 1
			if reset {
				want = 2
			}
			if fresh != want {
				t.Fatalf("fresh=%d stale=%d reset=%v", fresh, stale, reset)
			}
			after, _ := j.Load(c.OrgID, item.RequestID)
			if !bytes.Equal(saved.Receipt.Bytes, after.Receipt.Bytes) || len(b.delivered) != 1 {
				t.Fatal("saved decision recreated or redelivered")
			}
		})
	}
}
