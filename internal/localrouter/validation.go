package localrouter

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/BroLabel/brosettlement-mpc-core/protocol"
)

var (
	ErrInvalidFrame     = errors.New("invalid routed frame")
	ErrDuplicateFrame   = errors.New("duplicate routed frame")
	ErrFrameConflict    = errors.New("conflicting routed frame")
	ErrUnsupportedParty = errors.New("party is not registered with local router")
	ErrRouterClosed     = errors.New("local router closed")
)

type frameRecord struct {
	digest [32]byte
}

type sequenceRecord struct {
	messageID string
	digest    [32]byte
}

type frameValidator struct {
	config        Config
	seen          map[string]frameRecord
	maxSequence   map[string]uint64
	sequenceOwner map[string]map[uint64]sequenceRecord
}

func newFrameValidator(config Config) *frameValidator {
	return &frameValidator{
		config:        config,
		seen:          make(map[string]frameRecord),
		maxSequence:   make(map[string]uint64),
		sequenceOwner: make(map[string]map[uint64]sequenceRecord),
	}
}

func (v *frameValidator) validateAndRecord(frame protocol.Frame, authenticatedSender string) error {
	if frame.SessionID != v.config.SessionID ||
		frame.Stage != v.config.Stage ||
		!strings.EqualFold(frame.Protocol, v.config.Protocol) ||
		frame.FromParty != authenticatedSender ||
		frame.MessageID == "" ||
		frame.Seq == 0 ||
		(frame.Round == 0 && frame.RoundHint == 0) ||
		len(frame.Payload) == 0 {
		return ErrInvalidFrame
	}
	if frame.Round != 0 && frame.RoundHint != 0 && frame.Round != frame.RoundHint {
		return ErrInvalidFrame
	}
	if frame.PayloadHash != "" && frame.PayloadHash != payloadHash(frame.Payload) {
		return ErrInvalidFrame
	}
	if err := v.validateRecipient(frame); err != nil {
		return err
	}

	owners := v.sequenceOwner[frame.FromParty]
	if owners == nil {
		owners = make(map[uint64]sequenceRecord)
		v.sequenceOwner[frame.FromParty] = owners
	}
	if frame.Seq < v.maxSequence[frame.FromParty] {
		return ErrInvalidFrame
	}
	sequenceDigest := frameSequenceDigest(frame)
	if owner, exists := owners[frame.Seq]; exists &&
		(owner.messageID != frame.MessageID || owner.digest != sequenceDigest) {
		return ErrFrameConflict
	}

	key := frameKey(frame)
	digest := frameDigest(frame)
	if previous, exists := v.seen[key]; exists {
		if previous.digest != digest {
			return ErrFrameConflict
		}
		return ErrDuplicateFrame
	}

	v.seen[key] = frameRecord{digest: digest}
	owners[frame.Seq] = sequenceRecord{messageID: frame.MessageID, digest: sequenceDigest}
	if frame.Seq > v.maxSequence[frame.FromParty] {
		v.maxSequence[frame.FromParty] = frame.Seq
	}
	return nil
}

func (v *frameValidator) validateRecipient(frame protocol.Frame) error {
	if frame.Broadcast {
		if frame.ToParty != "" {
			return ErrInvalidFrame
		}
		return nil
	}
	if frame.ToParty == "" || frame.ToParty == frame.FromParty {
		return ErrInvalidFrame
	}
	if frame.ToParty != v.config.PlatformPartyID &&
		frame.ToParty != v.config.PrimaryPartyID &&
		frame.ToParty != v.config.RecoveryPartyID {
		return ErrInvalidFrame
	}
	return nil
}

func payloadHash(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:8])
}

func frameKey(frame protocol.Frame) string {
	recipient := frame.ToParty
	if frame.Broadcast {
		recipient = "*"
	}
	return strings.Join([]string{
		frame.SessionID,
		frame.FromParty,
		recipient,
		frame.MessageID,
		strconv.FormatUint(frame.Seq, 10),
	}, "|")
}

func frameDigest(frame protocol.Frame) [32]byte {
	return sha256.Sum256([]byte(fmt.Sprintf(
		"%s\x00%s\x00%s\x00%d\x00%d\x00%d\x00%t\x00%s\x00%s\x00%s\x00%s\x00%s\x00%x",
		frame.SessionID,
		frame.Stage,
		frame.Protocol,
		frame.Seq,
		frame.Round,
		frame.RoundHint,
		frame.Broadcast,
		frame.FromParty,
		frame.ToParty,
		frame.MessageType,
		frame.PayloadHash,
		frame.DerivationContextHash,
		frame.Payload,
	)))
}

func frameSequenceDigest(frame protocol.Frame) [32]byte {
	return sha256.Sum256([]byte(fmt.Sprintf(
		"%s\x00%s\x00%s\x00%d\x00%d\x00%d\x00%t\x00%s\x00%s\x00%s\x00%s\x00%x",
		frame.SessionID,
		frame.Stage,
		frame.Protocol,
		frame.Seq,
		frame.Round,
		frame.RoundHint,
		frame.Broadcast,
		frame.FromParty,
		frame.MessageType,
		frame.PayloadHash,
		frame.DerivationContextHash,
		frame.Payload,
	)))
}
