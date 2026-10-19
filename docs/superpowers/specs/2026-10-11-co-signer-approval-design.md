# Co-Signer approval and rejection with immediate principal reservation

Date: 2026-10-11
Updated: 2026-10-19 — user approved and this revision incorporates review remedies R1–R3 and simplification S1; all six v1 product decisions remain unchanged.
Status: **DESIGN DECISIONS APPROVED — documentation only; implementation not started**
Supersedes: the earlier conversation proposal to reserve principal after approval.
Scope: backend + customer-hosted Co-Signer; documentation only in this change.

## Context and problem

An organization must explicitly authorize or reject a transfer through its Co-Signer. The user requires the transfer principal to be reserved when the request is accepted. Approval may take an unlimited amount of time and must survive restarts. No blockchain transaction, nonce allocation, or MPC session may exist before approval. The user selected immediate reservation of the network-fee ceiling together with principal. This revision removes the alternative fee-after-approval branch and simplifies implementation around the existing combined reservation service.

Source baselines inspected locally:

| Repository | Revision | Relevant observed behavior |
| --- | --- | --- |
| `brosettlement-mpc-co-signer` | `5df5ac25953debeb94f2c37d89a2ad458a73057c` | SIGN admission acquires a permit before claim; claimed work has execution deadlines and orphan cleanup. |
| `BroSettlement` backend | `e14e175ff5fb2974907bcd5efd123d20425a5001` | Transfer preparation currently precedes outgoing creation and principal/fee reservation. |

Evidence anchors (paths relative to the named repository):

- Backend `apps/client-api/src/transactions/application/create-outgoing/outgoing-transaction-reservation.service.ts:58`: `prepareTransfer()` runs before the DB reservation transaction.
- Backend `apps/client-api/src/ledger/application/withdrawals/ledger-withdrawal-reservation.service.ts:61`: `createReservation()` reserves principal and fee together only when the ledger transaction is newly created. Calling it again does not add a missing fee reservation.
- Backend `packages/database/prisma/schema.prisma:479`: ledger transactions have text source identities; a principal journal can exist before an outgoing row. Unique `(orgId, sourceIdentity)` and `(transactionId, effectKey)` already exist.
- Backend `apps/client-api/src/transactions/transactions.controller.ts:49`: the public transaction ID is already the ledger transaction ID. Preserve it throughout approval and execution.
- Backend `packages/database/prisma/migrations/20260918090000_ethereum_outgoing_preparation/migration.sql:35`: an Ethereum outgoing row without a prepared nonce is incompatible with the current constraint; an unresolved outgoing also occupies the sender gate.
- Co-Signer `internal/worker/scheduler.go:237`, `internal/worker/session_worker.go:134`: permits precede claim, and orphaned CLAIMED SIGN work is cleaned up after restart.
- Co-Signer `internal/monolith/types.go:147`, `internal/signingpolicy/README.md:15`: claim contains digest and policy metadata, not the unsigned transaction bytes needed for independent verification.
- Co-Signer `internal/monolith/client.go:166`: HTTP authentication has fresh timestamps/nonces. It is not a stable signed business-decision receipt.

These are source observations, not production-runtime or executed-test evidence.

## Goals and non-goals

Goal: reserve principal immediately, wait safely for an organizational decision without a deadline, and execute at most one authorized transfer using freshly prepared and independently verified transaction bytes.

Reuse the existing ledger journals, balance locks, PostgreSQL, outbox/worker patterns, Ed25519 HTTP authentication, JCS dependency, SIGN scheduler, and MPC runtime.

Not in scope:

- A human approval UI, inbound webhook server, quorum approvals, or a generic rules/workflow engine.
- A new broker, a second ledger, long-lived DB locks, or a goroutine per pending request.
- Changing MPC algorithms, key-share custody, DKG, or chain-scanner behavior.
- Automatic replacement transactions, fee bumping, or recovery of ambiguous broadcasts.
- Fee-after-approval mode, automatic fee top-ups, and a WAITING_FOR_FEE workflow.
- Implementing the selected policy in this documentation-only change.
- Production configuration changes, funding, migrations, releases, or deployments.

## Requirements

| ID | Requirement |
| --- | --- |
| REQ-001 | Successful creation atomically persists a request, one principal reservation and one explicit network-fee-ceiling reservation. Insufficient funds in either asset roll back all of them; same-asset amounts are aggregated. |
| REQ-002 | `PENDING_APPROVAL` has no expiration or maximum age and survives process/database restarts. |
| REQ-003 | Before approval, no unsigned blockchain transaction, allocated nonce, outgoing execution row, SIGN intent, MPC session, or SIGN permit exists for the request. |
| REQ-004 | Support `external` approval through the organization's configured API and explicitly enabled `auto_approve`; both use the same validation, persistence, and signed-decision path. Co-Signer polls the organization API only for unresolved external-mode PENDING_APPROVAL requests; no eligible work means no organization API calls. |
| REQ-005 | External PENDING, timeout, connection failure, invalid response, and authentication failure do not authorize signing or release principal automatically. |
| REQ-006 | REJECTED records a bounded reason; reject/cancel/proven pre-activation failure atomically terminalizes the request, debit ledger record and any internal credit sibling, records history, and releases both reservations exactly once. |
| REQ-007 | After approve, revalidate conditions, prepare a fresh transaction, and verify actual transaction bytes against authorized terms before MPC. |
| REQ-008 | Persist canonical signed decision bytes before publication. Retries return the same receipt and do not create another reservation or transfer. |
| REQ-009 | Preserve tenant isolation and authorization at creation, discovery, decision, cancellation, activation, and signing. |
| REQ-010 | Provide safe audit evidence without private keys, shares, encryption keys, credentials, or MPC protocol frames. |
| REQ-011 | Reserve the explicit immutable fee ceiling at creation. Fresh preparation budget must fit the ceiling; settle actual receipt fees and release unused reserved funds once. Unknown fee/ceiling is never encoded as zero. |
| REQ-012 | Preserve the public ledger transaction ID and supported transfer semantics; store approval status separately, derive execution status from outgoing, and retain ledger as the accounting status owner. |
| REQ-013 | Recover safely across duplicate delivery, concurrent decisions, multiple backend workers, process crashes, and application rollback. A committed outgoing without an MPC binding is automatically resumed by the approval worker using the same saved bytes, without client resubmission. |

## Assumptions

- ASSUMPTION-001: the supported execution scope remains existing native/token transfers on supported Ethereum/TRON networks. Arbitrary smart-contract calls are not added. Validate against existing chain-policy fixtures during implementation.
- ASSUMPTION-002: one active Co-Signer installation per organization remains the operational model documented by the repository. Validate the process lock and durable journal mount during restart/rollout tests.
- ASSUMPTION-003: backend PostgreSQL and the Co-Signer journal are durable and backed up. A local restart is supported; loss or rollback of the journal requires reconciliation, not automatic reapproval or re-signing.
- ASSUMPTION-004: external approval may be asynchronous but its API can expose an idempotent response for a stable request ID/hash. Validate with a contract stub and then the organization's integration test environment.

## Human decisions

The user approved all six v1 decisions on 2026-10-19 and explicitly confirmed that Co-Signer polls the organization API only when requests await approval. The decisions below are normative. Design approval does not change the current documentation-only task scope.

| ID | State / owner | Decision and alternatives |
| --- | --- | --- |
| DECISION-001 | Confirmed / user | Reserve principal immediately at creation. Supersedes reservation after approve. |
| DECISION-002 | Confirmed / user | Approval waiting is unlimited; never create blockchain transaction, nonce, or MPC before approve. |
| DECISION-003 | Confirmed / user | External organization API plus explicit `auto_approve`; API errors never permit signing. |
| DECISION-004 | Confirmed / user | Reject/cancel releases principal idempotently; decisions are signed, durable, isolated by organization, and auditable. |
| DECISION-005 | Confirmed / user (2026-10-19) | Reserve principal and an explicit network-fee ceiling together at creation. Fee-after-approval is removed. V1 contract: require `maxNetworkFeeAtomic` in the request; do not add an organization-policy fallback or automatic top-up. |
| DECISION-006 | Confirmed / user (2026-10-19) | Cancel PENDING_APPROVAL or APPROVED until atomic outgoing creation. Reuse the same request lock as activation; after activation return 409. PENDING-only saves one allowed state but can trap funds behind indefinite post-approval retries. Do not add cancellation of active MPC. |
| DECISION-007 | Confirmed / user (2026-10-19) | One outbound idempotent HTTPS POST from Co-Signer to the organization's endpoint, returning PENDING/APPROVED/REJECTED. Poll the same endpoint only for unresolved external-mode PENDING_APPROVAL requests; with no eligible requests, make no organization API calls. No inbound callback listener or separate status API in v1. |
| DECISION-008 | Confirmed / user (2026-10-19) | Keep original ledger ID and both journals, with immutable request source. Add outgoing.approvalRequestId and reuse one scoped ledger resolver. Adapt the existing combined reservation service to a two-case source type; do not introduce separate principal/fee reservation services. |
| DECISION-009 | Confirmed / user (2026-10-19) | Retry known temporary failures with the same holds. Terminalize proven non-execution and release holds atomically; after outgoing activation use its existing lifecycle. Unknown broadcast outcome retains funds for reconciliation. The approved over-cap treatment is terminal failure of this request, followed by a new authorization if needed. |
| DECISION-010 | Confirmed / user (2026-10-19) | One approval authorizes one transfer and, before MPC, one immutable outgoing/byte sequence/digest. No automatic replacement under that approval. Retries may redeliver the same receipt/transaction only under existing execution ownership rules. |

All six v1 decisions are approved. On 2026-10-19 the user also approved R1–R3 and S1 from the source-backed review: durable outgoing-to-SIGN recovery, atomic pre-activation ledger terminalization, fresh-pending-page polling, and derived execution status. These are normative refinements of the existing decisions, not additional product modes. This revision changes documentation only.

### Network-fee policy — selected: reserve the ceiling at creation

Approved minimal API contract: required `maxNetworkFeeAtomic`, a canonical integer string in the chain's native fee asset. Include it in the immutable request and authorization hash. An explicit zero is valid only where the supported chain policy permits it; absence is a validation error, never an inferred zero. The client supplies its chosen ceiling; defaults from mutable organization policy and a second fee mode are deferred.

At creation, reserve principal and the fee ceiling in one database transaction. For a native transfer, sum both amounts when checking the same balance. For a token transfer, lock/check token principal and native fee balances in deterministic asset order. Either both holds exist or neither does. No transaction build, nonce allocation, or SIGN work occurs to calculate the cap.

Keep three different quantities:

| Quantity | Stored in | Meaning |
| --- | --- | --- |
| Authorized fee ceiling | Request + existing fee reservation journal | Maximum authorized budget; held throughout waiting and execution. |
| Fresh prepared fee budget | Outgoing.networkFeeBudgetAtomic and its verified preparation evidence | Budget for these exact bytes, calculated after approve; must be <= ceiling. Preserve existing exact-byte budget checks. |
| Actual network fee | Validated receipt fee evidence | Amount actually spent; existing settlement posts it and releases the reservation once. |

Do not overwrite the outgoing's exact prepared budget with the larger authorization ceiling. Do not shrink/re-expand the original hold at activation; keep it unchanged through settlement. Reject/cancel and proven pre-activation failure void both journals atomically. If a valid receipt proves an on-chain failure that consumed a fee, release principal and account for that fee before freeing the unused fee reservation.

Approved v1 over-cap rule: fail with `FEE_LIMIT_EXCEEDED` before MPC and release both holds using the correct lifecycle owner; a larger ceiling needs a new request and approval. This is simpler than waiting for fee prices or topping up an old authorization. If receipt fee evidence is absent, invalid, or above the reserved cap, preserve the existing reconciliation behavior; do not invent a refund or silently debit beyond the cap. A reservation is accounting coverage, not a guarantee of future chain fees; chain-specific byte/quote verification remains required.

This choice reuses `LedgerWithdrawalReservationService.createReservation()` instead of splitting it into new public principal/fee primitives. Generalize only its source binding to `{kind: outgoing_transaction | transfer_approval_request, id}` across transaction, journals, history and pending counterparty credit. Keep its existing sorted balance locks and journal effect keys. Do not call the higher-level `OutgoingTransactionReservationService.reserve()` at creation because that builds the blockchain transaction.

Platform billing fees retain their existing contract.

## Architecture and responsibilities

```text
Client -> Backend create request + principal and fee holds [one DB transaction]
                     |
                     v
             PENDING_APPROVAL [no deadline]
                     |
          Co-Signer approval dispatcher [no SIGN permit]
                     |
          external API OR explicit auto_approve
                     |
          durable canonical decision + Ed25519 signature
                     |
          Backend apply decision [locked request + CAS]
             /                         \
     reject/cancel                   approve
     close ledger + both holds       keep both holds
                                         |
                         revalidate -> fresh preparation
                                         |
                  bind outgoing to EXISTING ledger + both holds
                  verify fresh fee budget <= authorized ceiling
                                         |
                  approval worker resumes saved CREATED outgoing
                  until durable MPC binding / outgoing failure
                                         |
                         existing SIGN claim/runtime
                                         |
                Co-Signer verify bytes + persist execution binding
                                         |
                      MPC -> existing broadcast/settlement
```

- Backend owns the request lifecycle, principal/fee accounting, decision application, tenant checks, and the unique outgoing link. The approval worker owns activation and the durable handoff of a saved CREATED outgoing into SIGN; the persisted MPC binding is its acknowledgement.
- Co-Signer owns the decision source, trusted local configuration, signed decision evidence, and independent authorization/transaction verification before releasing share B into MPC.
- The external organization API decides; it neither receives shares nor manipulates backend ledger state.
- Existing transaction services provide chain preparation and transactional SIGN creation; existing outgoing/MPC workers own execution once the MPC binding commits. Outgoing lifecycle events own refunds and settlement after outgoing activation, including a proven failure during the handoff. The approval worker never refunds an activated request directly.

The new persistent request is necessary because the current outgoing and CoSignerIntent structures already imply prepared execution. A new queue service or generic state-machine framework is not necessary.

## Interfaces and contracts

### Backend persistence

Proposed `TransferApprovalRequest` fields:

- `id`, `orgId`, `idempotencyKey`, `clientReference`, `ledgerTransactionId`.
- Immutable canonical authorization bytes/hash: wallet/account/key identity, derivation identity version, network, sender/recipient, asset/token identity, amount in atomic units, supported transfer classification, and authorized fee terms.
- `status`: `PENDING_APPROVAL | APPROVED | REJECTED | CANCELLED | PREPARATION_FAILED`. `PREPARATION_FAILED` is allowed only for a proven permanent failure before any outgoing exists.
- `createdAt`, `updatedAt`, backend activation/handoff `nextAttemptAt`, short worker lease/fencing metadata, and nullable `preparationErrorCode`; **no approval expiresAt**. Co-Signer organization-poll backoff is separate from this backend schedule.
- Applied decision receipt and key identity, or a relation to the append-only decision evidence. Decision and application disposition are separate: `APPLIED | NOT_APPLIED_CANCELLED | TERMINAL_CONFLICT`.

Add a unique, tenant-bound nullable `approvalRequestId` to outgoing; request lookup provides the existing ledger transaction. Enforce unique `(orgId, idempotencyKey)`, existing clientReference semantics, one request per principal ledger transaction, and at most one outgoing per request. Use composite tenant foreign keys or equivalent transactional checks, never an unscoped ID lookup.

Idempotency spans legacy outgoing and approval requests: before reserving new principal, resolve the organization/key and clientReference across both origins. The same organization/idempotencyKey with the same normalized terms returns the original public ledger ID; changed terms conflict. Reuse of clientReference under a different idempotencyKey preserves existing `CLIENT_REFERENCE_CONFLICT` behavior, even when the transfer terms match. Separate table constraints are insufficient: both compatible intake paths must share a short transaction-level identity guard and recheck both origins under it. Drain old writers that do not participate before enabling approval; mixed deployment must not admit competing legacy/new creates for the same identity.

The ledger transaction, `withdrawal_principal` and `network_fee_reservation` journals retain their IDs and source `transfer_approval_request:<requestId>` throughout their lifetime. Existing outgoing-origin transactions remain supported by a compatibility lookup. No fake outgoing row or fictional nonce is created to obtain a ledger identity.

### State ownership and pre-activation terminalization

Approval, execution and ledger status have separate owners; do not persist a second execution-state projection on the approval request:

| Situation | Stored request status | Execution projection | Ledger status |
| --- | --- | --- | --- |
| Awaiting decision | PENDING_APPROVAL | null | PENDING |
| Approved; preparation pending/retrying | APPROVED | null | PENDING |
| Outgoing created, signing, or submitted | APPROVED | Read outgoing.status and its failure fields | Existing outgoing lifecycle |
| Outgoing confirmed or failed | APPROVED, unchanged | Read terminal outgoing.status | Existing receipt/lifecycle settlement |
| Rejected before activation | REJECTED | null | FAILED |
| Cancelled before activation | CANCELLED | null | FAILED |
| Proven permanent preparation failure | PREPARATION_FAILED | null | FAILED |

An applied approve receipt remains immutable when the request later becomes CANCELLED or PREPARATION_FAILED. Approval does not claim successful execution. Cancellation still checks both request status and absence of outgoing, so an APPROVED request with an outgoing cannot be cancelled. API execution/error fields are resolved from outgoing; approval reason and preparationErrorCode explain pre-outgoing outcomes. No worker copies outgoing terminal status into the approval row.

Use one `TerminalizeApprovalBeforeActivationTx(request, outcome, reason, tx)` operation for REJECTED, CANCELLED, and PREPARATION_FAILED. Allowed transitions are PENDING_APPROVAL to REJECTED, PENDING_APPROVAL or APPROVED to CANCELLED, and APPROVED to PREPARATION_FAILED. Under the request lock, verify that source state, authenticated tenant, ledger/source binding, and absence of outgoing. In the same DB transaction: transition the request; transition its debit ledger record and any expected internal-transfer CREDIT sibling from PENDING to FAILED; append status history with a stable request/outcome identity; void both principal and fee journals using their existing PENDING-to-VOIDED CAS and balance deltas. Preserve exact external rejection/cancellation/preparation reasons in the approval projection and history; no new ledger enum is required.

Reuse `LedgerTransactionStore.transitionTransactionStatus` and `InternalTransferCreditLifecycleService.applyTerminalToCredit` after a tenant-scoped lookup by the immutable request source. Validate sibling account/wallet/asset/amount against the saved internal-transfer classification. A missing expected ledger record, sibling, or journal is an integrity failure: roll back the whole operation; do not silently skip it. A legitimate external transfer has no credit sibling. Never post recipient credit here or synthesize an outgoing lifecycle event. Exact repeated terminalization returns the original outcome with no extra history or deltas; conflicting outcomes do not mutate it.

### Public transaction API

Reuse `POST /api/v1/transactions`: validate terms and explicit fee ceiling, atomically reserve both amounts/create request, then return the existing ledger detail representation. Keep the stable public `id`; add a nullable approval projection (`requestId`, state, applied decision source, rejection reason and preparationErrorCode). Ledger remains PENDING while waiting; execution-specific fields remain null. After activation, expose execution status/error by resolving the outgoing, without copying them into approval persistence.

`GET /transactions/:id` and list results must resolve both approval-origin and legacy outgoing-origin records. Do not implement the old controller lookup through a nonexistent outgoing.

Proposed cancellation endpoint: `POST /api/v1/transactions/:id/cancel`, authenticated and authorized for the organization's withdrawal management. Its exact RBAC scope must follow the existing role model during contract approval. Success requires an atomic PENDING_APPROVAL/APPROVED-to-CANCELLED transition with no activated outgoing and release of both holds; repeated cancellation returns the same outcome. An activated outgoing returns 409 without releasing funds. If approval had already been applied, preserve that receipt and append the cancellation event rather than rewriting the receipt or its original application history.

### Co-Signer platform API

Proposed org-scoped `GET /co-signer/approval-requests` returns only currently PENDING_APPROVAL requests with immutable details. Use stable keyset ordering `(createdAt, id)`, a server-issued fixed upper bound for each sweep, and an opaque continuation cursor bound to the organization and sweep. Every page rechecks authenticated tenant and pending status. Advance within that bound even when local backoff suppresses every returned item; at the end, start a new sweep on a later bounded discovery tick. New arrivals beyond the bound enter the next sweep, so arrivals and indefinitely pending first-page items cannot starve older or later-page work. Empty pages/end-of-sweep are not cancellation notifications.

`POST /co-signer/approval-requests/:id/decision` accepts a signed decision envelope. Read-only discovery does not claim a SIGN intent or create a server-side polling lease. Existing Ed25519 HTTP authentication and scope enforcement are reused. No separate terminal-status endpoint is required for polling eligibility.

Decision response distinguishes `ACCEPTED`, `EXACT_REPLAY`, `NOT_APPLIED_CANCELLED`, and `TERMINAL_CONFLICT`; the exact HTTP/status schema must be shared by fixtures. A repeat of a decision originally accepted remains an exact replay even after execution advances. The same request identity with different canonical decision bytes conflicts. Preserve rejected/late evidence without applying it or rewriting an earlier signed receipt.

### Organization API and configuration

One outbound idempotent HTTPS `POST` to a configured endpoint sends immutable `orgId`, `requestId`, `authorizationHash`, and authorization terms. The API returns those bindings plus `PENDING | APPROVED | REJECTED`; REJECTED must include a bounded reason code/text. Repeating the same POST polls the decision; the organization must keep final responses stable for that request/hash. There is no long-held HTTP request. PENDING is a valid response; transport/auth/schema errors are retryable delivery failures, never an approval. No separate webhook server, polling-status endpoint, transport plugin framework or mTLS setup is required in v1.

Polling is driven by pending work:

- Co-Signer discovers its organization's PENDING_APPROVAL requests from the platform backend, including after restart. This backend discovery is separate from calls to the organization's API.
- Every new external attempt must originate from a request returned in a fresh authenticated pending page. Schedule at most one attempt per item in the current bounded batch. A local admission/backoff record can suppress that attempt, but cannot schedule another POST on its own. No eligible work means zero organization API calls; bounded backend discovery may continue.
- PENDING and transport/auth/schema errors set Co-Signer-local backoff, then return the item to backend discovery rather than a local retry queue. Another attempt needs both a fresh pending item and a due local delay. This backoff is advisory/suppressive and bounded; it may reset after restart without changing pinned admission or final decisions. Waiting has no deadline and retains both holds.
- Once a final APPROVED/REJECTED decision is durably saved, stop organization polling for that request. If publication to the backend fails, resend the saved signed receipt rather than asking the organization again.
- A cancelled/finalized request stops appearing in fresh pending pages, so local records cannot keep it polling. During backend discovery failure, start no new external attempts from cached records. Do not infer a terminal state from absence in a page. Cancellation after discovery may race with the already selected/in-flight bounded batch; the backend non-applied-decision rules prevent execution or revival. Never persist a queue of speculative future organization calls.
- In auto_approve mode, use the same durable decision path without calling the organization API.

Use HTTPS with one dedicated integration bearer credential in v1; do not forward the platform API credential. Endpoint selection is trusted configuration, not payload. Reject redirects to a different authority and enforce request/response size limits. Cryptographic organization-response signatures are only needed if independent non-repudiation by the organization is a product requirement; the Co-Signer receipt attests what the Co-Signer accepted from its authenticated integration.

Proposed `CO_SIGNER_APPROVAL_MODE=external|auto_approve` is explicit; missing/invalid configuration prevents admission of new approval work. External mode requires valid endpoint/auth configuration. Pin mode and config revision in a durable local request-admission record before the first evaluation; retries never reinterpret an already admitted external request as auto. Enabling auto for previously unseen backlog must be an explicit reviewed operational action, not an error fallback. Existing final decisions are immutable across mode changes.

### Signed decision and execution binding

Decision fields: `version`, domain/audience/environment, `orgId`, `requestId`, `authorizationHash`, `decision`, reject reason, `source`, external decision reference if present, `decidedAt`, `signingKeyId`, and pinned policy/config revision. Atomic amounts are decimal strings. Canonicalize with strict JSON/JCS, reject duplicate or unknown fields, and sign a domain-separated envelope with the Co-Signer Ed25519 identity. No approval-expiry field is required; `decidedAt` is audit time.

Persist exact envelope bytes/signature before delivery; HTTP authentication may refresh timestamps/nonces. Reuse the existing JCS dependency and standard Go crypto, not a new cryptographic protocol. Audit verification retains public verification keys; revoked keys must not be allowed to introduce new executable decisions merely by backdating their receipt.

Extend final SIGN claim with the original signed authorization receipt, tenant/request/outgoing bindings, exact unsigned bytes, payload encoding/type, and the expected digest. Before MPC, independently decode the supported transaction form, reconstruct digest, and compare all authorized economic terms and fee constraints. Native transfers must reject unexpected contract/data behavior; token transfers must validate contract, method, recipient, and amount.

Persist create-only local `ExecutionBinding(orgId, requestId, outgoingId, authorizationHash, executionDigest, payloadType, hashAlgorithm)` before MPC. A different outgoing or digest cannot reuse the same authorization. Existing ownership/recovery rules still govern same-digest retries; a journal does not make an orphaned MPC protocol resumable.

## Domain invariants

| ID | Invariant |
| --- | --- |
| INV-001 | An accepted pending request has exactly one principal hold and one fee-ceiling hold matching immutable owner/assets/amounts. |
| INV-002 | Principal is not spent/posted merely because it is reserved or approved; available = posted - reserved. |
| INV-003 | No time-based approval expiry, auto-release, raw transaction, nonce allocation, MPC session, or long-held resource exists while PENDING_APPROVAL. |
| INV-004 | Request creation, both journals, projection deltas, and dispatch visibility commit or roll back together. |
| INV-005 | Pre-activation reject/cancel/permanent failure, debit/expected-credit FAILED statuses, history and both reservation releases commit together; the reservation-status CAS winner alone changes reserved balance. |
| INV-006 | Approval preserves both original holds. Activation never releases/reacquires them or reserves principal/fee a second time. |
| INV-007 | One request binds at most one outgoing and one immutable execution digest; replay does not create a second transfer. |
| INV-008 | Signing requires an applied approve, valid local receipt/binding, valid organization/key context, and verified transaction bytes. |
| INV-009 | External errors, malformed data, missing journal durability, and unauthorized mode changes never permit signing. |
| INV-010 | Decision bytes/signature/time/reason are immutable; transport retry metadata is not business-decision identity. |
| INV-011 | No cross-organization request, hold, decision, key, outgoing, or ledger relation is accepted. |
| INV-012 | Explicit authorized fee cap, exact prepared fee budget, and actual receipt fee are distinct. Unknown is not zero; settlement never consumes principal or fee twice. |
| INV-013 | No release on an ambiguous broadcast or a cancel that lost to outgoing activation. |
| INV-014 | Pending approval is durable data, not a permanent DB transaction, worker lease, socket, or MPC permit. |
| INV-015 | The original public ledger transaction ID and its audit lineage survive activation and settlement; outgoing is the only stored execution status owner. |

## Data and failure flows

1. **Create:** authenticate; normalize transfer terms and required fee ceiling; resolve supported classification and cross-origin idempotency. In a short DB transaction create the approval request, reuse the combined ledger reservation with the request source, reserve principal plus fee cap, record audit and make the request discoverable. Replays return the original public ID. No chain-building RPC occurs here.
2. **Wait:** retain durable request and both holds indefinitely. Co-Signer discovers pending work from the backend and polls the organization API only for unresolved, due external-mode requests, independent of SIGN permits. No eligible requests or auto_approve mode means zero organization API calls. A saved final decision is redelivered without polling again. Each HTTP attempt has a timeout and jittered backoff; there is no maximum approval age or terminal retry count that releases funds. Every new attempt is selected from fresh pending discovery; local records alone never start an external call.
3. **Decide:** external API or explicit auto mode produces one durably frozen signed receipt. Backend verifies its bindings and locks the request. Approve changes PENDING_APPROVAL to APPROVED and makes its backend nextAttemptAt due in the same transaction, without touching either hold. The durable request row is the activation queue; no extra execution-command outbox consumer is required. Reject runs TerminalizeApprovalBeforeActivationTx to close the request, ledger debit/expected credit, history and both holds atomically.
4. **Cancel:** allow PENDING_APPROVAL and APPROVED until the outgoing activation transaction commits. The same request lock serializes cancel, approval and activation. Cancel uses the same terminalization operation to close debit/expected credit and history together with both releases; late approval remains non-applied evidence. An already applied receipt remains immutable if cancellation follows. Prepared uncommitted candidates are discarded if cancel wins. Once outgoing exists, return 409; no active-MPC cancellation protocol is added.
5. **Prepare/activate:** claim APPROVED work with a short fenced lease. Revalidate wallet/key, withdrawals, chain/sender conditions and both exact holds. Prepare fresh bytes and budget outside locks. Commit only after rechecking request state/lease/freshness, prepared budget <= cap, and hold identities. Create one outgoing linked to the existing request/ledger and keep the request APPROVED and due for handoff. This commit itself provides durable worker visibility; audit outbox events are informational, not the SIGN-start mechanism. The approval worker then starts SIGN from the saved outgoing as specified below. Do not call createReservation or assert another free principal+cap; the funds are already held.
6. **Sign:** retain the normal permit-before-claim ordering. Independently verify the original approval, exact bytes/digest and budget; durably bind one outgoing/byte sequence/digest before MPC. The external API is never awaited under a SIGN permit.
7. **Settle:** resolve outgoing events to the original ledger row. Confirmation posts principal once; validated receipt fee is settled and its hold released once. Proven non-execution voids both holds. On-chain failure can still spend network fees. Ambiguous broadcast and missing/invalid fee evidence keep the relevant holds for existing reconciliation.

### Durable outgoing-to-SIGN handoff

The planned approval worker scans two due populations with short fenced leases: (a) APPROVED requests without outgoing, for preparation/activation; (b) APPROVED requests whose linked outgoing is CREATED and has no mpcSessionId, for SIGN handoff. The second population remains eligible after any crash following outgoing commit. Scope both selections to the same tenant/request relation; use separate bounded quotas or fair ordering so new preparation cannot starve committed handoffs.

For (b), re-read the saved outgoing and current wallet/key/policy context; call the existing `OutgoingTransactionSigningService.start` with the same outgoing ID and saved prepared bytes. Do not run the create/reserve/build path, allocate a new nonce, or rebuild an expired transaction. The signing service transaction creates the SIGN aggregate, binds mpcSessionId through the existing CAS, and publishes its existing lifecycle events. That durable aggregate/binding, not an HTTP acknowledgement or an in-memory flag, ends the handoff. After a crash with a committed binding, discovery excludes the row and the current MPC/outgoing lifecycle continues; it does not start another MPC session.

Temporary pre-binding failures retain both holds and reschedule the same linked outgoing. Re-read persistent binding/status after an uncertain call result before doing anything else. A proven non-execution failure while the outgoing is still CREATED uses `OutgoingTransactionFailureService.markFailedAndEnqueue` with a CREATED-only conditional transition (or the signing service’s existing equivalent); its OutgoingFailed lifecycle event owns ledger terminalization and refunds. A competing successful SIGN binding must make that failure transition lose. Never invoke the pre-activation refund operation after an outgoing exists. Inconsistent aggregate/binding state requires integrity reconciliation with holds retained. Existing outgoing workers do not currently select CREATED; this approval-worker handoff is explicit new planned behavior.

```text
APPROVED + no outgoing -- prepare / unique commit --> APPROVED + outgoing CREATED
       cancel allowed                                    cancel returns 409
                                                               |
                                         approval worker reselects after crash
                                                               |
                                  same saved bytes -> transactional SIGN binding
                                                               |
                                  current MPC / outgoing execution and settlement
```

### Minimal failure policy

| Situation | Action | Reservation owner |
| --- | --- | --- |
| Before activation: RPC timeout/5xx, temporary provider outage, sender busy | Retry with backoff; keep APPROVED and both holds; cancel remains available. | Approval worker; short transactions only. |
| Before activation: disabled/revoked key, permanently invalid approved terms, fresh budget above cap | PREPARATION_FAILED; atomically close debit/expected credit, history and both holds after checking no outgoing won activation. | Approval service under the request lock. |
| Outgoing CREATED without MPC binding: safe transient SIGN-admission error | Approval worker retries existing signing service with the same saved outgoing/bytes; reread binding after uncertain result. | Approval worker owns handoff; outgoing lifecycle alone owns refunds. |
| After persisted MPC binding: safe transient execution error | Existing MPC/outgoing paths retry only where already supported, with the same identity. Orphan MPC restart rules are unchanged. | Existing MPC/outgoing lifecycle. |
| After activation: proven non-execution / expired bytes that are definitely unsubmitted | Existing lifecycle marks failure and voids unspent holds. No rebuild under the same approval. | Existing outgoing lifecycle, not an approval-worker refund. |
| Confirmed on-chain failure | Release principal; settle actual fee; free unused fee reservation when evidence is valid. | Existing receipt settlement. |
| Timeout after possible broadcast, unknown outcome, missing/invalid/over-cap fee evidence | Do not infer non-execution or refund; reconcile existing execution. | Existing reconciliation. |

Use existing error codes/outcome classes plus a small explicit allowlist for retryable preparation errors, `nextAttemptAt` and backoff. Do not introduce a generic retry framework or WAITING_FOR_FEE state. Unknown errors never count as proof of safe release. Before one outgoing is committed, stale unsigned preparation candidates may be discarded/reprepared. After the committed byte/digest binding, neither changed nonce/TAPOS nor an expired transaction permits another transfer under the old authorization.

## Security and authorization

Derive tenant identity from authenticated credentials and trusted Co-Signer configuration. Check request/ledger/wallet/key ownership at every lookup and before binding; never trust payload `orgId` alone. All raw-sign entrypoints must require the authorization binding for approval-enabled organizations; the historical empty-claim/direct-sign path cannot be a bypass.

The Co-Signer journal uses a separate private durable directory, single-writer locking, create-only atomic publication, file fsync and directory fsync. Do not store approval evidence among key-share artifacts or change their custody format. Loss of durable evidence disables affected signing until reconciliation; it is not an invitation to recompute a decision.

Audit stores IDs, authorization hashes, decision source/reason, public-key identity, application disposition, ledger transitions, execution binding, and safe error codes. Do not dump raw external responses, credentials, private keys, shares, encryption keys, or MPC frames to logs. Sensitive business fields belong in access-controlled records, not metric labels. Bound reason length and sanitize log rendering. API errors remain operational errors rather than invented organization rejection reasons.

## Transactions and concurrency

Principal reservation is a durable accounting entry, not a DB row lock held for days. Use short transactions and the existing balance-locking pattern. Establish one lock order for request/ledger/balance updates and deterministic asset ordering when principal and fee use different assets.

- Duplicate create: unique organization/idempotency identity plus exact immutable fingerprint comparison; `skipDuplicates` alone is insufficient.
- Competing distinct requests: locked balance check plus projection update prevents oversubscription.
- Approve versus reject: request row lock and conditional state update decide one applied decision. Cancel versus approval may produce either CANCELLED with non-applied late approval or APPROVED followed by CANCELLED; neither sequence may activate after cancellation. Cancel versus activation has one winner; release happens in the same transaction as a winning cancel/reject.
- Activation: unique request-to-outgoing relation plus fenced lease makes concurrent/restarted workers converge on the same outgoing. A stale worker cannot commit prepared bytes after another worker progressed.
- Preparation may produce discarded unsigned candidates after approval. A nonce read is not durable allocation; existing sender/nonce guards and the unique outgoing commit determine which candidate owns it. Discarded candidates are never submitted for MPC.
- Revalidation must recognize both existing holds; do not call `available >= principal + fee` again. Compare the active journals to the immutable request and ensure fresh prepared budget <= the held fee cap. No top-up or second balance delta is part of activation.
- Before activation, one request-locked transaction terminalizes debit/expected credit/history and both journals; after activation, only outgoing lifecycle handles refunds. Release uses PENDING-to-VOIDED CAS and one reserved delta per principal/fee journal, not an unconditional balance increment. The audit records the reason without duplicate compensating money.
- Local decision/execution file conflicts compare exact bytes and return a conflict; never overwrite a winning binding. Crash between file publication and backend ACK resends the persisted receipt.

## Migration, compatibility, and recovery

Use additive, forward-only migrations for approval records and the optional outgoing link. Install the shared intake identity guard on both new and legacy paths before capability enablement; replay of a pre-enable request must never create another principal hold. Do not weaken Ethereum preparation/nonce constraints or convert existing prepared transactions into waiting approvals.

Keep legacy ledger lookups for rows without an approval link. Update outgoing lifecycle consumers, query projections, reconciliation snapshots, internal-transfer credit lookup, and chain-review mutations to resolve the approval-origin ledger row. In particular, `outgoing-transaction.store.ts:405` and `application/worker/broadcasted-transaction.processor.ts:142` currently set `chainReviewRequired` through the outgoing source identity; both must use the scoped approval/legacy resolver so changed receipts still block subsequent spending. Preserve internal-transfer debit/credit classification and settlement; do not credit a recipient merely because principal was held or approved.

Deploy backend schemas and disabled request/decision paths first; deploy Co-Signer protocol support and durable journal configuration; verify both sides' capabilities; then enable selected organizations. Creation on an enabled organization always creates an approval request, including auto mode. Existing direct SIGN/legacy create routes must not bypass it. Old Co-Signers must fail closed for new approval-required work. Unknown wire fields require a versioned claim/fixture update because current decoders are closed.

Application rollback must retain a compatible drain/reject/cancel path for held requests. Disabling intake does not release holds, auto-approve, or send queued requests through the old direct-sign path. Never roll back to a version that can execute approval-origin records without verifying approval. Preserve the additive schema. Restore PostgreSQL and local journal together or reconcile bindings before resuming.

## Rollout and operations

Roll out behind an organization capability gate; initially use synthetic funds in an isolated local/staging test. Observe pending count/age, held principal by asset, decision API errors, delivery retries, unbound accepted decisions, activation lease recovery, and ledger inconsistencies. Request/organization IDs are not metric labels.

Unlimited waiting is not an error. Existing reconciliation's default 24-hour `stale_pending` classification must distinguish intentional approval waits without suppressing genuine posted/on-chain mismatches. Alerts can report long-held requests for operators but cannot expire them. No age-based cleanup of live requests/holds or automatic deletion of replay protection is allowed.

An intentional hold is an in-platform balance reservation, not on-chain escrow. Funds moved outside the platform or changed key/chain conditions can still prevent execution after approval. Surface the reason and use the defined failure owner, never pretend the transfer completed.

Use fixed-bound repeatable keyset sweeps with an index supporting organization/status/createdAt/id; backend due-work indexes cover activation and CREATED handoff. Bound page size, worker concurrency, and suppressive Co-Signer backoff memory. Never accumulate all pending requests or one goroutine per request. Reset/eviction of backoff cannot bypass fresh-page eligibility or regenerate immutable decisions. Short worker leases may expire to permit recovery; their expiry never expires the underlying approval. Configuration changes and any decision to apply auto mode to unseen backlog must be auditable operational actions.

Stop rollout for duplicate reserve/release, unauthorized signing, mismatched bytes, missing journal durability, silent bypass, or unexplained hold loss. Existing images/release channels are reused; no new distributable service is introduced.

## Test strategy

Use unit tests for canonicalization, validation, state rules, and atomic amount arithmetic; real PostgreSQL concurrency tests for holds/decisions/adoption; filesystem crash/reopen tests for decision journals; Go/backend shared fixtures for signed decisions and raw transaction verification; coordinated integration tests for approval through signing and settlement.

```text
create -- insufficient principal --> no request/hold           [DB]
  |
  +-- concurrent/replayed create --> one public ID + hold       [DB]
  |
pending -- 30/365 days + restart --> same hold; no execution    [E2E, fake clock]
  +-- external errors/PENDING --> still pending                [UNIT + E2E]
  +-- reject/cancel/race --> FAILED debit/credit + both releases [DB]
  +-- paging/cancel/outage --> fresh-only calls, fair sweeps     [Go + fake clock]
  +-- late signed approve --> audited, not applied              [DB + contract]
  +-- external/auto approve --> same durable receipt pipeline  [contract + E2E]
  |
activation -- crash/race --> one outgoing + eventual handoff   [DB + E2E]
  +-- crash after SIGN binding --> existing session, no restart [DB + E2E]
  +-- fee cap --> cap held once; fresh budget fits; settle once [DB + chain stub]
  +-- raw/digest/tenant substitution --> no MPC                 [Go vectors + E2E]
  |
execution -- replay --> one transfer                          [E2E]
  +-- receipt/failure --> post/void principal once              [DB + E2E]
  +-- ambiguous broadcast --> retain hold, no replacement      [regression]
```

Detailed named tests and requirement mappings are in the companion plan. No tests are claimed to pass merely because they are specified.

## Acceptance criteria

- AC-001 (REQ-001, INV-001/002/004): creation reserves principal and fee cap immediately; duplicate/concurrent submissions have one ledger ID and one balance delta; insufficient funds roll back everything.
- AC-002 (REQ-002/003, INV-003/014): after 30 and 365 simulated days plus process restarts, the same pending request and hold remain, with zero raw transactions, nonce allocations, SIGN intents/sessions, and consumed SIGN permits.
- AC-003 (REQ-004/005, INV-009): external and explicit auto modes produce signed decisions; unavailable or invalid external responses never trigger signing or release. Idle and auto_approve make zero organization API calls; final-decision retries reuse the persisted receipt, and restart resumes polling only from fresh pending pages. Cached local records cannot poll cancelled work or bypass a discovery outage; bounded sweeps revisit old and later-page pending items under continuous arrivals.
- AC-004 (REQ-006/013, INV-005/013): reject/cancel retries and competing approve/cancel have one monetary effect, including the fee-ceiling reservation; debit/expected-credit statuses and their histories become terminal in the same transaction. Permanent pre-activation failure follows the same rule. Late approve cannot revive cancelled work.
- AC-005 (REQ-007/008, INV-006/007/008/010): approval preserves the same hold; activation and local binding admit one outgoing/digest; a crash after outgoing commit automatically resumes the same saved bytes without client resubmission, until durable MPC binding or proven terminal failure. Lost acknowledgement after binding cannot create another session. Substituted bytes fail before MPC.
- AC-006 (REQ-009/010, INV-011): cross-tenant/key/request substitutions fail; audit contains decision and reserve/release evidence without sensitive key material.
- AC-007 (REQ-011, INV-012): principal and fee cap commit atomically, fresh prepared budget is checked against the cap, and valid receipt settlement frees unused funds once; a missing cap is never a zero placeholder.
- AC-008 (REQ-012/013, INV-015): public IDs, cross-origin legacy replay, chain-review spending guards, internal-transfer settlement, restarts, and mixed-version fail-closed behavior are verified. Request stays APPROVED after activation; execution status/errors come only from outgoing, with no mirrored completion writes.

## Residual risks

- RISK-001: fee timing, required request cap and over-cap failure are approved; chain-specific fee evidence can still require reconciliation and must not be treated as guaranteed refunds.
- RISK-002: independent unsigned transaction parsers are not currently provided by Co-Signer/MPC Core. Parser/dependency selection and hostile-vector validation are substantial implementation work, not a metadata comparison.
- RISK-003: unlimited holds can retain customer funds indefinitely. Explicit cancellation before activation and accurate operational visibility are required; no silent time-based release is permitted.
- RISK-004: preserving original ledger source lineage requires adapting every relevant read/settlement consumer. Missing one could leave a hold unsettled; query and reconciliation integration tests are mandatory.
- RISK-005: signed decisions attest Co-Signer action; independent proof that an organization authored a response requires its own signing key/contract.
- RISK-006: local journal loss/rollback and an uncertain broadcast require recovery, not automatic re-signing. This design does not claim Byzantine-backend resistance through a freely replayable semantic approval.
- RISK-007: revisions are local evidence only; current production versions/configuration and actual organization API contracts have not been verified.

## Structural readiness

The design uses `feature-artifact-contract` (DESIGN); all mandatory categories are present and the six v1 decisions were approved by the user on 2026-10-19. Pending-only organization polling is recorded in REQ-004, DECISION-007, the organization API contract, the wait flow, and AC-003. No product decision among those six remains open. A Git revision still needs to be pinned for plan execution. R1–R3 and S1 were explicitly approved and incorporated on 2026-10-19; see the companion plan for repeat-review evidence. This is authoring/decision evidence, not implementation, executed feature tests or deployment evidence.
