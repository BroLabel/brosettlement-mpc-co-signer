# Co-Signer approval: repeat design and plan review

Date: 2026-10-19
Status: **CLEAN — no remaining findings in this documentation revision**
Scope: incorporate the user-approved R1–R3 and S1, then review the revised design and plan against the current repository source. Feature implementation remains unstarted.

## Reviewed revision

| Artifact / source | Revision |
| --- | --- |
| DESIGN | SHA-256 `6f2003b182a3df0d0b6a7fa58a6fc6f6a155022e84531d6d248b976f65b30d9b` |
| PLAN, including final review metadata | SHA-256 `3948daacb72f0f1a4ba31b0cf96d578ee31d34d51f351a88c60b3ca4682292e9` |
| Co-Signer source | `5df5ac25953debeb94f2c37d89a2ad458a73057c` |
| Backend source | `e14e175ff5fb2974907bcd5efd123d20425a5001` |

DESIGN is `docs/superpowers/specs/2026-10-11-co-signer-approval-design.md`; PLAN is `docs/superpowers/plans/2026-10-11-co-signer-approval.md`. Both are in `/Users/vlas/.codex/worktrees/2c85/mpc-co-signer`. Backend source paths below are relative to `/Users/vlas/Work/BroLabel/back-end`.

The original six product decisions were preserved. All three findings and the simplification are incorporated into normative requirements/contracts, implementation steps and test assertions, not just the review footer. The historical first-pass report remains available separately.

## Closure evidence

| Item | Normative design and plan change | Required proof during implementation |
| --- | --- | --- |
| R1: durable outgoing-to-SIGN recovery | DESIGN “Durable outgoing-to-SIGN handoff”; STEP-005 selects APPROVED/no-outgoing and APPROVED/linked CREATED/no-MPC work. Saved outgoing bytes feed the existing signing service. The persisted MPC aggregate/binding acknowledges handoff. CREATED-only failure uses outgoing lifecycle, not pre-activation refunds. | TEST-013/020: kill after outgoing commit; restart without client resubmission; eventual same-outgoing MPC binding or proven failure. Kill after binding commit/lost ACK, expiry during downtime and failure/SIGN races cannot create another session, outgoing, nonce allocation or hold. |
| R2: ledger terminalization before activation | DESIGN state/terminalization contract and STEP-003/005/007 atomically transition request, debit and expected internal CREDIT, history and both holds. Ledger maps to FAILED; request retains exact rejection/cancellation/preparation outcome. Missing expected records roll back. | TEST-010/015B/025: exact debit/credit terminal states and history, both refunds, no recipient posting, duplicates/conflicts, rollback after each write; external and internal, same-asset and token/native cases. |
| R3: fresh pending discovery | DESIGN pending API/polling and STEP-001/004 require every new organization call to come from a fresh authenticated pending page. Local records suppress calls or redeliver final receipts; they cannot schedule polls. Fixed-bound keyset sweeps advance and repeat with bounded work. | TEST-007/009/022: cancelled local records after restart, discovery failure, cancellation between pages, more than two pages, first-page permanent waiters and continuous arrivals; no cached-only calls, fair revisits, bounded resources. |
| S1: derived execution state | Request stores PENDING_APPROVAL, APPROVED, REJECTED, CANCELLED or PREPARATION_FAILED. Outgoing owns execution status/error. Request remains APPROVED after activation; permanent pre-activation failure has no outgoing. No execution-state synchronization consumer. | TEST-024: public status/error projection across CREATED/SIGNING/success/failure/ambiguity, duplicate and reordered events; no mirrored completion writes; cancellation still requires no outgoing. |

## Source feasibility checks

- `apps/client-api/src/transactions/application/create-outgoing/outgoing-transaction-signing.service.ts:112` creates SIGN and binds outgoing in one database transaction. `infra/outgoing-transaction.store.ts:220` performs the conditional CREATED/no-MPC → SIGNING binding and recognizes exact replay. This supports R1 without another MPC state machine.
- `apps/client-api/src/transactions/application/create-outgoing/outgoing-transaction-failure.service.ts:29` conditionally marks outgoing failure and enqueues its lifecycle event transactionally. A CREATED-only transition loses to a committed SIGNING binding. The new plan explicitly keeps refunds under outgoing lifecycle after activation.
- `apps/client-api/src/transactions/infra/outgoing-transaction.store.ts:160` still excludes CREATED from the existing execution worker. The plan now explicitly introduces the approval-worker handoff rather than assuming existing background recovery supplies it.
- `apps/client-api/src/ledger/infra/ledger-transaction.store.ts:103` provides conditional status transition and history; `ledger/application/withdrawals/internal-transfer-credit-lifecycle.service.ts:144` accepts the shared transaction for terminalizing the credit leg. R2 scopes resolution through the request origin; it does not claim that the existing outgoing-only lookup already supports approval records.
- The Co-Signer dispatcher remains planned new code. Fresh-page eligibility and immutable final-receipt delivery have separate responsibilities, so cancellation/discovery failure cannot turn the local evidence journal into an authoritative pending queue.

## Architecture, simplicity and performance assessment

The smallest sound structure remains one backend request linked to the original ledger/journals, one bounded approval worker covering activation and handoff, one atomic pre-activation terminalization operation, and one bounded Co-Signer dispatcher/evidence journal. Existing signing, outgoing failure, receipt settlement and crypto primitives remain the implementation building blocks.

No additional queue/consumer is needed to start SIGN: the durable approved request plus linked outgoing supplies work discovery. The binding supplies acknowledgement. Removing the execution-state mirror also removes synchronization and recovery cases without changing user-visible execution information.

Performance remains a planned verification obligation: bounded page/concurrency/backoff memory, fixed sweep upper bounds, due-work indexes and fair activation/handoff selection are explicit. No throughput, latency or capacity claim was measured in this review.

## Failure coverage

```text
fresh pending page -> external decision -> saved receipt -> backend apply
     |                        |                 |
     |                        |                 +-- ACK loss: receipt-only retry
     +-- cancelled/absent     +-- error/PENDING: later fresh-page eligibility
         no local-only poll

approve -> fresh prepare -> outgoing CREATED commit -> persisted MPC binding
                               |                          |
                      crash: same-outgoing retry     crash: existing lifecycle
                               |
                      proven failure: outgoing event owns refund

reject/cancel/permanent failure BEFORE outgoing
  -> one transaction: request + debit/CREDIT history + both hold releases
```

| Failure | Specified handling / tests | Expected observable outcome |
| --- | --- | --- |
| Crash between outgoing and SIGN commits | R1; TEST-013 | Automatic same-outgoing handoff/failure; no client retry required. |
| Lost ACK after MPC binding | Binding is acknowledgement; TEST-013/018 | Existing session continues under current ownership/orphan rules. |
| Expired saved bytes / failure racing with SIGN | CREATED-only conditional failure; TEST-013/020 | One outcome, no rebuild, no competing refund. |
| Partial reject/cancel/early-failure writes | R2 single transaction; TEST-010/015B/025 | All records/holds commit together or all roll back. |
| Missing expected internal credit | Integrity error; TEST-025 | No partial refund or silent incomplete history. |
| Cancelled cached admission / discovery outage | Fresh-page gate; TEST-007/009 | No new organization call from local state alone. |
| First-page waiters / continuous arrivals | Fixed-bound repeatable sweeps; TEST-022 | Old and later-page eligible requests are revisited. |
| Conflicting/late decision | Immutable receipt/application disposition; TEST-011/019 | No revival or additional reserve/transfer. |
| Altered transaction bytes / cross-tenant binding | Independent verification; TEST-017/019 | No MPC authorization. |
| Ambiguous broadcast / invalid fee receipt | Existing reconciliation; TEST-016/020/022 | Relevant holds retained; no invented refund or replacement. |

No new missing failure-handling/test specification was found. These are planned assertions, not passing runtime tests.

## NOT in scope

Human approval UI, callbacks or a second status endpoint, generic workflow/transport frameworks, another queue/ledger, execution-status synchronization, split reservation APIs, fee top-ups, transaction replacement, new chain support and share-store refactoring remain excluded. Implementation, migrations, feature test execution, commits and deployment were not performed.

## Execution plan and readiness

The original nine steps (000–008) remain. Contract preparation precedes the backend lane (002 → 003 → 005 → 007) and Co-Signer lane (004 → 006); coordinated integration (008) follows both. Shared backend SIGN/claim changes in 005/006 need one owner or sequential integration. No additional implementation tasks or deferred TODOs were produced by the repeat review.

STEP-000 still pins the approved DESIGN revision, maps cancellation RBAC to the current role model and freezes wire schemas/fixture IDs. These are explicit preparation tasks rather than reopened product decisions. The source HEADs above are not substitutes for a pinned DESIGN commit.

Small ownership comments/diagrams belong in the future handoff worker, pre-activation terminalizer and dispatcher eligibility loop.

## Independent review and limits

A new external Claude Code attempt failed authentication: session `319f4f1a-f449-4a11-9331-b5bfefd22a08`, zero input/output usage and no completed outside review. The gstack fallback ran a fresh-context Codex reviewer, which returned `no_findings` and independently verified all four closure paths against relevant source. Its recommendation was to accept the revised documentation because the fixes are consistently incorporated, feasible with existing primitives, and covered by liveness/accounting tests.

No external cross-model agreement is claimed. Review evidence consists of document/source inspection and structural consistency checks. Feature code does not exist yet; no feature tests, production state or real organization endpoint were evaluated. Primary and fallback review found no remaining material issue in this revision; that is not a guarantee about a future implementation.

## Completion summary

- Scope: preserved; all user-approved corrections incorporated.
- Architecture, code quality, performance: zero remaining findings.
- Tests: recovery/accounting/fairness gaps closed in the specification; zero feature tests run.
- Required reuse, scope, failure and parallelization outputs: recorded.
- Prior items closed: R1, R2, R3 and S1.
- New implementation tasks or TODOs from this pass: none.
- Critical gaps: 0; unresolved decisions: 0; Lake Score: N/A.
- Verdict: engineering review cleared for the revised documents; implementation unstarted.

NO UNRESOLVED DECISIONS
