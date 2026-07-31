package terminal

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/contract/mpc2of3"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/monolith"
)

type ArtifactInput struct {
	PartyID     string
	Purpose     string
	Fingerprint mpc2of3.ArtifactFingerprint
}

type CompletedInput struct {
	IntentID              string
	SessionID             string
	KeyID                 string
	DescriptorFingerprint mpc2of3.DescriptorFingerprint
	AccountPublicKey      []byte
	ChainCodeHash         mpc2of3.ChainCodeHash
	Primary               ArtifactInput
	Recovery              ArtifactInput
}

type terminalRequestV1 struct {
	TerminalResult            json.RawMessage `json:"terminalResult"`
	TerminalResultFingerprint string          `json:"terminalResultFingerprint"`
}

type Job struct {
	intentID    string
	body        []byte
	status      mpc2of3.TerminalStatus
	fingerprint mpc2of3.TerminalResultFingerprint
}

func NewCompletedJob(input CompletedInput) (Job, error) {
	return newJob(mpc2of3.TerminalResultV1{
		IntentID:      input.IntentID,
		KeyID:         input.KeyID,
		ResultKind:    "mpc-dkg-terminal-result",
		ResultVersion: 1,
		SessionID:     input.SessionID,
		Status:        mpc2of3.TerminalStatusCompleted,
		Result: &mpc2of3.CompletedResultV1{
			AccountPublicKey: hex.EncodeToString(input.AccountPublicKey),
			Artifacts: []mpc2of3.ArtifactResultV1{
				{
					ArtifactFingerprint: input.Primary.Fingerprint.String(),
					PartyID:             input.Primary.PartyID,
					Purpose:             input.Primary.Purpose,
				},
				{
					ArtifactFingerprint: input.Recovery.Fingerprint.String(),
					PartyID:             input.Recovery.PartyID,
					Purpose:             input.Recovery.Purpose,
				},
			},
			ChainCodeHash:         input.ChainCodeHash.String(),
			DescriptorFingerprint: input.DescriptorFingerprint.String(),
		},
	})
}

func NewFailedJob(intentID, sessionID, keyID string) (Job, error) {
	return newJob(mpc2of3.TerminalResultV1{
		IntentID:      intentID,
		KeyID:         keyID,
		ResultKind:    "mpc-dkg-terminal-result",
		ResultVersion: 1,
		SessionID:     sessionID,
		Status:        mpc2of3.TerminalStatusFailed,
	})
}

func newJob(result mpc2of3.TerminalResultV1) (Job, error) {
	terminalBytes, err := mpc2of3.CanonicalTerminalResultBytes(result)
	if err != nil {
		return Job{}, err
	}
	fingerprint := mpc2of3.TerminalResultFingerprintFor(terminalBytes)
	body, err := json.Marshal(terminalRequestV1{
		TerminalResult:            terminalBytes,
		TerminalResultFingerprint: fingerprint.String(),
	})
	if err != nil {
		return Job{}, fmt.Errorf("marshal terminal request: %w", err)
	}
	return Job{
		intentID:    result.IntentID,
		body:        body,
		status:      result.Status,
		fingerprint: fingerprint,
	}, nil
}

func ParseJob(body []byte) (Job, error) {
	var request terminalRequestV1
	if err := decodeStrict(body, &request); err != nil {
		return Job{}, fmt.Errorf("decode terminal request: %w", err)
	}
	result, fingerprint, err := mpc2of3.ParseCanonicalTerminalResult(request.TerminalResult)
	if err != nil {
		return Job{}, err
	}
	submittedFingerprint, err := mpc2of3.ParseTerminalResultFingerprint(request.TerminalResultFingerprint)
	if err != nil || submittedFingerprint != fingerprint {
		return Job{}, errors.New("terminal request fingerprint mismatch")
	}
	canonical, err := json.Marshal(request)
	if err != nil || !bytes.Equal(canonical, body) {
		return Job{}, errors.New("terminal request is not canonical")
	}
	return Job{
		intentID:    result.IntentID,
		body:        append([]byte(nil), body...),
		status:      result.Status,
		fingerprint: fingerprint,
	}, nil
}

func (j Job) Body() []byte {
	return append([]byte(nil), j.body...)
}

func (j Job) Status() mpc2of3.TerminalStatus {
	return j.status
}

func (j Job) Fingerprint() mpc2of3.TerminalResultFingerprint {
	return j.fingerprint
}

func (j Job) valid() bool {
	return j.intentID != "" && len(j.body) != 0
}

type terminalSender interface {
	PostTerminalResult(context.Context, string, []byte) (monolith.TerminalHTTPResponse, error)
}

type OutcomeKind string

const (
	OutcomeAccepted         OutcomeKind = "ACCEPTED"
	OutcomeExactReplay      OutcomeKind = "EXACT_REPLAY"
	OutcomeTerminalConflict OutcomeKind = "TERMINAL_CONFLICT"
)

type Outcome struct {
	Kind                     OutcomeKind
	AuthoritativeStatus      mpc2of3.TerminalStatus
	AuthoritativeFingerprint mpc2of3.TerminalResultFingerprint
}

type ProtocolAlert struct {
	HTTPStatus int
	Reason     string
}

type ProtocolAlerter interface {
	Alert(ProtocolAlert)
}

type ProtocolAlertFunc func(ProtocolAlert)

func (f ProtocolAlertFunc) Alert(alert ProtocolAlert) {
	if f != nil {
		f(alert)
	}
}

type Publisher struct {
	sender  terminalSender
	policy  RetryPolicy
	sleeper Sleeper
	alerts  ProtocolAlerter
}

func NewPublisher(sender terminalSender, policy RetryPolicy, sleeper Sleeper, alerts ProtocolAlerter) *Publisher {
	if policy == nil {
		policy = DefaultRetryPolicy()
	}
	if sleeper == nil {
		sleeper = contextSleeper{}
	}
	return &Publisher{sender: sender, policy: policy, sleeper: sleeper, alerts: alerts}
}

func (p *Publisher) Publish(ctx context.Context, job Job) (Outcome, error) {
	if p == nil || p.sender == nil || p.policy == nil || p.sleeper == nil {
		return Outcome{}, errors.New("terminal publisher dependencies are required")
	}
	if !job.valid() {
		return Outcome{}, errors.New("terminal publication job is invalid")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	body := append([]byte(nil), job.body...)
	for attempt := uint64(1); ; attempt = nextAttempt(attempt) {
		if err := ctx.Err(); err != nil {
			return Outcome{}, err
		}
		response, err := p.sender.PostTerminalResult(ctx, job.intentID, body)
		if err != nil {
			if ctx.Err() != nil {
				return Outcome{}, ctx.Err()
			}
			if err := p.sleepRetry(ctx, attempt, RetryTransient); err != nil {
				return Outcome{}, err
			}
			continue
		}
		if response.ProtocolViolation != "" {
			p.alert(response.StatusCode, response.ProtocolViolation)
			if err := p.sleepRetry(ctx, attempt, RetryProtocol); err != nil {
				return Outcome{}, err
			}
			continue
		}

		switch {
		case response.StatusCode >= http.StatusInternalServerError:
			if err := p.sleepRetry(ctx, attempt, RetryTransient); err != nil {
				return Outcome{}, err
			}
		case response.StatusCode == http.StatusOK || response.StatusCode == http.StatusConflict:
			outcome, err := validateResponse(response, job)
			if err == nil {
				if outcome.Kind == OutcomeTerminalConflict {
					p.alert(response.StatusCode, "terminal_conflict")
				}
				return outcome, nil
			}
			p.alert(response.StatusCode, "malformed_authoritative_response")
			if err := p.sleepRetry(ctx, attempt, RetryProtocol); err != nil {
				return Outcome{}, err
			}
		default:
			p.alert(response.StatusCode, "unexpected_http_status")
			if err := p.sleepRetry(ctx, attempt, RetryProtocol); err != nil {
				return Outcome{}, err
			}
		}
	}
}

func (p *Publisher) sleepRetry(ctx context.Context, attempt uint64, kind RetryKind) error {
	return p.sleeper.Sleep(ctx, p.policy.NextDelay(attempt, kind))
}

func (p *Publisher) alert(status int, reason string) {
	if p.alerts != nil {
		p.alerts.Alert(ProtocolAlert{HTTPStatus: status, Reason: reason})
	}
}

type terminalResponseV1 struct {
	AuthoritativeResultFingerprint string `json:"authoritativeResultFingerprint"`
	AuthoritativeStatus            string `json:"authoritativeStatus"`
	HTTPStatus                     int    `json:"httpStatus"`
	Outcome                        string `json:"outcome"`
}

func validateResponse(response monolith.TerminalHTTPResponse, job Job) (Outcome, error) {
	var wire terminalResponseV1
	if err := decodeStrict(response.Body, &wire); err != nil {
		return Outcome{}, err
	}
	if wire.HTTPStatus != response.StatusCode {
		return Outcome{}, errors.New("terminal response HTTP status mismatch")
	}
	status := mpc2of3.TerminalStatus(wire.AuthoritativeStatus)
	switch status {
	case mpc2of3.TerminalStatusCompleted, mpc2of3.TerminalStatusFailed, mpc2of3.TerminalStatusTimedOut:
	default:
		return Outcome{}, errors.New("terminal response has invalid authoritative status")
	}
	fingerprint, err := mpc2of3.ParseTerminalResultFingerprint(wire.AuthoritativeResultFingerprint)
	if err != nil {
		return Outcome{}, errors.New("terminal response has invalid authoritative fingerprint")
	}
	outcome := Outcome{
		Kind:                     OutcomeKind(wire.Outcome),
		AuthoritativeStatus:      status,
		AuthoritativeFingerprint: fingerprint,
	}
	switch response.StatusCode {
	case http.StatusOK:
		if outcome.Kind != OutcomeAccepted && outcome.Kind != OutcomeExactReplay {
			return Outcome{}, errors.New("terminal 200 response has invalid outcome")
		}
		if status != job.status || fingerprint != job.fingerprint {
			return Outcome{}, errors.New("terminal 200 response does not confirm submitted result")
		}
	case http.StatusConflict:
		if outcome.Kind != OutcomeTerminalConflict {
			return Outcome{}, errors.New("terminal 409 response has invalid outcome")
		}
	default:
		return Outcome{}, errors.New("terminal response status is not authoritative")
	}
	return outcome, nil
}

func decodeStrict(raw []byte, target any) error {
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
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON object key is not a string")
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("duplicate JSON object key %q", key)
			}
			seen[key] = struct{}{}
			if err := walkJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil {
			return err
		}
		if end != json.Delim('}') {
			return errors.New("JSON object is not closed")
		}
	case '[':
		for decoder.More() {
			if err := walkJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil {
			return err
		}
		if end != json.Delim(']') {
			return errors.New("JSON array is not closed")
		}
	default:
		return errors.New("invalid JSON delimiter")
	}
	return nil
}
