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
	DerivationContextHash string             `json:"derivationContextHash,omitempty"`
	PartyID               string             `json:"partyId,omitempty"`
	DerivationContext     *DerivationContext `json:"derivationContext,omitempty"`
}

type OutboundFrame struct {
	MessageID   string `json:"messageId"`
	ProtocolSeq uint64 `json:"protocolSeq"`
	Round       uint32 `json:"round"`
	ToPartyID   string `json:"toPartyId,omitempty"`
	Broadcast   bool   `json:"broadcast,omitempty"`
	Payload     []byte `json:"payload"`
}

type InboundMessage struct {
	DeliverySeq uint64 `json:"deliverySeq"`
	ProtocolSeq uint64 `json:"protocolSeq"`
	MessageID   string `json:"messageId"`
	Round       uint32 `json:"round"`
	FromPartyID string `json:"fromPartyId"`
	ToPartyID   string `json:"toPartyId"`
	Broadcast   bool   `json:"broadcast"`
	Payload     []byte `json:"payload"`
}

type ClaimResult struct {
	ExpiresAt time.Time `json:"expiresAt"`
}

type IntentResult struct {
	Status       string                `json:"status"`
	ErrorCode    string                `json:"errorCode,omitempty"`
	ErrorMessage string                `json:"errorMessage,omitempty"`
	DkgMaterial  *DkgParticipantResult `json:"dkgMaterial,omitempty"`
}

type DkgParticipantResult struct {
	KeyID            string `json:"keyId"`
	AccountPublicKey string `json:"accountPublicKey"`
	ChainCodeHash    string `json:"chainCodeHash"`
	ChainCodePresent bool   `json:"chainCodePresent"`
	PublicKeyFormat  string `json:"publicKeyFormat"`
	DerivationScheme string `json:"derivationScheme"`
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
