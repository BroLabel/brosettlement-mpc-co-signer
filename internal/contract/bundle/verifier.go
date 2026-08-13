package bundle

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/contract/mpc2of3"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/strictjson"
)

const (
	signerBundleIdentity = "dRgKBw7Y392uHY7AkBj5dkahjKmwYcFYhjtYv62-5mA"
	httpBundleIdentity   = "DfiDHjeT5xYiTN7ICY9XDAjHP9XioZYO4N2EAYaH5WQ"
	backendSourceCommit  = "3932586337691e95ecbe6c6fb09df93babf73784"
)

var (
	signerPaths = []string{
		"descriptor/canonical.json", "digest-jcs/jcs.json", "digest-jcs/sha256.json", "proto.sha256",
		"terminal-result/completed.json", "terminal-result/failed.json", "terminal-result/timed-out.json",
	}
	httpPaths = []string{
		"accepted-response.json", "claim-response.json", "conflict-response.json", "listing-response.json",
		"mailbox-frame.json", "replay-response.json", "sign-claim-response.json", "sign-terminal-completed-request.json",
		"sign-terminal-failed-request.json", "terminal-completed-request.json", "terminal-failed-request.json",
	}
	identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)
	keyIDPattern      = regexp.MustCompile(`^mpc_key_[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	messageIDPattern  = regexp.MustCompile(`^msg_[0-9a-f]{16}$`)
	utcPattern        = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`)
	uuidV4Pattern     = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
)

type manifestFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type signerManifest struct {
	ContractVersion int            `json:"contractVersion"`
	Files           []manifestFile `json:"files"`
}

type httpManifest struct {
	BackendSourceCommit string         `json:"backendSourceCommit"`
	BundleVersion       int            `json:"bundleVersion"`
	Files               []manifestFile `json:"files"`
}

// VerifySignerBundle checks every signer-owned file and its producer-pinned identity.
func VerifySignerBundle(root string) (string, error) {
	manifestBytes, files, err := verifyManifest(root, signerPaths, func(raw []byte) error {
		var manifest signerManifest
		if err := decodeClosed(raw, &manifest, []string{"contractVersion", "files"}); err != nil {
			return err
		}
		if manifest.ContractVersion != 1 {
			return fmt.Errorf("invalid signer manifest schema")
		}
		return verifyManifestEntries(root, manifest.Files, signerPaths)
	})
	if err != nil {
		return "", err
	}
	_ = files
	if identity := digest(manifestBytes); identity != signerBundleIdentity {
		return "", fmt.Errorf("unexpected signer bundle identity %q", identity)
	}
	if err := verifySignerVectors(root); err != nil {
		return "", err
	}
	return signerBundleIdentity, nil
}

// VerifyHTTPBundle checks the backend-owned fixture corpus and all cross-fixture bindings.
func VerifyHTTPBundle(root string) (string, error) {
	manifestBytes, _, err := verifyManifest(root, httpPaths, func(raw []byte) error {
		var manifest httpManifest
		if err := decodeClosed(raw, &manifest, []string{"backendSourceCommit", "bundleVersion", "files"}); err != nil {
			return err
		}
		if manifest.BundleVersion != 1 || manifest.BackendSourceCommit != backendSourceCommit {
			return fmt.Errorf("invalid HTTP manifest schema")
		}
		return verifyManifestEntries(root, manifest.Files, httpPaths)
	})
	if err != nil {
		return "", err
	}
	if identity := digest(manifestBytes); identity != httpBundleIdentity {
		return "", fmt.Errorf("unexpected HTTP bundle identity %q", identity)
	}
	if err := verifyHTTPFixtures(root); err != nil {
		return "", err
	}
	return httpBundleIdentity, nil
}

func verifySignerVectors(root string) error {
	descriptor, err := os.ReadFile(filepath.Join(root, "descriptor/canonical.json"))
	if err != nil {
		return err
	}
	if _, _, err := mpc2of3.ParseCanonicalDescriptor(descriptor); err != nil {
		return fmt.Errorf("descriptor vector: %w", err)
	}
	for _, name := range []string{"completed.json", "failed.json", "timed-out.json"} {
		raw, err := os.ReadFile(filepath.Join(root, "terminal-result", name))
		if err != nil {
			return err
		}
		if _, _, err := mpc2of3.ParseCanonicalTerminalResult(raw); err != nil {
			return fmt.Errorf("terminal vector %s: %w", name, err)
		}
	}
	return nil
}

func verifyManifest(root string, expected []string, validate func([]byte) error) ([]byte, []string, error) {
	manifest, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil {
		return nil, nil, err
	}
	if err := mpc2of3.RequireCanonicalJCS(manifest); err != nil {
		return nil, nil, fmt.Errorf("manifest: %w", err)
	}
	if err := validate(manifest); err != nil {
		return nil, nil, err
	}
	actual, err := bundleFiles(root)
	if err != nil {
		return nil, nil, err
	}
	want := append([]string{"manifest.json"}, expected...)
	sort.Strings(want)
	if !equalStrings(actual, want) {
		return nil, nil, fmt.Errorf("bundle contains missing or unexpected files")
	}
	return manifest, actual, nil
}

func verifyManifestEntries(root string, entries []manifestFile, expected []string) error {
	if len(entries) != len(expected) {
		return fmt.Errorf("manifest has an invalid file count")
	}
	for i, entry := range entries {
		if entry.Path != expected[i] {
			return fmt.Errorf("manifest path %q is not canonical", entry.Path)
		}
		if !isDigest(entry.SHA256) {
			return fmt.Errorf("manifest digest for %q is invalid", entry.Path)
		}
		contents, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(entry.Path)))
		if err != nil {
			return err
		}
		if digest(contents) != entry.SHA256 {
			return fmt.Errorf("manifest digest mismatch for %q", entry.Path)
		}
	}
	return nil
}

func bundleFiles(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("bundle entry is not a regular file")
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(relative))
		return nil
	})
	sort.Strings(files)
	return files, err
}

func decodeClosed(raw []byte, target any, expected []string) error {
	if err := mpc2of3.RequireCanonicalJCS(raw); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	if !sameKeys(fields, expected) {
		return fmt.Errorf("invalid closed schema")
	}
	return strictjson.DecodeClosed(raw, target)
}

func digest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
func isDigest(raw string) bool { _, err := mpc2of3.ParseDescriptorFingerprint(raw); return err == nil }
func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
func sameKeys(fields map[string]json.RawMessage, expected []string) bool {
	if len(fields) != len(expected) {
		return false
	}
	for _, key := range expected {
		if _, ok := fields[key]; !ok {
			return false
		}
	}
	return true
}

type dkgFixture struct {
	intentID, sessionID, keyID, orgID, deadline, descriptorBytes, descriptorFingerprint string
	createdAt                                                                           time.Time
	descriptor                                                                          mpc2of3.KeyDescriptorV1
}

func verifyHTTPFixtures(root string) error {
	listing, err := readObject(root, "listing-response.json", []string{"httpStatus", "ownClaimedDkg", "ownClaimedSign", "pending"})
	if err != nil {
		return err
	}
	if integer(listing, "httpStatus") != 200 {
		return fmt.Errorf("invalid listing status")
	}
	own, err := array(listing, "ownClaimedDkg")
	if err != nil {
		return err
	}
	ownSign, err := array(listing, "ownClaimedSign")
	if err != nil {
		return err
	}
	pending, err := array(listing, "pending")
	if err != nil {
		return err
	}
	if len(own) != 1 || len(ownSign) != 1 || len(pending) != 2 {
		return fmt.Errorf("listing must contain one claimed DKG, one claimed SIGN, and pending DKG/SIGN pair")
	}

	claimed, err := parseDKG(objectRaw(own[0]), "CLAIMED", true)
	if err != nil {
		return fmt.Errorf("own claimed DKG: %w", err)
	}
	pendingDKG, err := parseDKG(objectRaw(pending[0]), "PENDING", false)
	if err != nil {
		return fmt.Errorf("pending DKG: %w", err)
	}
	if kind, _ := stringValue(objectRaw(pending[1]), "type"); kind != "SIGN" {
		return fmt.Errorf("second pending intent must be SIGN")
	}
	if err := parseSign(objectRaw(pending[1])); err != nil {
		return err
	}
	claimedSign, err := parseOwnedSign(objectRaw(ownSign[0]))
	if err != nil {
		return err
	}
	if !claimed.createdAt.Before(pendingDKG.createdAt) || !pendingDKG.createdAt.Before(signCreatedAt(objectRaw(pending[1]))) {
		return fmt.Errorf("pending intents are not sorted by createdAt,intentId")
	}
	if claimed.intentID == pendingDKG.intentID || claimed.sessionID == pendingDKG.sessionID || claimed.keyID == pendingDKG.keyID {
		return fmt.Errorf("listing reuses DKG identifier")
	}
	if signKey, _ := stringValue(objectRaw(pending[1]), "keyId"); signKey == pendingDKG.keyID || signKey == claimed.keyID {
		return fmt.Errorf("listing reuses keyId")
	}

	claim, err := readObject(root, "claim-response.json", []string{"chainCodeBase64", "deadline", "descriptorBytesBase64", "descriptorFingerprint", "httpStatus", "intentId", "keyId", "orgId", "sessionId", "status"})
	if err != nil {
		return err
	}
	if integer(claim, "httpStatus") != 200 || stringMust(claim, "status") != "CLAIMED" {
		return fmt.Errorf("invalid claim status")
	}
	claimDKG, err := parseClaim(claim)
	if err != nil {
		return err
	}
	if claimDKG.intentID != pendingDKG.intentID || claimDKG.sessionID != pendingDKG.sessionID || claimDKG.keyID != pendingDKG.keyID || claimDKG.orgID != pendingDKG.orgID || claimDKG.deadline != pendingDKG.deadline || claimDKG.descriptorBytes != pendingDKG.descriptorBytes || claimDKG.descriptorFingerprint != pendingDKG.descriptorFingerprint {
		return fmt.Errorf("claim differs from pending DKG")
	}
	chain, err := parseStandardBase64(stringMust(claim, "chainCodeBase64"), 32)
	if err != nil || digest(chain) != claimDKG.descriptor.ChainCodeHash {
		return fmt.Errorf("claim chain code does not match descriptor")
	}

	signClaim, err := readObject(root, "sign-claim-response.json", []string{"deadline", "httpStatus", "intentId", "payload", "sessionId", "status", "type"})
	if err != nil {
		return err
	}
	if err := validateSignClaim(signClaim, claimedSign); err != nil {
		return err
	}
	if err := validateSignTerminalFixtures(root); err != nil {
		return err
	}

	terminals := map[string]string{}
	for _, specification := range []struct{ name, status string }{{"terminal-completed-request.json", "COMPLETED"}, {"terminal-failed-request.json", "FAILED"}} {
		request, err := readObject(root, specification.name, []string{"terminalResult", "terminalResultFingerprint"})
		if err != nil {
			return err
		}
		terminalRaw := request["terminalResult"]
		terminal, fingerprint, err := mpc2of3.ParseCanonicalTerminalResult(terminalRaw)
		if err != nil {
			return fmt.Errorf("%s terminal: %w", specification.name, err)
		}
		if terminal.Status != mpc2of3.TerminalStatus(specification.status) || fingerprint.String() != stringMust(request, "terminalResultFingerprint") {
			return fmt.Errorf("invalid %s", specification.name)
		}
		if terminal.IntentID != claimDKG.intentID || terminal.SessionID != claimDKG.sessionID || terminal.KeyID != claimDKG.keyID {
			return fmt.Errorf("terminal differs from claimed DKG")
		}
		if terminal.Result != nil && (terminal.Result.DescriptorFingerprint != claimDKG.descriptorFingerprint || terminal.Result.ChainCodeHash != claimDKG.descriptor.ChainCodeHash) {
			return fmt.Errorf("completed terminal is not bound to claim")
		}
		terminals[specification.status] = fingerprint.String()
	}
	for _, specification := range []struct {
		name, outcome, status string
		httpStatus            int
	}{{"accepted-response.json", "ACCEPTED", "COMPLETED", 200}, {"replay-response.json", "EXACT_REPLAY", "COMPLETED", 200}, {"conflict-response.json", "TERMINAL_CONFLICT", "FAILED", 409}} {
		response, err := readObject(root, specification.name, []string{"authoritativeResultFingerprint", "authoritativeStatus", "httpStatus", "outcome"})
		if err != nil {
			return err
		}
		if integer(response, "httpStatus") != specification.httpStatus || stringMust(response, "outcome") != specification.outcome || stringMust(response, "authoritativeStatus") != specification.status || stringMust(response, "authoritativeResultFingerprint") != terminals[specification.status] {
			return fmt.Errorf("invalid %s", specification.name)
		}
	}
	frame, err := readObject(root, "mailbox-frame.json", []string{"authenticatedPartyId", "broadcast", "fromPartyId", "intentId", "messageId", "orgId", "payload", "protocolSeq", "round", "sessionId", "toPartyId"})
	if err != nil {
		return err
	}
	if err := validateMailbox(frame, claimDKG); err != nil {
		return err
	}
	return nil
}

func readObject(root, name string, expected []string) (map[string]json.RawMessage, error) {
	raw, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		return nil, err
	}
	if err := mpc2of3.RequireCanonicalJCS(raw); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	if !sameKeys(fields, expected) {
		return nil, fmt.Errorf("%s has an invalid closed schema", name)
	}
	return fields, nil
}

func parseDKG(fields map[string]json.RawMessage, expectedStatus string, _ bool) (dkgFixture, error) {
	expected := []string{"createdAt", "deadline", "descriptorBytesBase64", "descriptorFingerprint", "intentId", "keyId", "orgId", "sessionId", "status", "type"}
	if !sameKeys(fields, expected) {
		return dkgFixture{}, fmt.Errorf("invalid DKG schema")
	}
	if stringMust(fields, "type") != "DKG" || stringMust(fields, "status") != expectedStatus {
		return dkgFixture{}, fmt.Errorf("invalid DKG type or status")
	}
	createdAt, err := exactUTC(stringMust(fields, "createdAt"))
	if err != nil {
		return dkgFixture{}, err
	}
	deadline, err := exactUTC(stringMust(fields, "deadline"))
	if err != nil || !createdAt.Before(deadline) {
		return dkgFixture{}, fmt.Errorf("invalid DKG deadline")
	}
	fixture, err := descriptorFixture(fields, createdAt)
	if err != nil {
		return dkgFixture{}, err
	}
	fixture.deadline = stringMust(fields, "deadline")
	return fixture, nil
}

func parseClaim(fields map[string]json.RawMessage) (dkgFixture, error) {
	if !identifier(stringMust(fields, "intentId"), "intentId", "intent-") ||
		!uuidV4Pattern.MatchString(stringMust(fields, "sessionId")) ||
		!identifier(stringMust(fields, "orgId"), "orgId", "org-") ||
		!keyIDPattern.MatchString(stringMust(fields, "keyId")) {
		return dkgFixture{}, fmt.Errorf("invalid claim identity")
	}
	if _, err := exactUTC(stringMust(fields, "deadline")); err != nil {
		return dkgFixture{}, err
	}
	fixture, err := descriptorFixture(fields, time.Time{})
	if err != nil {
		return dkgFixture{}, err
	}
	fixture.deadline = stringMust(fields, "deadline")
	return fixture, nil
}

func descriptorFixture(fields map[string]json.RawMessage, createdAt time.Time) (dkgFixture, error) {
	intentID, sessionID, orgID, keyID := stringMust(fields, "intentId"), stringMust(fields, "sessionId"), stringMust(fields, "orgId"), stringMust(fields, "keyId")
	if !identifier(intentID, "intentId", "intent-") || !uuidV4Pattern.MatchString(sessionID) || !identifier(orgID, "orgId", "org-") || !keyIDPattern.MatchString(keyID) {
		return dkgFixture{}, fmt.Errorf("invalid DKG identity")
	}
	bytes, err := parseStandardBase64(stringMust(fields, "descriptorBytesBase64"), -1)
	if err != nil {
		return dkgFixture{}, err
	}
	descriptor, fingerprint, err := mpc2of3.ParseCanonicalDescriptor(bytes)
	if err != nil {
		return dkgFixture{}, err
	}
	if descriptor.KeyID != keyID || fingerprint.String() != stringMust(fields, "descriptorFingerprint") {
		return dkgFixture{}, fmt.Errorf("descriptor does not bind to DKG")
	}
	return dkgFixture{intentID: intentID, sessionID: sessionID, keyID: keyID, orgID: orgID, descriptorBytes: stringMust(fields, "descriptorBytesBase64"), descriptorFingerprint: fingerprint.String(), createdAt: createdAt, descriptor: descriptor}, nil
}

func parseSign(fields map[string]json.RawMessage) error {
	if !sameKeys(fields, []string{"createdAt", "intentId", "keyId", "orgId", "status", "type"}) || stringMust(fields, "type") != "SIGN" || stringMust(fields, "status") != "PENDING" || !identifier(stringMust(fields, "intentId"), "intentId", "intent-") || !identifier(stringMust(fields, "orgId"), "orgId", "org-") || !keyIDPattern.MatchString(stringMust(fields, "keyId")) {
		return fmt.Errorf("invalid pending SIGN")
	}
	_, err := exactUTC(stringMust(fields, "createdAt"))
	return err
}

type signDiscoveryFixture struct {
	intentID, sessionID, keyID, orgID, deadline string
}

func parseOwnedSign(fields map[string]json.RawMessage) (signDiscoveryFixture, error) {
	expected := []string{"createdAt", "deadline", "intentId", "keyId", "orgId", "sessionId", "status", "type"}
	if !sameKeys(fields, expected) || stringMust(fields, "type") != "SIGN" || stringMust(fields, "status") != "CLAIMED" {
		return signDiscoveryFixture{}, fmt.Errorf("invalid own claimed SIGN schema")
	}
	createdAt, err := exactUTC(stringMust(fields, "createdAt"))
	if err != nil {
		return signDiscoveryFixture{}, err
	}
	deadline, err := exactUTC(stringMust(fields, "deadline"))
	if err != nil || !createdAt.Before(deadline) {
		return signDiscoveryFixture{}, fmt.Errorf("invalid own claimed SIGN deadline")
	}
	fixture := signDiscoveryFixture{
		intentID: stringMust(fields, "intentId"), sessionID: stringMust(fields, "sessionId"),
		keyID: stringMust(fields, "keyId"), orgID: stringMust(fields, "orgId"), deadline: stringMust(fields, "deadline"),
	}
	if !identifier(fixture.intentID, "intentId", "intent-") || !identifier(fixture.sessionID, "sessionId", "sign-") || !identifier(fixture.orgID, "orgId", "org-") || !keyIDPattern.MatchString(fixture.keyID) {
		return signDiscoveryFixture{}, fmt.Errorf("invalid own claimed SIGN identity")
	}
	return fixture, nil
}

func validateSignClaim(fields map[string]json.RawMessage, listed signDiscoveryFixture) error {
	if integer(fields, "httpStatus") != 200 || stringMust(fields, "type") != "SIGN" || stringMust(fields, "status") != "CLAIMED" {
		return fmt.Errorf("invalid SIGN claim status")
	}
	if stringMust(fields, "intentId") != listed.intentID || stringMust(fields, "sessionId") != listed.sessionID || stringMust(fields, "deadline") != listed.deadline {
		return fmt.Errorf("SIGN claim differs from discovery identity")
	}
	if _, err := exactUTC(stringMust(fields, "deadline")); err != nil {
		return err
	}
	payload := objectRaw(fields["payload"])
	expected := []string{"algorithm", "chain", "curve", "derivationContext", "derivationContextHash", "digest", "digestType", "hashAlgorithm", "keyId", "orgId", "parties", "partyId", "profileId", "profileTemplateId", "profileVersion", "signingPayloadType", "threshold", "type", "walletId"}
	if !sameKeys(payload, expected) || stringMust(payload, "type") != "SIGN" || stringMust(payload, "keyId") != listed.keyID || stringMust(payload, "orgId") != listed.orgID {
		return fmt.Errorf("invalid SIGN claim payload schema or identity")
	}
	for _, key := range []string{"algorithm", "chain", "curve", "derivationContextHash", "digestType", "hashAlgorithm", "partyId", "profileId", "profileTemplateId", "signingPayloadType", "walletId"} {
		if stringMust(payload, key) == "" {
			return fmt.Errorf("invalid SIGN claim payload %s", key)
		}
	}
	if digest, err := parseStandardBase64(stringMust(payload, "digest"), -1); err != nil || len(digest) == 0 {
		return fmt.Errorf("invalid SIGN claim digest")
	}
	parties, err := array(payload, "parties")
	if err != nil || len(parties) != 2 {
		return fmt.Errorf("invalid SIGN claim parties")
	}
	for _, party := range parties {
		var value string
		if json.Unmarshal(party, &value) != nil || value == "" {
			return fmt.Errorf("invalid SIGN claim party")
		}
	}
	threshold, err := integerValue(payload, "threshold")
	if err != nil || threshold != 2 {
		return fmt.Errorf("invalid SIGN claim threshold")
	}
	profileVersion, err := integerValue(payload, "profileVersion")
	if err != nil || profileVersion < 1 {
		return fmt.Errorf("invalid SIGN claim profile version")
	}
	contextFields := objectRaw(payload["derivationContext"])
	contextExpected := []string{"accountPath", "addressEncoding", "algorithm", "chain", "childPath", "curve", "descriptorVersion", "expectedAddress", "expectedPublicKey", "fullPath", "keyVersion", "profileId", "profileTemplateId", "profileVersion", "publicKeyFormat", "scheme"}
	if !sameKeys(contextFields, contextExpected) {
		return fmt.Errorf("invalid SIGN derivation context schema")
	}
	for _, key := range []string{"accountPath", "addressEncoding", "algorithm", "chain", "childPath", "curve", "expectedAddress", "expectedPublicKey", "fullPath", "profileId", "profileTemplateId", "publicKeyFormat", "scheme"} {
		if stringMust(contextFields, key) == "" {
			return fmt.Errorf("invalid SIGN derivation context %s", key)
		}
	}
	for _, key := range []string{"descriptorVersion", "keyVersion", "profileVersion"} {
		value, err := integerValue(contextFields, key)
		if err != nil || value < 1 {
			return fmt.Errorf("invalid SIGN derivation context %s", key)
		}
	}
	return nil
}

func validateSignTerminalFixtures(root string) error {
	completed, err := readObject(root, "sign-terminal-completed-request.json", []string{"status"})
	if err != nil || stringMust(completed, "status") != "COMPLETED" {
		return fmt.Errorf("invalid completed SIGN terminal request")
	}
	failed, err := readObject(root, "sign-terminal-failed-request.json", []string{"errorCode", "status"})
	if err != nil || stringMust(failed, "status") != "FAILED" || stringMust(failed, "errorCode") == "" {
		return fmt.Errorf("invalid failed SIGN terminal request")
	}
	return nil
}

func signCreatedAt(fields map[string]json.RawMessage) time.Time {
	value, _ := exactUTC(stringMust(fields, "createdAt"))
	return value
}

func validateMailbox(fields map[string]json.RawMessage, claim dkgFixture) error {
	for key, prefix := range map[string]string{"intentId": "intent-", "orgId": "org-"} {
		if !identifier(stringMust(fields, key), key, prefix) {
			return fmt.Errorf("invalid mailbox %s", key)
		}
	}
	if !uuidV4Pattern.MatchString(stringMust(fields, "sessionId")) || !messageIDPattern.MatchString(stringMust(fields, "messageId")) {
		return fmt.Errorf("invalid mailbox session or message identity")
	}
	if stringMust(fields, "intentId") != claim.intentID || stringMust(fields, "sessionId") != claim.sessionID || stringMust(fields, "orgId") != claim.orgID {
		return fmt.Errorf("mailbox differs from claimed DKG")
	}
	authenticated, from, to := stringMust(fields, "authenticatedPartyId"), stringMust(fields, "fromPartyId"), stringMust(fields, "toPartyId")
	broadcast, err := boolean(fields, "broadcast")
	if err != nil {
		return err
	}
	round, err := integerValue(fields, "round")
	if err != nil {
		return err
	}
	protocolSeq, err := integerValue(fields, "protocolSeq")
	if err != nil {
		return err
	}
	if (authenticated != "co-signer-primary" && authenticated != "co-signer-recovery") || authenticated != from || round < 0 || protocolSeq < 0 {
		return fmt.Errorf("invalid party-bound mailbox frame")
	}
	if (broadcast && to != "broadcast") || (!broadcast && (to == from || (to != "mpc-signer" && to != "co-signer-primary" && to != "co-signer-recovery"))) {
		return fmt.Errorf("invalid mailbox target")
	}
	payload, err := parseStandardBase64(stringMust(fields, "payload"), -1)
	if err != nil || len(payload) == 0 || len(payload) > 1<<20 {
		return fmt.Errorf("invalid mailbox payload")
	}
	return nil
}

func objectRaw(raw json.RawMessage) map[string]json.RawMessage {
	var value map[string]json.RawMessage
	_ = json.Unmarshal(raw, &value)
	return value
}
func array(fields map[string]json.RawMessage, key string) ([]json.RawMessage, error) {
	var value []json.RawMessage
	if err := json.Unmarshal(fields[key], &value); err != nil {
		return nil, err
	}
	return value, nil
}
func stringValue(fields map[string]json.RawMessage, key string) (string, error) {
	var value string
	if err := json.Unmarshal(fields[key], &value); err != nil {
		return "", err
	}
	return value, nil
}
func stringMust(fields map[string]json.RawMessage, key string) string {
	value, _ := stringValue(fields, key)
	return value
}
func integer(fields map[string]json.RawMessage, key string) int {
	var value int
	_ = json.Unmarshal(fields[key], &value)
	return value
}
func integerValue(fields map[string]json.RawMessage, key string) (int, error) {
	var value int
	if err := json.Unmarshal(fields[key], &value); err != nil {
		return 0, fmt.Errorf("invalid %s: %w", key, err)
	}
	return value, nil
}
func boolean(fields map[string]json.RawMessage, key string) (bool, error) {
	var value bool
	err := json.Unmarshal(fields[key], &value)
	return value, err
}
func identifier(value, label, prefix string) bool {
	return len(value) > 0 && len(value) <= 255 && identifierPattern.MatchString(value) && (prefix == "" || (len(value) > len(prefix) && value[:len(prefix)] == prefix))
}
func exactUTC(value string) (time.Time, error) {
	if !utcPattern.MatchString(value) {
		return time.Time{}, fmt.Errorf("invalid exact UTC date")
	}
	parsed, err := time.Parse("2006-01-02T15:04:05.000Z", value)
	if err != nil || parsed.UTC().Format("2006-01-02T15:04:05.000Z") != value {
		return time.Time{}, fmt.Errorf("invalid exact UTC date")
	}
	return parsed, nil
}
func parseStandardBase64(value string, size int) ([]byte, error) {
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil || base64.StdEncoding.EncodeToString(decoded) != value || (size >= 0 && len(decoded) != size) {
		return nil, fmt.Errorf("invalid standard base64")
	}
	return decoded, nil
}
