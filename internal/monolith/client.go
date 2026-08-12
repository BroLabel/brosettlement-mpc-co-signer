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
	ErrTerminalConflict    = errors.New("terminal result conflict")
)

type ResultConflictError struct {
	AuthoritativeStatus string
}

func (e *ResultConflictError) Error() string { return ErrTerminalConflict.Error() }
func (e *ResultConflictError) Unwrap() error { return ErrTerminalConflict }

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
	intents := make([]Intent, 0, len(listing.OwnClaimedSign)+len(listing.Pending))
	appendIntent := func(item ActionableIntent) {
		intents = append(intents, Intent{
			CreatedAt:       item.CreatedAt,
			DeadlineRaw:     item.DeadlineRaw,
			DiscoveryStatus: item.Status,
			IntentID:        item.IntentID,
			SessionID:       item.SessionID,
			Type:            item.Type,
			ExpiresAt:       item.Deadline,
			Payload: IntentPayload{
				Type:                  item.Type,
				OrgID:                 item.OrgID,
				KeyID:                 item.KeyID,
				DescriptorBytes:       append([]byte(nil), item.DescriptorBytes...),
				DescriptorFingerprint: item.DescriptorFingerprint,
			},
		})
	}
	for _, item := range listing.OwnClaimedSign {
		appendIntent(item)
	}
	for _, item := range listing.Pending {
		appendIntent(item)
	}
	return intents, nil
}

type actionableListingWire struct {
	HTTPStatus     int                     `json:"httpStatus"`
	OwnClaimedDKG  *[]actionableIntentWire `json:"ownClaimedDkg"`
	OwnClaimedSign *[]actionableIntentWire `json:"ownClaimedSign"`
	Pending        *[]actionableIntentWire `json:"pending"`
}

type actionableIntentWire struct {
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
	fields                map[string]struct{}
}

func (wire *actionableIntentWire) UnmarshalJSON(raw []byte) error {
	type plain actionableIntentWire
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	allowed := map[string]struct{}{
		"createdAt": {}, "deadline": {}, "descriptorBytesBase64": {},
		"descriptorFingerprint": {}, "intentId": {}, "keyId": {}, "orgId": {}, "sessionId": {}, "status": {}, "type": {},
	}
	for name := range fields {
		if _, ok := allowed[name]; !ok {
			return fmt.Errorf("unknown actionable listing field %q", name)
		}
	}
	var decoded plain
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	*wire = actionableIntentWire(decoded)
	wire.fields = make(map[string]struct{}, len(fields))
	for name := range fields {
		wire.fields[name] = struct{}{}
	}
	return nil
}

func (wire actionableIntentWire) hasExactFields(expected ...string) bool {
	if len(wire.fields) != len(expected) {
		return false
	}
	for _, name := range expected {
		if _, ok := wire.fields[name]; !ok {
			return false
		}
	}
	return true
}

// ListActionableIntents consumes the backend-owned closed listing contract.
// It deliberately preserves defensively invalid or terminal entries for the
// reconciliation layer to classify as protocol-integrity failures.
func (c *Client) ListActionableIntents(ctx context.Context) (ActionableListing, error) {
	var wire actionableListingWire
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/co-signer/intents/pending", nil, "", &wire, http.StatusOK); err != nil {
		return ActionableListing{}, err
	}
	if wire.HTTPStatus != http.StatusOK {
		return ActionableListing{}, errors.New("actionable listing body HTTP status mismatch")
	}
	if wire.OwnClaimedDKG == nil || wire.OwnClaimedSign == nil || wire.Pending == nil {
		return ActionableListing{}, errors.New("actionable listing collections are required")
	}

	listing := ActionableListing{
		HTTPStatus:     wire.HTTPStatus,
		OwnClaimedDKG:  make([]ActionableIntent, 0, len(*wire.OwnClaimedDKG)),
		OwnClaimedSign: make([]ActionableIntent, 0, len(*wire.OwnClaimedSign)),
		Pending:        make([]ActionableIntent, 0, len(*wire.Pending)),
	}
	for _, item := range *wire.OwnClaimedDKG {
		decoded, err := decodeActionableIntent(item, "own claimed DKG")
		if err != nil {
			return ActionableListing{}, fmt.Errorf("decode own claimed DKG listing item: %w", err)
		}
		listing.OwnClaimedDKG = append(listing.OwnClaimedDKG, decoded)
	}
	for _, item := range *wire.OwnClaimedSign {
		decoded, err := decodeActionableIntent(item, "own claimed SIGN")
		if err != nil {
			return ActionableListing{}, fmt.Errorf("decode own claimed SIGN listing item: %w", err)
		}
		listing.OwnClaimedSign = append(listing.OwnClaimedSign, decoded)
	}
	for _, item := range *wire.Pending {
		decoded, err := decodeActionableIntent(item, "pending")
		if err != nil {
			return ActionableListing{}, fmt.Errorf("decode pending listing item: %w", err)
		}
		listing.Pending = append(listing.Pending, decoded)
	}
	return listing, nil
}

func decodeActionableIntent(wire actionableIntentWire, collection string) (ActionableIntent, error) {
	if wire.IntentID == "" || wire.KeyID == "" || wire.OrgID == "" || wire.Type == "" || wire.Status == "" {
		return ActionableIntent{}, errors.New("actionable listing identity is incomplete")
	}
	createdAt, err := time.Parse(time.RFC3339Nano, wire.CreatedAt)
	if err != nil || wire.CreatedAt == "" {
		return ActionableIntent{}, errors.New("actionable listing createdAt is invalid")
	}

	item := ActionableIntent{
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
	switch collection {
	case "own claimed DKG":
		if !wire.hasExactFields("createdAt", "deadline", "descriptorBytesBase64", "descriptorFingerprint", "intentId", "keyId", "orgId", "sessionId", "status", "type") ||
			wire.Type != "DKG" || wire.Status != "CLAIMED" || wire.SessionID == "" || wire.Deadline == "" || wire.DescriptorBytes == "" || wire.DescriptorFingerprint == "" {
			return ActionableIntent{}, errors.New("own claimed DKG fields are invalid")
		}
	case "own claimed SIGN":
		if !wire.hasExactFields("createdAt", "deadline", "intentId", "keyId", "orgId", "sessionId", "status", "type") ||
			wire.Type != "SIGN" || wire.Status != "CLAIMED" || wire.SessionID == "" || wire.Deadline == "" || wire.DescriptorBytes != "" || wire.DescriptorFingerprint != "" {
			return ActionableIntent{}, errors.New("own claimed SIGN fields are invalid")
		}
	case "pending":
		switch wire.Type {
		case "DKG":
			if !wire.hasExactFields("createdAt", "deadline", "descriptorBytesBase64", "descriptorFingerprint", "intentId", "keyId", "orgId", "sessionId", "status", "type") ||
				wire.Status != "PENDING" || wire.SessionID == "" || wire.Deadline == "" || wire.DescriptorBytes == "" || wire.DescriptorFingerprint == "" {
				return ActionableIntent{}, errors.New("pending DKG fields are invalid")
			}
		case "SIGN":
			if !wire.hasExactFields("createdAt", "intentId", "keyId", "orgId", "status", "type") ||
				wire.Status != "PENDING" || wire.SessionID != "" || wire.Deadline != "" || wire.DescriptorBytes != "" || wire.DescriptorFingerprint != "" {
				return ActionableIntent{}, errors.New("pending SIGN fields are invalid")
			}
		default:
			return ActionableIntent{}, errors.New("pending intent type is invalid")
		}
	default:
		return ActionableIntent{}, errors.New("actionable listing collection is invalid")
	}
	return item, nil
}

func (c *Client) ClaimIntent(ctx context.Context, intentType, intentID string) (ClaimResult, error) {
	pathType, err := intentTypePath(intentType)
	if err != nil {
		return ClaimResult{}, err
	}
	path := "/api/v1/co-signer/intents/" + pathType + "/" + url.PathEscape(intentID) + "/claim"
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
	if err := validateClaimResult(out, intentType); err != nil {
		return ClaimResult{}, err
	}
	return out, nil
}

func validateClaimResult(claim ClaimResult, expectedType string) error {
	if claim.Status != "CLAIMED" {
		return errors.New("claim response status is invalid")
	}
	actualType := claim.Intent().Type
	if !strings.EqualFold(actualType, expectedType) {
		return errors.New("claim response kind does not match requested intent type")
	}
	if actualType == "SIGN" {
		return validateSignClaimResult(claim)
	}
	if actualType == "DKG" {
		if claim.Payload.Type != "" && claim.Payload.Type != "DKG" {
			return errors.New("DKG claim payload kind mismatch")
		}
		return nil
	}
	if len(claim.DescriptorBytes) > 0 {
		return nil
	}
	return errors.New("claim response kind is invalid")
}

func validateSignClaimResult(claim ClaimResult) error {
	payload := claim.Payload
	if claim.IntentID == "" || claim.SessionID == "" || claim.Deadline.IsZero() || claim.DeadlineRaw == "" ||
		claim.ClaimedBy != "" || claim.ClaimedAt != nil || !claim.ExpiresAt.IsZero() || claim.OrgID != "" || claim.KeyID != "" || len(claim.DescriptorBytes) != 0 ||
		claim.DescriptorFingerprint != "" || len(claim.ChainCode) != 0 ||
		payload.Type != "SIGN" || payload.OrgID == "" || payload.KeyID == "" || payload.WalletID == "" || payload.ProfileID == "" || payload.ProfileTemplateID == "" ||
		payload.ProfileVersion == 0 || len(payload.Parties) < 2 || payload.Threshold < 2 || payload.Algorithm == "" || payload.Curve == "" || payload.Chain == "" ||
		len(payload.Digest) == 0 || payload.DigestType == "" || payload.HashAlgorithm == "" || payload.SigningPayloadType == "" || payload.DerivationContextHash == "" ||
		payload.PartyID == "" || payload.DerivationContext == nil || payload.ChainCode != "" || payload.ChainCodeHash != "" || payload.DerivationScheme != "" ||
		len(payload.DescriptorBytes) != 0 || payload.DescriptorFingerprint != "" {
		return errors.New("SIGN claim response is incomplete")
	}
	derivation := payload.DerivationContext
	if derivation.ProfileID == "" || derivation.ProfileTemplateID == "" || derivation.Chain == "" || derivation.Algorithm == "" || derivation.Curve == "" ||
		derivation.Scheme == "" || derivation.AccountPath == "" || derivation.ChildPath == "" || derivation.FullPath == "" || derivation.PublicKeyFormat == "" ||
		derivation.DescriptorVersion == 0 || derivation.ProfileVersion == 0 || derivation.KeyVersion == 0 {
		return errors.New("SIGN claim derivation context is incomplete")
	}
	if payload.ProfileID != derivation.ProfileID || payload.ProfileTemplateID != derivation.ProfileTemplateID || payload.ProfileVersion != derivation.ProfileVersion ||
		payload.Chain != derivation.Chain || !strings.EqualFold(payload.Algorithm, derivation.Algorithm) || !strings.EqualFold(payload.Curve, derivation.Curve) {
		return errors.New("SIGN claim derivation context mismatch")
	}
	return nil
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
	body, err := marshalSignResult(result)
	if err != nil {
		return err
	}
	path := "/api/v1/co-signer/intents/sign/" + url.PathEscape(intentID) + "/result"
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		req, err := c.newRequest(ctx, http.MethodPost, path, body)
		if err != nil {
			return err
		}
		req.Header.Set("X-Idempotency-Key", intentID)
		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			if attempt < maxAttempts && isRetryable(err) {
				time.Sleep(backoff(attempt))
				continue
			}
			return err
		}
		responseBody, readErr := io.ReadAll(io.LimitReader(resp.Body, maxTerminalResponseBytes+1))
		_ = resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			if attempt < maxAttempts {
				time.Sleep(backoff(attempt))
				continue
			}
			return readErr
		}
		if len(responseBody) > maxTerminalResponseBytes {
			return errors.New("SIGN result response is too large")
		}
		if resp.StatusCode >= http.StatusInternalServerError {
			lastErr = &httpStatusError{statusCode: resp.StatusCode, body: string(responseBody)}
			if attempt < maxAttempts {
				time.Sleep(backoff(attempt))
				continue
			}
			return lastErr
		}
		return parseSignResultOutcome(resp.StatusCode, responseBody, result.Status)
	}
	return lastErr
}

func marshalSignResult(result IntentResult) ([]byte, error) {
	fields := make(map[string]string)
	switch result.Status {
	case "COMPLETED":
		if result.ErrorCode != "" || result.ErrorMessage != "" {
			return nil, errors.New("completed SIGN result must be minimal")
		}
		fields["status"] = result.Status
	case "FAILED":
		fields["status"] = result.Status
		if result.ErrorCode != "" {
			fields["errorCode"] = result.ErrorCode
		}
		if result.ErrorMessage != "" {
			fields["errorMessage"] = result.ErrorMessage
		}
	default:
		return nil, errors.New("invalid SIGN result status")
	}
	return json.Marshal(fields)
}

type signResultOutcomeWire struct {
	AuthoritativeStatus string `json:"authoritativeStatus"`
	HTTPStatus          int    `json:"httpStatus"`
	Outcome             string `json:"outcome"`
}

func parseSignResultOutcome(statusCode int, body []byte, submittedStatus string) error {
	var outcome signResultOutcomeWire
	if err := decodeStrictJSON(body, &outcome); err != nil {
		return fmt.Errorf("decode SIGN result outcome: %w", err)
	}
	if outcome.HTTPStatus != statusCode {
		return errors.New("SIGN result HTTP status mismatch")
	}
	switch statusCode {
	case http.StatusOK:
		if outcome.Outcome != "ACCEPTED" && outcome.Outcome != "EXACT_REPLAY" {
			return errors.New("invalid successful SIGN result outcome")
		}
		if outcome.AuthoritativeStatus != submittedStatus || (outcome.AuthoritativeStatus != "COMPLETED" && outcome.AuthoritativeStatus != "FAILED") {
			return errors.New("successful SIGN result authoritative status mismatch")
		}
		return nil
	case http.StatusConflict:
		if outcome.Outcome != "TERMINAL_CONFLICT" || (outcome.AuthoritativeStatus != "COMPLETED" && outcome.AuthoritativeStatus != "FAILED" && outcome.AuthoritativeStatus != "TIMED_OUT") {
			return errors.New("invalid conflicting SIGN result outcome")
		}
		return &ResultConflictError{AuthoritativeStatus: outcome.AuthoritativeStatus}
	default:
		return &httpStatusError{statusCode: statusCode, body: string(body)}
	}
}

// PostTerminalResult performs exactly one HTTP attempt with the exact body
// supplied by the lifecycle terminal publisher. Generic bounded retries remain
// in doJSON for non-terminal operations.
func (c *Client) PostTerminalResult(ctx context.Context, intentID string, body []byte) (TerminalHTTPResponse, error) {
	path := "/api/v1/co-signer/intents/dkg/" + url.PathEscape(intentID) + "/result"
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

func intentTypePath(intentType string) (string, error) {
	switch strings.ToUpper(strings.TrimSpace(intentType)) {
	case "DKG":
		return "dkg", nil
	case "SIGN":
		return "sign", nil
	default:
		return "", errors.New("unsupported intent type")
	}
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
