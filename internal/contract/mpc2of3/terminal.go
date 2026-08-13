package mpc2of3

import (
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/strictjson"
	"github.com/btcsuite/btcd/btcec"
)

const (
	maxCanonicalTerminalResultBytesV1 = 2048
	maxMPCIdentifierLengthV1          = 255
)

type TerminalStatus string

const (
	TerminalStatusCompleted TerminalStatus = "COMPLETED"
	TerminalStatusFailed    TerminalStatus = "FAILED"
	TerminalStatusTimedOut  TerminalStatus = "TIMED_OUT"
)

type ArtifactResultV1 struct {
	ArtifactFingerprint string `json:"artifactFingerprint"`
	PartyID             string `json:"partyId"`
	Purpose             string `json:"purpose"`
}

type CompletedResultV1 struct {
	AccountPublicKey      string             `json:"accountPublicKey"`
	Artifacts             []ArtifactResultV1 `json:"artifacts"`
	ChainCodeHash         string             `json:"chainCodeHash"`
	DescriptorFingerprint string             `json:"descriptorFingerprint"`
}

type TerminalResultV1 struct {
	IntentID      string             `json:"intentId"`
	KeyID         string             `json:"keyId"`
	Result        *CompletedResultV1 `json:"result,omitempty"`
	ResultKind    string             `json:"resultKind"`
	ResultVersion int                `json:"resultVersion"`
	SessionID     string             `json:"sessionId"`
	Status        TerminalStatus     `json:"status"`
}

func ParseCanonicalTerminalResult(raw []byte) (TerminalResultV1, TerminalResultFingerprint, error) {
	var result TerminalResultV1
	if len(raw) > maxCanonicalTerminalResultBytesV1 || !printableASCII(raw) {
		return result, TerminalResultFingerprint{}, fmt.Errorf("terminal result requires nonempty bounded ASCII bytes")
	}
	if err := requireCanonicalJCS(raw); err != nil {
		return result, TerminalResultFingerprint{}, err
	}
	if err := strictjson.DecodeClosed(raw, &result); err != nil {
		return result, TerminalResultFingerprint{}, fmt.Errorf("decode terminal result: %w", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return result, TerminalResultFingerprint{}, fmt.Errorf("decode terminal result fields: %w", err)
	}
	if err := result.validate(fields["result"] != nil); err != nil {
		return result, TerminalResultFingerprint{}, err
	}
	return result, TerminalResultFingerprintFor(raw), nil
}

// CanonicalTerminalResultBytes only originates the co-signer terminal states.
// TIMED_OUT is backend-created and remains parse-only for this process.
func CanonicalTerminalResultBytes(result TerminalResultV1) ([]byte, error) {
	if result.Status == TerminalStatusTimedOut {
		return nil, fmt.Errorf("co-signer cannot originate TIMED_OUT terminal results")
	}
	if err := result.validate(result.Result != nil); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("marshal terminal result: %w", err)
	}
	canonical, err := canonicalJCS(raw)
	if err != nil {
		return nil, err
	}
	return canonical, nil
}

func (r TerminalResultV1) validate(hasResult bool) error {
	if r.ResultKind != "mpc-dkg-terminal-result" || r.ResultVersion != 1 || !boundedASCII(r.IntentID) || !boundedASCII(r.SessionID) || !keyIDPattern.MatchString(r.KeyID) {
		return fmt.Errorf("invalid terminal result identity")
	}
	switch r.Status {
	case TerminalStatusFailed, TerminalStatusTimedOut:
		if hasResult || r.Result != nil {
			return fmt.Errorf("%s result must be minimal", r.Status)
		}
		return nil
	case TerminalStatusCompleted:
	default:
		return fmt.Errorf("invalid terminal status")
	}
	if !hasResult || r.Result == nil {
		return fmt.Errorf("completed result is required")
	}
	if _, err := ParseDescriptorFingerprint(r.Result.DescriptorFingerprint); err != nil {
		return fmt.Errorf("invalid descriptorFingerprint: %w", err)
	}
	if _, err := ParseChainCodeHash(r.Result.ChainCodeHash); err != nil {
		return fmt.Errorf("invalid chainCodeHash: %w", err)
	}
	if len(r.Result.AccountPublicKey) != 66 {
		return fmt.Errorf("invalid compressed public key")
	}
	for _, c := range r.Result.AccountPublicKey {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return fmt.Errorf("invalid compressed public key")
		}
	}
	publicKey, err := hex.DecodeString(r.Result.AccountPublicKey)
	if err != nil || (publicKey[0] != 2 && publicKey[0] != 3) {
		return fmt.Errorf("invalid compressed public key")
	}
	if _, err := btcec.ParsePubKey(publicKey, btcec.S256()); err != nil {
		return fmt.Errorf("invalid compressed public key")
	}
	want := [][2]string{{"co-signer-primary", "primary"}, {"co-signer-recovery", "recovery"}}
	if len(r.Result.Artifacts) != len(want) {
		return fmt.Errorf("completed result must contain two artifacts")
	}
	for i, artifact := range r.Result.Artifacts {
		if artifact.PartyID != want[i][0] || artifact.Purpose != want[i][1] {
			return fmt.Errorf("invalid artifact at index %d", i)
		}
		if _, err := ParseArtifactFingerprint(artifact.ArtifactFingerprint); err != nil {
			return fmt.Errorf("invalid artifact fingerprint: %w", err)
		}
	}
	return nil
}

func boundedASCII(value string) bool { return len(value) <= maxMPCIdentifierLengthV1 && ascii(value) }
