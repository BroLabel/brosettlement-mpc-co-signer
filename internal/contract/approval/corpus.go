package approval

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
)

type Vector struct {
	ID               string          `json:"id"`
	Kind             string          `json:"kind"`
	InputBase64      string          `json:"inputBase64"`
	InputSHA256      string          `json:"inputSHA256"`
	Expected         string          `json:"expected"`
	Binding          json.RawMessage `json:"binding"`
	ComparisonBase64 *string         `json:"comparisonBase64"`
}

// VerifyCorpus checks every fixture's integrity and executes consumer contracts.
// Cursor MAC/position semantics are backend-only and tested by its producer.
func VerifyCorpus(raw []byte) error {
	v, e := parse(raw, 2*1024*1024, true)
	if e != nil {
		return e
	}
	if !closed(v, "schemaVersion,syntheticSeedHex,publicKeyBase64,cursorSecretBase64,vectors") || integer(v, "schemaVersion") != 1 {
		return invalid()
	}
	public, e := standardBase64(str(v, "publicKeyBase64"), 32)
	if e != nil {
		return e
	}
	if _, e := standardBase64(str(v, "cursorSecretBase64"), 32); e != nil {
		return e
	}
	// The deliberately public deterministic seed is an explicit provenance aid.
	seedRaw, e := hexSeed(str(v, "syntheticSeedHex"))
	if e != nil {
		return e
	}
	key := ed25519.NewKeyFromSeed(seedRaw)
	if !bytesEqual(key[32:], public) {
		return invalid()
	}
	var vectors []json.RawMessage
	if json.Unmarshal(v["vectors"], &vectors) != nil || len(vectors) < 40 {
		return invalid()
	}
	seen := map[string]bool{}
	for _, rawVector := range vectors {
		fields, e := parse(rawVector, 131072, false)
		if e != nil {
			return e
		}
		if !closed(fields, "id,kind,inputBase64,inputSHA256,expected,binding,comparisonBase64") {
			return invalid()
		}
		var vector Vector
		if json.Unmarshal(rawVector, &vector) != nil {
			return invalid()
		}
		if seen[vector.ID] || vector.ID == "" || vector.Expected != "ACCEPT" && vector.Expected != "REJECT" {
			return invalid()
		}
		seen[vector.ID] = true
		input, e := standardBase64(vector.InputBase64, -1)
		if e != nil || hash(input) != vector.InputSHA256 {
			return fmt.Errorf("%s input integrity", vector.ID)
		}
		if vector.Kind == "cursor" {
			continue
		}
		err := VerifyVector(vector, public)
		if (err == nil) != (vector.Expected == "ACCEPT") {
			return fmt.Errorf("%s expected %s: %v", vector.ID, vector.Expected, err)
		}
	}
	for _, id := range []string{"APPROVAL-V1-001", "APPROVAL-V1-002", "APPROVAL-V1-003", "APPROVAL-V1-004", "APPROVAL-V1-005", "APPROVAL-V1-006", "APPROVAL-V1-007", "APPROVAL-V1-010", "APPROVAL-V1-010-empty-page", "APPROVAL-V1-010-end-page"} {
		if !seen[id] {
			return fmt.Errorf("missing required vector %s", id)
		}
	}
	return nil
}
func VerifyVector(v Vector, public ed25519.PublicKey) error {
	raw, e := base64.StdEncoding.DecodeString(v.InputBase64)
	if e != nil {
		return e
	}
	var binding Object
	_ = json.Unmarshal(v.Binding, &binding)
	switch v.Kind {
	case "authorization":
		_, e = ParseAuthorization(raw)
	case "receipt":
		var eligible bool
		_ = json.Unmarshal(binding["eligible"], &eligible)
		_, e = VerifyReceipt(raw, TrustedReceipt{str(binding, "orgId"), str(binding, "requestId"), str(binding, "authorizationHash"), str(binding, "signingKeyId"), public, eligible})
	case "organization":
		_, e = ParseOrganizationResponse(raw, str(binding, "orgId"), str(binding, "requestId"), str(binding, "authorizationHash"))
	case "application":
		_, e = ParseDecisionApplication(raw)
	case "replay":
		if v.ComparisonBase64 == nil {
			return invalid()
		}
		other, err := standardBase64(*v.ComparisonBase64, -1)
		if err != nil {
			return err
		}
		same, err := SameReceipt(raw, other)
		if err != nil {
			return err
		}
		if !same {
			return invalid()
		}
	case "page":
		e = ParsePendingPage(raw)
	default:
		e = fmt.Errorf("unknown vector kind %s", v.Kind)
	}
	return e
}
