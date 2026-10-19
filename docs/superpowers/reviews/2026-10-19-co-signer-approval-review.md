# Co-Signer approval: design and plan review

Date: 2026-10-19
Status: **HISTORICAL FIRST PASS — R1–R3 and S1 were subsequently approved and incorporated; see the [repeat review](2026-10-19-co-signer-approval-rereview.md).**

The observations and unresolved list below describe the pre-correction revision and are retained as historical evidence; they are not the current readiness verdict.

## Verdict and scope

Keep the approved architecture. Resolve three concrete mechanics before implementation: recovery between outgoing creation and SIGN, ledger terminalization before an outgoing exists, and pending-only polling eligibility. None requires reopening the six approved product decisions or introducing another service.

Goal: reserve principal and a fixed fee ceiling once, wait indefinitely for the organization, and execute at most one independently verified transfer after approval, with correct refunds and audit history.

This review applies `gstack / plan-eng-review` and the user-selected `simplify-code-solution` to the current design and plan. It is a source-backed review, not an interactive acceptance pass or runtime certification. Recommendations below remain proposals. No feature code, migrations, tests, commits, deployments, or external organization calls were performed.

Reviewed inputs:

| Input | Revision / fingerprint |
| --- | --- |
| Co-Signer source | `5df5ac25953debeb94f2c37d89a2ad458a73057c` |
| Backend source | `e14e175ff5fb2974907bcd5efd123d20425a5001` |
| DESIGN, before review | SHA-256 `971e3f1ede703917f4700720e7f2bb9c3b8657fa3f060573af5a2ff11b55a864` |
| PLAN, before review metadata update | SHA-256 `0d6403b418b2a7d0ba636a07f4ba3dc9968bb0aa18fcda5bb664bd35b15a5820` |

DESIGN: `docs/superpowers/specs/2026-10-11-co-signer-approval-design.md`.
PLAN: `docs/superpowers/plans/2026-10-11-co-signer-approval.md`.
Backend paths below are relative to `/Users/vlas/Work/BroLabel/back-end`; Co-Signer paths are relative to `/Users/vlas/.codex/worktrees/2c85/mpc-co-signer`.

## Findings

### R1 — P2: Assign durable recovery from committed outgoing to SIGN

**Document anchors:** PLAN:188–191 and TEST-013 at PLAN:262; DESIGN:238,248–249.

The plan correctly requires an atomic outgoing/dispatch commit and reuse of the same outgoing after a crash. However, it delegates post-activation retry to the existing outgoing lifecycle without naming the dispatch consumer or recovery selection that starts SIGN for a committed `CREATED` outgoing. The named existing components do not supply that behavior:

- `apps/client-api/src/transactions/infra/outgoing-transaction.store.ts:160` selects only `SIGNED`, `BROADCASTING`, `BROADCASTED`, and `BROADCAST_AMBIGUOUS` for the worker.
- `apps/client-api/src/transactions/application/worker/transaction-worker.service.ts:72` handles the same statuses.
- `apps/client-api/src/transactions/messaging/outgoing-transaction-events.listener.ts:14` only acknowledges `OutgoingCreated`.
- `apps/client-api/src/transactions/application/create-outgoing/create-outgoing-transaction.use-case.ts:83,112` currently calls `signingService.start` directly. That service creates/binds the MPC aggregate transactionally later, at `outgoing-transaction-signing.service.ts:112–181`.

**Failure scenario:** activation commits the outgoing and the process dies before SIGN creation. Cancellation is now forbidden. If the new worker selects only APPROVED requests without an outgoing, and dispatch is wired to the existing created-event listener, no executor resumes the transfer. Both holds remain indefinitely. TEST-013's “at most one outgoing” assertion still passes for this permanently stalled request.

**Minimum remedy:** make one existing/newly planned worker explicitly own the handoff until the outgoing has a persisted MPC binding or a proven terminal failure. Prefer extending the already planned approval worker: it selects approved work with no outgoing **and** linked `CREATED` outgoing without an MPC binding, then resumes the latter through the existing signing service using only saved bytes. A committed binding is the handoff acknowledgement. Reuse the existing signing aggregate CAS/idempotency; retain the outgoing lifecycle as the sole post-activation refund owner. Expired saved bytes follow proven non-execution handling, never rebuilding under the same approval.

An explicit durable outbox consumer can also satisfy the requirement, but name its event, consumer, retry/ack boundary, and crash behavior. Do not add both mechanisms without a demonstrated need. The phrase “dispatch atomically” already anticipates durability; this finding concerns the missing concrete owner, not a claim that the design intentionally uses fire-and-forget dispatch.

**Validation:** extend TEST-013 with a process kill immediately after activation commit and before `signingService.start`. Restart without a client retry. Assert eventual same-outgoing SIGN creation or proven terminal failure, unchanged journal identities, no second reservation, no second outgoing/nonce ownership, and correct handling when the saved bytes expire during downtime. Repeat the kill after the MPC binding commit but before the worker acknowledges it.

### R2 — P2: Terminalize ledger records when refunding before activation

**Document anchors:** DESIGN:111,177,236–237; PLAN:153,191 and TEST-010/015B/025.

The release helper specifies request transition plus two journal CAS operations. It does not specify the terminal transition/history for the public debit ledger transaction or the pending internal-transfer credit sibling. There is deliberately no outgoing to emit the existing terminal lifecycle event.

- `apps/client-api/src/ledger/application/withdrawals/ledger-withdrawal-reservation.service.ts:73,116–128` creates the pending internal-transfer credit sibling.
- `apps/client-api/src/ledger/application/withdrawals/ledger-withdrawal.service.ts:122–129` separately transitions ledger status, applies money effects, and terminalizes the credit leg.
- `apps/client-api/src/ledger/application/withdrawals/internal-transfer-credit-lifecycle.service.ts:124–141` currently enters that flow through an outgoing ID; its lower-level `applyTerminalToCredit` can be reused.

**Failure scenario:** reject, cancel, or a permanent preparation failure releases all held money, but the debit and credit records remain `PENDING` forever. Public history and stale-pending reconciliation disagree with the final request. Generic preservation of internal-transfer behavior and journal-count assertions do not close this path.

**Minimum remedy:** define one atomic pre-activation terminalization operation: lock/check the request and absence of outgoing, transition request, transition debit and any existing credit sibling, append status history, and void both holds with their existing CAS/delta rules. The smallest mapping with current ledger enums is `FAILED` for all three non-executed outcomes, retaining the distinct `REJECTED`, `CANCELLED`, or execution error in the approval projection/history. Reuse `transitionTransactionStatus` and credit terminalization primitives; do not synthesize an outgoing event or add a second accounting model.

**Validation:** extend TEST-010/015B/025 for external and internal transfers. After reject, cancel, or permanent pre-activation failure, both applicable ledger records are terminal, both reservations are voided, history has one transition per record, balances are exact, and the recipient receives no posted credit. Inject failure between these writes and prove full rollback. Duplicate requests/events must leave all results unchanged.

### R3 — P2: Make fresh pending discovery the only source of external polling

**Document anchors:** DESIGN:181,191–195; PLAN:168–174 and TEST-007/009/022.

Discovery returns pending requests, while cancellation handling says discovery reports cancellation or another state. A cancelled request disappears from a pending-only result. Absence from one page cannot establish its terminal state. The contract does not explicitly prevent a local admission/backoff record from continuing to schedule external POSTs after cancellation or after backend discovery fails.

**Minimum remedy:** schedule every new organization API attempt only from an item returned by a fresh authenticated pending page. Local records may suppress an attempt, pin admission/configuration, or retry delivery of an already saved final receipt; they must never independently authorize another organization poll. If discovery fails, schedule no new external attempts from cached entries. A cancellation after discovery can still race with the already selected/in-flight attempt; existing non-applied decision handling remains necessary.

Specify stable keyset pagination and repeatable sweeps, rather than treating the cursor as a one-time event stream or restarting at page one forever. A sweep should have a fixed upper bound so continuous arrivals cannot prevent revisiting old requests; within a sweep, bound page work and concurrency. Do not infer cancellation from absence in a page. Keep backend activation retry timing separate from Co-Signer organization-poll backoff. This is a scheduler contract, not a new queue or terminal-status endpoint.

The current Co-Signer scheduler already dispatches returned work (`internal/worker/scheduler.go:114–127`), so this approach follows an existing pattern.

**Validation:** cancel between pages; restore a local admission record for a cancelled request; fail backend discovery; keep more than two pages pending with early items waiting indefinitely; add new items during a sweep. Assert no cached-only organization calls, eventual polling of every eligible page and revisiting of old requests, bounded concurrency, zero calls while idle/auto, and saved-receipt redelivery without another organization call.

## Simplification recommendation S1 — derive execution status

DESIGN:161 adds `EXECUTING`, `COMPLETED`, and `EXECUTION_FAILED` to the approval record, alongside existing outgoing and ledger statuses. No consumer is assigned to synchronize every terminal execution result into this third lifecycle.

Prefer storing the approval lifecycle and decision, the unique outgoing relation, and any pre-activation failure code. Derive execution progress/outcome from the outgoing once it exists. A pre-activation failure still needs a durable terminal representation because there is no outgoing. Preserve immutable decision evidence independently of later cancellation/failure.

This removes synchronization writes, duplicate terminal-event handling, and another set of recovery cases. It changes the proposed schema, so it is a recommendation, not an edit already applied. If a persisted execution projection is necessary for measured query cost, define one lifecycle consumer and prove replay/order behavior; do not make it a second authoritative execution state.

## What already exists

| Capability | Reuse decision |
| --- | --- |
| Combined principal/fee reservation and deterministic balance locks | Keep `LedgerWithdrawalReservationService.createReservation`; adapt only the closed source union. |
| Journal status CAS, balance deltas, ledger status history | Reuse for atomic pre-activation terminalization; R2 identifies the missing orchestration. |
| SIGN aggregate transaction and binding CAS | Reuse; R1 adds an explicit durable caller, not another MPC state machine. |
| Outgoing broadcast, receipts, fee settlement and ambiguity handling | Keep the current owner after SIGN admission; do not presume it recovers `CREATED` today. |
| Ed25519 authentication, JCS, strict contract fixtures | Keep. A stable signed business receipt is still needed in addition to HTTP authentication. |
| Co-Signer returned-work scheduling and immutable file publication | Follow existing patterns; keep evidence outside share custody directories. |

Required new pieces are a durable approval request, authenticated decision/discovery contracts, a bounded dispatcher with a durable evidence journal, activation/handoff orchestration, and independent prepared-byte verification. None can be removed while retaining the approved requirements. No new infrastructure dependency is justified by this review.

## NOT in scope

- A callback server, second organization status endpoint, or generic transport adapter: the approved repeatable POST already covers v1.
- A broker, workflow engine, second ledger, or separate local authoritative pending queue: PostgreSQL and existing workers are sufficient.
- Splitting principal and fee reservation APIs: the current combined operation already supplies atomicity.
- Quorum approvals, new chains, arbitrary contract calls, fee top-ups, replacements, or changing cancellation boundaries: outside approved scope.
- Refactoring encrypted share storage or MPC internals: independent evidence storage and existing runtime boundaries suffice.
- Infrastructure tuning or benchmark claims: no measured runtime workload was collected.
- Feature implementation and deployment: this is a documentation/source review.

No unrelated follow-up TODOs were proposed; `TODOS.md` was not created or modified. R1–R3 belong in the current feature plan, not a later cleanup backlog.

## Test and failure review

```text
create + two holds [TEST-001..004]
       |
       v
pending -- fresh discovery --> external POST [TEST-007..009]
   |           GAP R3: stale local retries / page starvation
   |
   +-- reject/cancel/fatal preparation --> terminal ledger + release
   |           GAP R2: ledger and internal credit status assertions
   |
   +-- approve --> prepare --> outgoing CREATED commit
                               |
                      GAP R1: crash before SIGN admission
                               |
                      persisted MPC binding --> existing execution
                               |
                     receipt settlement / ambiguity [TEST-016..020]
```

| Path / production failure | Existing planned coverage and handling | Review delta / user-visible effect |
| --- | --- | --- |
| Create loses a write or races for last balance | TEST-001–004; one DB transaction, locks, immutable replay comparison | Adequate plan; no new gap. |
| Indefinite wait with restarts | TEST-005/006/009; durable request/holds, no chain preparation or permits | Adequate plan; approval age alone must never release funds. |
| Organization timeout, malformed response, or lost backend ACK | TEST-007–009; fail closed, durable receipt redelivery | Adequate error policy; R3 defines scheduling eligibility. |
| Reject/cancel before outgoing | TEST-010–012/025; request and journals covered | R2: refunded transfer can still appear pending; add ledger/history assertions. |
| Activation crashes after outgoing commit | TEST-013 currently asserts at most one outgoing | R1: silent indefinite hold without progress; add eventual handoff/failure assertion and explicit recovery owner. |
| Stale lease or cap exceeded during preparation | TEST-013–015B; recheck, fenced commit, pre-activation failure | Include R2 terminal records; no replacement or top-up. |
| Altered bytes, wrong tenant, orphaned MPC | TEST-017–019/021; independent verifier, durable binding, existing orphan rules | Adequate planned scope; exact vectors and parser choice remain STEP-000/006 deliverables. |
| Ambiguous broadcast or invalid fee evidence | TEST-016/020/022; existing settlement/reconciliation retains appropriate holds | Adequate plan; timeout is not proof of non-execution. |
| Old pending pages monopolize polling | TEST-022 is generic about batching/indexes | R3: specify wrap/fairness and continuous-arrival cases. |
| Restored/lost journal or incompatible rollout | TEST-021/026; reconciliation and capability gates | Adequate plan; no automatic reapproval from missing evidence. |

Three missing test specifications accompany R1–R3; they are not three additional independent findings. R1 has the highest recovery impact, but remains a plan-specific P2: atomic dispatch and crash testing are already intended, and their concrete owner/liveness assertion is missing. This review executed no feature tests: the feature does not yet exist. It verified source call sites, handlers, status selectors, and document coverage.

Add small inline state/ownership comments at the future approval worker handoff, pre-activation terminalization method, and dispatcher eligibility loop. The existing design already supplies the broader architecture diagram.

## Shortest safe implementation sequence

First revise the design/plan for R1–R3 and decide S1 before freezing schemas. Then preserve the existing implementation lanes:

| Lane | Modules | Dependency |
| --- | --- | --- |
| Contract | backend schema/fixtures; Co-Signer contract bundle | STEP-000 → 001; settle status ownership and API details first. |
| Backend | approval, ledger, outgoing, public reads | STEP-002 → 003 → 005 → 007; keep money transitions and handoff under one owner. |
| Co-Signer | approval dispatcher/journal, transport, prepared-byte policy | STEP-004 → 006 after contract; independent from backend implementation. |
| Integration | coordinated harness, rollout/recovery docs | STEP-008 after both lanes. |

Backend and Co-Signer lanes may run in parallel after contract freeze. Backend STEP-005 and the backend portion of STEP-006 share SIGN/claim modules and should be serialized or owned together. No new worktrees were created during review.

## Implementation Tasks — proposed, not applied

- [ ] **T1 / R1 (P2)** — Extend STEP-005 and TEST-013 with a durable same-outgoing SIGN handoff owner, explicit acknowledgement boundary, and crash/restart liveness tests. Touch the planned approval worker/store and existing outgoing signing integration. Verify targeted worker tests plus real PostgreSQL fault injection.
- [ ] **T2 / R2 (P2)** — Extend STEP-003/005/007 and TEST-010/015B/025 with atomic debit/credit ledger terminalization, history, and both reservation releases. Reuse ledger/credit transition primitives with scoped request-source lookup. Verify external/internal transfers, retries, and transaction rollback.
- [ ] **T3 / R3 (P2)** — Extend STEP-004 and TEST-007/009/022 with fresh-page-only polling, no cached-only retry, bounded repeatable pagination, and cancellation/discovery-outage tests. Verify Go dispatcher integration and fake-clock paging tests.

S1 is an optional schema simplification, not a prerequisite for the correctness remedies if an explicit projection owner is supplied instead. Implementation effort was not estimated; no repository AI-compression table was available and this review did not measure build time.

## Review provenance and completion

The enabled external Claude Code attempt failed authentication on 2026-10-19; session `47175dc6-4e30-4a9b-ab37-1692be5dcc29`, zero input/output usage, no completed outside review. A fresh-context in-host Codex reviewer completed the permitted fallback. It independently identified R2 and R3, then validated R1 in a bounded source-backed follow-up. Its P2 assessment of R1 was adopted: this is an unspecified handoff, not proof that every implementation of the plan would lose work. External cross-model agreement is not claimed.

| Area | Result |
| --- | --- |
| Scope challenge | Approved scope retained; no new product choices required. |
| Architecture | R1 and R2; explicit ownership gaps, not evidence of deployed feature defects. |
| Code quality / simplicity | S1 recommended; retain direct services and existing primitives. |
| Tests | Coverage diagram and three concrete assertion/recovery gaps produced; no tests executed. |
| Performance | R3 paging/fairness contract; bounded workers and short locks retained. |
| NOT in scope / existing code | Recorded above. |
| TODO updates | None proposed or written. |
| Critical gap count | 0 under the strict no-test/no-handling definition; three concrete specifications need correction, with R1 carrying the largest liveness risk. |
| Outside voice | External unavailable; native fallback completed, model identity not separately verified. |
| Parallelization | Two implementation lanes after contracts; shared backend SIGN edits serialized. |
| Lake Score | N/A: no new remedy was selected by the user during this review. |
| Engineering readiness | **NOT CLEARED**: three findings open; design approval remains valid. |

## Supporting standards

Short database transactions and consistent lock ordering follow [PostgreSQL explicit locking](https://www.postgresql.org/docs/current/explicit-locking.html). Reusing a specified canonical encoding for signed receipts follows [RFC 8785 (JCS)](https://www.rfc-editor.org/rfc/rfc8785). These references support existing design choices; the repository findings above come from local source inspection.

**UNRESOLVED DECISIONS:**
- R1: specify the durable owner and acknowledgement boundary between outgoing commit and SIGN admission.
- R2: specify and implement the pre-activation ledger-status/history mapping together with both refunds.
- R3: freeze fresh-pending-page eligibility and fair repeatable pagination.
- S1 (optional): derive execution status from outgoing, or explicitly assign a persisted projection owner.
