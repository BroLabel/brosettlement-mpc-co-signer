package monolith

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

func (c *Client) GetPendingIntents(ctx context.Context) ([]Intent, error) {
	listing, err := c.ListActionableIntents(ctx)
	if err != nil {
		return nil, err
	}
	intents := make([]Intent, 0, len(listing.OwnClaimedSign)+len(listing.Pending))
	appendIntent := func(item ActionableIntent) {
		intents = append(intents, Intent{
			Session:         item.Session,
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
	Session               SessionLifecycle `json:"session"`
	CreatedAt             string           `json:"createdAt"`
	Deadline              string           `json:"deadline,omitempty"`
	DescriptorBytes       string           `json:"descriptorBytesBase64,omitempty"`
	DescriptorFingerprint string           `json:"descriptorFingerprint,omitempty"`
	IntentID              string           `json:"intentId"`
	KeyID                 string           `json:"keyId"`
	OrgID                 string           `json:"orgId"`
	SessionID             string           `json:"sessionId,omitempty"`
	Status                string           `json:"status"`
	Type                  string           `json:"type"`
	fields                map[string]struct{}
}

func (wire *actionableIntentWire) UnmarshalJSON(raw []byte) error {
	type plain actionableIntentWire
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	allowed := map[string]struct{}{
		"session":   {},
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
	expected = append(expected, "session")
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

	listing := ActionableListing{HTTPStatus: wire.HTTPStatus}
	var err error
	listing.OwnClaimedDKG, err = decodeActionableCollection(*wire.OwnClaimedDKG, "own claimed DKG")
	if err != nil {
		return ActionableListing{}, err
	}
	listing.OwnClaimedSign, err = decodeActionableCollection(*wire.OwnClaimedSign, "own claimed SIGN")
	if err != nil {
		return ActionableListing{}, err
	}
	listing.Pending, err = decodeActionableCollection(*wire.Pending, "pending")
	if err != nil {
		return ActionableListing{}, err
	}
	return listing, nil
}

func decodeActionableCollection(items []actionableIntentWire, collection string) ([]ActionableIntent, error) {
	decoded := make([]ActionableIntent, 0, len(items))
	for _, item := range items {
		intent, err := decodeActionableIntent(item, collection)
		if err != nil {
			return nil, fmt.Errorf("decode %s listing item: %w", collection, err)
		}
		decoded = append(decoded, intent)
	}
	return decoded, nil
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
		Session:               wire.Session,
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
	if err := wire.Session.Validate(wire.Type, item.SessionID, item.Deadline); err != nil {
		return ActionableIntent{}, err
	}
	if item.SessionID == "" {
		item.SessionID = wire.Session.SessionID
	}
	if item.Deadline.IsZero() {
		item.Deadline = wire.Session.Deadline
		item.DeadlineRaw = item.Deadline.Format(time.RFC3339Nano)
	}
	return item, nil
}
