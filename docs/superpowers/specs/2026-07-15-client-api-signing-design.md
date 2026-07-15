# BroSettlement Client API Signing Design

## Goal

Update the monolith HTTP Ed25519 signing client to match the six-line BroSettlement Client API canonical request contract without changing MPC payloads, polling, retry, or idempotency behavior.

## Canonical Request

Each request is signed over exactly six newline-separated lines:

```text
METHOD
EXACT_REQUEST_TARGET
BODY_HASH
TIMESTAMP
NONCE
API_KEY_ID
```

- `METHOD` is the uppercase HTTP method.
- `EXACT_REQUEST_TARGET` is `req.URL.RequestURI()` after the request URL has been fully constructed. This preserves the path and the exact encoded query string, including parameter order, repeated parameters, empty values, and percent encoding.
- `BODY_HASH` is the lowercase hexadecimal SHA-256 digest of the exact transmitted body bytes. It is an empty line when the request has no body.
- `TIMESTAMP` and `NONCE` retain their current formats and are generated for each request attempt.
- `API_KEY_ID` is the exact configured key ID.

`X-Idempotency-Key` is not part of the canonical request.

## Implementation

Keep request construction and retry flow unchanged. In `Client.signRequest`, replace the path-only canonical line with `req.URL.RequestURI()` and append `c.keyID` as the sixth line. Preserve all existing signing headers and continue omitting `X-Api-Body-Hash` for an empty body.

Because `doJSON` creates and signs a new `http.Request` inside every retry iteration, every attempt continues to receive a fresh timestamp, nonce, and signature.

## Tests

Update signing tests to use a lowercase UUID API key ID and reconstruct the six-line canonical request from the request received by the test server. Cover:

- a query-bearing GET signed with `?afterSeq=10`;
- a body-bearing POST with the six-line canonical request and body hash;
- a bodyless claim POST with an empty body-hash line and no body-hash header;
- API key ID inclusion;
- signature invalidation after changing the query or API key ID;
- table-driven exact request targets for query ordering, repeated and empty parameters, and percent encoding.

## Documentation and Verification

Add the canonical format and body-hash rules to `README.md`. Format changed Go files with `gofmt` and run `go test ./...`.

## Compatibility Boundaries

The implementation assumes the server verifies the exact request target as exposed by Go's `RequestURI()` and uses the same newline separator, body bytes, timestamp, nonce, API key ID, Ed25519 signature, and standard base64 encoding. No server repository or deployment changes are in scope.
