# Task9 local approval proof

This is the historical implementation proof for the revisions below. Detailed
test output and development reviews are linked to their retained Git revision;
they are not current-tree test results. Subsequent cleanup preserves fixture
values and operational guarantees, and must be validated separately.

Status: **ALL_NINE_STEPS_IMPLEMENTED / LOCAL_PROOF_VERIFIED / CONTROLLER_REVIEW_APPROVED**. Main controller task and whole-branch reviews are approved for local handoff. No independent reviewers or review subagents ran.

## Authoritative completion table

This table records the current completion status accepted by the main controller. All nine specification and quality reviews and the final whole-branch review are approved for local handoff. The detailed plan checkboxes are retained as the historical requirements checklist; unchecked historical items do not override this table. Literal hosted CI, staging and deployment requirements remain unperformed release gates, rather than completed local checks.

| Step | Completed scope | Verified status |
| --- | --- | --- |
| STEP000 / PLAN-STEP-000 | Approved design revision, contracts and frozen identifiers pinned | Verified; controller approved |
| STEP001 / PLAN-STEP-001 | Durable request identity and producer-pinned protocol fixtures | Verified; controller approved |
| STEP002 / PLAN-STEP-002 | Atomic principal/cap reservation and stable public state | Verified; controller approved |
| STEP003 / PLAN-STEP-003 | Signed decisions and atomic rejection/cancellation release | Verified; controller approved |
| STEP004 / PLAN-STEP-004 | Separate approval dispatcher and durable journal | Verified; controller approved |
| STEP005 / PLAN-STEP-005 | One activation and recoverable CREATED-to-SIGN handoff | Verified; controller approved |
| STEP006 / PLAN-STEP-006 | Independent prepared-byte verification and execution binding | Verified; controller approved |
| STEP007 / PLAN-STEP-007 | Original ledger read, settlement and reconciliation ownership | Verified; controller approved |
| STEP008 / PLAN-STEP-008 | Actual coordinated MPC/Anvil proof, retained evidence and rollout runbooks | Verified; controller approved |

The [main controller review](https://github.com/BroLabel/brosettlement-mpc-co-signer/blob/24d3d43f6efa70c7095aa8c2b02a68f3971f5c17/docs/validation/approval/controller-review.md) records exact reviewed revisions, coverage and limits. [Implementation rulings](https://github.com/BroLabel/brosettlement-mpc-co-signer/blob/24d3d43f6efa70c7095aa8c2b02a68f3971f5c17/docs/validation/approval/implementation-rulings.md) retains all 21 decisions and their costs if wrong. No independent reviewers or review subagents were used. This local completion is not hosted CI, staging or deployment approval.

## Exact tested identities

- Backend implementation/fixture: 72a20dcb6cf284ca8c1a8d7312a61ab613e4d6f9.
- Co-Signer implementation/binary source: 9cc15089c66e934229e65209237b3926be505365; Go 1.27.2 darwin/arm64; Core v0.5.0; build VCS clean.
- Read-only signer: 79d61c2fd0968cf222643c2d2069c94b4665bbb9; Go 1.25.0; Core v0.5.0; source remained clean.
- Actual old Co-Signer: Git archive 37da3c1e8181db440a7f1c2e3cbef27b4ba8669a, explicit -buildvcs=false.
- Backend evidence/log commits: d7c44458 and 2aac9e12. Subsequent documentation commits do not change tested source or binary hashes.

The [coordinated proof](coordinated-proof.json) records all original public ledger/principal/cap IDs, request/outgoing/session identities, saved unsigned bytes/nonce, signed receipt/authorization hashes, actual ETH hashes, fees, approval/preparation times and cleanup. [Binary provenance](binary-provenance.json) records exact hashes and Go build metadata for four executables. These are owned disposable synthetic fixtures, not live organization data.

## Actual coordinated gate

The existing backend isolated runner's approval-full-e2e mode selects test/mpc-2of3/jest-approval-coordinated.json. Its required-result reporter rejects empty/skipped/failed/wrong selection even with --passWithNoTests.

Result: 1 executed Jest suite / 1 passed test, zero failed/skipped; 194.513 seconds; three confirmed real MPC ETH/Anvil approval withdrawals (external 30 days, external 365 days, explicit auto). The test includes:

- Original cap/principal reservation, stable IDs and idempotent public replay.
- Owned HTTPS PENDING/503/final decisions; discovery outage without cached organization calls.
- Real backend and Co restarts; stopped-writer whole-journal copy/restore; simulated 30/365-day discovery with unchanged original holds and no pre-approval outgoing/bound SIGN/active SIGN job.
- Observed committed CREATED/no-MPC row, hard crash of the sole running backend, recovery of the same outgoing/nonce/unsigned bytes without client retry.
- Actual old Co binary required v1 SIGN refusal, configured current Co/null-null claim refusal with capability temporarily false, cancellation/rejection release and pending-hold drain.
- Node Ed25519 receipt verification and exact receipt replay after settlement.
- Auto path: accepted signed bytes reach real Anvil, one HTTP response is lost, hash/receipt lookup is temporarily hidden; BROADCAST_AMBIGUOUS preserves original holds and one new broadcast, then the same hash reconciles and settles. Auto makes zero organization calls.
- Outgoing preparation time is at/after the APPLIED decision timestamp, compared as UTC; no arbitrary limit on total MPC duration.

All three original caps are 1000000000000000 ETH atomic units. Prepared/actual fees are respectively 26251233908324/21382502841000, 25343605602448/21051622956000 and 25221111331852/21006967128000. Request stays APPROVED; execution status is derived from the confirmed outgoing. All four exact owned PostgreSQL/Vault/NATS/Anvil resources were removed and absence verified. Append-only evidence was retained until parent disposal; the separate focused PostgreSQL container was left temporarily for controller verification at the initial handoff.

## Recorded implementation checks

| Check | Result |
| --- | --- |
| Minimum Go 1.24.0 all-package race | 19 packages, 1200 passing events including parents/helpers; 408 top-level tests; zero failed/skipped tests; one command package without tests |
| Native Linux approval race/crash | 1 package, 79 passing events, zero failed/skipped; source Git-archived into container; actual platform=linux/arm64 temp=/tmp |
| Go 1.27 default transport TLS | 3 leaf cases: trusted CA, rejected wrong CA, rejected hostname |
| Backend focused PostgreSQL | 10 suites / 332 tests, zero skipped; exact owned child dropped and absence verified |
| Backend affected units | 9 / 376, zero skipped |
| Full Swagger/public registry/runtime | 16 / 117, zero skipped; all four new operations integrated |
| Harness unit tests | 44 passing suites / 280 passing tests; 1 existing skipped suite / 5 skipped tests, not actual MPC proof |
| Node runner/result guards | 109 / 109, zero skipped |
| Backend typecheck/build, both contract verifiers | Exit 0 |

Commands for this repository:

~~~sh
GOWORK=off go test -race -json ./... -count=1
GOWORK=off go run ./cmd/mpc-contracts verify
GOTOOLCHAIN=go1.27.2 GOWORK=off go test ./internal/approval -run '^TestExternalPlatformRootProbe$' -v -count=1
~~~

[Minimum-Go test results](https://github.com/BroLabel/brosettlement-mpc-co-signer/blob/24d3d43f6efa70c7095aa8c2b02a68f3971f5c17/docs/validation/approval/go124-race-results.json) and [Linux results](https://github.com/BroLabel/brosettlement-mpc-co-signer/blob/24d3d43f6efa70c7095aa8c2b02a68f3971f5c17/docs/validation/approval/linux-approval-race-results.json) retain every passing test identity, package/count summary and raw-output SHA-256. [TLS output](https://github.com/BroLabel/brosettlement-mpc-co-signer/blob/24d3d43f6efa70c7095aa8c2b02a68f3971f5c17/docs/validation/approval/go127-tls-default-transport.log) and [contract verification](https://github.com/BroLabel/brosettlement-mpc-co-signer/blob/24d3d43f6efa70c7095aa8c2b02a68f3971f5c17/docs/validation/approval/go-contract.log) are retained. Full coordinated command, output, PostgreSQL/Swagger/runner logs, negative selection exit codes, red/green record and AC-001–008 mapping are tracked in the backend repository under docs/validation/transfer-approval/.

Linux uses local image mpc-v3-node22-go125:20260929, ID sha256:4b9f8087613ddd10644dd2ae0ab89e48d91b012abe2eb4bdd4e7642883378e25; read-only module cache, network none, source copied to native container storage. This is local Linux proof, not hosted CI.

## Scope and limitations

The new injected-clock/reopen test executes exact 30/365-day advances. The coordinated fixture ages only the owned approval INSERT before immutable freeze, drops its temporary trigger in finally and retains trusted real HTTP auth time. It does not rewrite an existing row or claim a year of wall-clock execution.

The macOS full binary uses standard SSL_CERT_FILE and GODEBUG=x509sslcertoverrideplatform=1 on Go 1.27.2. Wrong CA and hostname still fail. No application CA setting, insecure TLS flag, Keychain mutation or minimum-module-Go upgrade was added.

Real monetary coordination covers ETH on Anvil. The other accepted native/token forms use pinned real backend producer corpus and scoped lifecycle/independent RPC proof; no real TRON network settlement is claimed. The actual old Co executable is tested. The other compatibility direction is the precisely recorded current-backend v2 null/null transport/application boundary; no arbitrary old backend executable or simultaneous double-old rollback is certified. Double-old rollback is prohibited while approval-origin records/holds exist.

No primary .env, live provider/org/DB, real capability enablement, push/PR/merge/deployment or staging pilot was used. No frozen wire/schema/producer pin changed. Source changes are limited to missing backend security-module wiring, correct Swagger metadata, isolated harness/proof tests and documentation. Main controller review is approved for local handoff; its coverage and retained limits are recorded in [controller-review.md](https://github.com/BroLabel/brosettlement-mpc-co-signer/blob/24d3d43f6efa70c7095aa8c2b02a68f3971f5c17/docs/validation/approval/controller-review.md).

The backend runbook docs/runbooks/transfer-approval-rollout.md covers controlled intake and legacy SIGN pause/drain, unseen backlog, explicit modes, durable volume/backup, pending cancellation, fee policy, journal loss, stuck activation and ambiguous broadcast. [Co-Signer operations](../../approval-operations.md) retains the local custody and journal recovery rules.

Durable operational notes: Prisma supplies INSERT timestamps; raw pg reads of the old timestamp-without-time-zone outgoing field need explicit UTC; metrics use the existing exact name{}=value parser; old binaries require a healthy API at startup; immutable evidence cleanup belongs to owned database disposal. These observations are saved here; no persistent user memory was modified.

## Final controller cleanup

After final controller verification, the main controller stopped the separate focused PostgreSQL container `codex-approval-pg-2c85`, exact ID `e304235b8c969fc04fe1f6c075acad2f9d689e888349ac7bcf36951925c540d5` (PostgreSQL 16, auto-remove, loopback port 56902). A repeated exact-ID container lookup returned no container; absence was verified. No Task9 test services remain.
