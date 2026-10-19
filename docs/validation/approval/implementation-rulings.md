# Implementation rulings

These are the 21 implementation decisions retained from the main controller's execution ledger on 2026-10-19. They preserve its reasoning and costs if wrong. They are not new product decisions or another review. The final ruling explicitly distinguishes accepted local Linux proof from unperformed hosted CI and staging release gates.

## IMPLEMENTATION-RULING-001: Current implementation authorization

**Decision:** The user's authorization to implement supersedes historical documentation-only constraints and unstarted labels.

**Reason:** The latest explicit user request governs.

**Cost if wrong:** Local changes must be reverted; no deployment occurs.

## IMPLEMENTATION-RULING-002: Controller-only review

**Decision:** Use no review subagents. The main controller reviews every task and the final diff.

**Reason:** The latest explicit user instruction overrides SDD reviewer dispatch.

**Cost if wrong:** There is less independent review coverage.

## IMPLEMENTATION-RULING-003: Implementer model and effort

**Decision:** Use GPT-6.1 Sol medium implementers, with xhigh for cross-repository contract, security and concurrency tasks.

**Reason:** This follows the user's explicit model preference.

**Cost if wrong:** Implementation is slower or more costly.

## IMPLEMENTATION-RULING-004: Isolated backend worktree

**Decision:** Use a manually linked backend worktree under its ignored `.worktrees` directory.

**Reason:** The native worktree tool targets only this Co-Signer chat repository; the primary backend checkout contains unrelated documentation.

**Cost if wrong:** The worktree needs manual cleanup.

## IMPLEMENTATION-RULING-005: Stable step-to-brief mapping

**Decision:** Extract briefs from a plan-scoped execution view mapping Task 1 through Task 9 to PLAN-STEP-000 through PLAN-STEP-008.

**Reason:** The helper accepts Task N headings, while the canonical plan uses stable PLAN-STEP IDs.

**Cost if wrong:** Correct the brief mapping before execution.

## IMPLEMENTATION-RULING-006: Direct focused Jest invocation

**Decision:** Invoke Jest with `npm exec -w brosettlement-client-api -- jest` rather than nested `npm run test:client` flag forwarding.

**Reason:** npm 11 consumes nested flags instead of forwarding them reliably.

**Cost if wrong:** Recheck test selection; never accept an empty suite.

## IMPLEMENTATION-RULING-007: Signed derivation algorithm spelling

**Decision:** Correct the frozen derivation algorithm spelling to the existing uppercase `ECDSA`, retaining `secp256k1`.

**Reason:** Current backend metadata and the committed SIGN fixture use `ECDSA`. Lowercase is an internal policy classification, not the signed derivation JSON value.

**Cost if wrong:** Revise the cross-language fixtures and contract. No signed field may be silently normalized.

## IMPLEMENTATION-RULING-008: TRON token contract representation

**Decision:** Keep `tokenContractCanonical` as the existing `0x` prefix plus 40 lowercase hexadecimal characters; `from` and `to` remain Base58Check.

**Reason:** `tron-transfer-builders.ts` validates canonical20 and converts only for network preparation.

**Cost if wrong:** Token-byte verification mismatches. Correct the technical documentation before shipping new fixtures.

## IMPLEMENTATION-RULING-009: Decision evidence migration ownership

**Decision:** Add append-only signed decision/application evidence persistence in STEP-003 with its transactional behavior, rather than a placeholder table in STEP-001.

**Reason:** STEP-001 exposes durable request identity and receipt validation contracts while capability remains disabled.

**Cost if wrong:** Another additive migration is needed. The full decision path cannot ship without evidence persistence.

## IMPLEMENTATION-RULING-010: Provider preparation outside transactions

**Decision:** Keep legacy provider preparation outside the database transaction, with guarded identity/capability preflight and an authoritative repeated check before reservation.

**Reason:** Existing RPC preparation occurs outside the database transaction. Holding identity locks and an interactive transaction across provider I/O introduces timeout/resource regressions. The final guard prevents duplicate holds, and rollout requires old writers to drain.

**Cost if wrong:** Rework the intake race protocol. No deployment or enablement occurs now.

## IMPLEMENTATION-RULING-011: Canonical zero fee cap

**Decision:** Accept canonical UInt zero `maxNetworkFeeAtomic` at intake, preserve existing chain-specific requested-fee validation, and enforce fresh prepared budget less than or equal to the cap after approval.

**Reason:** Existing total-cap policy does not prohibit zero, combined holds accept non-negative amounts, and the authorization wire already defines UInt. Estimating a minimum before approval would violate deferred preparation.

**Cost if wrong:** A zero-cap request may later fail preparation and require replacement. There is no automatic cap increase.

## IMPLEMENTATION-RULING-012: Historical receipt signature verification

**Decision:** Reverify an exact saved receipt replay against retained API-key-version public material, without current eligibility or current environment-config checks.

**Reason:** The frozen contract requires historical verification and auditability. Immutable evidence alone was safe for disposition but did not execute this requirement.

**Cost if wrong:** Each replay adds one indexed retained-key lookup and Ed25519 verification. Damaged verification material must fail internally without rewriting evidence or state, or fabricating a rejection.

## IMPLEMENTATION-RULING-013: Cancellation targets the original DEBIT

**Decision:** Cancellation accepts the original principal DEBIT ledger ID returned at creation, not the recipient's expected CREDIT row ID.

**Reason:** The expected CREDIT is a separate receiving accounting row. Shared read projections do not require another cancellation entrypoint. The narrow request principal foreign key matches the approved stable transfer identity and `withdrawals:revoke`.

**Cost if wrong:** A strict request-source/recipient reverse resolver may be needed later. CREDIT-targeted calls currently return `409 APPROVAL_NOT_REQUIRED` with no state or money change; unknown or foreign IDs return 404.

## IMPLEMENTATION-RULING-014: Bounded saved-delivery backpressure

**Decision:** Apply narrow approval-evaluation backpressure when all 1000 bounded retry entries are saved-delivery hints. Do not evict those hints for organization-suppression entries. Keep bounded discovery/stream recovery and fair oldest-attempt saved retries running.

**Reason:** Continuous journal growth could otherwise prevent EOF and lose older lost-ACK retry hints. This preserves evidence and fair recovery without another durable queue or disk segmentation.

**Cost if wrong:** New approval evaluations pause longer during a large delivery outage. Original pending holds remain intact, with no rejection, expiry or refund. Capacity resumes after ACK; permanent errors require visible reconciliation.

## IMPLEMENTATION-RULING-015: Execution-known witness and binding

**Decision:** Publish one narrow immutable execution-known SHA-256 marker before the exact `ExecutionBinding` artifact, and require both exact-bound artifacts before MPC. A partial pair or hash mismatch fails integrity and requires manual reconciliation; it is never regenerated from a backend claim.

**Reason:** A single missing execution file otherwise cannot be distinguished from first binding. The retained witness supplies this distinction without a global history engine.

**Cost if wrong:** Each execution adds one create-only fsync artifact, and partial two-file publication requires manual reconciliation. Deleting or rolling back both artifacts or the complete request history remains subject to the explicit coordinated-volume-restore assumption, rather than an absolute anti-rollback guarantee.

## IMPLEMENTATION-RULING-016: Minimal shared origin resolver timing

**Decision:** Move only the minimal shared tenant-scoped closed legacy/approval ledger-origin resolver and direct terminal withdrawal/CREDIT lookups from Task8 into Task6.

**Reason:** CREATED handoff failure must prove that the existing outgoing lifecycle actually finds and releases original approval holds, rather than only emitting an event no consumer can resolve.

**Cost if wrong:** The small common resolver appears one stage earlier and needs Task8 integration revalidation. Full deposit-txid/adoption, both chain-review mutations, reconciliation and public consumer completion remain in Task8. No reverse outgoing-to-request `FOR UPDATE` is added.

## IMPLEMENTATION-RULING-017: Trusted TRON TAPOS verification

**Decision:** Verify approved TRON TAPOS against an explicitly configured trusted HTTPS JSON-RPC endpoint for each supported network. Check `eth_chainId` and the canonical referenced block. Keep pure byte parsing separate, and fail closed on missing, unavailable or mismatched evidence.

**Reason:** `Transaction.raw` contains no chain ID. Existing local chain labels cannot prove membership of the selected network.

**Cost if wrong:** Approved TRON SIGN adds one bounded read-only network check, endpoint availability/configuration requirements and operational trust in the configured provider. No new approval mode, generic RPC framework or public fallback is introduced.

## IMPLEMENTATION-RULING-018: Narrow public v2 claim errors

**Decision:** Register only the new v2 SIGN claim as a public-error target, with `409 APPROVAL_PROTOCOL_REQUIRED`, one safe `409 MPC_INTENT_CONFLICT` for existing SIGN conflicts, and existing `404 RESOURCE_NOT_FOUND`. Leave v1/DKG target registration and legacy error branches unchanged.

**Reason:** The frozen protocol requires stable 409 behavior and preserved old envelopes. The existing public mapper would otherwise turn an unknown 409 into 400.

**Cost if wrong:** New v2 clients may later need finer conflict codes. No existing v1/DKG response format is migrated.

## IMPLEMENTATION-RULING-019: Local HTTPS toolchain variant

**Decision:** Build only the local HTTPS coordinated Co-Signer fixture with Go 1.27.2, standard `SSL_CERT_FILE`, and explicit `GODEBUG=x509sslcertoverrideplatform=1`. Keep `go.mod` unchanged and retain minimum-Go 1.24 full unit, race and contract proof separately.

**Reason:** Official `crypto/x509` support for macOS environment trust roots changed in Go 1.27; installed Go 1.24 uses Keychain. This avoids a production CA-file feature, Keychain mutation, insecure TLS or a second Linux runtime harness.

**Cost if wrong:** Local E2E adds a newer toolchain variant and does not prove private-CA configuration on minimum-Go Darwin. Actual certificate/hostname verification and final minimum-toolchain tests remain required.

## IMPLEMENTATION-RULING-020: Owned immutable-evidence cleanup and aging

**Decision:** Only in the owned approval coordinated test, retain immutable approval rows until the existing parent runner disposes its exact owned PostgreSQL. Require explicit approval mode, loopback and the disposable marker. Age new synthetic-organization requests through a temporary BEFORE INSERT fixture trigger, removed in `finally`.

**Reason:** Append-only evidence correctly forbids generic per-identity deletion. Prisma explicitly inserts `createdAt`, so a column default does not simulate age.

**Cost if wrong:** Scoped rows remain until parent disposal, and age proof is injected creation-time simulation rather than elapsed wall-clock months. Production deletion protections and clocks remain unchanged.

## IMPLEMENTATION-RULING-021: Local Linux proof and hosted release gate

**Decision:** Treat the plan's hosted Linux CI wording as a release gate, while accepting actually executed native-Linux Docker race/crash tests for this local implementation handoff.

**Reason:** Current scope permits isolated tests and local commits, but no shared push or deployment. Local Linux execution proves the filesystem behavior without claiming remote CI.

**Cost if wrong:** Later hosted CI may reveal environment or pipeline differences and require rework before release. Hosted CI and staging remain explicitly unperformed.
