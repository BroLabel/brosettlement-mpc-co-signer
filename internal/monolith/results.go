package monolith

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/strictjson"
	"io"
	"net/http"
	"net/url"
	"time"
)

func (c *Client) PostResult(ctx context.Context, intentID string, result IntentResult) error {
	request, err := NewSignResultRequest(result)
	if err != nil {
		return err
	}
	return c.PostSignResult(ctx, intentID, request)
}

// SignResultRequest retains the exact serialized result for its live owner.
// Strings and private fields prevent mutation between delivery attempts.
type SignResultRequest struct{ body, status string }

func NewSignResultRequest(result IntentResult) (SignResultRequest, error) {
	body, err := marshalSignResult(result)
	return SignResultRequest{body: string(body), status: result.Status}, err
}

func (r SignResultRequest) MarshalJSON() ([]byte, error) { return []byte(r.body), nil }

func (c *Client) PostSignResult(ctx context.Context, intentID string, result SignResultRequest) error {
	ctx, cancel := context.WithTimeout(ctx, 400*time.Millisecond)
	defer cancel()
	path := "/api/v1/co-signer/intents/sign/" + url.PathEscape(intentID) + "/result"
	body, err := result.MarshalJSON()
	if err != nil {
		return err
	}
	req, err := c.newRequest(ctx, http.MethodPost, path, body)
	if err != nil {
		return err
	}
	req.Header.Set("X-Idempotency-Key", intentID)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	responseBody, readErr := io.ReadAll(io.LimitReader(resp.Body, maxTerminalResponseBytes+1))
	_ = resp.Body.Close()
	if readErr != nil {
		return readErr
	}
	if len(responseBody) > maxTerminalResponseBytes {
		return errors.New("SIGN result response is too large")
	}
	if resp.StatusCode >= http.StatusInternalServerError {
		return &httpStatusError{statusCode: resp.StatusCode, body: string(responseBody)}
	}
	return parseSignResultOutcome(resp.StatusCode, responseBody, result.status)
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
	if err := strictjson.DecodeClosed(body, &outcome); err != nil {
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
