# brosettlement-mpc-co-signer

[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)

Runtime model:

- polls BroSettlement monolith for pending intents
- claims work over signed HTTP requests
- exchanges MPC frames via monolith message endpoints
- posts final MPC results back to monolith

## Client API request signing

The co-signer authenticates monolith HTTP requests with Ed25519 signatures. The
canonical request contains exactly six newline-separated lines:

```text
METHOD
EXACT_REQUEST_TARGET
BODY_HASH
TIMESTAMP
NONCE
API_KEY_ID
```

- `METHOD` is the uppercase HTTP method.
- `EXACT_REQUEST_TARGET` is the value of `req.URL.RequestURI()` after the URL is
  fully constructed. Query parameter order, repeated and empty values, and
  percent encoding are preserved exactly.
- `BODY_HASH` is the lowercase hexadecimal SHA-256 digest of the exact request
  body bytes. It is an empty line for requests without a body.
- `TIMESTAMP`, `NONCE`, and `API_KEY_ID` match the corresponding API signing
  headers and configured key ID.

`X-Api-Body-Hash` is sent only when a body is present.
`X-Idempotency-Key` is not part of the canonical request. Each retry constructs
and signs a new request with a fresh timestamp and nonce.
