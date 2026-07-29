# MPC 2-of-3 Dual Local Parties Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Run primary party B and recovery party C as one fixed 2-of-3 DKG job, durably publish their encrypted artifacts, and preserve independent A+B production signing.

**Architecture:** One lifecycle-locked process retains the current scheduler and general semaphore, adds a binary DKG guard, and hosts two party-bound core runtimes plus a validated local router. One store implementation is instantiated as immutable primary and recovery capabilities using one lifetime key, while deterministic pre-activation reconciliation and terminal replay replace a durable journal.

**Tech Stack:** Go 1.24, `mpc-core`, signed HTTP mailbox, AES-256-GCM, Linux `renameat2(RENAME_NOREPLACE)`, advisory FD locks, RFC 8785/JCS.

## Global Constraints

- Exactly one A+B+C DKG, product threshold 2; B and C are distinct parties.
- One standard-base64 32-byte key and one `keyRef` encrypt both v1 stores for
  the lifetime of every v1 artifact; no hashing passphrases or key rotation.
- Final paths are `<keyId>.primary.json` and `<keyId>.recovery.json`; create-only
  publication never replaces or adopts an existing file.
- Coordinator receives only `ArtifactEvidence`; decrypted share blobs remain
  inside store/runtime capabilities.
- Recovery store cannot satisfy the production SIGN share-reader interface.
- Reconciliation is actionable DKG only and never scans or mutates unrelated
  files. `COMPLETED` artifacts are normal durable state.
- Process crash does not resume protocol rounds; unsuccessful key IDs are never
  reused.
- Deployment support is Linux, one replica, one local-filesystem writer, and no
  overlapping rollout replica.
- Use TDD and one conventional commit per `PLAN-STEP-*`.

---

## Design Reference

- Repository: `mpc-signer`
- Approved DESIGN: `docs/superpowers/specs/2026-07-27-mpc-2-of-3-recovery-design.md`
- Git revision: `4bb54acdb4afabfeb58b1fa495b2e63fbfb70e83`
- Co-signer PLAN baseline: `5cf9a34d69741e9e95aee3fa67e0e6f2bd222432`

Active requirements are `REQ-001`–`REQ-070` excluding retired `REQ-023`,
`REQ-028`, `REQ-031`, `REQ-033`, `REQ-040`, `REQ-042`, `REQ-043`, and
`REQ-045`. Active invariants are `INV-001`–`INV-045` excluding retired
`INV-024`, `INV-025`, and `INV-027`.

### Repository applicability

- Direct co-signer requirements: `REQ-001`, `REQ-002`, `REQ-004`–`REQ-014`,
  `REQ-016`–`REQ-021`, `REQ-024`–`REQ-026`, `REQ-029`, `REQ-030`,
  `REQ-032`, `REQ-034`, `REQ-036`, `REQ-041`, `REQ-044`, `REQ-046`–`REQ-063`,
  `REQ-065`, `REQ-068`–`REQ-070`.
- Integration requirements: `REQ-003`, `REQ-015`, `REQ-022`, `REQ-027`,
  `REQ-035`, `REQ-037`–`REQ-039`, `REQ-064`, `REQ-067`.
- No co-signer implementation: `REQ-066` (backend key-row ordering).
- Direct invariants: `INV-001`–`INV-004`, `INV-006`–`INV-019`,
  `INV-023`, `INV-026`, `INV-028`–`INV-041`, `INV-044`, `INV-045`.
- Integration invariants: `INV-005`, `INV-020`–`INV-022`, `INV-042`,
  `INV-043`.

### Assumptions, decisions, and risks

| Inventory                     | Co-signer disposition                                                                                                                                                                                                               |
| ----------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `ASSUMPTION-001`              | Consume the core proof and preserve threshold 2 at both local adapters.                                                                                                                                                             |
| `ASSUMPTION-002`, `005`       | Validate filesystem capabilities, lifetime lock, and supported topology before readiness.                                                                                                                                           |
| `ASSUMPTION-003`              | Reject all legacy artifacts/intents.                                                                                                                                                                                                |
| `ASSUMPTION-004`              | Validate key/keyRef/directories and document customer backup responsibility.                                                                                                                                                        |
| `ASSUMPTION-006`              | Accept authenticated SIGN only; do not add mutable local eligibility.                                                                                                                                                               |
| `ASSUMPTION-007`              | Document quorum-bearing host risk without inventing an activation ceremony.                                                                                                                                                         |
| `DECISION-001`–`DECISION-035` | Directly implement `001`–`013`, `015`, `018`–`032`, `034`, `035`; integrate `014`, `016`, `033`; `DECISION-017` is retired.                                                                                                         |
| `RISK-001`–`RISK-017`         | All risks affect co-signer operations. Mitigations are key custody docs, strict artifacts, fail-closed capabilities, metrics, locks, deterministic restart, benchmarks, and explicit accepted residual risk—not new control planes. |

## Responsibility Map

| Component/file                             | Primary responsibility                                                                         | Boundary                               |
| ------------------------------------------ | ---------------------------------------------------------------------------------------------- | -------------------------------------- |
| `internal/contract/mpc2of3/`               | Exact descriptor, terminal, digest, and corpus codecs                                          | Does not create descriptors            |
| `internal/config/`                         | Stable deployment ID, B/C bindings, directories, key/keyRef, lock/free-space/preparams profile | No secret logging                      |
| `internal/sharestore/`                     | Encrypted v1 envelope, Linux publish, inspect, primary load                                    | Recovery has no SIGN capability        |
| `internal/localrouter/`                    | B↔C validated frames and network/local route split                                             | Same logical envelope checks           |
| `internal/worker/dkg_coordinator.go`       | Two handles, common start barrier, sibling cancellation, evidence comparison                   | One scheduler job                      |
| `internal/worker/scheduler.go`             | General permits, fixed binary DKG guard, one DKG attempt per batch                             | SIGN continues when DKG is blocked     |
| `internal/terminal/`                       | Immutable request bytes, retry policy, typed authoritative response                            | No maximum attempts                    |
| `internal/reconcile/`                      | Actionable-only startup matrix                                                                 | No directory scan/inventory cleanup    |
| `internal/lifecycle/` and `cmd/co-signer/` | Lock-first startup, readiness, intake, drain, lock-last shutdown                               | One local filesystem                   |
| `internal/health/`, `internal/metrics/`    | Process/signing/provisioning health and safe inventory metrics                                 | Queue age alerts do not stop admission |

## Requirement-to-Step Mapping

| Design items                                                                                                                                                                                                | Ownership                | Steps                                  | Tests               | Evidence                      |
| ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------ | -------------------------------------- | ------------------- | ----------------------------- |
| `REQ-001`, `002`, `024`, `034`, `044`, `047`, `048`, `065`; `INV-001`, `002`, `014`, `026`, `029`, `041`                                                                                                    | strict contract          | `PLAN-STEP-CS-001`                     | `TEST-CS-001`–`004` | corpus/validation             |
| `REQ-008`–`014`, `REQ-021`, `REQ-025`, `REQ-026`, `REQ-049`, `REQ-060`–`063`, `REQ-069`; `INV-008`, `INV-013`, `INV-015`, `INV-017`–`019`, `INV-026`, `INV-035`, `INV-037`, `INV-039`, `INV-040`, `INV-044` | store/config             | `PLAN-STEP-CS-002`, `PLAN-STEP-CS-003` | `TEST-CS-005`–`014` | filesystem/fuzz/readback      |
| `REQ-004`–`007`, `REQ-014`, `REQ-019`, `REQ-029`, `REQ-036`, `REQ-046`, `REQ-053`–`055`; `INV-003`, `INV-004`, `INV-010`, `INV-016`, `INV-018`, `INV-023`, `INV-032`, `INV-033`, `INV-038`–`040`            | dual coordinator/router  | `PLAN-STEP-CS-004`, `PLAN-STEP-CS-005` | `TEST-CS-015`–`022` | local integration/race        |
| `REQ-016`–`018`, `REQ-038`, `REQ-052`, `REQ-056`–`058`, `REQ-064`, `REQ-068`; `INV-009`, `INV-011`, `INV-034`, `INV-038`, `INV-041`, `INV-045`                                                              | scheduler/terminal       | `PLAN-STEP-CS-006`, `PLAN-STEP-CS-007` | `TEST-CS-023`–`031` | stateful HTTP/admission       |
| `REQ-050`, `051`, `057`, `070`; `INV-030`, `031`, `035`, `045`                                                                                                                                              | lifecycle/reconciliation | `PLAN-STEP-CS-008`                     | `TEST-CS-032`–`038` | subprocess/matrix             |
| `REQ-020`, `030`, `041`, `059`; `INV-006`, `007`, `028`, `036`, `044`                                                                                                                                       | recovery proof/docs      | `PLAN-STEP-CS-009`                     | `TEST-CS-039`–`043` | isolated B+C/product boundary |

## Implementation Steps

### PLAN-STEP-CS-001: Add strict shared contract codecs

**Sources:** `REQ-001`, `REQ-002`, `REQ-024`, `REQ-034`, `REQ-044`,
`REQ-047`, `REQ-048`, `REQ-065`

**Files:**

- Create: `internal/contract/mpc2of3/descriptor.go`
- Create: `internal/contract/mpc2of3/terminal.go`
- Create: `internal/contract/mpc2of3/digest.go`
- Create: `internal/contract/mpc2of3/jcs.go`
- Create: `internal/contract/mpc2of3/corpus_test.go`
- Create: `contracts/mpc-2of3/v1/`
- Modify: `go.mod`
- Modify: `go.sum`

**Interfaces:**

- Parses exact canonical descriptor bytes without repair/reserialization.
- Produces branded descriptor/artifact/terminal/chain-code digest types.
- Serializes minimal `COMPLETED`/`FAILED` terminal payload once.

- [ ] Add failing shared-corpus tests for canonical/noncanonical descriptor,
      terminal statuses, duplicate/unknown keys, order, ASCII/numbers, and digest
      encodings.
- [ ] Run `GOWORK=off go test ./internal/contract/mpc2of3 -count=1`; expect the
      module/corpus to be absent.
- [ ] Pin RFC 8785 behind a local adapter, implement strict token-first parsing,
      fixed roster semantics, exact key ID regex, and canonical terminal encoding.
- [ ] Run the package and manifest verifier; expect byte-identical hashes with
      the canonical signer corpus.
- [ ] Commit with `feat(contract): add strict 2-of-3 wire codecs`.

### PLAN-STEP-CS-002: Replace configuration with two immutable store profiles

**Sources:** `REQ-008`–`REQ-011`, `REQ-025`, `REQ-044`, `REQ-062`, `REQ-069`,
`ASSUMPTION-004`

**Files:**

- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `cmd/co-signer/runtime_helpers.go`
- Create: `internal/sharestore/config.go`
- Create: `internal/sharestore/key_provider.go`
- Modify: `README.md`
- Modify: `SECURITY.md`

**Interfaces:**

- Config adds stable deployment ID, primary/recovery party IDs and directories,
  stable state/lock paths, keyRef, free-space threshold, and explicit preparams
  generation parallelism.
- Key parser accepts standard base64 decoding to exactly 32 bytes; no hash
  derivation.

- [ ] Add failing tests for hashed passphrases, wrong key lengths, same paths,
      party/purpose mismatch, changed keyRef, relative/overlapping final paths, and
      missing stable deployment ID.
- [ ] Run `GOWORK=off go test ./internal/config ./internal/sharestore ./cmd/co-signer -count=1`;
      expect current single-store/passphrase behavior to fail.
- [ ] Build one immutable primary and one recovery `StoreConfig` using the same
      provider/keyRef, remove `CO_SIGNER_PARTY_ID`/single `SHARES_DIR` semantics,
      and expose no key bytes from config objects.
- [ ] Run the same packages; expect strict startup validation and redacted errors.
- [ ] Commit with `feat(config): bind primary and recovery stores`.

### PLAN-STEP-CS-003: Implement create-only encrypted artifact v1

**Sources:** `REQ-011`–`REQ-014`, `REQ-021`, `REQ-026`, `REQ-049`,
`REQ-060`–`REQ-063`, `DECISION-024`, `DECISION-025`

**Files:**

- Replace: `internal/sharestore/file_store.go`
- Create: `internal/sharestore/artifact.go`
- Create: `internal/sharestore/fs_linux.go`
- Create: `internal/sharestore/fs_unsupported.go`
- Create: `internal/sharestore/reader.go`
- Create: `internal/sharestore/publisher.go`
- Create: `internal/sharestore/primary_reader.go`
- Create: `internal/sharestore/artifact_test.go`
- Create: `internal/sharestore/fs_linux_test.go`
- Create: `internal/sharestore/artifact_fuzz_test.go`
- Create: `testdata/artifact-v1/`

**Interfaces:**

- Produces `PublishAndInspect` and `InspectExisting` returning only
  `ArtifactEvidence`.
- Produces a separate primary-only SIGN reader; recovery store does not satisfy it.
- Uses temp/write/sync/close/`RENAME_NOREPLACE`/dir-sync/no-follow/readback/
  decrypt/inspect ordering.

- [ ] Add failing golden, syscall-recorder, real-filesystem, corruption, size,
      symlink/nonregular, `ErrArtifactExists`, readback, entropy, and crash-boundary
      tests.
- [ ] On Linux, run `GOWORK=off go test ./internal/sharestore -count=1`;
      expect overwrite and incomplete durability behavior to fail.
- [ ] Implement the closed envelope/payload, strict padded-base64 readers,
      Linux-only filesystem adapter, exact final-byte hashing, core evidence
      inspection, and best-effort secret-buffer clearing.
- [ ] Run normal tests, a bounded 10-second fuzz run, and helper-process crash
      tests; every final file must strictly inspect and all published bytes remain
      unchanged.
- [ ] Commit with `feat(store): publish immutable recovery artifacts`.

### PLAN-STEP-CS-004: Run B and C behind one coordinator and validated local router

**Sources:** `REQ-005`–`REQ-007`, `REQ-014`, `REQ-019`, `REQ-029`, `REQ-046`,
`INV-004`, `INV-015`, `INV-039`, `INV-040`

**Files:**

- Create: `internal/localrouter/router.go`
- Create: `internal/localrouter/validation.go`
- Create: `internal/localrouter/router_test.go`
- Create: `internal/worker/dkg_coordinator.go`
- Create: `internal/worker/dkg_coordinator_test.go`
- Refactor: `internal/worker/session_worker.go`
- Modify: `internal/transport/http_transport.go`
- Modify: `internal/transport/http_transport_test.go`

**Interfaces:**

- B and C each receive an independent runtime, party ID, transport, store, and
  cancellation context.
- Local frames use the same session/sender/recipient/round/sequence/dedupe
  validation; only A-bound frames use HTTP.
- Coordinator compares post-runtime `InspectExisting` evidence only.

- [ ] Add failing tests for two distinct runtime calls, threshold 2 unchanged,
      B↔C local delivery, A routing, spoofed/wrong-round/duplicate frames, sibling
      cancellation, and post-runtime evidence mismatch.
- [ ] Run `GOWORK=off go test -race ./internal/localrouter ./internal/worker ./internal/transport -run 'DKG|Local|Party' -count=1`;
      expect the single-party worker to fail.
- [ ] Split SIGN and DKG worker paths, add the local router and common start
      barrier, and ensure both runtimes stop before authoritative inspection.
- [ ] Run the full worker/transport/localrouter suites with `-race`.
- [ ] Commit with `feat(dkg): coordinate dual local parties`.

### PLAN-STEP-CS-005: Acquire two sealed handles and defer refill

**Sources:** `REQ-053`–`REQ-055`, `DECISION-023`, `INV-032`, `INV-033`,
`INV-038`

**Files:**

- Modify: `internal/worker/dkg_coordinator.go`
- Create: `internal/preparams/profile.go`
- Create: `internal/preparams/controller.go`
- Create: `internal/preparams/controller_test.go`
- Modify: `cmd/co-signer/main.go`
- Modify: `cmd/co-signer/main_test.go`

**Interfaces:**

- Acquire B, then C, then open start barrier; failed second acquire discards B.
- Target=2, worker=1, explicit parallelism, no sync fallback, no auto-refill;
  refill resumes only after both runtimes stop.

- [ ] Add failing state/race tests for acquisition order, second-acquire failure,
      no early party start, cancellation, no refill while active, and refill after
      every outcome.
- [ ] Run `GOWORK=off go test -race ./internal/preparams ./internal/worker ./cmd/co-signer -run 'PreParams|Barrier|Refill' -count=1`;
      expect missing handle integration.
- [ ] Wire core handles as opaque values and use the controller to pause/resume
      refill without exposing or reserving material.
- [ ] Run the same packages; expect both handles consumed/discarded exactly once.
- [ ] Commit with `feat(preparams): gate dual-party dkg startup`.

### PLAN-STEP-CS-006: Add the binary DKG scheduler guard

**Sources:** `REQ-056`–`REQ-058`, `REQ-063`, `REQ-068`, `DECISION-022`,
`RISK-014`

**Files:**

- Modify: `internal/worker/scheduler.go`
- Modify: `internal/worker/scheduler_test.go`
- Create: `internal/worker/permits.go`
- Create: `internal/metrics/scheduler.go`

**Interfaces:**

- One serialized dispatcher owns `TryAcquire` for fixed DKG guard and general
  slots; workers receive already-held permits.
- At most one DKG claim per ordered batch; skipped DKG never blocks visible SIGN.

- [ ] Add failing deterministic tests for guard-first/general-second order,
      no claim without both permits, claim conflict release, one attempt per batch,
      continued SIGN, low-disk DKG skip, concurrent wakeups, and accepted
      head-of-line behavior.
- [ ] Run `GOWORK=off go test -race ./internal/worker -run Scheduler -count=1`;
      expect current general-only dispatcher to fail.
- [ ] Add nonblocking fixed guard, typed permit ownership, oldest ordered batch
      traversal, and safe wakeups without a second polling lane.
- [ ] Run scheduler tests and a no-wall-clock starvation simulation; expect no
      duplicate DKG and continued SIGN admission.
- [ ] Commit with `feat(scheduler): serialize dkg admission`.

### PLAN-STEP-CS-007: Publish immutable terminal results until authoritative

**Sources:** `REQ-018`, `REQ-038`, `REQ-052`, `REQ-058`, `REQ-064`,
`INV-011`, `INV-034`

**Files:**

- Modify: `internal/monolith/types.go`
- Refactor: `internal/monolith/client.go`
- Create: `internal/terminal/publisher.go`
- Create: `internal/terminal/retry.go`
- Create: `internal/terminal/publisher_test.go`
- Modify: `internal/worker/dkg_coordinator.go`

**Interfaces:**

- Serializes request bytes once; retries with `RetryPolicy.NextDelay` and
  context-aware `Sleeper.Sleep` without `maxAttempts`.
- Typed 200/409 response always carries authoritative status/fingerprint.
- DKG/general permits remain held until accepted, exact replay, or typed conflict.

- [ ] Add stateful HTTP tests for commit-then-EOF, byte-identical replay,
      malformed 200/409, 5xx, unexpected 4xx, long failure sequences, conflict,
      cancellation, and concurrent SIGN.
- [ ] Run `GOWORK=off go test ./internal/terminal ./internal/monolith ./internal/worker -run 'Terminal|PostResult|Retry' -count=1`;
      expect current three-attempt client to fail.
- [ ] Isolate generic bounded HTTP operations from lifecycle terminal
      publication, add typed responses and capped jittered retry, and retain permits
      through all unconfirmed outcomes.
- [ ] Run the stateful tests with injected no-sleep policy; verify no retry limit
      and no attempt after lifecycle cancellation.
- [ ] Commit with `feat(terminal): retry authoritative dkg results`.

### PLAN-STEP-CS-008: Add lock-first lifecycle and actionable reconciliation

**Sources:** `REQ-050`, `REQ-051`, `REQ-057`, `REQ-070`, `DECISION-019`,
`DECISION-021`, `DECISION-030`

**Files:**

- Create: `internal/lifecycle/coordinator.go`
- Create: `internal/lifecycle/lock_unix.go`
- Create: `internal/lifecycle/lock_test.go`
- Create: `internal/lifecycle/subprocess_test.go`
- Create: `internal/reconcile/reconciler.go`
- Create: `internal/reconcile/reconciler_test.go`
- Modify: `cmd/co-signer/main.go`
- Modify: `internal/monolith/client.go`
- Modify: `internal/health/server.go`
- Modify: `cmd/co-signer/main_test.go`
- Modify: `internal/health/server_test.go`
- Modify: `internal/monolith/client_test.go`

**Interfaces:**

- Startup: config → FD lock → stores/capabilities → own CLAIMED and actionable
  PENDING DKG reconciliation → scheduler/intake → readiness.
- Exposes `processReady`, `signingReady`, `provisioningReady`.
- Shutdown closes readiness/intake, drains/cancels workers/publishers/refill/
  stores, then releases lock.

- [ ] Add lifecycle-order unit tests, full reconciliation matrix tests, and real
      helper-process lock tests for contention, no backend access by loser,
      graceful/SIGKILL release, drain ownership, and close-on-exec.
- [ ] Run `GOWORK=off go test -race ./internal/lifecycle ./internal/reconcile ./internal/health ./cmd/co-signer -count=1`;
      expect missing startup barrier/lock behavior.
- [ ] Implement fail-fast lock and actionable exact-path reconciliation,
      including claim-then-fail for PENDING DKG+artifact, exact replay for matching
      own CLAIMED, failure for missing/partial/mismatch, and no mutation of all
      other files.
- [ ] Run tests on Linux; snapshot bytes, mode, mtime, and content for
      nonactionable files and require equality after reconciliation.
- [ ] Commit with `feat(lifecycle): reconcile dkg under local lock`.

### PLAN-STEP-CS-009: Add recovery proof, observability, and operational contract

**Sources:** `REQ-020`, `REQ-030`, `REQ-041`, `REQ-059`, `REQ-068`–`REQ-070`,
`DECISION-004`, `DECISION-031`, `DECISION-034`

**Files:**

- Create: `internal/integration/mpc2of3_test.go`
- Create: `internal/integration/recovery_harness_test.go`
- Create: `internal/metrics/filesystem.go`
- Modify: `internal/health/server.go`
- Modify: `README.md`
- Modify: `SECURITY.md`
- Create: `docs/artifact-format-v1.md`
- Create: `docs/runbooks/recovery-artifact.md`
- Create: `scripts/verify-deployment-contract.sh`
- Create: `scripts/testdata/deployment-valid.yaml`
- Create: `scripts/testdata/deployment-overlap.yaml`

**Interfaces:**

- Test-only harness receives copied B/C artifacts, shared test key, production
  readers, and generic runtime only.
- Deployment verifier requires replica count 1, no overlap, one stable writable
  state topology, and stable lock path; production manifest ownership remains
  external.

- [ ] Add failing product-boundary tests for isolated B+C, C deletion/corruption,
      B deletion/corruption, failed C publication, wrong key/keyRef, no old-key
      fallback, safe metrics, and valid/invalid rendered deployment fixtures.
- [ ] Run `GOWORK=off go test ./internal/integration ./internal/metrics ./internal/health -count=1`
      and the deployment verifier; expect missing proof/docs/metrics behavior.
- [ ] Build the test-only isolated harness, safe metrics/readiness, artifact and
      custody docs, runbooks, and manifest contract checker without a production
      recovery API or binary.
- [ ] Run `GOWORK=off go test -race ./... -timeout=30m`, bounded fuzz tests, and
      deployment verification; ensure failure output contains no test key/share.
- [ ] Commit with `test(recovery): prove isolated B-C signing`.

## Test Matrix

| Test IDs            | Behavior                                                       | Step               | Evidence                    |
| ------------------- | -------------------------------------------------------------- | ------------------ | --------------------------- |
| `TEST-CS-001`–`004` | Descriptor/terminal/digest/corpus strictness                   | `PLAN-STEP-CS-001` | shared vectors              |
| `TEST-CS-005`–`007` | Key/path/store binding                                         | `PLAN-STEP-CS-002` | config tests                |
| `TEST-CS-008`–`014` | Publish order, no-replace, readback, corruption, fuzz, crashes | `PLAN-STEP-CS-003` | recorder/real FS/subprocess |
| `TEST-CS-015`–`018` | Two runtimes, local routing, validation, cancellation          | `PLAN-STEP-CS-004` | race/integration            |
| `TEST-CS-019`–`022` | Two handles, barrier, refill lifecycle                         | `PLAN-STEP-CS-005` | state/race tests            |
| `TEST-CS-023`–`026` | Guard, permits, batch admission, SIGN concurrency              | `PLAN-STEP-CS-006` | deterministic scheduler     |
| `TEST-CS-027`–`031` | Lost response, retries, typed conflict, shutdown               | `PLAN-STEP-CS-007` | stateful HTTP               |
| `TEST-CS-032`–`035` | Lock lifecycle and process death                               | `PLAN-STEP-CS-008` | helper process              |
| `TEST-CS-036`–`038` | Reconciliation matrix/nonmutation/readiness                    | `PLAN-STEP-CS-008` | exact-path tests            |
| `TEST-CS-039`–`043` | B+C proof, A+B boundaries, key lifetime, deployment            | `PLAN-STEP-CS-009` | isolated harness/contracts  |

## Migration, Compatibility, and Recovery

- **Configuration migration:** replace single party/share directory and hashed
  secret behavior with explicit deployment ID, two party/directory bindings,
  strict standard-base64 key, keyRef, state/lock path, free-space threshold,
  and generation parallelism. Startup fails closed if any mandatory value or
  capability is absent.
- **Artifact migration:** N/A — clean cut starts before legacy shares require
  preservation. Existing mutable `.json` shares are not read or converted.
- **Deployment order:** core handle/inspector release, backend mailbox/terminal
  contract, signer canonical proto, then co-signer. DKG admission remains closed
  until all manifest hashes match.
- **Rollback:** safe only before v1 activation. After activation retain the
  original key/keyRef and perform coordinated forward recovery.
- **Crash recovery:** no protocol resume. Own actionable intents replay
  completion from exact B/C or publish FAILED; unrelated files remain untouched.
- **Customer recovery:** backup/restore B, C, original key, and keyRef. V1
  provides format and test proof, not a supported user recovery tool.
- **External manifests:** production deployment manifests are not stored here;
  `scripts/verify-deployment-contract.sh` validates rendered manifests supplied
  by the deployment owner.

## Completion Criteria

### Repository implementation

- `GOWORK=off go test -race ./... -timeout=30m` passes.
- Linux lock/filesystem/helper-process suites and bounded fuzz runs pass.
- Contract manifest equals the canonical signer version.
- `rg` finds no share-status mutation, passphrase hashing, overwrite rename,
  recovery-store SIGN wiring, fixed terminal retry count, or artifact scan.

### Installation/deployment

- Rendered production manifest passes the contract verifier.
- Original shared key/keyRef backup and restore drill succeeds.
- Process/signing/provisioning readiness reports the specified independent
  capability outcomes.

### Behavioral validation

- One job runs B and C only after two handles, publishes and re-inspects both
  artifacts, and holds permits until authoritative terminal confirmation.
- SIGN continues while DKG is active/unconfirmed when a general slot remains.
- Isolated copied B+C signs; missing C does not impair normal A+B.

### External rollout

- Backend activation/CAS, signer dual streams, and core signing matrix must pass
  the coordinated release job. This repository alone does not claim product
  activation.

## Structural Readiness

All design IDs and residual risks are inventoried; every co-signer-owned
requirement/invariant maps to files, steps, tests, and evidence. Compatibility,
configuration migration, crash recovery, custody, external deployment
ownership, and the absence of a user-facing recovery tool are explicit. No new
control plane or architecture decision is introduced. Status:
`STRUCTURALLY_READY`.
