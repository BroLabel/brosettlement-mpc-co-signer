package monolith

import (
	"context"
	"fmt"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/strictjson"
	"io"
	"net/http"
	"net/url"
	"time"
)

func (c *Client) PostMessage(ctx context.Context, sessionID string, frame OutboundFrame) error {
	path := "/api/v1/co-signer/sessions/" + url.PathEscape(sessionID) + "/messages"
	return c.doJSON(ctx, http.MethodPost, path, frame, frame.MessageID, nil, 0)
}

func (c *Client) GetMessages(ctx context.Context, sessionID string, afterSeq uint64) (MessagesResult, error) {
	path := fmt.Sprintf("/api/v1/co-signer/sessions/%s/messages?afterSeq=%d", url.PathEscape(sessionID), afterSeq)
	// One bounded attempt: the existing polling loop owns retries and cadence.
	req, err := c.newRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return MessagesResult{}, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return MessagesResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return MessagesResult{}, &httpStatusError{statusCode: resp.StatusCode}
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return MessagesResult{}, err
	}
	var out MessagesResult
	if err := strictjson.DecodeClosed(raw, &out); err != nil {
		return MessagesResult{}, fmt.Errorf("%w: %v", ErrInvalidLifecycle, err)
	}
	if err := out.Session.Validate("", sessionID, time.Time{}); err != nil {
		return MessagesResult{}, err
	}
	return out, nil
}
