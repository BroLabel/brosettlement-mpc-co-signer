# brosettlement-mpc-co-signer

[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)

Runtime model:

- polls BroSettlement monolith for pending intents
- claims work over signed HTTP requests
- exchanges MPC frames via monolith message endpoints
- posts final MPC results back to monolith

## 2-of-3 co-signer configuration

The co-signer has two fixed local store profiles and is scoped by the
authenticated organization on every backend request. Legacy
`CO_SIGNER_PARTY_ID` and `CO_SIGNER_SHARES_DIR` settings are unsupported; if
either is set, configuration validation rejects startup. Configure the explicit
primary and recovery settings below instead.

| Variable | Required value |
| --- | --- |
| `CO_SIGNER_PRIMARY_PARTY_ID` | Exactly `co-signer-primary`. |
| `CO_SIGNER_RECOVERY_PARTY_ID` | Exactly `co-signer-recovery`. |
| `CO_SIGNER_PRIMARY_SHARES_DIR` | Absolute pre-existing private primary-store directory. |
| `CO_SIGNER_RECOVERY_SHARES_DIR` | Absolute pre-existing private recovery-store directory that does not equal or overlap the primary directory. |
| `CO_SIGNER_STATE_DIR` | Absolute persistent state directory. |
| `CO_SIGNER_LOCK_PATH` | Absolute stable lock-file path inside `CO_SIGNER_STATE_DIR`. |
| `CO_SIGNER_SHARE_ENCRYPTION_KEY` | Canonical padded standard-base64 encoding of exactly 32 bytes. |
| `CO_SIGNER_SHARE_ENCRYPTION_KEY_ID` | Stable printable-ASCII identifier (1–255 bytes) for that same non-secret key reference. |
| `CO_SIGNER_FREE_SPACE_THRESHOLD_BYTES` | Explicit minimum free-space threshold for provisioning. |
| `CO_SIGNER_PREPARAMS_GENERATION_PARALLELISM` | Explicit positive preparams generation parallelism. |

The primary and recovery profiles use one in-memory key provider and the same
key reference. The raw encryption key is never logged or returned by a store
configuration object. Configure and back up the original key and its key
reference for the lifetime of every v1 artifact; there is no passphrase hashing
or automatic key rotation.

## Recovery artifacts

Run exactly one active co-signer installation per organization. A second
installation using the same organization credentials may claim or replay the
same work and cause a self-inflicted availability failure. During upgrades,
ensure the old process has exited before starting the new one.

For filesystem topology, key custody, backup, restore, and failure handling,
see
[`docs/artifact-format-v1.md`](docs/artifact-format-v1.md) and
[`docs/runbooks/recovery-artifact.md`](docs/runbooks/recovery-artifact.md), as
well as the [Security Policy](SECURITY.md). V1 does not include a recovery CLI
or SDK.

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
