# Co-Signer approval wire contracts v1

Date: 2026-10-19
Status: **TASK-000 CONTRACT FREEZE — implementation and isolated tests authorized; feature code and fixtures not yet implemented**
Normative policy: [approved DESIGN](2026-10-11-co-signer-approval-design.md), decision revision pinned by commit `37da3c1e8181db440a7f1c2e3cbef27b4ba8669a` (DESIGN SHA-256 `6f2003b182a3df0d0b6a7fa58a6fc6f6a155022e84531d6d248b976f65b30d9b`). This document selects wire representations, not additional product modes. Controller review follows preparation.

## Encoding and closed-schema rules

The TypeScript shapes below specify JSON, not implementation exports. Every listed property is required; `T | null` means an explicit JSON null, not omission. A union selects one exact property set. Unknown/duplicate properties, invalid UTF-8, non-finite numbers and trailing JSON values are errors at every nesting level. Validate before JCS canonicalization; never canonicalize away duplicate fields. Existing legacy schemas keep their existing validation.

`Id` is a nonempty existing identifier string, at most 128 UTF-8 bytes, without control characters; preserve its existing canonical representation. New request/outgoing IDs use the existing backend UUID representation. `UInt` is a decimal string matching `^(0|[1-9][0-9]*)$`; `PositiveUInt` excludes zero. Never use JSON numbers for atomic amounts, chain IDs or nonce values. Existing chain range checks still apply. `PositiveInt` is a JSON integer in `[1, 2147483647]`. `Timestamp` is exactly UTC `YYYY-MM-DDTHH:mm:ss.sssZ`, a valid instant after the Unix epoch. Waiting has no expiry field.

`Hex256` is exactly 64 lowercase hexadecimal characters, without `0x`. `Base64` is canonical padded RFC 4648 standard base64, verified by decode/re-encode equality. Existing descriptor/derivation fingerprints retain their current encoding; do not convert them to `Hex256`. Strings are compared byte for byte after backend normalization at creation; do not trim, case-fold or Unicode-normalize signed fields in the verifier.

`Reason = {code: string, text: string}`: code matches `^[A-Z][A-Z0-9_]{0,63}$`; text is nonempty, at most 512 UTF-8 bytes, without control characters. `Reference` is a nonempty string of at most 128 UTF-8 bytes without control characters. These are wire bounds, not a new allowlist of rejection policies. Reject invalid responses rather than truncating signed fields. Keep reasons in access-controlled records and sanitize their log rendering.

## Immutable authorization snapshot

```typescript
type AuthorizationV1 = {
  version: 1;
  orgId: Id;
  requestId: Id;
  ledgerTransactionId: Id;
  clientReference: Reference | null;
  accountId: Id;
  walletId: Id;
  keyId: Id;
  descriptorFingerprint: string;
  derivationContextHash: string;
  derivationContext: SignDerivationContextV1;
  chain: "ethereum:mainnet" | "ethereum:sepolia" | "tron:mainnet" | "tron:nile";
  fromAddress: string;
  toAddress: string;
  asset: string;
  tokenStandard: "erc20" | "trc20" | null;
  tokenContractCanonical: string | null;
  tokenDecimals: number | null;
  amountAtomic: PositiveUInt;
  transferKind: "EXTERNAL_WITHDRAWAL" | "INTERNAL_TRANSFER";
  internalRecipient: {accountId: Id, walletId: Id} | null;
  networkFeeAsset: "ETH" | "TRX";
  maxNetworkFeeAtomic: UInt;
  requestedChainParams: RequestedChainParamsV1 | null;
};
type RequestedChainParamsV1 =
  | {ethereum: {maxFeePerGas: UInt, maxPriorityFeePerGas: UInt}}
  | {tron: {feeLimitSun: UInt | null}};
```

`SignDerivationContextV1` is the existing closed SIGN derivation shape, with exactly: `profileId`, `profileTemplateId`, `profileVersion`, `chain`, `algorithm`, `curve`, `scheme`, `accountPath`, `childPath`, `fullPath`, `addressEncoding`, `expectedAddress`, `expectedPublicKey`, `publicKeyFormat`, `descriptorVersion`, `keyVersion`. The three version fields are PositiveInt; other fields are nonempty strings, retaining the existing descriptor/scheme, path, key, address and context-hash validation. Algorithm/curve remain `ecdsa`/`secp256k1`. Bind the descriptor fingerprint and this full context to the authoritative tenant-owned key/wallet before admission and again before signing.

Network/asset/token metadata comes from existing supported chain configuration. `tokenDecimals` is integer 0–255 for tokens; all three token fields are null for native transfers. Token contracts and sender/recipient addresses use existing network canonicalization (EVM lowercase `0x` + 40 hex; TRON verified canonical Base58Check). The derived sender must equal `fromAddress`. Internal classification requires the tenant-scoped expected recipient account/wallet; external classification requires null. This preserves accounting classification; it does not add another transfer form. Request IDs and ledger IDs have different roles even if both are UUIDs.

`requestedChainParams` preserves normalized optional client constraints without preparing a transaction. An absent object normalizes to null; a TRON object without `feeLimitSun` normalizes that property to null. A present object must match the selected protocol and existing chain policy. Do not invent zero, nonce, gas limit, TAPOS, raw bytes or a current fee quote during creation. Provided Ethereum fee fields and TRON fee limit retain the existing request semantics during preparation and byte verification; the total prepared budget must also fit the separately authorized cap.

Persist `authorizationBytes = UTF8(JCS(AuthorizationV1))` and `authorizationHash = lowercaseHex(SHA256(authorizationBytes))` once. Hash all properties above, including explicit nulls, internal classification and fee cap. Hashing applies to canonical bytes, not a reconstructed mutable wallet/asset view. The snapshot is at most 16 KiB; changed normalized terms under the same idempotency key conflict. Existing cross-origin identity/clientReference guards apply before another reservation. Transport idempotency keys are not authorization terms and are not exposed to the organization endpoint.

## Public transactions and cancellation

`POST /api/v1/transactions` keeps the existing DTO (`walletId`, `asset`, `toAddress`, `amountAtomic`, optional `clientReference`, optional `chainParams`) and, for approval-required intake, requires `maxNetworkFeeAtomic: UInt`. Keep existing signed-body, authentication and strict `X-Idempotency-Key` handling and `RbacScopes.withdrawals.create` (`withdrawals:create`). Missing cap is HTTP 400; zero is accepted only by the existing selected chain policy. No mutable organization fallback is added. Existing legacy intake remains supported only where approval is not required; enabled organizations cannot select it to bypass this contract.

Create returns HTTP 201 and the existing `TransactionDetailDto`, whose public `id` stays the original ledger ID on every replay/read. Extend detail and list items with exactly these nullable projections:

```typescript
type ApprovalProjectionV1 = {
  version: 1;
  requestId: Id;
  status: "PENDING_APPROVAL" | "APPROVED" | "REJECTED" | "CANCELLED" | "PREPARATION_FAILED";
  decisionSource: "external" | "auto_approve" | null;
  rejectionReason: Reason | null;
  cancellationReason: "CLIENT_CANCELLED" | null;
  preparationErrorCode: string | null;
  maxNetworkFeeAtomic: UInt;
  networkFeeAsset: "ETH" | "TRX";
};
type ExecutionProjectionV1 = {
  version: 1;
  status: OutgoingTransactionStatus;
  failureCode: string | null;
  failureMessage: string | null;
};
// Added to TransactionDetailDto and TransactionListItemDto:
// approval: ApprovalProjectionV1 | null;
// execution: ExecutionProjectionV1 | null;
```

`execution.status`, `failureCode`, and `failureMessage` use the existing outgoing enum and sanitized public failure projection unchanged, and are read from outgoing only. No outgoing means `execution: null`. Legacy rows have `approval: null`. Ledger `status` stays its existing accounting status; an activated request remains APPROVED even when outgoing fails. `preparationErrorCode` is nonnull only for PREPARATION_FAILED, including `FEE_LIMIT_EXCEEDED`; it is an existing safe error code or that explicit new code, at most 64 ASCII characters. Rejection reason remains visible if the applied receipt rejected. An applied approve source remains visible after cancellation or preparation failure. `cancellationReason` is nonnull only when CANCELLED. Do not persist this execution projection on the approval request.

`POST /api/v1/transactions/:id/cancel` takes the public ledger ID and an empty JSON body `{}` under existing signed-body and composite authentication. Require **`RbacScopes.withdrawals.revoke` (`withdrawals:revoke`)**, using the existing RbacScopeGuard; no new permission or role grant. Scope every lookup to authenticated org. Creation, read and cancellation remain separate existing actions (`withdrawals:create`, `transactions:read`, `withdrawals:revoke`). The cancel endpoint does not additionally grant read access or call an independently protected GET; it returns only:

```typescript
type CancellationResultV1 = {
  version: 1;
  transactionId: Id;
  requestId: Id;
  status: "CANCELLED";
};
```

HTTP 200 for cancellation and exact cancellation replay; no separate idempotency header is necessary because tenant/public-ID identifies the operation. Persist the fixed reason `CLIENT_CANCELLED` in request/history. Lock request and atomically terminalize request/debit/expected credit/history/both holds as DESIGN specifies. HTTP 409 for activated outgoing (`APPROVAL_ALREADY_ACTIVATED`) or another terminal result (`APPROVAL_TERMINAL_CONFLICT`); legacy non-approval transfer is also 409 (`APPROVAL_NOT_REQUIRED`). HTTP 404 for unknown or other-tenant public ID; 401/403 retain existing auth behavior. These codes use the existing public error encoder, not a new error envelope. No free-text cancel reason or active-MPC cancellation is introduced.

## Pending discovery and cursor

`GET /api/v1/co-signer/approval-requests?limit=50&cursor=...` reuses ApiKeySigningGuard, RbacScopeGuard and **`mpc:raw`**, deriving org from authenticated request context. No org query parameter. `limit` defaults to 50, range 1–100; cursor is optional and at most 4096 ASCII bytes; all other query fields are rejected. No server-side SIGN claim/polling lease is created.

```typescript
type PendingApprovalPageV1 = {
  version: 1;
  items: Array<{
    requestId: Id;
    createdAt: Timestamp;
    authorization: AuthorizationV1;
    authorizationHash: Hex256;
  }>;
  nextCursor: string | null;
};
```

HTTP 200 including empty items; never infer cancellation from absence. Response body limit is 2 MiB. Each item rechecks tenant and current PENDING_APPROVAL status. At most one new organization POST per eligible item of a fresh authenticated page, even if its HTTP attempt fails. Local backoff suppresses attempts; it never starts them. Page dispatch is completed or suppressed before advancing. Discovery failure schedules zero new organization calls from local records. A final saved decision can still be redelivered without discovery or another organization call.

Reuse **ClientApiCursorService**, its canonical encoding and existing `cursorConfig.hmacSecret`; no new signing secret/helper. Its exact binding is `{resource: "co-signer.approval-requests.v1", orgId: authenticatedOrgId, status: "PENDING_APPROVAL", order: "createdAt.asc,id.asc"}`. Position is a closed object `{upper: {createdAt: Timestamp, id: Id}, after: {createdAt: Timestamp, id: Id}}`. The service's v1 token stays `base64url(canonicalJson({v:1,resource,bindingHash,position})).mac`, including its existing `binding:v1\n` and `token:v1\n` HMAC-SHA256 domains. Token contents are opaque to Co-Signer. Recheck exact position fields and `after <= upper` after verifying MAC/binding.

For a cursorless request, select the greatest visible pending `(createdAt,id)` as the fixed `upper`, and read ascending pairs `<= upper` in the same read snapshot. Continuations select `after < pair <= upper`, always under current tenant/status filters. Read `limit + 1` to determine continuation. If another eligible row exists, `after` is the last returned pair; otherwise return null. Cancellation may empty/shrink a later page and ends the sweep normally; do not restart at page one within that sweep. After null, the next bounded discovery tick starts a new sweep. New rows beyond upper join the next sweep; no pending age limit or cursor expiry is added. Invalid/cross-org/tampered cursor is HTTP 400 through existing InvalidCursorError. A stale cursor cannot reveal another tenant or authorize a cached call.

## Organization API

Send one HTTPS POST to the configured exact endpoint with `Authorization: Bearer <dedicated integration credential>` and `Content-Type: application/json`. Never forward platform API credentials. Set `Idempotency-Key: <requestId>:<authorizationHash>`; the immutable JSON body is:

```typescript
type OrganizationApprovalRequestV1 = {
  version: 1;
  orgId: Id;
  requestId: Id;
  authorizationHash: Hex256;
  authorization: AuthorizationV1;
};
type OrganizationApprovalResponseV1 = {
  version: 1;
  orgId: Id;
  requestId: Id;
  authorizationHash: Hex256;
  status: "PENDING" | "APPROVED" | "REJECTED";
  reason: Reason | null;
  decisionReference: Reference | null;
};
```

Only HTTP 200 plus a valid closed body with exact bindings is a decision response. REJECTED requires reason; other statuses require null. PENDING requires null decisionReference; a final response may have a reference or null. Final status/reason/reference must remain identical on replay of the same request/hash. PENDING is not a signed final decision and cannot release holds. Transport failures, non-200 status (including 202/204/3xx), invalid body/auth/hash/tenant and oversized responses cause backoff only. One transport attempt per fresh-page admission; disable hidden automatic POST retry. Follow no redirects (a stricter implementation of the DESIGN's cross-authority prohibition, with no redirect mode to configure). Both request and response are at most 64 KiB. Reuse configured HTTPTimeout (currently 30 seconds by default), cancellation and existing transport; no long-held request, webhook or second status endpoint.

## Signed decision receipt and application response

```typescript
type DecisionEnvelopeV1 = {
  version: 1;
  domain: "brosettlement.co-signer.approval-decision";
  audience: "brosettlement-transfer-approval";
  environment: Reference;
  orgId: Id;
  requestId: Id;
  authorizationHash: Hex256;
  decision: "APPROVED" | "REJECTED";
  reason: Reason | null;
  source: "external" | "auto_approve";
  externalDecisionReference: Reference | null;
  decidedAt: Timestamp;
  signingKeyId: Id;
  configRevision: Reference;
};
type SignedDecisionReceiptV1 = {
  version: 1;
  envelopeBase64: Base64;
  signatureBase64: Base64;
};
type DecisionApplicationV1 = {
  version: 1;
  requestId: Id;
  outcome: "ACCEPTED" | "EXACT_REPLAY" | "NOT_APPLIED_CANCELLED" | "TERMINAL_CONFLICT";
  disposition: "APPLIED" | "NOT_APPLIED_CANCELLED" | "TERMINAL_CONFLICT";
};
```

Compute `envelopeBytes = UTF8(JCS(DecisionEnvelopeV1))`; sign **`UTF8("brosettlement.co-signer.approval-decision/v1\n") || envelopeBytes`** with the existing Co-Signer Ed25519 identity. `envelopeBase64` stores exactly those JCS bytes; `signatureBase64` decodes to 64 bytes. Require the decoded JSON to already equal its JCS serialization. APPROVED requires null reason; REJECTED requires Reason. Auto mode produces APPROVED with null reason/reference through the same store/sign/delivery path. Source, environment and revision must match durable local admission. Environment must equal trusted deployment configuration on both sides; no hostname/request-derived fallback.

Persist admission and then immutable exact receipt bytes/signature with the DESIGN's create-only publication/fsync protocol before delivery. Decision envelope maximum is 4096 bytes; decision POST body maximum is 8192 bytes. Retry sends the same wrapper/decoded bytes, refreshing only HTTP auth. `decidedAt` is audit time, never an expiry or a license to backdate a revoked key. Verify historical applied receipts with retained public keys, but validate current signing-key eligibility before accepting a previously unseen executable receipt. Current HTTP identity, receipt signing key and request must be tenant-bound; key rotation cannot automatically replace a saved business decision.

`POST /api/v1/co-signer/approval-requests/:id/decision` body is exactly SignedDecisionReceiptV1, reusing `mpc:raw` and signed-body API-key authentication. Route ID must equal signed request ID; recompute authorizationHash from saved immutable bytes. Do not require another idempotency key: receipt identity is the exact canonical bytes/signature for the tenant/request. Record decision evidence separately from application state under the request lock.

| Situation | HTTP | outcome | disposition / effect |
| --- | --- | --- | --- |
| First valid final receipt on pending request | 200 | ACCEPTED | APPLIED; approve schedules activation, reject atomically terminalizes. |
| Identical already recorded receipt | 200 | EXACT_REPLAY | Original disposition; no state/history/reservation changes, even if execution or cancellation later advanced. |
| First valid late receipt after cancellation | 200 | NOT_APPLIED_CANCELLED | NOT_APPLIED_CANCELLED; audit receipt, never revive work or touch money. |
| Different receipt for same decision identity or other incompatible terminal state | 409 | TERMINAL_CONFLICT | TERMINAL_CONFLICT; retain safe non-applied evidence, never overwrite the winning receipt. |

Exact replay includes the same signature, not merely equivalent economic fields; a different decidedAt/signature/reason/source/config is a conflict. An originally accepted approve followed by cancellation still replays as EXACT_REPLAY/APPLIED, while the current request remains CANCELLED. Replaying a first late receipt returns EXACT_REPLAY/NOT_APPLIED_CANCELLED. Repeating the same conflicting candidate remains HTTP 409/TERMINAL_CONFLICT with no duplicate audit or monetary effect; EXACT_REPLAY applies to the saved applied or late-cancelled receipt, never promotes a conflicting candidate. Invalid signature/schema/binding is 400 (auth remains 401/403); unknown/other-tenant request is 404. A contract or authentication failure is not a business rejection. Permanent delivery errors require visible reconciliation; never regenerate a different receipt.

## Prepared SIGN claim v2

Add **`POST /api/v2/co-signer/intents/sign/:intentId/claim`** with existing `mpc:raw`, Ed25519 HTTP auth and ApiKeyIdempotencyGuard. New Co-Signer uses v2 for SIGN, including legacy SIGN; DKG/message/result endpoints and lifecycle semantics remain v1. No fallback to v1 after a v2 failure. The v1 SIGN claim route rejects approval-required work *before* claim/session mutation with HTTP 409 `APPROVAL_PROTOCOL_REQUIRED`. Capability rollout must verify both sides before enabled intake; existing direct raw-sign/create entrypoints cannot omit approval binding for enabled organizations. Draining legacy writers/work is part of the existing enablement gate.

V2 preserves every existing SIGN claim field exactly: `httpStatus: 200`, `intentId`, `sessionId`, `type: "SIGN"`, `payload`, `status: "CLAIMED"`, `deadline`, `session`. Add exactly `version: 2`, `approval: ApprovedExecutionV1 | null`, `prepared: PreparedTransferV1 | null`. No other fields, including DKG-only top-level fields. The existing payload remains closed with its current required policyContext, digest, parties, profile and derivation fields; `digest` stays canonical standard base64 of 32 bytes, `digestType` stays `transaction`. Session retains exactly sessionId/status/startedAt/deadline/executionExpiresAt and all current deadline/start/TTL checks; approval waiting introduces no execution TTL reset.

```typescript
type ApprovedExecutionV1 = {
  version: 1;
  requestId: Id;
  outgoingId: Id;
  authorization: AuthorizationV1;
  authorizationHash: Hex256;
  decisionReceipt: SignedDecisionReceiptV1;
};
type PreparedTransferV1 = {
  version: 1;
  encoding: "hex";
  unsignedTransaction: string;
  networkFeeAsset: "ETH" | "TRX";
  networkFeeBudgetAtomic: UInt;
  feeEvidence: TronFeeReservationQuoteV1 | null;
};
type TronFeeReservationQuoteV1 = {
  version: 1;
  destinationAccountExists: boolean;
  chainParameters: {
    transactionFeeSunPerBandwidth: UInt;
    createAccountFeeSun: UInt;
    createNewAccountFeeInSystemContractSun: UInt;
    createNewAccountBandwidthRate: UInt;
  };
  amountAtomic: UInt;
  signedTransactionBytes: PositiveInt;
  bandwidthBytes: PositiveInt;
};
```

`unsignedTransaction` is nonempty lowercase even-length hex, no prefix; decoded bytes at most 16 KiB. Claim response maximum is 128 KiB. It is the exact saved outgoing rawDataHex, not JSON rendered from policy metadata. Both new fields may be null only for genuinely legacy non-required work; approval-origin or capability-required work must have both nonnull. The backend derives that requirement from persisted origin/capability, not client claims. Every duplicated org/request/wallet/key/context/hash/chain/asset/amount/address/fee field must agree, with an applied APPROVED receipt also matching local immutable evidence. A REJECTED or non-applied receipt never authorizes SIGN.

For `ethereum-transaction`/`keccak256`, independently decode the unsigned EIP-1559 type-2 serialization, including its `0x02` prefix, reject signatures/access lists/extra data or unsupported forms, reconstruct digest, verify network chainId, native value/data or exact ERC-20 transfer contract/method/recipient/amount, and sender via verified derived public key. Budget is exactly `gasLimit * maxFeePerGas`; feeEvidence is null. For `tron-transaction`/`sha256`, decode exact protobuf Transaction.raw bytes (not signed outer Transaction), reconstruct digest, verify allowed native/TRC20 form, owner/network/TAPOS/expiration and economic fields, and require the existing closed TronFeeReservationQuoteV1 above. Independently reproduce signed size + 64 result bytes, bandwidth/activation/energy budget using `tron-fee-reservation.ts`; quote amount must equal networkFeeBudgetAtomic. A quote is bounded preparation evidence, not a guarantee of receipt fees or independent proof of provider honesty.

For both protocols verify native fee asset, prepared budget <= authorization.maxNetworkFeeAtomic, existing exact-byte policy context, fee constraints and existing chain freshness policy before MPC. Verify digest equality to existing payload.digest; no digest from unchecked metadata. Parser/dependency selection and hostile-byte vectors remain STEP-006 work. No unknown chain/parser fallback or automatic replacement.

Persist a create-only closed local `ExecutionBindingV1 = {version:1, orgId, requestId, outgoingId, authorizationHash, executionDigest, payloadType, hashAlgorithm}`. `executionDigest` is the same canonical 32-byte base64 digest; payloadType/hashAlgorithm are the exact supported pairs above. Compare all fields on replay; a different outgoing, bytes/digest or algorithm conflicts before MPC. Backend also binds one saved byte sequence/outgoing to the request. Same-digest delivery still obeys existing session ownership/orphan rules and cannot restart an orphaned MPC session automatically.

## Trusted configuration and admission v1

New required settings when advertising/using approval protocol support:

| Setting | Exact rule |
| --- | --- |
| CO_SIGNER_APPROVAL_MODE | Exactly `external` or `auto_approve`; no implicit default or error fallback. |
| CO_SIGNER_APPROVAL_ORG_ID | Id; must match authenticated discovery, receipt, wallet/key and claim org. |
| CO_SIGNER_APPROVAL_ENVIRONMENT | Reference; same trusted value configured in backend verification. |
| CO_SIGNER_APPROVAL_CONFIG_REVISION | Reference identifying the immutable operational configuration. |
| CO_SIGNER_APPROVAL_JOURNAL_DIR | Absolute private durable directory, distinct from primary/recovery share directories; files 0600, directory 0700, single-writer lock and fsync support required. |
| CO_SIGNER_APPROVAL_ENDPOINT | Required only for external; absolute HTTPS URL with authority, no userinfo/fragment; exact configured URL, no payload override. |
| CO_SIGNER_APPROVAL_BEARER_TOKEN | Required nonempty only for external; dedicated integration secret; never included in receipts, persisted public config, logs or organization payload. |

In auto mode endpoint/token are never used and zero organization calls are made. Missing/invalid approval configuration prevents *new approval admission and approval capability advertisement*, without treating legacy startup as approval or silently changing an existing admission. Reuse CO_SIGNER_API_KEY_ID/private key for receipt signing and existing MONOLITH_URL/HTTPTimeout/PollMinInterval/PollMaxInterval/PollBackoffFactor for transport/discovery and suppressive full-jitter backoff. Validate finite factor >= 1 and positive intervals/timeout for this dispatcher. No new generic config service or secret store.

V1 approval dispatch has one bounded in-flight organization call, independent of SIGN permits; page limit 50 (server max 100), at most 1000 in-memory suppressive backoff entries (oldest entry may be evicted). Backoff ceiling is min(PollMaxInterval, PollMinInterval * factor^attempt), full jitter from zero to ceiling. Neither retry count nor backoff can terminalize a request. Timer reset/eviction after restart still requires fresh discovery. Durable admission/decision/execution records are read on demand and never all loaded into memory; do not evict replay protection or unresolved durable evidence for these memory bounds.

Admission is a closed local record `{version:1, orgId, requestId, authorizationHash, mode, environment, configRevision, endpoint, admittedAt}` with endpoint null for auto, configured exact endpoint for external, and Timestamp admittedAt. Persist it before evaluation. Reject changed hash or reuse of a revision for different nonsecret config. Config switching never changes an admitted request's mode/environment/revision/endpoint. If its pinned configuration/credential is unavailable, suspend evaluation visibly; no auto migration or reapproval. Credentials may rotate operationally for the same pinned integration, without changing decision identity or forwarding platform credentials. Starting auto for unseen backlog remains an explicit reviewed operational action, as DESIGN requires.

Backend enablement is the existing organization capability gate, not a client-provided mode flag. It must prove v1 discovery/receipt plus v2 prepared claim support and compatible old-writer drain before intake. Exact backend storage of that capability is STEP-001/008 implementation detail; it must not weaken these wire checks.

## Frozen vector registry (planned, no fixture files yet)

IDs below are reserved immutable identifiers, **not generated vectors or passing tests**. STEP-001 must create the shared manifest and producer provenance; STEP-006 adds genuine prepared bytes and cross-language verification. Each vector records producer repository/revision, schema versions, canonical input bytes/hash, expected outcome, and (where applicable) exact signature/digest/raw bytes. Never claim this preparation generated signed fixtures. Do not reuse a retired ID for new semantics.

| Frozen ID | Required input / expected result | Plan proof |
| --- | --- | --- |
| APPROVAL-V1-001 | Native ETH authorization canonical bytes/hash, explicit cap, null token/internal fields | TEST-003/004/017 |
| APPROVAL-V1-002 | ERC-20 authorization, native fee asset distinct from principal, full derivation binding | TEST-004/017 |
| APPROVAL-V1-003 | Native TRX authorization and native/TRC20 fee quote distinction | TEST-004/015B/017 |
| APPROVAL-V1-004 | TRC20 internal-transfer authorization with expected recipient identity | TEST-017/025 |
| APPROVAL-V1-005 | External APPROVED receipt, deterministic signing seed and exact canonical signature bytes | TEST-007/009/019 |
| APPROVAL-V1-006 | External REJECTED receipt with bounded reason; reject missing/oversized/invalid reason | TEST-008/010/023 |
| APPROVAL-V1-007 | Auto APPROVED receipt/admission; zero organization calls | TEST-007/009 |
| APPROVAL-V1-008 | Duplicate/unknown fields, wrong hash/org/key/environment/domain/audience, bad base64/signature, revoked new receipt | TEST-008/019/021 |
| APPROVAL-V1-009 | All decision HTTP/disposition outcomes and immutable exact replay after cancel/execution | TEST-010/011/019 |
| APPROVAL-V1-010 | Existing cursor service token, fixed upper/after, wrong org/resource/MAC, invalid position | TEST-019/022 |
| APPROVAL-V1-011 | Fresh-only multi-page/repeated sweep, cancellation/outage/continuous arrivals, bounded dispatch/backoff | TEST-007/009/022 |
| APPROVAL-V1-012 | Public cap validation, stable ledger ID/projections, cancel scope/200 replay/404/409 | TEST-003/004/012/024 |
| APPROVAL-V1-013 | Valid native ETH prepared v2 type-2 bytes, reconstructed keccak256/budget | TEST-015B/017 |
| APPROVAL-V1-014 | Valid ERC-20 prepared v2 bytes, exact selector/calldata and sender derivation | TEST-017 |
| APPROVAL-V1-015 | Valid native TRX prepared v2 protobuf/raw bytes, SHA256 and closed fee quote | TEST-015B/017 |
| APPROVAL-V1-016 | Valid TRC20 prepared v2 bytes and fee quote | TEST-015B/017 |
| APPROVAL-V1-017 | Mutated recipient/amount/token/chain/calldata/nonce/TAPOS/encoding/signature/quote/budget/digest; no MPC | TEST-017/019 |
| APPROVAL-V1-018 | Same execution binding replay versus changed outgoing/digest; no replacement or orphan restart | TEST-018/020 |
| APPROVAL-V1-019 | Legacy v1/v2 compatibility and approval-required null/missing binding/v1 bypass rejection | TEST-021/026 |
| APPROVAL-V1-020 | Config/admission revision switch, missing journal/durability, saved final receipt redelivery without organization call | TEST-007/009/026 |

## Source anchors and technical rulings

Verified source baselines: Co-Signer `5df5ac25953debeb94f2c37d89a2ad458a73057c`; backend `e14e175ff5fb2974907bcd5efd123d20425a5001`. Paths below are relative to their respective repositories; backend inspection uses `/Users/vlas/Work/BroLabel/back-end/.worktrees/co-signer-approval` only.

- Backend `security/rbac/rbac-scopes.constants.ts:46` (under apps/client-api/src) already defines withdrawals:create/update/revoke. `transactions/transactions.controller.ts:30` uses withdrawals:create; Co-Signer controller uses mpc:raw. RULING-001 selects existing withdrawals:revoke for cancellation, without adding grants.
- Co-Signer `internal/monolith/client.go:166` provides fresh transport authentication; `internal/contract/mpc2of3/jcs.go` already provides JCS. RULING-002 freezes separate canonical snapshot/receipt bytes and signature domain; HTTP timestamps are not business-decision identity.
- Backend `apps/client-api/src/client-contract/pagination/client-api-cursor.service.ts` already supplies canonical authenticated binding/position tokens. RULING-003 reuses it with org/resource/status/order and fixed-bound position, without another secret/helper or polling queue.
- Existing ClaimSignIntentResponse and Go ClaimResult/IntentPayload are closed and do not contain prepared bytes. RULING-004 adds the explicit v2 SIGN claim route and fail-closed v1 gate, preserving current payload, lifecycle and legacy v1 behavior where allowed; no negotiable silent downgrade.
- Backend `apps/client-api/src/transactions/chains/tron/tron-fee-reservation.ts` defines the exact quote above; Ethereum `ethereum-transfer.ts` binds budget to type-2 bytes. RULING-005 carries the existing TRON quote, reproduces its arithmetic, and keeps prepared budget distinct from cap/receipt fee. Parser choice remains STEP-006, not a new fee policy.
- RULING-006 freezes bounded HTTP fields, explicit configuration, one-attempt/fresh-page dispatch and four application outcomes. These are technical representations of DECISION-003/007 and R3; errors cannot become authorization. No new expiry, top-up, cancellation phase, approval source, chain support or replacement policy is introduced.

These rulings require controller review before STEP-001. A policy change outside this representation is DESIGN_REOPEN_REQUIRED, with affected REQ/INV/DECISION IDs; do not silently reinterpret this freeze.
