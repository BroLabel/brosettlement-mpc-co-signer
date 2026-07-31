package mpc2of3

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const canonicalDescriptor = `{"algorithm":"ECDSA","chainCodeHash":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","curve":"secp256k1","derivationScheme":"bip32_secp256k1","descriptorKind":"mpc-key-descriptor","descriptorVersion":1,"keyId":"mpc_key_123e4567-e89b-42d3-a456-426614174000","parties":[{"partyId":"mpc-signer","purpose":"platform"},{"partyId":"co-signer-primary","purpose":"primary"},{"partyId":"co-signer-recovery","purpose":"recovery"}],"protocolVersion":1,"publicKeyFormat":"compressed_sec1","threshold":2}`

func TestSharedCorpusParsesExactCanonicalVectors(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	descriptorBytes, err := os.ReadFile(filepath.Join(root, "contracts", "mpc-2of3", "v1", "descriptor", "canonical.json"))
	if err != nil {
		t.Fatal(err)
	}
	descriptor, fingerprint, err := ParseCanonicalDescriptor(descriptorBytes)
	if err != nil {
		t.Fatalf("ParseCanonicalDescriptor() error = %v", err)
	}
	if descriptor.KeyID != "mpc_key_123e4567-e89b-42d3-a456-426614174000" || fingerprint.String() != "EGwPFc4mUrwf-ePdP2Xehoh_rJODado_hZUsiBR2FGY" {
		t.Fatalf("descriptor = %#v, fingerprint = %q", descriptor, fingerprint)
	}

	for name, wantStatus := range map[string]TerminalStatus{
		"completed.json": TerminalStatusCompleted,
		"failed.json":    TerminalStatusFailed,
		"timed-out.json": TerminalStatusTimedOut,
	} {
		raw, err := os.ReadFile(filepath.Join(root, "contracts", "mpc-2of3", "v1", "terminal-result", name))
		if err != nil {
			t.Fatal(err)
		}
		terminal, _, err := ParseCanonicalTerminalResult(raw)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if terminal.Status != wantStatus {
			t.Fatalf("%s status = %q, want %q", name, terminal.Status, wantStatus)
		}
	}
}

func TestStrictCodecsRejectNonCanonicalAndInvalidValues(t *testing.T) {
	for name, raw := range map[string]string{
		"reordered descriptor keys": `{"descriptorVersion":1,"algorithm":"ECDSA"}`,
		"duplicate descriptor key":  `{"algorithm":"ECDSA","algorithm":"ECDSA"}`,
		"unknown descriptor key":    canonicalDescriptor[:len(canonicalDescriptor)-1] + `,"unexpected":true}`,
		"reordered parties": strings.NewReplacer(
			`[{"partyId":"mpc-signer","purpose":"platform"},{"partyId":"co-signer-primary","purpose":"primary"}`,
			`[{"partyId":"co-signer-primary","purpose":"primary"},{"partyId":"mpc-signer","purpose":"platform"}`,
		).Replace(canonicalDescriptor),
		"non ascii party":     strings.NewReplacer("mpc-signer", "mpc-signér").Replace(canonicalDescriptor),
		"noncanonical number": strings.NewReplacer(`"threshold":2`, `"threshold":2.0`).Replace(canonicalDescriptor),
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := ParseCanonicalDescriptor([]byte(raw)); err == nil {
				t.Fatal("ParseCanonicalDescriptor() error = nil")
			}
		})
	}

	for _, raw := range []string{
		"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
		"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA+",
		"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"*******************************************",
	} {
		if _, err := ParseDescriptorFingerprint(raw); err == nil {
			t.Fatalf("ParseDescriptorFingerprint(%q) error = nil", raw)
		}
	}

	for name, raw := range map[string]string{
		"failed result null":       `{"intentId":"intent-123","keyId":"mpc_key_123e4567-e89b-42d3-a456-426614174000","result":null,"resultKind":"mpc-dkg-terminal-result","resultVersion":1,"sessionId":"dkg-123","status":"FAILED"}`,
		"timed out result null":    `{"intentId":"intent-123","keyId":"mpc_key_123e4567-e89b-42d3-a456-426614174000","result":null,"resultKind":"mpc-dkg-terminal-result","resultVersion":1,"sessionId":"dkg-123","status":"TIMED_OUT"}`,
		"completed missing result": `{"intentId":"intent-123","keyId":"mpc_key_123e4567-e89b-42d3-a456-426614174000","resultKind":"mpc-dkg-terminal-result","resultVersion":1,"sessionId":"dkg-123","status":"COMPLETED"}`,
		"failed unknown key":       `{"intentId":"intent-123","keyId":"mpc_key_123e4567-e89b-42d3-a456-426614174000","resultKind":"mpc-dkg-terminal-result","resultVersion":1,"sessionId":"dkg-123","status":"FAILED","diagnostic":"secret"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := ParseCanonicalTerminalResult([]byte(raw)); err == nil {
				t.Fatal("ParseCanonicalTerminalResult() error = nil")
			}
		})
	}
}

func TestCoSignerCannotCreateTimedOutTerminal(t *testing.T) {
	_, err := CanonicalTerminalResultBytes(TerminalResultV1{
		IntentID: "intent-123", KeyID: "mpc_key_123e4567-e89b-42d3-a456-426614174000",
		ResultKind: "mpc-dkg-terminal-result", ResultVersion: 1, SessionID: "dkg-123", Status: TerminalStatusTimedOut,
	})
	if err == nil {
		t.Fatal("CanonicalTerminalResultBytes(TIMED_OUT) error = nil")
	}
}

func TestCanonicalTerminalResultBytesMatchesMinimalProducerVectors(t *testing.T) {
	root := filepath.Join("..", "..", "..", "contracts", "mpc-2of3", "v1", "terminal-result")
	for _, name := range []string{"completed.json", "failed.json"} {
		raw, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		terminal, _, err := ParseCanonicalTerminalResult(raw)
		if err != nil {
			t.Fatal(err)
		}
		got, err := CanonicalTerminalResultBytes(terminal)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if string(got) != string(raw) {
			t.Fatalf("%s bytes = %s, want %s", name, got, raw)
		}
	}
}

func TestSharedDigestAndJCSVectorsAreExact(t *testing.T) {
	root := filepath.Join("..", "..", "..", "contracts", "mpc-2of3", "v1", "digest-jcs")
	jcsVector, err := os.ReadFile(filepath.Join(root, "jcs.json"))
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := canonicalJCS(jcsVector)
	if err != nil {
		t.Fatal(err)
	}
	if string(canonical) != `{"a":1,"b":[true,"x"]}` {
		t.Fatalf("canonical JCS = %s", canonical)
	}
	if got := DescriptorFingerprintFor([]byte("descriptor")).String(); got != "GUtSDcMDhLP8Iz4SN3iDXircNi2RxuMwFe09sjedfqE" {
		t.Fatalf("descriptor digest = %q", got)
	}
}
