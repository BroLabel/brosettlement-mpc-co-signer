# Co-Signer Approval Workflow Implementation Plan

Updated: 2026-10-19 — user-approved R1–R3 and S1 incorporated; repeat source-backed engineering review completed with no remaining findings. All six v1 product decisions remain unchanged.

> **For agentic workers:** When implementation is requested, use `superpowers:subagent-driven-development` or `superpowers:executing-plans` task by task. The six design decisions are approved; pin the approved design revision before execution. This turn records documentation only. Checkbox steps describe future work, not work performed in this task.

**Goal:** Immediately reserve transfer principal and an explicit network-fee ceiling, support durable unlimited external/automatic approval, and execute at most one fresh, verified transfer without duplicate reserves or releases.

**Architecture:** Add a backend-owned approval request linked to the original ledger transaction and its principal/fee journals. Co-Signer evaluates requests outside SIGN admission and persists signed decisions and execution bindings. On approval, backend creates a fresh outgoing that adopts both existing holds. The approval worker durably resumes its CREATED-to-SIGN handoff until a persisted MPC binding exists, then the current MPC/broadcast lifecycle continues. Execution status is derived from outgoing; pre-activation terminalization closes ledger records and both holds together.

**Tech Stack:** Existing NestJS/TypeScript, Prisma/PostgreSQL and ledger/outbox infrastructure; Go 1.24+, existing JCS dependency, standard Ed25519 and durable local files. Parser dependencies require the bounded evaluation in PLAN-STEP-006; no dependency was installed for this plan.

**Spec:** `docs/superpowers/specs/2026-10-11-co-signer-approval-design.md` in the Co-Signer repository.

## Global constraints

- Documentation only has been authorized; no production implementation, test execution, migration, release, or deployment is part of this task.
- Principal and the explicit fee ceiling are reserved atomically at request creation; activation does not release/re-reserve, top up or shrink either hold.
- PENDING_APPROVAL has no expiration or maximum age; before approve there is no raw transaction, nonce allocation, outgoing execution row, SIGN intent/session, or SIGN permit.
- External API errors never authorize signing. Auto approval must be explicitly configured and recorded. Each new external call requires a freshly discovered, unresolved, due PENDING_APPROVAL item; cached local records cannot schedule calls during discovery failure or after cancellation. Idle and auto_approve produce zero organization calls.
- Fee timing DECISION-005 is selected: cap at creation. Use the required request cap approved in the DESIGN. Keep authorized cap, exact prepared budget and actual receipt fee distinct; no automatic top-ups or fee-after-approval mode.
- Preserve tenant isolation, existing public ledger IDs, immutable decisions, and existing MPC/key-share custody.
- Request status is PENDING_APPROVAL | APPROVED | REJECTED | CANCELLED | PREPARATION_FAILED. After outgoing creation it stays APPROVED; execution status/error comes only from outgoing. PREPARATION_FAILED requires no outgoing.
- Before outgoing creation, terminal request/debit/expected-credit transitions, history and both hold releases are one transaction. After outgoing creation, the existing outgoing lifecycle is the sole refund owner, including handoff failure.
- New public/config/wire contracts must be versioned/documented and tested on both sides; old paths cannot bypass approval for enabled organizations.
- Commit messages use Conventional Commits with DCO sign-off when implementation is eventually authorized.

## Design reference and readiness

This plan follows the v1 design decisions approved by the user on 2026-10-19, including pending-only organization API polling, and the subsequently approved R1–R3/S1 review corrections. The approved document has not yet been pinned to a Git commit; do not substitute the source baselines below for that design revision. Implementation has not been requested in this documentation update.

- Co-Signer source baseline: `5df5ac25953debeb94f2c37d89a2ad458a73057c`.
- Backend source baseline: `e14e175ff5fb2974907bcd5efd123d20425a5001`.
- Before execution, PLAN-STEP-000 records the Git revision of the already approved design; no repeat approval of the six decisions is required. If later work needs a new design-level decision, use `DESIGN_REOPEN_REQUIRED` with affected IDs rather than silently inventing it here.

Inventory: REQ-001 through REQ-013; INV-001 through INV-015; ASSUMPTION-001 through ASSUMPTION-004; DECISION-001 through DECISION-010; RISK-001 through RISK-007. The requirement mapping and decision dispositions below cover these sets.

## Responsibility map

All `backend:` paths are relative to `/Users/vlas/Work/BroLabel/back-end`; all `cosigner:` paths are relative to `/Users/vlas/.codex/worktrees/2c85/mpc-co-signer`. Exact new filenames are proposed; existing paths were inspected. Keep normal repository/module DI boundaries.

| Owner / files | Responsibility and dependencies |
| --- | --- |
| backend: `packages/database/prisma/schema.prisma`, a new additive migration | Approval request, unique tenant/idempotency/ledger/outgoing bindings and due-work indexes; no changes to Ethereum preparation constraints. |
| backend: new `apps/client-api/src/transactions/approval/transfer-approval.store.ts`, `.service.ts`, `.worker.ts` | Atomic request/decision/cancel lifecycle, pre-activation terminalization, and due selection for both no-outgoing activation and linked CREATED/no-MPC handoff; depends on existing ledger and transaction services, not on MPC internals. |
| backend: `apps/client-api/src/ledger/application/withdrawals/ledger-withdrawal-reservation.service.ts`, `ledger-withdrawal.service.ts`, `ledger-withdrawal-fee.service.ts` | Reuse combined createReservation with a two-case source type and shared idempotent release of both holds; route settlement to the original ledger row. |
| backend: `apps/client-api/src/ledger/infra/ledger-transaction.store.ts`, `ledger-journal-entry.store.ts`, `ledger-balance-projection.store.ts` | Scoped immutable replay checks, hold resolution and transactional monetary effects. |
| backend: `apps/client-api/src/transactions/application/create-outgoing/`, `infra/outgoing-transaction.store.ts`, `application/worker/broadcasted-transaction.processor.ts` | Create approval before preparation; activate approved requests by adopting both existing holds, not calling legacy full reservation again; preserve chain-review mutations using the same ledger resolver. |
| backend: `apps/client-api/src/transactions/transactions.controller.ts`, `dto/transaction-read-response.dto.ts`, `transaction-response.mapper.ts`, `transactions.swagger.ts` | Stable ledger ID at creation, visible approval/execution state, authorized cancel, compatibility documentation. |
| backend: `apps/client-api/src/ledger/application/transactions/ledger-transaction-query.service.ts`, `ledger-transaction-read-model.util.ts`, `infra/ledger-transaction-read.store.ts` | Read approval-origin records before an outgoing exists and after activation without changing public ID. |
| backend: `apps/client-api/src/ledger/application/withdrawals/internal-transfer-credit-lifecycle.service.ts`, `infra/ledger-reconciliation-snapshot.store.ts`, `domain/ledger-reconciliation.rules.ts` | Preserve credit/settlement routing and reconciliation for approval-origin holds; no recipient posting during approval wait. |
| backend: `apps/client-api/src/mpc/co-signer/http/co-signer.controller.ts`, new `approval-request.controller.ts` and DTOs; `use-cases/claim-intent/claim-sign-intent.service.ts` | Authenticated approval discovery/receipt endpoints and versioned prepared-claim contract. |
| backend: `apps/client-api/src/mpc/application/wallet-signing/start-wallet-sign.use-case.ts` and its callers | Enforce approval binding at execution entry; close alternate direct-sign bypasses for enabled organizations. |
| cosigner: new `internal/approval/{types,decision,store,dispatcher,external}.go` and tests | Closed contracts, canonical receipt/signature, durable admission/decision/execution records and bounded external polling; no share access. |
| cosigner: new `internal/monolith/approval.go`; existing `types.go`, `claim.go` | Platform request/receipt transport and strict v2 claim decoding. |
| cosigner: new `internal/signingpolicy/{prepared,ethereum_prepared,tron_prepared}.go` and tests | Verify exact transaction bytes/digest and allowed economic behavior. Existing metadata validation remains shared. |
| cosigner: `internal/worker/session_worker.go`, `sign_session.go`; `internal/config/config.go`; `cmd/co-signer/{resources,lifecycle}.go` | Require verified receipt/binding before MPC and wire a separate approval dispatcher with explicit configuration. |
| Both: module wiring, contract fixtures/producers/verifiers, test runners, README/API docs/runbooks and existing CI/release workflows | Connect and distribute the feature; fixtures must identify the actual producer revision. Do not create a second release artifact. |

## Interfaces used by the plan

These names describe planned interfaces, not existing exports:

- `CreateApprovalRequest(input, idempotencyKey)` validates the required `maxNetworkFeeAtomic`, returns the stable ledger ID, and commits request + principal/fee reservations atomically.
- Existing `LedgerWithdrawalReservationService.createReservation(input, tx)` remains the sole reservation operation. Extend only its source argument to `{kind: outgoing_transaction | transfer_approval_request, id}`; request cap becomes `nativeFeeReservation.amountAtomic`. Preserve exact replay checks, sorted locks and effect keys. Do not add split reservation APIs.
- `TerminalizeApprovalBeforeActivationTx(request, outcome, reason, tx)` verifies tenant/source/absence of outgoing and atomically transitions the request, debit and expected internal CREDIT sibling to the defined terminal states, records status history, and releases both holds using existing journal CAS/delta primitives. The ledger mapping is FAILED; the request preserves REJECTED/CANCELLED/PREPARATION_FAILED and the precise reason. Missing expected records are integrity errors; exact repeats have no extra effects.
- `ApplyApprovalDecision(scopedRequest, signedReceipt)` returns `ACCEPTED | EXACT_REPLAY | NOT_APPLIED_CANCELLED | TERMINAL_CONFLICT`.
- `CancelApprovalRequest(scopedLedgerTransactionId)` competes with activation under the request lock and follows DECISION-006.
- `ActivateApprovedRequest(scopedRequestId, leaseToken)` commits one outgoing using the original ledger row and both holds, while keeping the request APPROVED and due for handoff. The durable row, not an extra command queue, schedules work.
- The planned approval worker resumes a linked CREATED outgoing with no mpcSessionId through existing `OutgoingTransactionSigningService.start`, using saved bytes and the same ID. A persisted MPC binding acknowledges handoff; post-commit crashes cannot require client resubmission. Proven CREATED failure uses the existing conditional outgoing failure/outbox path, never the pre-activation refund operation.
- Public approval/preparation fields come from the request; execution status/error comes from outgoing. No request EXECUTING/COMPLETED/post-activation failure mirror or synchronization consumer is added.
- Co-Signer `DecisionStore` persists request admission, exact decision receipt, and create-only execution binding; no generic workflow storage API is required.
- Co-Signer `VerifyPreparedTransaction(authorization, claim)` verifies unsigned bytes and yields a validated digest/execution binding for the existing SIGN worker.

## Requirement-to-step mapping

Every completion reference below means captured output from the stated test, not an assertion that it has run.

| Design items | Owner | Steps | Tests | Completion evidence |
| --- | --- | --- | --- | --- |
| REQ-001; INV-001/002/004 | backend approval + ledger | 001, 002 | 001–004 | PostgreSQL row counts, original journal ID, balance deltas, rollback proof |
| REQ-002/003; INV-003/014 | backend worker + Co-Signer dispatcher | 002, 004, 008 | 005, 006, 022 | Simulated 30/365-day wait and restarts; zero raw/nonce/session/permit observations |
| REQ-004/005; INV-009 | Co-Signer approval/config + backend receipt gate | 003, 004 | 007–009, 021 | External/auto/error and configuration tests |
| REQ-006; INV-005/013 | backend lifecycle + ledger release | 002, 003, 005 | 010–013, 020 | Real concurrent transactions and exactly-once release evidence |
| REQ-007; INV-006/008 | backend activation + Go raw verifier | 005, 006 | 014–018 | Original hold retained, fresh chain fields, hostile byte vectors rejected before MPC |
| REQ-008; INV-007/010 | both persistence/receipts | 001, 003–006 | 003, 009, 013, 017, 018 | Stable bytes/signature, one outgoing/digest, crash/replay results |
| REQ-009; INV-011 | all API/store/claim boundaries | 001–006 | 008, 011, 019, 021 | Wrong organization/key/request denied across every boundary |
| REQ-010 | both audit and logs | 002–004, 007 | 009, 019, 023 | Durable safe decision/ledger evidence and captured-output redaction checks |
| REQ-011; INV-012 | combined reservation + activation + fee settlement | 002, 005, 007 | 004, 015B, 016 | Cap reserved once; prepared budget <= cap; actual fee settlement/reconciliation; absence is not zero |
| REQ-012; INV-015 | public API/read model/ledger consumers | 002, 007 | 016, 024, 025 | Same public ID and correct legacy/internal-transfer settlement |
| REQ-013 | schema/workers/journal/rollout | 001, 003–008 | 003, 005, 009, 011–014, 018, 020–026 | Crash/concurrency/restore/mixed-version regression results |

R1 is addressed in STEP-005/TEST-013/018/020; R2 in STEP-003/005/007 and TEST-010/015B/025; R3 in STEP-001/004 and TEST-007/009/022; S1 in STEP-001/002/007 and TEST-024. All four corrections were explicitly approved for documentation on 2026-10-19.

Decision dispositions: 001–004 are planned throughout the mapped steps; 005 selects cap-at-create in 002/005/007 and TEST-004/015B/016; 006 is implemented/tested in 003/011–012; 007 in 004/007–008; 008 in 002/005/007/014/024; 009 in 005/013–015; 010 in 006/017–020. User approval of all six v1 decisions was explicitly recorded on 2026-10-19.

Assumption validation: 001 → STEP-006/TEST-017 against all supported chain fixtures; 002 → STEP-004/TEST-009 and installation-lock check; 003 → STEP-008/TEST-026 recovery exercise; 004 → STEP-004/TEST-007–008 plus an organization's non-production endpoint contract test. Residual risks 001–007 map respectively to STEP-000, 006, 003/007, 007, 004, 006/008, and 008; unknown live deployment remains outside repository-completion evidence.

## Implementation steps

All steps below are planned. For each implementation unit use RED → verify the intended failure → minimal GREEN → verify behavior → bounded refactor → signed-off Conventional Commit. Do not run integration commands against live databases or funded networks. Do not write production code from this draft before PLAN-STEP-000.

### PLAN-STEP-000 — Pin the approved design and contracts

Sources: DECISION-005–010; RISK-001/002/005; compatibility obligations.

- [x] User approved fee ceiling at creation, required request cap, cancellation before outgoing activation, and over-cap failure without top-ups on 2026-10-19.
- [x] User approved outbound organization API polling for pending requests, immutable ledger lineage, failure ownership and one-digest binding on 2026-10-19.
- [x] User approved R1–R3 and S1: explicit durable SIGN handoff, full pre-activation ledger terminalization, fresh-page polling, and derived execution state; incorporated in both documents.
- [ ] Map the cancellation endpoint to the existing withdrawal-management RBAC scope; do not broaden permissions.
- [ ] Record the reviewed DESIGN commit, exact request/receipt/claim/config/API schemas and frozen test-vector IDs in this plan. Do not mark unresolved choices as defaults.
- [ ] Recheck both repository heads and applicable AGENTS instructions. Keep unrelated backend plan/spec files out of feature changes.
- [ ] Pin the already approved design/plan with a documentation commit when preparing execution: `docs(mpc): specify durable co-signer approvals`.

Deliverable: an executable plan tied to one reviewed design revision. Fee timing is resolved. Design acceptance is recorded; a pinned revision remains preparation for implementation, which the user has not requested.

### PLAN-STEP-001 — Add durable approval identity and protocol fixtures

Sources: REQ-001/008/009/013; INV-004/007/010/011.
Depends on: 000.

Files: backend Prisma schema/new migration; new `transactions/approval/transfer-approval.store.ts` and `.postgres.spec.ts`; backend-owned approval fixtures; Co-Signer `internal/contract/bundle/verifier.go` and new `testdata/approval-v1/` corpus. Add the new PostgreSQL test path to `apps/client-api/scripts/test-mpc-sign-postgres.mjs` and its required-suite accounting.

- [ ] Add failing schema tests for organization-scoped request idempotency, clientReference uniqueness, one principal-ledger link, one outgoing link, and invalid cross-tenant relations (TEST-003/019/021).
- [ ] Add positive/negative closed JSON/JCS and receipt vectors: APPROVED, REJECTED+reason, PENDING external response, exact replay/conflict, unknown field, duplicate key, wrong hash/signature/environment. Use clearly synthetic keys.
- [ ] Run the focused unit vectors and disposable PostgreSQL runner; record failures proving constraints/contracts are missing, not setup failure.
- [ ] Add the minimal schema/store methods and versioned fixtures. Use the five request states and preparationErrorCode from DESIGN; keep execution status/error on outgoing only. Define org-bound stable (createdAt,id) pending-page cursors with a fixed sweep upper bound, including empty/end/invalid-cursor fixtures and indexes. Index backend due selection for both activation and CREATED handoff. Do not create outgoing placeholders or weaken existing nonce constraints. Export Prisma generated artifacts using the repository's normal generation command.
- [ ] Verify the named tests and fixture producer identity; commit `feat(approval): add durable request and decision contracts`.

Commands (backend root): `npm run prisma:generate`; `npm run test:mpc-sign-postgres -w brosettlement-client-api`. The runner requires `TEST_DATABASE_URL` for an explicitly disposable PostgreSQL instance and creates its own test database.
Command (Co-Signer root): `GOWORK=off go run ./cmd/mpc-contracts verify` after fixtures/producer metadata are updated together.

### PLAN-STEP-002 — Reuse combined reservation at submission and expose stable public state

Sources: REQ-001/002/003/011/012; INV-001/002/004/006/012/015; DECISION-005/008.
Depends on: 001.

Files: ledger reservation/store/balance modules from responsibility map; new `transfer-approval.service.ts` and `.spec.ts`; `create-outgoing-transaction.use-case.ts`; public controller, read-model/DTO/mapper and module wiring. Tests: new `ledger-withdrawal-reservation.service.spec.ts`, approval service tests and existing public/query tests.

- [ ] Add TEST-001–006: principal+cap reserved together; missing/invalid cap rejected; insufficient principal OR fee rolls back all rows/deltas; duplicate key versus changed terms; last-balance contention; rollback at each journal/projection/outbox boundary; no buildTransfer or SIGN call during create.
- [ ] Include native-asset case: posted 100, principal 80, cap 5 → reserved 85, available 15; approval/activation with fresh budget 3 leaves reserved 85 and requires no additional free 85. Test token/native holds and contention for either final balance.
- [ ] Reuse the low-level combined createReservation: replace hardcoded outgoing source with a closed two-case source argument across ledger row, journals, history and pending credit. Keep legacy behavior and exact replay comparison. Do not call the high-level outgoing reserve/build flow before approval and do not introduce split principal/fee services.
- [ ] In one DB transaction create request + ledger + principal hold + fee-ceiling hold + audit/discovery visibility. Return the same ledger ID for idempotent replays before and after later execution.
- [ ] Extend TEST-003/021/024 across legacy/new origins: under a shared transaction-level identity guard, resolve existing org/key and clientReference before any new hold. Same org/idempotencyKey and normalized terms return the existing ledger ID; changed terms conflict. A reused clientReference under a different idempotencyKey keeps existing `CLIENT_REFERENCE_CONFLICT` semantics even for equal terms. Test competing compatible intake paths and require incapable old writers to drain before enablement.
- [ ] Preserve org/clientReference uniqueness before outgoing exists, and keep supported internal-transfer classification in the immutable snapshot. No recipient posted credit is created by an approval hold.
- [ ] Extend TEST-024 to show APPROVED separately from outgoing progress/failure/completion; before outgoing, execution is null and preparationErrorCode explains PREPARATION_FAILED. Keep request APPROVED through the outgoing lifecycle and add no mirrored execution-state writes.
- [ ] Verify focused tests, `npm run typecheck:client`, and real PostgreSQL contention. Commit `feat(ledger): reserve approval principal and fee cap at creation`.

Focused command: `npm run test:client -- --runInBand --runTestsByPath src/transactions/approval/transfer-approval.service.spec.ts src/ledger/application/withdrawals/ledger-withdrawal-reservation.service.spec.ts src/ledger/application/transactions/ledger-transaction-query.service.spec.ts`.

### PLAN-STEP-003 — Apply signed decisions and release on reject/cancel

Sources: REQ-006/008/009/010/013; INV-005/010/011/013; DECISION-006.
Depends on: 001, 002.

Files: new backend `mpc/co-signer/http/approval-request.controller.ts`/DTOs; approval store/service; cancel endpoint; shared ledger release primitive; transaction outbox/module wiring. Add `transfer-approval.decisions.spec.ts` and expand `.postgres.spec.ts`.

- [ ] Add TEST-010–013/019: reject reason required; repeated reject/cancel; approve/reject race; approve then cancel before activation; cancel during preparation; activation-first cancel conflict; late receipt after cancellation; scope/key/hash substitution.
- [ ] Verify the tests fail on missing receipt validation/CAS/release behavior.
- [ ] Validate canonical receipt and trusted key; record exact bytes once and application disposition. Lock request for state transitions. Do not replace earlier receipt evidence with a new decision or fabricate a signed cancel.
- [ ] Implement TerminalizeApprovalBeforeActivationTx under the request lock and no-outgoing check. In one transaction transition request, debit and expected internal CREDIT sibling to the defined terminal states, append stable status history, and void both holds. Reuse transitionTransactionStatus/applyTerminalToCredit via tenant-scoped request-source resolution and PENDING-to-VOIDED journal CAS with one delta per winner. Ledger maps to FAILED; request records REJECTED/CANCELLED/PREPARATION_FAILED. Missing expected ledger/sibling/journal is an integrity error; external transfers legitimately have no credit sibling.
- [ ] Extend TEST-010/015B/025 for exact terminal statuses and one history entry per transition, same/different principal/fee assets, no recipient posting, exact replay, conflicting terminal outcomes, and rollback after each status/history/journal/projection write. Failure of either release must roll back all status/history changes as well.
- [ ] Make the APPROVED request nextAttemptAt due in the same transaction as the applied decision. The row is the durable activation queue; existing outbox is for lifecycle/audit, not an additional SIGN-start mechanism. A lost ACK returns exact replay even after cancellation, preparation failure or outgoing completion; a cancelled request cannot be revived.
- [ ] Update RBAC/API documentation for cancel and late decisions. Verify unit and PostgreSQL races; commit `feat(approval): apply decisions and release cancelled holds`.

Commands: `npm run test:client -- --runInBand --runTestsByPath src/transactions/approval/transfer-approval.decisions.spec.ts`; `npm run test:mpc-sign-postgres -w brosettlement-client-api`.

### PLAN-STEP-004 — Add the Co-Signer approval dispatcher and durable journal

Sources: REQ-002/004/005/008/010/013; INV-009/010/014; DECISION-007.
Depends on: 001 (can run alongside backend steps 002–003).

Files: new `internal/approval/` files from responsibility map; `internal/monolith/approval.go`; `internal/config/config.go`/tests; `cmd/co-signer/resources.go`, `lifecycle.go`/tests; README and a new approval operations runbook.

- [ ] Add TEST-005/007–009: identical external/auto receipt paths; PENDING/timeouts/5xx/malformed/unauthenticated responses; per-attempt timeout with unlimited total waiting; bounded concurrency; durable mode admission; no SIGN permits used.
- [ ] Add journal crash/reopen and conflict tests at file write/fsync/create-only publication/directory fsync and receipt-send/ACK boundaries. Test a second process cannot share the same journal unsafely.
- [ ] Discover org-scoped pending pages in stable (createdAt,id) order with a fixed server-issued sweep upper bound and org-bound cursor. Bound each batch; continue past suppressed items and wrap after the sweep, so old first-page waiters and continuous arrivals cannot starve other requests. Empty/absent page entries are not terminal notifications. Backend discovery may continue while idle.
- [ ] Each new organization POST must be selected from a fresh authenticated pending page and pass pinned-mode, unresolved-decision and due-backoff checks. Local admission/backoff records can only suppress calls, never independently schedule them. PENDING/errors return work to backend discovery after suppressive delay; discovery outages allow no cached-only calls. Bound backoff memory; reset/eviction may reset delay but cannot reset pinned admission/decision evidence. No speculative durable local poll queue.
- [ ] Use one outbound idempotent HTTPS POST with a dedicated bearer credential. A cancelled request disappears from future pending pages; an already selected/in-flight bounded batch can race with cancellation and follows non-applied-decision handling. After a saved final receipt, only its backend delivery is retried. Auto_approve never calls the organization API; no callback or second status endpoint is added.
- [ ] Extend TEST-007/009/022 for idle -> pending -> final -> idle, auto, pending restart, cancelled local-admission restoration, cancellation between pages, backend discovery outage, and lost decision ACK without organization re-polling. With more than two pages, first-page items pending forever and continuous arrivals, prove later pages and old requests are revisited. Enforce response bindings/final stability, bounded concurrency/memory, and cursor tenant isolation.
- [ ] Implement a small approval-local durable file helper following the existing immutable-publication pattern. Reuse safe filesystem primitives where appropriate; do not refactor the encrypted share format or introduce SQLite solely for this feature.
- [ ] Persist mode/hash before evaluation and exact receipt before delivery. Fail closed on any inability to make the decision/binding durable. Keep final decisions immutable across configuration changes.
- [ ] Add explicit `CO_SIGNER_APPROVAL_MODE`, external endpoint/credential settings and `CO_SIGNER_APPROVAL_JOURNAL_DIR`. Do not resurrect the currently unsupported `CO_SIGNER_STATE_DIR` by accident. Reject overlap with share directories and validate storage at startup.
- [ ] Wire outbound discovery/decision delivery as a separate bounded worker. Keep pending requests on backend as the authoritative queue, with local records for independent decision/execution evidence.
- [ ] Verify targeted Go tests and Linux journal crash tests; commit `feat(cosigner): add durable external and automatic approvals`.

Commands: `GOWORK=off go test ./internal/config ./internal/approval ./internal/monolith ./cmd/co-signer`; `GOWORK=off go test -race ./internal/approval -count=1`. Linux filesystem crash tests must actually run on Linux CI; macOS passing is not Linux crash evidence.

### PLAN-STEP-005 — Activate once and durably hand off the saved outgoing to SIGN

Sources: REQ-007/011/013; INV-006/007/012/013; DECISION-005/008/009.
Depends on: 002, 003 and reviewed v1 contracts in 000.

Files: new approval worker/tests; existing outgoing reservation/build/signing services and store; ledger reservation/fee methods; lease/outbox wiring.

- [ ] Add TEST-013–016/020: two activation/handoff workers; stale lease; crashes before/after outgoing commit and MPC-binding commit; original journal IDs and unchanged reservation deltas; cancelled-after-preparation candidate discarded; no duplicate nonce ownership/outgoing. TEST-013 must assert eventual same-outgoing SIGN or proven terminal failure after restart without a client retry, not merely at most one outgoing.
- [ ] Add TEST-015B: same/different principal and fee assets; valid explicit zero versus missing cap; fresh exact budget below/equal/above cap; original fee journal unchanged; over-cap terminal release and atomic rollback. TEST-015A is retired, not implemented.
- [ ] Use short fenced leases and existing sender gates. Prepare fresh chain bytes outside DB locks only after an applied approve; commit the unique outgoing link and exact existing principal/fee adoption while leaving request APPROVED and due for handoff. The committed row provides durable worker visibility. No extra command queue/consumer or execution-status mirror is required.
- [ ] During adoption compare organization/account/wallet/asset/amount/request hash and both active journal identities/amounts. Set outgoing.networkFeeBudgetAtomic to the exact fresh budget, not the held cap; enforce budget <= cap. Do not demand additional fee availability or top up; do not call the legacy full principal+fee reservation path.
- [ ] Recheck snapshot, cancellation, lease ownership and preparation freshness at activation commit. Select two due populations: APPROVED/no-outgoing for activation, and APPROVED/linked outgoing CREATED/no mpcSessionId for handoff. Apply bounded fair quotas/order so preparation cannot starve handoff or vice versa.
- [ ] For handoff, re-read saved bytes and current signing context; call existing OutgoingTransactionSigningService.start with the same outgoing ID. Never call reserve/ensureBuiltTransfer, allocate nonce or rebuild bytes here. Its persisted MPC aggregate/binding is the only handoff acknowledgement; loss of worker ACK after commit must not start another session. After uncertainty re-read authoritative binding/status before retry/failure.
- [ ] Proven non-execution while still CREATED calls existing OutgoingTransactionFailureService.markFailedAndEnqueue with a CREATED-only conditional transition (including expired saved bytes); a competing SIGN binding must make failure lose. Its existing outgoing event owns terminal ledger/refunds. Inconsistent aggregate state holds funds for reconciliation. Add TEST-013/020 for crash recovery, expiry during downtime, lost binding ACK, and terminalization racing with successful SIGN binding.
- [ ] Use a small retryable-error allowlist with backend nextAttemptAt/backoff for temporary preparation or handoff failures; keep both holds. Cancellation is allowed only before outgoing. Proven permanent pre-activation failure, including FEE_LIMIT_EXCEEDED, uses TerminalizeApprovalBeforeActivationTx and PREPARATION_FAILED. After outgoing creation, the existing outgoing lifecycle is the sole release owner, even when the approval worker encounters the failure. No generic retry framework or unknown-outcome refund.
- [ ] Verify real database races and chain-policy regression tests; commit `feat(transactions): activate approved transfers using existing holds`.

Commands: `npm run test:client -- --runInBand --runTestsByPath src/transactions/approval/transfer-approval.worker.spec.ts src/transactions/application/create-outgoing/outgoing-transaction-reservation.service.spec.ts src/transactions/application/create-outgoing/outgoing-transfer-build.service.spec.ts`; disposable PostgreSQL runner.

### PLAN-STEP-006 — Verify prepared bytes and prevent authorization replay into a new transfer

Sources: REQ-007/008/009; INV-007/008/009/011; DECISION-010; RISK-002/006.
Depends on: 001, 004; integrates with 005.

Files: backend claim factory/start-wallet-sign gate; Co-Signer `monolith/types.go`, `claim.go`, new prepared verifiers, `worker/session_worker.go`/`sign_session.go`; execution journal; shared contract bundle and protocol vectors.

- [ ] Evaluate narrow, maintained protocol decoding choices using official sources. Existing JCS, hash and protobuf primitives are not a transaction parser. Record dependency/version/license/size and strict parsing behavior before choosing a library; do not roll a permissive generic parser to avoid a dependency.
- [ ] Add TEST-017–019/021: native ETH, ERC-20, native TRX, TRC20 positive vectors plus hostile recipient/amount/token/chain/calldata/fee/unknown-contract/encoding mutations. Bind Ethereum sender to verified derivation identity; it is not an explicit field in unsigned Ethereum bytes.
- [ ] Extend the versioned claim with actual signing preimage bytes, explicit format and receipt/request/outgoing bindings. Backend and Go verifiers must accept the same valid corpus and reject the same negative corpus where applicable.
- [ ] Recompute digest independently and enforce authorized economic terms and the immutable fee ceiling before MPC, while preserving the current exact prepared-budget/bytes checks. Metadata-only comparison is not completion.
- [ ] Persist one outgoing/digest binding locally before invoking Core. Same-byte retry follows existing worker ownership; a different digest/outgoing conflicts. Do not make orphaned MPC sessions resumable or silently rebuild expired bound transactions.
- [ ] Close direct wallet/raw-sign bypasses for approval-required transfers and test old Co-Signer capability rejection. Preserve DKG and existing terminal-delivery semantics.
- [ ] Verify cross-language fixtures and SIGN regression/race tests; commit `feat(mpc): bind signing to approved transaction bytes`.

Commands: `GOWORK=off go test ./internal/approval ./internal/signingpolicy ./internal/monolith ./internal/worker ./internal/contract/bundle`; `GOWORK=off go run ./cmd/mpc-contracts verify`; backend claim/start-wallet-sign unit tests and `npm run test:mpc-2of3:harness`.

### PLAN-STEP-007 — Preserve read, settlement and reconciliation behavior

Sources: REQ-010/012/013; INV-002/005/006/012/015; DECISION-008.
Depends on: 002, 003, 005.

Files: ledger withdrawal service/store/query/read-model; internal-transfer credit lifecycle; `transactions/infra/outgoing-transaction.store.ts` and `transactions/application/worker/broadcasted-transaction.processor.ts`; reconciliation snapshot/rules; public DTOs/mappers/Swagger; safe metrics and runbook updates.

- [ ] Add TEST-016/020/022–025: outgoing events resolve the original principal journal through request binding; confirmation posts principal once and settles actual fee; proven non-execution releases both once; on-chain failure retains actual fee charges; ambiguous broadcast retains; internal recipient receives no premature posted credit; public ID never changes.
- [ ] Keep legacy source lookup for historical outgoing-origin rows. Use one scoped resolution helper for approval-origin settlement and chain-review mutations; do not scatter string-based source remapping through consumers.
- [ ] Extend TEST-022: changed terminal receipt/finality through each existing mutation path sets `chainReviewRequired` on the original approval-origin ledger row and the next affected spend remains blocked; preserve the legacy case.
- [ ] Reuse existing LedgerWithdrawalFeeService for actualFee <= held cap: post actual fee and void the cap reservation once, returning cap-minus-actual to available funds. Missing/invalid/over-cap receipt evidence keeps the existing fee reconciliation path; principal follows the real transfer outcome. Test duplicates in each branch (TEST-015B/016/020).
- [ ] Preserve financial reconciliation of held funds. Tag intentional approval waits separately from stuck execution; do not suppress actual balance mismatches or globally disable stale-pending diagnostics.
- [ ] Expose principal held, request state, decision reason/source, preparationErrorCode and outgoing-derived execution status/error separately. Request remains APPROVED through outgoing terminal outcomes; no event consumer mirrors execution into it. Extend TEST-024 across CREATED/SIGNING/confirmed/failed/ambiguous outcomes, replay and out-of-order events. Metrics are bounded and do not use request IDs, addresses, reason text or organization IDs as labels.
- [ ] Verify query/lifecycle/internal-transfer/reconciliation tests and fixture compatibility; commit `feat(ledger): settle and expose approval-backed transfers`.

Command: `npm run test:client -- --runInBand --runTestsByPath src/ledger/application/withdrawals/ledger-withdrawal.service.spec.ts src/ledger/application/withdrawals/internal-transfer-credit-lifecycle.service.spec.ts src/ledger/application/transactions/ledger-transaction-query.service.spec.ts src/ledger/domain/ledger-reconciliation.spec.ts`; `npm run typecheck:client`.

### PLAN-STEP-008 — Prove the full workflow and document safe rollout

Sources: REQ-002/003/004/006–013; ASSUMPTION-001–004; RISK-003–007.
Depends on: 003–007.

Files: new backend `apps/client-api/test/mpc-2of3/approval.external.e2e-spec.ts` and explicitly selected runner config; existing harness; new Co-Signer approval crash tests/CI selection; public API/config/recovery docs; existing release workflows only if test inclusion requires change.

- [ ] Add coordinated TEST-005/018/020–026 using fake clock for long waits, stub organization API, isolated PostgreSQL and local chain fixtures. Ensure the runner discovers the new suite; an empty selection is failure.
- [ ] Exercise principal+fee-cap reservation → external pending → restart/time advance → approve → fresh transaction → independent verification → MPC → settlement, and reject/cancel release paths. Run the same monetary path with explicit auto approval.
- [ ] Exercise disabled intake, old/new binary combinations, legacy-key replay and fenced intake cutover, journal restore/reconciliation and pending-hold drain/cancel. Demonstrate no rollback path can bypass approval or orphan a held principal.
- [ ] Run backend targeted suites/typecheck and coordinated harness, then Co-Signer `GOWORK=off go test -race ./... -count=1` and contract verification. Broaden only for failures or relevant uncovered paths.
- [ ] Record exact backend/Co-Signer/fixture revisions, executable suite counts and evidence for each acceptance criterion. Separate code/CI proof from staging pilot and live deployment.
- [ ] Publish runbook: mode changes and unseen backlog, durable volume/backup, pending cancellation, fee policy, journal loss, stuck activation and ambiguous broadcast. Commit `test(approval): verify durable approval and reservation lifecycle`.

Planned integration command after adding the explicit suite config: `npm exec -w brosettlement-client-api -- jest --config ./test/mpc-2of3/jest-approval-coordinated.json --runInBand`. The config and required-suite guard are deliverables of this step, not existing commands claimed to work today.

## Test matrix

| Test | Level | Required behavior / evidence |
| --- | --- | --- |
| TEST-001 | PostgreSQL | Request/public ledger/principal/fee-cap committed together; posted unchanged; native combined and token/native reservation deltas are exact. |
| TEST-002 | PostgreSQL fault injection | Failure after each create/journal/projection/audit boundary rolls back the full transaction. |
| TEST-003 | PostgreSQL concurrency | Same key+same terms gives one request/hold/ID; changed terms conflict; different keys cannot oversubscribe the last balance; cross-origin legacy/new retries and clientReference cannot create another operation. |
| TEST-004 | Unit + PostgreSQL | Required cap validated and both holds created through existing combined service; absence rejected, explicit zero follows chain policy; insufficient fee or principal rolls back both. |
| TEST-005 | E2E fake clock + process restart | 30/365-day wait preserves request/hold; no raw transaction, nonce, outgoing, SIGN intent/session or permit; restart repeatedly. |
| TEST-006 | Unit + DB observation | Creation/discovery never calls transaction builder/nonce allocator/Core; other sender work is not blocked by pending approval. |
| TEST-007 | Go integration | External PENDING/APPROVED/REJECTED and explicit auto share receipt handling; only fresh pending-page items can cause due external calls. Idle/auto/cached-only cancelled records/discovery outage cause no new organization calls; selected in-flight cancellation races remain non-applied. |
| TEST-008 | Go HTTP integration | Timeout, 5xx, invalid schema/auth/hash/tenant/status and redirect do not sign, reject automatically, or release principal. |
| TEST-009 | Linux filesystem + restart | Admission/decision durability, unchanged replay bytes/signature, writer exclusion, crash at sync/publication/ACK and config switch; restart resumes unresolved pending polls, while saved final decisions retry delivery without re-polling the organization. |
| TEST-010 | PostgreSQL | Reject requires reason; reject/cancel atomically terminalizes request, debit and expected CREDIT sibling/history plus both holds. Exact retry adds no history/delta, conflicting outcome cannot mutate; inject rollback after each write for native/token and internal/external cases. |
| TEST-011 | PostgreSQL concurrency | Approve/reject conflict; cancel-before-approve yields non-applied receipt; approve-then-cancel-before-activation keeps receipt but no execution. |
| TEST-012 | PostgreSQL + worker race | Cancel versus activation: either released/no outgoing, or activated/409/no release; discard uncommitted candidate. |
| TEST-013 | PostgreSQL + process fault injection | Two workers/leases; kill immediately after outgoing commit, restart without client retry, and eventually reach the same-outgoing MPC binding or proven terminal failure. Kill after binding commit before ACK: same session, no new admission. Expired saved bytes never rebuild; SIGN/failure race has one winner; one outgoing/nonce and unchanged holds. |
| TEST-014 | PostgreSQL + unit | Original ledger and both journal IDs/amounts survive activation unchanged; exact scope/hold comparison; no second availability charge or reserve delta. |
| TEST-015B | PostgreSQL + chain stub | Reserve cap once; fresh outgoing budget retains exact bytes/evidence match and fits cap; over-cap fails without top-up; cap held unchanged until settlement; reject/cancel/proven pre-activation failure atomically releases both and closes debit/expected-credit/history; outgoing remains absent. |
| TEST-016 | Ledger integration | Confirmation/failure handles original principal once; valid fee evidence posts actualFee and frees cap once; missing/invalid/over-cap evidence preserves reconciliation; duplicate/out-of-order events have no additional money effect. |
| TEST-017 | Cross-language crypto vectors | Real supported raw transaction forms match authorization; altered economic fields/calldata/token/chain/fees/digest/encoding are rejected. |
| TEST-018 | Go + coordinated E2E | Atomic execution binding accepts one outgoing/digest; lost ACK/restart never starts a second transfer; existing orphan rules remain. |
| TEST-019 | HTTP + DB + Go | Cross-org/key/request/hash substitutions fail at create/discovery/decision/cancel/activation/claim; safe audit has no secrets or MPC frames. |
| TEST-020 | E2E regression | Ambiguous broadcast retains holds and blocks automatic replacements/releases; definite failure uses one release owner. |
| TEST-021 | Compatibility integration | Old direct create/raw-sign/empty-claim cannot bypass required approval; old Co-Signer cannot accept new required work; cross-origin identity guard and old-writer drain prevent mixed-intake duplicates. |
| TEST-022 | Reconciliation + performance | Intentional wait is not expired; real mismatches and both chain-review mutation paths remain effective. More than two pages, old first-page waiters and continuous arrivals prove fixed-bound sweep fairness/revisits. Verify org-bound cursors, batch/concurrency/backoff-memory limits, due indexes and fair activation/handoff work selection. |
| TEST-023 | Captured-log/API test | Bounded reason/source/error audit without credentials, key material, shares, raw external response dumps or unsafe labels. |
| TEST-024 | Public API/query regression | Stable ID; execution null before outgoing; request PREPARATION_FAILED has reason and ledger FAILED. After activation request remains APPROVED while execution status/error is read only from outgoing, through success/failure/replays/out-of-order events; no mirrored completion writes. |
| TEST-025 | Internal-transfer integration | Reject/cancel/permanent pre-activation failure closes debit and expected credit to FAILED with one history transition each and both refunds atomically; missing expected sibling rolls back. No recipient posting. Preserve post-outgoing confirmation/failure/deposit settlement through request-source resolution. |
| TEST-026 | Isolated recovery exercise | Mixed versions, disabled intake and restored journal/DB cannot lose holds, regenerate conflicting decisions or silently re-sign. |

TEST-015A (fee-after-approval) was retired on 2026-10-19; its ID is not reused. TEST-015B is the sole selected fee test group.

## Migration, compatibility and recovery

Order: additive schema/constraints and disabled backend paths → versioned fixtures/API → Co-Signer durable journal and verifier → jointly proven capability → organization enablement. Both compatible intake paths share the identity guard; drain incapable legacy writers before enabling approval. Existing prepared outgoing transactions drain on their existing path; never backfill them into PENDING_APPROVAL.

The new approval origin must be readable by query/settlement/reconciliation code before organizations can create requests. Keep legacy handling for historical rows. No down migration or destructive source rewrite is required by this proposal.

Rollback means stop new intake while retaining compatible cancellation/decision/settlement workers and the schema. Do not downgrade to code that can bypass the approval binding. Recover requests with principal journals but no outgoing from durable request/decision state; recover activated CREATED/no-MPC work through the approval worker using its unique saved outgoing until a persisted MPC binding or proven failure; then use existing MPC/outgoing recovery; never derive approval permission from a missing row or missing local file.

No production migration/rollback commands are supplied for automatic execution. They belong to the reviewed release runbook after implementation evidence exists.

## Execution order and parallel work

| Lane | Steps | Dependencies / conflict boundary |
| --- | --- | --- |
| Contract | 000 → 001 | Must precede both implementations; freezes shared bytes and identity. |
| Backend | 002 → 003 → 005 → 007 | Sequential because all touch approval/ledger/outgoing ownership. |
| Co-Signer | 004 → 006 | Starts after 001; 006 integrates the prepared claim with backend 005. |
| Integration | 008 | Waits for both lanes; only the selected cap-at-create policy exists. |

Backend 005 and the backend portion of 006 both touch SIGN creation/claim contracts: assign one owner or serialize those edits. Separate repository worktrees can run backend and Go lanes concurrently after contract approval. No worktree or feature branch is created by this document-only task.

## Completion criteria

- Repository implementation: each checked-off step has passing targeted tests and exact revisions; every REQ/INV maps to captured evidence; the cap-at-create tests are complete.
- Contract compatibility: backend producer fixture identity, Co-Signer verifier and old/new behavior are tested together; unknown fields do not silently downgrade security.
- Financial proof: exactly one principal hold, stable IDs through activation, exactly one post/void outcome, and no release on ambiguity are demonstrated in real PostgreSQL tests.
- Behavioral proof: long-wait/restart, fresh-only fair polling and discovery outage, approve/reject/cancel races, complete pre-activation ledger terminalization, automatic CREATED-to-SIGN crash recovery, auto approval, raw-byte substitution and duplicate-transfer prevention pass coordinated tests.
- Distribution: existing container/build/release workflows include required verification and durable-volume configuration documentation.
- Deployment/pilot: remains separate work requiring actual environment revision/capability evidence and authorized staging/production operations. A green test suite is not deployment evidence.

## Structural readiness

Status: **DESIGN_APPROVED / ENGINEERING_REVIEW_CLEARED / REVISION_PIN_PENDING**. User-approved R1–R3/S1 are now incorporated into the normative design and implementation steps. All six v1 decisions remain approved. The approved DESIGN Git revision still needs to be pinned in STEP-000 before future implementation. No feature code, migrations, tests or deployments were executed.

## GSTACK REVIEW REPORT

Repeat review: 2026-10-19, after the user authorized incorporation of R1–R3 and S1. Result: **no remaining findings in the revised design/plan**. This clears the reviewed documentation; it is not feature implementation, executed-test or deployment evidence.

Detailed closure and source evidence: [repeat review](../reviews/2026-10-19-co-signer-approval-rereview.md). The [first-pass report](../reviews/2026-10-19-co-signer-approval-review.md) is retained as historical evidence.

| Review | Trigger / provenance | Result | Coverage |
| --- | --- | --- | --- |
| Engineering | `gstack / plan-eng-review`, primary Codex source review | Clean | Architecture, code reuse, state ownership, test specifications and bounded polling. |
| Simplification | User-selected `simplify-code-solution` | Incorporated | One approval worker for activation/handoff, one atomic pre-activation terminalizer, fresh-page polling, no mirrored execution lifecycle. |
| External outside voice | Claude Code, host codex, phase plan-review | Unavailable | Authentication failure; 1 attempted call, 0 completed reviews. |
| Native fallback | Fresh-context Codex reviewer after external failure | `no_findings` | Independently checked revised documents against signing/binding/failure/ledger source; closed R1–R3/S1. |
| CEO / UI design / DX | Not invoked | Not claimed | Approved product scope retained; no human UI added. |

**Closure:**
- R1: STEP-005 now selects both APPROVED/no-outgoing and linked CREATED/no-MPC work; saved bytes and the current signing service resume the same outgoing. The persisted MPC binding acknowledges handoff. TEST-013 proves eventual recovery without client resubmission and tests failure/SIGN races, expiry and lost ACK.
- R2: STEP-003/005/007 now use one request-locked, no-outgoing transaction for debit/expected-credit FAILED states, status history and both releases. TEST-010/015B/025 prove complete terminal records, replay and rollback, including internal transfers.
- R3: STEP-001/004 specify fresh-pending-page eligibility, no cached-only calls, fixed-bound repeatable keyset sweeps, bounded batches/backoff and in-flight cancellation handling. TEST-007/009/022 cover discovery failure, restored cancelled records and fair revisits under continuous arrivals.
- S1: STEP-001/002/007 store five request states and a pre-activation failure code; execution status/error is read from outgoing. APPROVED remains unchanged after activation. TEST-024 checks this throughout execution and replay.

**What already exists:** combined reservation/sorted balance locks, ledger status/history and journal CAS, SIGN aggregate/binding transaction, CREATED-only outgoing failure with outbox, receipt settlement, Ed25519/JCS and immutable local publication. The plan reuses these primitives and explicitly adds the missing orchestration; it does not claim the existing outgoing worker handles CREATED.

**NOT in scope:** callback or second status servers, another queue/ledger/workflow engine, execution-state synchronization, split reservation services, transaction replacement/fee top-up, share-custody refactoring, new chains, implementation and deployment. No unrelated TODO was proposed or written.

**Failure/test review:** the DESIGN test diagram, detailed plan matrix and repeat-review failure table cover crashes before/after handoff, terminalization rollback, stale/cancelled discovery, conflicting decisions, unsafe bytes, ambiguous broadcasts and journal recovery. The previously missing liveness/accounting/fairness assertions are incorporated. No new test-specification gap was found; no feature tests ran. Future worker handoff, terminalizer and dispatcher should include small ownership-flow comments.

**Parallelization:** Contract → Backend (002→003→005→007) and Co-Signer (004→006) → Integration (008). The backend and Co-Signer lanes can run concurrently after contracts; backend SIGN/claim edits shared by 005/006 need one owner or serialized integration. No worktrees were created for this documentation update.

**Implementation Tasks:** the user-approved corrections are integrated into the existing unchecked implementation steps and named tests above. This repeat review adds no separate tasks. The fresh review task artifact is empty to record a completed pass with no new findings; implementation remains future work.

**Completion summary:** approved scope preserved; architecture 0 remaining issues; code quality/simplicity 0; test specifications 0 new gaps; performance 0 new issues; 3 prior findings and S1 closed. Critical gaps 0; unresolved decisions 0; Lake Score N/A (no new alternatives required user selection). Required scope/reuse/failure/parallelization outputs are recorded. Durable project-memory updates were not requested and were not made.

**Remaining preparation:** STEP-000 still pins the DESIGN Git revision, maps cancellation RBAC to the existing role model, and freezes executable wire schemas/fixture IDs. These are explicit implementation-preparation tasks, not unresolved choices among the approved policies. The source baselines are not substitutes for a pinned DESIGN revision.

**OUTSIDE COVERAGE:** `claude-code` / `codex` / `plan-review` unavailable on this attempt because authentication failed; session `319f4f1a-f449-4a11-9331-b5bfefd22a08`. Native fallback completion is recorded separately; no cross-model agreement is claimed.

**VERDICT:** ENGINEERING REVIEW CLEARED for this documentation revision. Design decisions and R1–R3/S1 are approved and incorporated. Implementation remains unstarted and requires a separate user request.

NO UNRESOLVED DECISIONS
