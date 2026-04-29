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

const maxAttempts = 3

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
	var out struct {
		Intents []Intent `json:"intents"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/co-signer/intents/pending", nil, "", &out); err != nil {
		return nil, err
	}
	return out.Intents, nil
}

func (c *Client) ClaimIntent(ctx context.Context, intentID string) (ClaimResult, error) {
	path := "/api/v1/co-signer/intents/" + url.PathEscape(intentID) + "/claim"
	var out ClaimResult
	if err := c.doJSON(ctx, http.MethodPost, path, nil, intentID, &out); err != nil {
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
	return out, nil
}

func (c *Client) PostMessage(ctx context.Context, sessionID string, frame OutboundFrame) error {
	path := "/api/v1/co-signer/sessions/" + url.PathEscape(sessionID) + "/messages"
	return c.doJSON(ctx, http.MethodPost, path, frame, frame.MessageID, nil)
}

func (c *Client) GetMessages(ctx context.Context, sessionID string, afterSeq uint64) ([]InboundMessage, error) {
	path := fmt.Sprintf("/api/v1/co-signer/sessions/%s/messages?afterSeq=%d", url.PathEscape(sessionID), afterSeq)
	var out struct {
		Messages []InboundMessage `json:"messages"`
	}
	if err := c.doJSON(ctx, http.MethodGet, path, nil, "", &out); err != nil {
		return nil, err
	}
	return out.Messages, nil
}

func (c *Client) PostResult(ctx context.Context, intentID string, result IntentResult) error {
	path := "/api/v1/co-signer/intents/" + url.PathEscape(intentID) + "/result"
	return c.doJSON(ctx, http.MethodPost, path, result, intentID, nil)
}

func (c *Client) doJSON(ctx context.Context, method, path string, payload any, idempotencyKey string, out any) error {
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

		if out == nil || len(respBody) == 0 {
			return nil
		}
		if err := json.Unmarshal(respBody, out); err != nil {
			return err
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
		req.URL.Path,
		bodyHash,
		timestamp,
		nonce,
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
