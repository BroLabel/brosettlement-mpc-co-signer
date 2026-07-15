# BroSettlement Client API Signing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make every monolith HTTP request use the BroSettlement Client API six-line Ed25519 canonical request contract.

**Architecture:** Keep URL construction, request bodies, headers, retries, and endpoint methods unchanged. Extend only canonical construction in `Client.signRequest`, and verify it through requests received by HTTP test servers so tests cover the exact transmitted request target and body.

**Tech Stack:** Go, `net/http`, `crypto/ed25519`, `crypto/sha256`, `httptest`

---

### Task 1: Add signing contract regression tests

**Files:**
- Modify: `internal/monolith/client_test.go`

- [ ] **Step 1: Define the API key fixture and signature verifier**

Add a lowercase UUID fixture and a helper that reconstructs the server-side canonical request from the received request:

```go
const testAPIKeyID = "11111111-2222-3333-4444-555555555555"

func verifyRequestSignature(t *testing.T, r *http.Request, pub ed25519.PublicKey, bodyHash string) bool {
	t.Helper()
	canonical := strings.Join([]string{
		strings.ToUpper(r.Method),
		r.URL.RequestURI(),
		bodyHash,
		r.Header.Get("X-Api-Timestamp"),
		r.Header.Get("X-Api-Nonce"),
		r.Header.Get("X-Api-Key-Id"),
	}, "\n")
	signature, err := base64.StdEncoding.DecodeString(r.Header.Get("X-Api-Signature"))
	if err != nil {
		t.Fatalf("DecodeString() error = %v", err)
	}
	return ed25519.Verify(pub, []byte(canonical), signature)
}
```

Use `testAPIKeyID` in `newTestClient` instead of `key-1`.

- [ ] **Step 2: Update the body-bearing POST test**

Make `TestPostMessageAddsSigningAndIdempotencyHeaders` verify the lowercase SHA-256 body hash, `X-Api-Key-Id`, and the six-line signature over `r.URL.RequestURI()`. Also reconstruct a canonical request with a different API key ID and assert that the existing signature does not verify.

- [ ] **Step 3: Add a bodyless claim POST regression test**

Extend the claim request test to verify:

```go
if got := r.Header.Get("X-Api-Body-Hash"); got != "" {
	t.Fatalf("X-Api-Body-Hash = %q, want absent", got)
}
if !verifyRequestSignature(t, r, pub, "") {
	t.Fatal("bodyless claim signature validation failed")
}
```

Keep the existing assertion that `X-Idempotency-Key` equals the intent ID. This proves the header remains outside the canonical request.

- [ ] **Step 4: Add exact request-target regression tests**

Verify `GetMessages` signs `/api/v1/co-signer/sessions/session-1/messages?afterSeq=10`. Add a table-driven test that calls `client.newRequest` with these exact request targets and verifies each signature:

```go
tests := []string{
	"/resource?b=2&a=1",
	"/resource?tag=one&tag=two&empty=",
	"/resource?value=a%2Fb&space=a%20b",
}
```

For the first case, reconstruct the canonical request with `/resource?a=1&b=2` and assert that the unchanged signature fails, proving query tampering invalidates it.

- [ ] **Step 5: Run the focused tests and verify RED**

Run: `GOWORK=off go test ./internal/monolith`

Expected: FAIL with signature-validation failures because production code still signs only `req.URL.Path` and omits the API key ID canonical line.

### Task 2: Implement six-line canonical signing

**Files:**
- Modify: `internal/monolith/client.go`

- [ ] **Step 1: Sign the exact request target and API key ID**

Replace the canonical construction with:

```go
canonical := strings.Join([]string{
	strings.ToUpper(req.Method),
	req.URL.RequestURI(),
	bodyHash,
	timestamp,
	nonce,
	c.keyID,
}, "\n")
```

Do not change request construction, retry flow, idempotency headers, payload serialization, or the existing rule that `X-Api-Body-Hash` is omitted for an empty body.

- [ ] **Step 2: Run the focused tests and verify GREEN**

Run: `GOWORK=off go test ./internal/monolith`

Expected: PASS.

### Task 3: Document and verify

**Files:**
- Modify: `README.md`
- Format: `internal/monolith/client.go`
- Format: `internal/monolith/client_test.go`

- [ ] **Step 1: Document the signing contract**

Add the exact six-line canonical format and state that `EXACT_REQUEST_TARGET` comes from `req.URL.RequestURI()`, `BODY_HASH` is lowercase SHA-256 of exact transmitted bytes or an empty line, `X-Api-Body-Hash` is omitted for empty bodies, `X-Idempotency-Key` is excluded, and every retry creates a new timestamp, nonce, and signature.

- [ ] **Step 2: Format and inspect the changes**

Run:

```bash
gofmt -w internal/monolith/client.go internal/monolith/client_test.go
git diff --check
git diff -- internal/monolith/client.go internal/monolith/client_test.go README.md
```

Expected: no whitespace errors and no changes outside signing-contract tests, implementation, and documentation.

- [ ] **Step 3: Run the complete test suite**

Run: `GOWORK=off go test ./...`

Expected: PASS for every package.
