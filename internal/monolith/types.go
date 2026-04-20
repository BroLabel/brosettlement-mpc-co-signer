package monolith

import "time"

type Intent struct {
	IntentID  string        `json:"intentId"`
	SessionID string        `json:"sessionId"`
	Type      string        `json:"type"`
	ExpiresAt time.Time     `json:"expiresAt"`
	Payload   IntentPayload `json:"payload"`
}

type IntentPayload struct {
	KeyID     string   `json:"keyId"`
	Parties   []string `json:"parties"`
	Threshold uint32   `json:"threshold"`
	Algorithm string   `json:"algorithm"`
	Curve     string   `json:"curve"`
	Chain     string   `json:"chain"`
	Digest    []byte   `json:"digest"`
}

type OutboundFrame struct {
	MessageID   string `json:"messageId"`
	ProtocolSeq uint64 `json:"protocolSeq"`
	Round       uint32 `json:"round"`
	ToPartyID   string `json:"toPartyId"`
	Payload     []byte `json:"payload"`
}

type InboundMessage struct {
	DeliverySeq uint64 `json:"deliverySeq"`
	ProtocolSeq uint64 `json:"protocolSeq"`
	MessageID   string `json:"messageId"`
	Round       uint32 `json:"round"`
	FromPartyID string `json:"fromPartyId"`
	ToPartyID   string `json:"toPartyId"`
	Payload     []byte `json:"payload"`
}

type ClaimResult struct {
	ExpiresAt time.Time `json:"expiresAt"`
}

type IntentResult struct {
	Status       string `json:"status"`
	ErrorCode    string `json:"errorCode,omitempty"`
	ErrorMessage string `json:"errorMessage,omitempty"`
}
