package monolith

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	maxAttempts              = 3
	maxTerminalResponseBytes = 8 << 10
)

type Client struct {
	baseURL    string
	keyID      string
	privateKey ed25519.PrivateKey
	httpClient *http.Client
}

var (
	ErrAlreadyClaimed      = errors.New("intent already claimed")
	ErrNotFound            = errors.New("intent not found")
	ErrClaimOutcomeUnknown = errors.New("claim outcome unknown")
)

func New(baseURL, keyID string, privateKey ed25519.PrivateKey, timeout time.Duration) *Client {
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		keyID:      keyID,
		privateKey: privateKey,
		httpClient: &http.Client{Timeout: timeout},
	}
}

func (c *Client) GetPendingIntents(ctx context.Context) ([]Intent, error) {
	listing, err := c.ListActionableIntents(ctx)
	if err != nil {
		return nil, err
	}
	intents := make([]Intent, 0, len(listing.Pending))
	for _, item := range listing.Pending {
		intents = append(intents, Intent{
			IntentID:  item.IntentID,
			SessionID: item.SessionID,
			Type:      item.Type,
			ExpiresAt: item.Deadline,
			Payload: IntentPayload{
				Type:                  item.Type,
				OrgID:                 item.OrgID,
				KeyID:                 item.KeyID,
				DescriptorBytes:       append([]byte(nil), item.DescriptorBytes...),
				DescriptorFingerprint: item.DescriptorFingerprint,
			},
		})
	}
	return intents, nil
}

type actionableListingWire struct {
	HTTPStatus    int                     `json:"httpStatus"`
	OwnClaimedDKG *[]actionableIntentWire `json:"ownClaimedDkg"`
	Pending       *[]actionableIntentWire `json:"pending"`
}

type actionableIntentWire struct {
	CoSignerDeploymentID  string `json:"coSignerDeploymentId,omitempty"`
	CreatedAt             string `json:"createdAt"`
	Deadline              string `json:"deadline,omitempty"`
	DescriptorBytes       string `json:"descriptorBytesBase64,omitempty"`
	DescriptorFingerprint string `json:"descriptorFingerprint,omitempty"`
	IntentID              string `json:"intentId"`
	KeyID                 string `json:"keyId"`
	OrgID                 string `json:"orgId"`
	SessionID             string `json:"sessionId,omitempty"`
	Status                string `json:"status"`
	Type                  string `json:"type"`
}

// ListActionableIntents consumes the backend-owned closed listing contract.
// It deliberately preserves defensive foreign/terminal entries for the
// reconciliation layer to classify as protocol-integrity failures.
func (c *Client) ListActionableIntents(ctx context.Context) (ActionableListing, error) {
	var wire actionableListingWire
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/co-signer/intents/pending", nil, "", &wire, http.StatusOK); err != nil {
		return ActionableListing{}, err
	}
	if wire.HTTPStatus != http.StatusOK {
		return ActionableListing{}, errors.New("actionable listing body HTTP status mismatch")
	}
	if wire.OwnClaimedDKG == nil || wire.Pending == nil {
		return ActionableListing{}, errors.New("actionable listing collections are required")
	}

	listing := ActionableListing{
		HTTPStatus:    wire.HTTPStatus,
		OwnClaimedDKG: make([]ActionableIntent, 0, len(*wire.OwnClaimedDKG)),
		Pending:       make([]ActionableIntent, 0, len(*wire.Pending)),
	}
	for _, item := range *wire.OwnClaimedDKG {
		decoded, err := decodeActionableIntent(item)
		if err != nil {
			return ActionableListing{}, fmt.Errorf("decode own claimed DKG listing item: %w", err)
		}
		listing.OwnClaimedDKG = append(listing.OwnClaimedDKG, decoded)
	}
	for _, item := range *wire.Pending {
		decoded, err := decodeActionableIntent(item)
		if err != nil {
			return ActionableListing{}, fmt.Errorf("decode pending listing item: %w", err)
		}
		listing.Pending = append(listing.Pending, decoded)
	}
	return listing, nil
}

func decodeActionableIntent(wire actionableIntentWire) (ActionableIntent, error) {
	if wire.IntentID == "" || wire.KeyID == "" || wire.OrgID == "" || wire.Type == "" || wire.Status == "" {
		return ActionableIntent{}, errors.New("actionable listing identity is incomplete")
	}
	createdAt, err := time.Parse(time.RFC3339Nano, wire.CreatedAt)
	if err != nil || wire.CreatedAt == "" {
		return ActionableIntent{}, errors.New("actionable listing createdAt is invalid")
	}

	item := ActionableIntent{
		CoSignerDeploymentID:  wire.CoSignerDeploymentID,
		CreatedAt:             createdAt,
		CreatedAtRaw:          wire.CreatedAt,
		DeadlineRaw:           wire.Deadline,
		DescriptorFingerprint: wire.DescriptorFingerprint,
		IntentID:              wire.IntentID,
		KeyID:                 wire.KeyID,
		OrgID:                 wire.OrgID,
		SessionID:             wire.SessionID,
		Status:                wire.Status,
		Type:                  wire.Type,
	}
	if wire.Deadline != "" {
		item.Deadline, err = time.Parse(time.RFC3339Nano, wire.Deadline)
		if err != nil {
			return ActionableIntent{}, errors.New("actionable listing deadline is invalid")
		}
	}
	if wire.DescriptorBytes != "" {
		item.DescriptorBytes, err = base64.StdEncoding.Strict().DecodeString(wire.DescriptorBytes)
		if err != nil || base64.StdEncoding.EncodeToString(item.DescriptorBytes) != wire.DescriptorBytes {
			clear(item.DescriptorBytes)
			return ActionableIntent{}, errors.New("actionable listing descriptor is not canonical padded base64")
		}
	}
	return item, nil
}

func (c *Client) ClaimIntent(ctx context.Context, intentID string) (ClaimResult, error) {
	path := "/api/v1/co-signer/intents/" + url.PathEscape(intentID) + "/claim"
	var out ClaimResult
	if err := c.doJSON(ctx, http.MethodPost, path, nil, intentID, &out, http.StatusOK); err != nil {
		switch {
		case statusCode(err) == http.StatusConflict:
			return ClaimResult{}, ErrAlreadyClaimed
		case statusCode(err) == http.StatusNotFound:
			return ClaimResult{}, ErrNotFound
		case isAmbiguous(err):
			return ClaimResult{}, ErrClaimOutcomeUnknown
		default:
			return ClaimResult{}, err
		}
	}
	if out.HTTPStatus != http.StatusOK {
		return ClaimResult{}, errors.New("claim response body HTTP status mismatch")
	}
	return out, nil
}

func (c *Client) PostMessage(ctx context.Context, sessionID string, frame OutboundFrame) error {
	path := "/api/v1/co-signer/sessions/" + url.PathEscape(sessionID) + "/messages"
	return c.doJSON(ctx, http.MethodPost, path, frame, frame.MessageID, nil, 0)
}

func (c *Client) GetMessages(ctx context.Context, sessionID string, afterSeq uint64) ([]InboundMessage, error) {
	path := fmt.Sprintf("/api/v1/co-signer/sessions/%s/messages?afterSeq=%d", url.PathEscape(sessionID), afterSeq)
	var out struct {
		Messages []InboundMessage `json:"messages"`
	}
	if err := c.doJSON(ctx, http.MethodGet, path, nil, "", &out, 0); err != nil {
		return nil, err
	}
	return out.Messages, nil
}

func (c *Client) PostResult(ctx context.Context, intentID string, result IntentResult) error {
	path := "/api/v1/co-signer/intents/" + url.PathEscape(intentID) + "/result"
	return c.doJSON(ctx, http.MethodPost, path, result, intentID, nil, 0)
}

// PostTerminalResult performs exactly one HTTP attempt with the exact body
// supplied by the lifecycle terminal publisher. Generic bounded retries remain
// in doJSON for non-terminal operations.
func (c *Client) PostTerminalResult(ctx context.Context, intentID string, body []byte) (TerminalHTTPResponse, error) {
	path := "/api/v1/co-signer/intents/" + url.PathEscape(intentID) + "/result"
	req, err := c.newRequest(ctx, http.MethodPost, path, body)
	if err != nil {
		return TerminalHTTPResponse{}, err
	}
	req.Header.Set("X-Idempotency-Key", intentID)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return TerminalHTTPResponse{}, err
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, maxTerminalResponseBytes+1))
	if err != nil {
		return TerminalHTTPResponse{StatusCode: resp.StatusCode, Body: responseBody}, err
	}
	if len(responseBody) > maxTerminalResponseBytes {
		return TerminalHTTPResponse{
			StatusCode:        resp.StatusCode,
			ProtocolViolation: "authoritative_response_too_large",
		}, nil
	}
	return TerminalHTTPResponse{StatusCode: resp.StatusCode, Body: responseBody}, nil
}

func (c *Client) doJSON(
	ctx context.Context,
	method string,
	path string,
	payload any,
	idempotencyKey string,
	out any,
	expectedStatus int,
) error {
	var body []byte
	if payload != nil {
		var err error
		body, err = json.Marshal(payload)
		if err != nil {
			return err
		}
	}

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		req, err := c.newRequest(ctx, method, path, body)
		if err != nil {
			return err
		}
		if idempotencyKey != "" {
			req.Header.Set("X-Idempotency-Key", idempotencyKey)
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			if attempt < maxAttempts && isRetryable(err) {
				time.Sleep(backoff(attempt))
				continue
			}
			return lastErr
		}

		respBody, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			if attempt < maxAttempts {
				time.Sleep(backoff(attempt))
				continue
			}
			return lastErr
		}

		if resp.StatusCode >= 500 {
			lastErr = &httpStatusError{statusCode: resp.StatusCode, body: string(respBody)}
			if attempt < maxAttempts {
				time.Sleep(backoff(attempt))
				continue
			}
			return lastErr
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return &httpStatusError{statusCode: resp.StatusCode, body: string(respBody)}
		}
		if expectedStatus != 0 && resp.StatusCode != expectedStatus {
			return &httpStatusError{statusCode: resp.StatusCode, body: string(respBody)}
		}

		if out == nil || len(respBody) == 0 {
			return nil
		}
		if err := decodeStrictJSON(respBody, out); err != nil {
			return err
		}
		if observer, ok := out.(interface {
			retainExactResponseFields([]byte) error
		}); ok {
			if err := observer.retainExactResponseFields(respBody); err != nil {
				return err
			}
		}
		return nil
	}

	if lastErr != nil {
		return lastErr
	}
	return errors.New("request failed")
}

func (c *Client) newRequest(ctx context.Context, method, path string, body []byte) (*http.Request, error) {
	requestURL := c.baseURL + path

	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, requestURL, reader)
	if err != nil {
		return nil, err
	}
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.signRequest(req, body)
}

func (c *Client) signRequest(req *http.Request, body []byte) (*http.Request, error) {
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonce, err := newNonce()
	if err != nil {
		return nil, err
	}
	bodyHash := ""
	if len(body) > 0 {
		sum := sha256.Sum256(body)
		bodyHash = hex.EncodeToString(sum[:])
		req.Header.Set("X-Api-Body-Hash", bodyHash)
	}

	canonical := strings.Join([]string{
		strings.ToUpper(req.Method),
		req.URL.RequestURI(),
		bodyHash,
		timestamp,
		nonce,
		c.keyID,
	}, "\n")
	signature := ed25519.Sign(c.privateKey, []byte(canonical))

	req.Header.Set("X-Api-Key-Id", c.keyID)
	req.Header.Set("X-Api-Timestamp", timestamp)
	req.Header.Set("X-Api-Nonce", nonce)
	req.Header.Set("X-Api-Signature", base64.StdEncoding.EncodeToString(signature))
	return req, nil
}

func newNonce() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

type httpStatusError struct {
	statusCode int
	body       string
}

func (e *httpStatusError) Error() string {
	if e.body == "" {
		return fmt.Sprintf("http status %d", e.statusCode)
	}
	return fmt.Sprintf("http status %d: %s", e.statusCode, e.body)
}

func statusCode(err error) int {
	var statusErr *httpStatusError
	if errors.As(err, &statusErr) {
		return statusErr.statusCode
	}
	return 0
}

func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	return true
}

func isAmbiguous(err error) bool {
	if err == nil {
		return false
	}
	if statusCode(err) != 0 {
		return false
	}
	return !errors.Is(err, context.Canceled)
}

func backoff(attempt int) time.Duration {
	if attempt < 1 {
		return 0
	}
	return time.Duration(1<<(attempt-1)) * 10 * time.Millisecond
}

func decodeStrictJSON(raw []byte, target any) error {
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func rejectDuplicateJSONKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := walkJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func walkJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			nameToken, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := nameToken.(string)
			if !ok {
				return errors.New("JSON object name is not a string")
			}
			if _, exists := seen[name]; exists {
				return fmt.Errorf("duplicate JSON field %q", name)
			}
			seen[name] = struct{}{}
			if err := walkJSONValue(decoder); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := walkJSONValue(decoder); err != nil {
				return err
			}
		}
	default:
		return errors.New("invalid JSON delimiter")
	}
	_, err = decoder.Token()
	return err
}
