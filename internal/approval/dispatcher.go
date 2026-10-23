package approval

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"sync"
	"time"

	wire "github.com/BroLabel/brosettlement-mpc-co-signer/internal/contract/approval"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/monolith"
	"github.com/gowebpki/jcs"
)

type Item = wire.PendingItem
type Page = wire.PendingPage
type Backend interface {
	PendingApprovals(context.Context, string) (Page, error)
	SendApprovalDecision(context.Context, string, []byte) ([]byte, error)
}
type Organization interface {
	Evaluate(context.Context, Item) (wire.Object, error)
}
type suppressionKey struct {
	orgID, requestID string
	savedReceipt     bool
}

type suppression struct {
	attempts uint
	until    time.Time
	sequence uint64
}
type Dispatcher struct {
	cfg          Config
	journal      *Journal
	backend      Backend
	organization Organization
	keyID        string
	key          ed25519.PrivateKey
	cursor       string
	backoff      map[suppressionKey]suppression
	sequence     uint64
	recoveryTurn bool
	now          func() time.Time
	jitter       func(time.Duration) time.Duration
	mu           sync.Mutex
}

func NewDispatcher(c Config, j *Journal, b Backend, o Organization, keyID string, key ed25519.PrivateKey) (*Dispatcher, error) {
	if e := c.Validate(); e != nil {
		return nil, e
	}
	if j == nil || b == nil || !wire.ValidReference(keyID) || len(key) != ed25519.PrivateKeySize || c.Mode == "external" && o == nil {
		return nil, ErrIntegrity
	}
	return &Dispatcher{cfg: c, journal: j, backend: b, organization: o, keyID: keyID, key: append(ed25519.PrivateKey(nil), key...), backoff: make(map[suppressionKey]suppression), now: time.Now, jitter: func(max time.Duration) time.Duration {
		if max <= 0 {
			return 0
		}
		return time.Duration(rand.Int64N(int64(max)))
	}}, nil
}
func (d *Dispatcher) Run(ctx context.Context, report func(error)) {
	defer clear(d.key)
	for {
		if e := d.Step(ctx); e != nil && ctx.Err() == nil && report != nil {
			report(e)
		}
		timer := time.NewTimer(d.cfg.MinInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
func (d *Dispatcher) due(key suppressionKey) bool {
	s, ok := d.backoff[key]
	return !ok || !d.now().Before(s.until)
}
func (d *Dispatcher) suppress(key suppressionKey) {
	s := d.backoff[key]
	if s.attempts < 1024 {
		s.attempts++
	}
	ceiling := float64(d.cfg.MinInterval) * math.Pow(d.cfg.BackoffFactor, float64(s.attempts))
	ceilingDuration := d.cfg.MaxInterval
	if !math.IsInf(ceiling, 0) && ceiling < float64(d.cfg.MaxInterval) {
		ceilingDuration = time.Duration(ceiling)
	}
	s.until = d.now().Add(d.jitter(ceilingDuration))
	d.sequence++
	s.sequence = d.sequence
	if _, ok := d.backoff[key]; !ok && len(d.backoff) >= 1000 {
		var oldest suppressionKey
		seq := uint64(math.MaxUint64)
		for k, v := range d.backoff {
			if !k.savedReceipt && v.sequence < seq {
				oldest = k
				seq = v.sequence
			}
		}
		// Saved-delivery hints cannot be evicted by fresh evaluations. Once all
		// capacity belongs to saved evidence, discovery continues with backpressure.
		if oldest == (suppressionKey{}) {
			return
		}
		delete(d.backoff, oldest)
	}
	d.backoff[key] = s
}

// Step performs bounded saved-receipt recovery, then one fresh authenticated page.
// The mutex also prevents accidental concurrent callers from increasing org concurrency.
func (d *Dispatcher) Step(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if e := ctx.Err(); e != nil {
		return e
	}
	var result error
	result = errors.Join(result, d.recoverSaved(ctx))

	pageCtx, cancel := context.WithTimeout(ctx, d.cfg.Timeout)
	page, e := d.backend.PendingApprovals(pageCtx, d.cursor)
	cancel()
	if e != nil {
		// Cursor key rotation invalidates a sweep, not saved decisions. Restart
		// discovery on the next scheduled step; ordinary outages retain position.
		if errors.Is(e, monolith.ErrInvalidApprovalCursor) {
			d.cursor = ""
		}
		return errors.Join(result, e)
	}
	raw, e := json.Marshal(page)
	if e != nil {
		return errors.Join(result, e)
	}
	if e = wire.ParsePendingPage(raw); e != nil {
		return errors.Join(result, e)
	}
	if len(page.Items) > 50 {
		return errors.Join(result, ErrIntegrity)
	}
	for _, item := range page.Items {
		if e = ctx.Err(); e != nil {
			return errors.Join(result, e)
		}
		if e = d.process(ctx, item); e != nil {
			result = errors.Join(result, fmt.Errorf("approval request %q: %w", item.RequestID, e))
		}
	}
	d.cursor = ""
	if page.NextCursor != nil {
		d.cursor = *page.NextCursor
	}
	return result
}
func (d *Dispatcher) process(ctx context.Context, item Item) error {
	orgID, e := item.OrganizationID()
	if e != nil {
		return e
	}
	state, e := d.journal.Load(orgID, item.RequestID)
	if e != nil {
		return e
	}
	if state.Admission != nil && state.Admission.AuthorizationHash != item.AuthorizationHash {
		return ErrConflict
	}
	if state.Receipt != nil {
		if len(state.Application) == 0 && d.due(suppressionKey{orgID, item.RequestID, true}) {
			return d.deliver(ctx, *state.Receipt)
		}
		return nil
	}
	if d.deliveryHints() >= 1000 {
		return errors.New("approval delivery retry capacity exhausted; new evaluations suspended")
	}
	key := suppressionKey{orgID, item.RequestID, false}
	if !d.due(key) {
		return nil
	}
	var endpoint *string
	if d.cfg.Mode == "external" {
		v := d.cfg.Endpoint
		endpoint = &v
	}
	a := Admission{1, orgID, item.RequestID, item.AuthorizationHash, d.cfg.Mode, endpoint, d.now().UTC().Format("2006-01-02T15:04:05.000Z")}
	if e = d.journal.Admit(a); e != nil {
		return e
	}
	// Admit compares pinned nonsecret config, including the endpoint and mode. No
	// credential fallback or reconstruction from backend/claim evidence is allowed.
	var response wire.Object
	if a.Mode == "external" {
		attemptCtx, cancel := context.WithTimeout(ctx, d.cfg.Timeout)
		response, e = d.organization.Evaluate(attemptCtx, item)
		cancel()
		if e != nil {
			d.suppress(key)
			return e
		}
		if field(response, "status") == "PENDING" {
			d.suppress(key)
			return nil
		}
	} else {
		response = wire.Object{"status": json.RawMessage(`"APPROVED"`), "reason": json.RawMessage(`null`), "decisionReference": json.RawMessage(`null`)}
	}
	raw, e := d.receipt(a, response)
	if e != nil {
		return e
	}
	saved := SavedReceipt{1, a.OrgID, a.RequestID, a.AuthorizationHash, d.keyID, d.key.Public().(ed25519.PublicKey), raw}
	if e = d.journal.SaveReceipt(saved); e != nil {
		return e
	}
	delete(d.backoff, key)
	return d.deliver(ctx, saved)
}
func (d *Dispatcher) receipt(a Admission, response wire.Object) ([]byte, error) {
	envelope := map[string]any{"version": 1, "domain": "brosettlement.co-signer.approval-decision", "audience": "brosettlement-transfer-approval", "orgId": a.OrgID, "requestId": a.RequestID, "authorizationHash": a.AuthorizationHash, "decision": field(response, "status"), "reason": response["reason"], "source": a.Mode, "externalDecisionReference": response["decisionReference"], "decidedAt": d.now().UTC().Format("2006-01-02T15:04:05.000Z"), "signingKeyId": d.keyID}
	raw, e := json.Marshal(envelope)
	if e != nil {
		return nil, e
	}
	raw, e = jcs.Transform(raw)
	if e != nil {
		return nil, e
	}
	if _, e = wire.ParseDecisionEnvelope(raw); e != nil {
		return nil, e
	}
	signature := ed25519.Sign(d.key, append([]byte(wire.DecisionDomain), raw...))
	return json.Marshal(map[string]any{"version": 1, "envelopeBase64": base64.StdEncoding.EncodeToString(raw), "signatureBase64": base64.StdEncoding.EncodeToString(signature)})
}
func (d *Dispatcher) deliver(ctx context.Context, r SavedReceipt) error {
	key := suppressionKey{r.OrgID, r.RequestID, true}
	attemptCtx, cancel := context.WithTimeout(ctx, d.cfg.Timeout)
	application, e := d.backend.SendApprovalDecision(attemptCtx, r.RequestID, r.Bytes)
	cancel()
	if e != nil {
		d.suppress(key)
		return e
	}
	if e = d.journal.Acknowledge(r.OrgID, r.RequestID, application); e != nil {
		d.suppress(key)
		return e
	}
	delete(d.backoff, key)
	parsed, e := wire.ParseDecisionApplication(application)
	if e != nil {
		return e
	}
	if field(parsed, "disposition") == "TERMINAL_CONFLICT" {
		return errors.New("approval decision terminal conflict; reconcile saved evidence")
	}
	return nil
}

func (d *Dispatcher) deliveryHints() int {
	n := 0
	for k := range d.backoff {
		if k.savedReceipt {
			n++
		}
	}
	return n
}
func (d *Dispatcher) retrySavedHint(ctx context.Context) (bool, error) {
	var key suppressionKey
	order := uint64(math.MaxUint64)
	for k, v := range d.backoff {
		if k.savedReceipt && d.due(k) && v.sequence < order {
			key = k
			order = v.sequence
		}
	}
	if key == (suppressionKey{}) {
		return false, nil
	}
	s, e := d.journal.Load(key.orgID, key.requestID)
	if e != nil {
		d.suppress(key)
		return true, e
	}
	if s.Receipt == nil {
		d.suppress(key)
		return true, ErrIntegrity
	}
	if len(s.Application) > 0 {
		delete(d.backoff, key)
		return false, nil
	}
	return true, d.deliver(ctx, *s.Receipt)
}
func (d *Dispatcher) recoverSaved(ctx context.Context) error {
	// Alternate due known retries with streaming recovery. Both are bounded and
	// refer only to already durable receipts, never cached organization work.
	d.recoveryTurn = !d.recoveryTurn
	if d.recoveryTurn {
		if attempted, e := d.retrySavedHint(ctx); attempted || e != nil {
			return e
		}
	}
	receipts, e := d.journal.PendingReceipts(50)
	result := e
	for _, r := range receipts {
		if d.due(suppressionKey{r.OrgID, r.RequestID, true}) {
			return errors.Join(result, d.deliver(ctx, r))
		}
	}
	if attempted, e := d.retrySavedHint(ctx); attempted || e != nil {
		return errors.Join(result, e)
	}
	return result
}
