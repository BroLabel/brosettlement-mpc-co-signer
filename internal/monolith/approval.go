package monolith

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	wire "github.com/BroLabel/brosettlement-mpc-co-signer/internal/contract/approval"
)

var ErrInvalidApprovalCursor = errors.New("invalid approval discovery cursor")

// approvalHTTP reuses signed authentication, but never retries, follows redirects,
// exposes arbitrary response bodies in errors, or reads an unbounded response.
func (c *Client) approvalHTTP(ctx context.Context, method, path string, body []byte, maximum int) (int, []byte, error) {
	req, e := c.newRequest(ctx, method, path, body)
	if e != nil {
		return 0, nil, e
	}
	req.GetBody = nil
	client := *c.httpClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, e := client.Do(req)
	if e != nil {
		return 0, nil, errors.New("approval backend transport failed")
	}
	defer resp.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(resp.Body, int64(maximum)+1))
	if e != nil || len(raw) > maximum {
		return resp.StatusCode, nil, errors.New("invalid approval backend response size")
	}
	return resp.StatusCode, raw, nil
}
func (c *Client) PendingApprovals(ctx context.Context, cursor string) (wire.PendingPage, error) {
	var page wire.PendingPage
	query := url.Values{"limit": []string{"50"}}
	if cursor != "" {
		if len(cursor) > 4096 {
			return page, errors.New("invalid approval cursor")
		}
		query.Set("cursor", cursor)
	}
	status, raw, e := c.approvalHTTP(ctx, http.MethodGet, "/api/v1/co-signer/approval-requests?"+query.Encode(), nil, 2*1024*1024)
	if e != nil {
		return page, e
	}
	if status != http.StatusOK {
		var failure struct {
			Code string `json:"code"`
		}
		if status == http.StatusBadRequest && json.Unmarshal(raw, &failure) == nil && failure.Code == "INVALID_CURSOR" {
			return page, ErrInvalidApprovalCursor
		}
		return page, fmt.Errorf("approval discovery HTTP status %d", status)
	}
	if e = wire.ParsePendingPage(raw); e != nil {
		return page, e
	}
	if e = json.Unmarshal(raw, &page); e != nil {
		return page, e
	}
	if len(page.Items) > 50 {
		return wire.PendingPage{}, errors.New("approval discovery exceeded requested page limit")
	}
	return page, nil
}
func (c *Client) SendApprovalDecision(ctx context.Context, request string, body []byte) ([]byte, error) {
	if !wire.ValidReference(request) || len(body) == 0 || len(body) > 8192 {
		return nil, errors.New("invalid approval decision request")
	}
	segment := url.PathEscape(request)
	if segment == "." || segment == ".." {
		segment = strings.ReplaceAll(segment, ".", "%2E")
	}
	status, raw, e := c.approvalHTTP(ctx, http.MethodPost, "/api/v1/co-signer/approval-requests/"+segment+"/decision", body, 4096)
	if e != nil {
		return nil, e
	}
	if status != http.StatusOK && status != http.StatusConflict {
		return nil, fmt.Errorf("approval decision HTTP status %d", status)
	}
	v, e := wire.ParseDecisionApplication(raw)
	if e != nil {
		return nil, e
	}
	var id, outcome string
	json.Unmarshal(v["requestId"], &id)
	json.Unmarshal(v["outcome"], &outcome)
	if id != request || (status == http.StatusConflict) != (outcome == "TERMINAL_CONFLICT") {
		return nil, errors.New("approval decision response status or binding mismatch")
	}
	return raw, nil
}
