package monolith

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/contract/mpc2of3"
)

type Intent struct {
	CreatedAt       time.Time
	DeadlineRaw     string
	DiscoveryStatus string
	IntentID        string        `json:"intentId"`
	SessionID       string        `json:"sessionId"`
	Type            string        `json:"type"`
	ExpiresAt       time.Time     `json:"expiresAt"`
	Payload         IntentPayload `json:"payload"`
}

// ActionableIntent is one exact backend-addressed listing item. DeadlineRaw is
// retained so reconciliation can prove that claim and restart do not replace
// the backend-created absolute deadline with a fresh TTL.
type ActionableIntent struct {
	CreatedAt             time.Time
	CreatedAtRaw          string
	Deadline              time.Time
	DeadlineRaw           string
	DescriptorBytes       []byte
	DescriptorFingerprint string
	IntentID              string
	KeyID                 string
	OrgID                 string
	SessionID             string
	Status                string
	Type                  string
}

type ActionableListing struct {
	HTTPStatus     int
	OwnClaimedDKG  []ActionableIntent
	OwnClaimedSign []ActionableIntent
	Pending        []ActionableIntent
}

type IntentPayload struct {
	Type                  string             `json:"type,omitempty"`
	OrgID                 string             `json:"orgId"`
	WalletID              string             `json:"walletId,omitempty"`
	KeyID                 string             `json:"keyId"`
	ProfileID             string             `json:"profileId,omitempty"`
	ProfileVersion        uint32             `json:"profileVersion,omitempty"`
	ProfileTemplateID     string             `json:"profileTemplateId,omitempty"`
	Parties               []string           `json:"parties"`
	Threshold             uint32             `json:"threshold"`
	Algorithm             string             `json:"algorithm"`
	Curve                 string             `json:"curve"`
	Chain                 string             `json:"chain,omitempty"`
	Digest                []byte             `json:"digest"`
	DigestType            string             `json:"digestType,omitempty"`
	HashAlgorithm         string             `json:"hashAlgorithm,omitempty"`
	SigningPayloadType    string             `json:"signingPayloadType,omitempty"`
	ChainCode             string             `json:"chainCode,omitempty"`
	ChainCodeHash         string             `json:"chainCodeHash,omitempty"`
	DerivationScheme      string             `json:"derivationScheme,omitempty"`
	DescriptorBytes       []byte             `json:"descriptorBytesBase64,omitempty"`
	DescriptorFingerprint string             `json:"descriptorFingerprint,omitempty"`
	DerivationContextHash string             `json:"derivationContextHash,omitempty"`
	PartyID               string             `json:"partyId,omitempty"`
	DerivationContext     *DerivationContext `json:"derivationContext,omitempty"`
	PolicyContext         *SignPolicyContext `json:"policyContext,omitempty"`
}

// SignPolicyContext is the immutable transaction-policy snapshot authorized by
// the backend for one SIGN intent. It is retained by the co-signer so claim
// validation can bind the cryptographic request to the authorized chain and
// source address.
type SignPolicyContext struct {
	AmountAtomic           string  `json:"amountAtomic"`
	Asset                  string  `json:"asset"`
	Chain                  string  `json:"chain"`
	FeeLimitSun            *string `json:"feeLimitSun"`
	FromAddress            string  `json:"fromAddress"`
	ToAddress              string  `json:"toAddress"`
	TokenContractCanonical *string `json:"tokenContractCanonical"`
	TokenDecimals          *int64  `json:"tokenDecimals"`
	TokenStandard          *string `json:"tokenStandard"`
}

func (c *SignPolicyContext) UnmarshalJSON(raw []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	expected := []string{
		"amountAtomic", "asset", "chain", "feeLimitSun", "fromAddress", "toAddress",
		"tokenContractCanonical", "tokenDecimals", "tokenStandard",
	}
	if len(fields) != len(expected) {
		return errors.New("SIGN policy context has invalid fields")
	}
	for _, name := range expected {
		if _, ok := fields[name]; !ok {
			return errors.New("SIGN policy context has invalid fields")
		}
	}
	type wire SignPolicyContext
	var decoded wire
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	*c = SignPolicyContext(decoded)
	return nil
}

type OutboundFrame struct {
	AuthenticatedPartyID  string `json:"authenticatedPartyId"`
	Broadcast             bool   `json:"broadcast"`
	FromPartyID           string `json:"fromPartyId"`
	IntentID              string `json:"intentId"`
	MessageID             string `json:"messageId"`
	OrgID                 string `json:"orgId"`
	Payload               []byte `json:"payload"`
	ProtocolSeq           uint64 `json:"protocolSeq"`
	Round                 uint32 `json:"round"`
	SessionID             string `json:"sessionId"`
	ToPartyID             string `json:"toPartyId"`
	DerivationContextHash string `json:"-"`
}

type InboundMessage struct {
	DeliverySeq           uint64 `json:"deliverySeq"`
	ProtocolSeq           uint64 `json:"protocolSeq"`
	MessageID             string `json:"messageId"`
	Round                 uint32 `json:"round"`
	FromPartyID           string `json:"fromPartyId"`
	ToPartyID             string `json:"toPartyId"`
	Broadcast             bool   `json:"broadcast"`
	Payload               []byte `json:"payload"`
	DerivationContextHash string `json:"derivationContextHash,omitempty"`
}

type ClaimResult struct {
	HTTPStatus            int           `json:"httpStatus,omitempty"`
	IntentID              string        `json:"intentId,omitempty"`
	SessionID             string        `json:"sessionId,omitempty"`
	Type                  string        `json:"type,omitempty"`
	Payload               IntentPayload `json:"payload,omitempty"`
	Status                string        `json:"status,omitempty"`
	ClaimedBy             string        `json:"claimedBy,omitempty"`
	ClaimedAt             *time.Time    `json:"claimedAt,omitempty"`
	ExpiresAt             time.Time     `json:"expiresAt"`
	Deadline              time.Time     `json:"deadline"`
	DeadlineRaw           string        `json:"-"`
	OrgID                 string        `json:"orgId,omitempty"`
	KeyID                 string        `json:"keyId,omitempty"`
	DescriptorBytes       []byte        `json:"descriptorBytesBase64,omitempty"`
	DescriptorFingerprint string        `json:"descriptorFingerprint,omitempty"`
	ChainCode             []byte        `json:"chainCodeBase64,omitempty"`
}

func (r *ClaimResult) retainExactResponseFields(raw []byte) error {
	var exact struct {
		Deadline string `json:"deadline"`
	}
	if err := json.Unmarshal(raw, &exact); err != nil {
		return err
	}
	r.DeadlineRaw = exact.Deadline
	return nil
}

func (r ClaimResult) DeadlineTime() time.Time {
	if !r.Deadline.IsZero() {
		return r.Deadline
	}
	return r.ExpiresAt
}

func (r ClaimResult) Intent() Intent {
	payload := r.Payload
	intentType := r.Type
	if len(r.DescriptorBytes) > 0 {
		if intentType == "" {
			intentType = "DKG"
		}
		payload.Type = intentType
		payload.OrgID = r.OrgID
		payload.KeyID = r.KeyID
		payload.DescriptorBytes = append([]byte(nil), r.DescriptorBytes...)
		payload.DescriptorFingerprint = r.DescriptorFingerprint
		payload.ChainCode = hex.EncodeToString(r.ChainCode)
		if descriptor, _, err := mpc2of3.ParseCanonicalDescriptor(r.DescriptorBytes); err == nil {
			payload.Threshold = uint32(descriptor.Threshold)
			payload.Algorithm = descriptor.Algorithm
			payload.Curve = descriptor.Curve
			payload.ChainCodeHash = descriptor.ChainCodeHash
			payload.DerivationScheme = descriptor.DerivationScheme
			payload.Parties = make([]string, 0, len(descriptor.Parties))
			for _, party := range descriptor.Parties {
				payload.Parties = append(payload.Parties, party.PartyID)
			}
		}
	}
	return Intent{
		IntentID:  r.IntentID,
		SessionID: r.SessionID,
		Type:      intentType,
		ExpiresAt: r.DeadlineTime(),
		Payload:   payload,
	}
}

type IntentResult struct {
	Status       string `json:"status"`
	ErrorCode    string `json:"errorCode,omitempty"`
	ErrorMessage string `json:"errorMessage,omitempty"`
}

// TerminalHTTPResponse is the raw result of exactly one DKG terminal HTTP
// attempt. The terminal publisher owns strict response decoding and retries.
type TerminalHTTPResponse struct {
	StatusCode        int
	Body              []byte
	ProtocolViolation string
}

type DerivationContext struct {
	ProfileID         string `json:"profileId"`
	ProfileTemplateID string `json:"profileTemplateId,omitempty"`
	Chain             string `json:"chain"`
	Algorithm         string `json:"algorithm"`
	Curve             string `json:"curve"`
	Scheme            string `json:"scheme"`
	AccountPath       string `json:"accountPath"`
	ChildPath         string `json:"childPath"`
	FullPath          string `json:"fullPath"`
	AddressEncoding   string `json:"addressEncoding,omitempty"`
	ExpectedAddress   string `json:"expectedAddress,omitempty"`
	ExpectedPublicKey string `json:"expectedPublicKey,omitempty"`
	PublicKeyFormat   string `json:"publicKeyFormat,omitempty"`
	DescriptorVersion uint32 `json:"descriptorVersion,omitempty"`
	ProfileVersion    uint32 `json:"profileVersion,omitempty"`
	KeyVersion        uint32 `json:"keyVersion,omitempty"`
}
