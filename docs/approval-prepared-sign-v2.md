# Approved prepared SIGN v2

SIGN claims use only `POST /api/v2/co-signer/intents/sign/:intentId/claim`.
DKG, frame transport, terminal delivery, worker ownership, startup cleanup and
absolute SIGN execution lifetimes retain their existing v1 protocols. There is
no v1 retry or metadata-only fallback. A configured approval Co-Signer rejects
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

## TRON network evidence

TRON Transaction.raw has no chain ID. The pure protobuf decoder does not claim
network membership. Approved TRON execution additionally requires a trusted,
operator-configured exact HTTPS JSON-RPC endpoint for that network:

- `CO_SIGNER_APPROVAL_TRON_MAINNET_RPC_URL` for `tron:mainnet`.
- `CO_SIGNER_APPROVAL_TRON_NILE_RPC_URL` for `tron:nile`.

There is no public default, backend URL, discovery or redirect following. URLs
may carry provider credentials in their path/query; they are not logged, copied
into public errors, receipts or the journal. Userinfo and fragments are rejected.
The existing positive `CO_SIGNER_HTTP_TIMEOUT` bounds the entire three-read verification;
responses are bounded to 8 MiB each because block(false) includes transaction
hashes. No live RPC is required for tests.

The checker verifies `eth_chainId` (mainnet `0x2b6653dc`, Nile `0xcd8690dc`),
reads the latest block and derives the nearest height <= head with the raw
TAPOS two-byte number within 65536 blocks. It fetches that canonical height,
validates the block number and 32-byte blockID height prefix, then compares the
number bytes[6:8] and blockID bytes[8:16] to raw references. Normal standard
block fields are accepted; malformed wrappers/duplicate JSON/invalid required
number/hash fields fail closed. This trusts the configured provider and TLS;
it is not a light client or proof against provider dishonesty. Rotation must
preserve the selected network and operational provider trust.

A missing endpoint, timeout, malformed/unavailable RPC response or outage is
operational unavailability. The same owned SIGN retries identical saved bytes
at one-second intervals inside the existing operation deadline and execution
TTL; it cannot create a new session, replace raw data, call Core or directly
release holds. At expiry/shutdown it uses the existing terminal failure delivery
and outgoing lifecycle. A wrong network or TAPOS mismatch is invalid evidence,
not an organization rejection. Approval request stays APPROVED after activation.

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

## HTTP error boundary

Only the new v2 SIGN claim joins the existing public error target registry.
Its existing public error envelope is `{code,message,details,requestId,retryable}`.
`APPROVAL_PROTOCOL_REQUIRED` is 409, `MPC_INTENT_CONFLICT` is the fixed safe 409
for the existing expired/terminal/binding/claimed conflicts, and scoped missing
work is `RESOURCE_NOT_FOUND` 404. Existing real Ed25519 signed-request auth,
`mpc:raw`, and nonempty `X-Idempotency-Key` remain mandatory. V1 SIGN/DKG stay
outside that registry and retain their legacy envelope. Only the new
protocol-required v1 rejection explicitly supplies its stable code to the
existing legacy filter; other v1 branches and frozen trust fixtures are unchanged.
