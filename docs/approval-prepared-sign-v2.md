# Approved prepared SIGN v2

DKG and SIGN claims use only `POST /api/v2/co-signer/intents/{dkg|sign}/:intentId/claim`.
V1 claim routes are removed and return 404. DKG response data, frame transport,
terminal delivery, worker ownership, startup cleanup and absolute SIGN execution
lifetimes retain their existing contracts. There is no version switching, V1 retry
or metadata-only fallback. A configured approval Co-Signer rejects
both-null legacy claims, including work for another organization.

Before Core, the worker validates the closed v2 envelope, existing policy and
full derivation identity, exact locally retained admission/approved receipt/APPLIED
acknowledgment, exact serialized transaction and its independent digest, economic
terms, requested fees and immutable fee cap. Ethereum sender is reconstructed
from the verified derived public key; an unsigned EIP-1559 preimage has no sender.
The gate uses the application-owned approval Journal. Both execution-known and
execution binding artifacts must be durable before Core. Exact byte retry still
requires the same live worker ownership. An orphaned session is cleanup only.
Missing local evidence is never reconstructed from a backend claim.

## TRON network trust boundary

Co-Signer performs no blockchain RPC requests. The former
`CO_SIGNER_APPROVAL_TRON_MAINNET_RPC_URL` and
`CO_SIGNER_APPROVAL_TRON_NILE_RPC_URL` settings are unused and can be removed
from an installation. Backend remains responsible for constructing a transaction
for the selected network with a valid TAPOS block reference and broadcasting it.

TRON Transaction.raw has no chain ID, and a TRON address does not identify a
network. Co-Signer verifies the declared network consistently across the
authorization, policy and derivation context, but cannot independently prove
that the raw transaction belongs to that network or that its TAPOS reference
exists on the selected chain. TAPOS validation by a blockchain node at broadcast
does not establish that Co-Signer signed for the network the organization
approved: an incorrect Backend could supply that other network's reference
before the signature is created.

Local checks still require canonical protobuf encoding, exactly one supported
transfer contract, the approved sender/public-key/address and derivation
identity, recipient, amount, token contract/calldata, fee limit and fee budget,
SHA-256 digest, two-byte reference number and eight-byte reference hash, and a
valid timestamp/expiration with more than 60 seconds remaining. Durable approved
receipt/APPLIED evidence and one immutable execution binding remain mandatory
for approval-backed execution. External approval and auto_approve have the same
transaction verification boundary; neither mode adds independent network proof.

Mainnet and Nile can currently use the same MPC root key and child path, which
produces the same wallet key/address. A compromised or incorrect Backend can
provide mainnet transaction bytes under an approved Nile transfer when the
other approved parameters match. The resulting signature could spend real
mainnet funds controlled by that shared address. This is an explicit limitation
of the current RPC-free configuration, not protection against cross-network
substitution. Ethereum retains its local chain-ID check in the signed preimage.

[BROSET-156](https://brolabel.atlassian.net/browse/BROSET-156) tracks separate
TRON derivation branches (/0/{index} for mainnet and /1/{index} for Nile),
independent enforcement in Backend, MPC-Signer and Co-Signer, and transition of
legacy wallets/approvals. This separation is not implemented here. It will
protect designated mainnet wallet keys from Nile approvals, but will not prove
the network of TRON bytes. Real mainnet funds sent to a designated test address
would still be controlled by that test key.

## Decoder scope and dependencies

Ethereum uses go-ethereum `rlp` v1.16.9 (LGPLv3) for the existing nine-field
unsigned type-2 preimage, with strict canonical round trip, empty access list,
exact transfer calldata and bounded integer checks. Signed twelve-field forms,
unknown types, contract creation and extra data are rejected. TRON uses only
GoTRON v0.24.0 generated `pkg/proto/core` (LGPLv3) with deterministic canonical
re-encoding and explicit raw/contract/Any/value unknown-field checks. It admits
only one native TransferContract or exact TRC20 TriggerSmartContract and no
permission override, memo, auth, scripts, token call value or extra fields.
Neither full SDK client nor geth core/types is linked. Distribution must honor
the upstream licenses. Exact graph, linked modules, binary sizes and test
commands are recorded in the Task7 implementation report.

The existing unsigned TRON policy uses the catalog spelling `TRC20`; only that
known boundary maps to signed `trc20`. Signed authorization is never rewritten.
The same actual SDK/policy producer corpus is checked in TypeScript and Go.
Legacy HTTP/descriptor/authorization trust bundles are retained independently.

The prepared corpus is stored as `testdata/approval-prepared-v1/corpus.json.gz`.
It contains all 202 cases (8 valid, 194 hostile), with no generated expectations.
The manifest describes the logical `corpus.json` backend export: the Go verifier
checks its SHA-256 after bounded gzip decompression. The compact export uses one
JSON object per line; transaction, signature and digest values are unchanged.
Gzip adds no dependency or wire-protocol change.

To inspect the fixtures from the repository root:

```sh
gzip -dc testdata/approval-prepared-v1/corpus.json.gz | python3 -m json.tool
GOWORK=off go run ./cmd/mpc-contracts verify
```

When importing an updated backend export, retain its manifest and compress the
exact `corpus.json` bytes with `gzip -n -9 -c`; do not regenerate transaction or
signature values in Go. Update the explicit bundle pin only for the reviewed
manifest. Corrupt, truncated, oversized and changed fixtures fail verification.

Both verifiers accept the intersection of existing producer numeric ranges:
Ethereum nonce is at most 9007199254740991 (ethers JavaScript safe integer),
gas is a positive uint64 (at most 18446744073709551615), and native TRX amount
and TRON feeLimit are at most 9007199254740991 (existing request builder bounds).
These are byte/producer compatibility checks; signed authorization is unchanged.

## HTTP error boundary

Both V2 claim routes use the existing public error target registry.
Its existing public error envelope is `{code,message,details,requestId,retryable}`.
`APPROVAL_PROTOCOL_REQUIRED` is 409, `MPC_INTENT_CONFLICT` is the fixed safe 409
for the existing expired/terminal/binding/claimed conflicts, and scoped missing
work is `RESOURCE_NOT_FOUND` 404. Existing real Ed25519 signed-request auth,
`mpc:raw`, and nonempty `X-Idempotency-Key` remain mandatory for both DKG and SIGN.
V1 claim routes are absent. Other V1 endpoints and frozen authentication vectors
remain unchanged; they do not provide claim fallback.
