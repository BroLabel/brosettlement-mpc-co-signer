package approval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	wire "github.com/BroLabel/brosettlement-mpc-co-signer/internal/contract/approval"
)

type External struct {
	cfg    Config
	client *http.Client
}

func NewExternal(c Config, client *http.Client) (*External, error) {
	if e := c.Validate(); e != nil {
		return nil, e
	}
	if c.Mode != "external" {
		return nil, errors.New("organization transport requires external approval mode")
	}
	if client == nil {
		client = &http.Client{}
	}
	copy := *client
	copy.Timeout = c.Timeout
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &External{c, &copy}, nil
}
func (o *External) Evaluate(ctx context.Context, item Item) (wire.Object, error) {
	orgID, e := item.OrganizationID()
	if e != nil {
		return nil, e
	}
	raw, e := json.Marshal(map[string]any{"version": 1, "orgId": orgID, "requestId": item.RequestID, "authorizationHash": item.AuthorizationHash, "authorization": item.Authorization})
	if e != nil {
		return nil, e
	}
	if len(raw) > 65536 {
		return nil, ErrIntegrity
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, o.cfg.Endpoint, bytes.NewReader(raw))
	if e != nil {
		return nil, errors.New("invalid organization approval request")
	}
	// Idempotency-Key makes net/http consider POST replayable. Remove GetBody so a
	// reused connection failure cannot turn one fresh admission into two attempts.
	req.GetBody = nil
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+o.cfg.BearerToken)
	req.Header.Set("Idempotency-Key", item.RequestID+":"+item.AuthorizationHash)
	resp, e := o.client.Do(req)
	if e != nil {
		return nil, errors.New("organization approval transport failed")
	}
	defer resp.Body.Close()
	body, e := io.ReadAll(io.LimitReader(resp.Body, 65537))
	if e != nil || len(body) > 65536 {
		return nil, errors.New("invalid organization approval response size")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("organization approval HTTP status %d", resp.StatusCode)
	}
	v, e := wire.ParseOrganizationResponse(body, orgID, item.RequestID, item.AuthorizationHash)
	if e != nil {
		return nil, errors.New("invalid organization approval response contract or bindings")
	}
	return v, nil
}
