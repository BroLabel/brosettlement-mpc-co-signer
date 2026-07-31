# MPC 2-of-3 Dual Local Parties Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Run primary party B and recovery party C as one fixed 2-of-3 DKG job, durably publish their encrypted artifacts, and preserve independent A+B production signing.

**Architecture:** One lifecycle-locked process retains the current scheduler and
general semaphore, adds a binary DKG guard, and owns one production `mpc-core`
DKG `Service` with one shared two-item preparams pool. Each DKG job runs
party-specific B and C executions concurrently through that service, a validated
local router, and a generation-bound immutable persistence pair. One store
implementation is instantiated as immutable primary and recovery capabilities
using one lifetime key. Actionable pre-activation reconciliation hands an
immutable terminal request to a lifecycle-owned publisher before normal intake;
unconfirmed startup publication closes only DKG admission, not SIGN.

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
- Production DKG uses exactly one `mpc-core` `Service`; B and C receive
  per-run party ID, transport, cancellation, and persistence dependencies. The
  service has no mutable global local-party or transport configuration.
- The committed module graph pins exactly `mpc-core v0.3.0`, contains no
  `replace`, and every Go verification entrypoint sets `GOWORK=off`.
- Reconciliation is actionable DKG only and never scans or mutates unrelated
  files. `COMPLETED` artifacts are normal durable state.
- Startup terminal retry owns no general session permit. While its immutable job
  is unconfirmed, `dkgAdmissionOpen=false`, provisioning is not ready, and SIGN
  continues.
- Process crash does not resume protocol rounds; unsuccessful key IDs are never
  reused.
- Supported runtime topology is Linux, one process, one local-filesystem writer,
  and a lifetime advisory FD lock. Packaging, Kubernetes, image publication, and
  deployment automation are outside this repository plan.
- Use TDD and one conventional commit per `PLAN-STEP-*`. Every step and substep
  compiles, has green local tests, introduces no temporary no-op security
  implementation, and names its incoming dependency.

## Repository Non-goals

- User-facing recovery CLI/SDK, packaging, distribution, and support lifecycle
  (`FUTURE-001` remains DESIGN-only).
- Managed recovery custody, active-key inventory, post-activation C monitoring,
  or disabling A+B because C is later missing.
- Product-generic N-party orchestration, legacy 2-of-2 compatibility, feature
  flags, compatibility matrices, or artifact migration.
- Durable DKG journals, protocol-round resume, tombstones, receipt
  infrastructure, paired preparams reservations, weighted admission, or separate
  scheduler lanes.
- Cross-host active/active, distributed leases, shared-NFS fencing, standby
  replicas, or automated deployment topology enforcement.
- Encryption-key rotation or re-encryption while any v1 artifact remains live.

---

## Design Reference

- Repository: `mpc-signer`
- Approved DESIGN: `docs/superpowers/specs/2026-07-27-mpc-2-of-3-recovery-design.md`
- Approved DESIGN Git revision:
  `01928c4e994f5d192cc9db41d19d6de2e9fed652`.
- Co-signer PLAN baseline: this document revision.
- Review decisions: approved D1-D21 engineering review, 2026-07-30

Active requirements are `REQ-001`–`REQ-081` excluding retired `REQ-023`,
`REQ-028`, `REQ-031`, `REQ-033`, `REQ-040`, `REQ-042`, `REQ-043`, and
`REQ-045`. Active invariants are `INV-001`–`INV-056` excluding retired
`INV-024`, `INV-025`, and `INV-027`.

### Repository applicability

- Direct co-signer requirements: `REQ-001`, `REQ-002`, `REQ-004`–`REQ-014`,
  `REQ-016`–`REQ-021`, `REQ-024`–`REQ-026`, `REQ-029`, `REQ-030`,
  `REQ-032`, `REQ-034`, `REQ-036`, `REQ-041`, `REQ-044`, `REQ-046`–`REQ-063`,
  `REQ-065`, `REQ-068`–`REQ-072`, `REQ-076`–`REQ-078`, `REQ-080`,
  `REQ-081`.
- Integration requirements: `REQ-003`, `REQ-015`, `REQ-022`, `REQ-027`,
  `REQ-035`, `REQ-037`–`REQ-039`, `REQ-064`, `REQ-067`, `REQ-073`–`REQ-075`,
  `REQ-079`.
- No co-signer implementation: `REQ-066` (backend key-row ordering).
- Direct invariants: `INV-001`–`INV-004`, `INV-006`–`INV-019`,
  `INV-023`, `INV-026`, `INV-028`–`INV-041`, `INV-044`–`INV-047`,
  `INV-051`–`INV-053`, `INV-055`, `INV-056`.
- Integration invariants: `INV-005`, `INV-020`–`INV-022`, `INV-042`,
  `INV-043`, `INV-048`–`INV-050`, `INV-054`.

### Assumptions, decisions, and risks

| Inventory                     | Co-signer disposition                                                                                                                                                                                                               |
| ----------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `ASSUMPTION-001`              | Consume the core proof and preserve threshold 2 at both local adapters.                                                                                                                                                             |
| `ASSUMPTION-002`, `005`       | Validate filesystem capabilities, lifetime lock, and supported topology before readiness.                                                                                                                                           |
| `ASSUMPTION-003`              | Reject all legacy artifacts/intents.                                                                                                                                                                                                |
| `ASSUMPTION-004`              | Validate key/keyRef/directories and document customer backup responsibility.                                                                                                                                                        |
| `ASSUMPTION-006`              | Accept authenticated SIGN only; do not add mutable local eligibility.                                                                                                                                                               |
| `ASSUMPTION-007`              | Document quorum-bearing host risk without inventing an activation ceremony.                                                                                                                                                         |
| `DECISION-001`–`DECISION-047` | Directly implement `001`–`013`, `015`, `018`–`032`, `034`–`039`, `042`–`044`, `047`; integrate `014`, `016`, `033`, `040`, `041`, `045`, `046`; `DECISION-017` is retired.                              |
| `RISK-001`–`RISK-017`         | Mitigate with key custody docs, strict artifacts, fail-closed capabilities, metrics, locks, deterministic restart, benchmarks, and explicit accepted residual risk—not new control planes.                                  |
| `RISK-018`                    | Signer Vault-write/DB-commit orphaning is integration-only here. Coordinated E2E proves no false product completion; co-signer adds no platform-share reconciliation.                                                    |
| `RISK-019`                    | Startup publication is memory-owned. Reconstruct identical bytes only while backend still returns an actionable intent; otherwise preserve unclassified files and publish nothing.                                    |
| `RISK-020`                    | Cutover is manual. Co-signer provides deterministic version/contract verification and shutdown behavior, while the backend-owned runbook retains the mandatory maintenance barrier.                                   |
| `RISK-021`                    | Benchmark database safety is backend-owned. Co-signer exposes safe metrics and accepts only the disposable harness workload through normal production protocols; it adds no test provisioning endpoint.                 |

## Cross-Repository Dependencies

The canonical dependency graph lives only in the DESIGN. This PLAN records the
incoming deliverables it consumes and the outputs it provides.

### Incoming

| Deliverable              | Producer                                      | Consumer steps                                  | Contract                                                                                                                                    |
| ------------------------ | --------------------------------------------- | ----------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------- |
| `CONTRACT-BUNDLE-V1`     | `mpc-signer / SIG-001 + SIG-002`              | `CS-001`, then all wire-facing steps            | Whole signer-owned bundle is vendored and hash-verified. It contains shared proto/descriptor/terminal/digest/JCS material, not artifact v1. |
| `COSIGNER-HTTP-V1`       | `back-end / BE-001`                           | vendored by `CS-001`; used by `CS-004`, `CS-007`, `CS-008a` | Backend-owned strict HTTP/mailbox fixtures and manifest.                                                                                    |
| `SIGN-CLAIM-HTTP-V1`     | `back-end / BE-008a`                          | `CS-009c`                                      | Strict SIGN claim/result fixture family added to the same backend-owned HTTP bundle.                                                        |
| `MPC-CORE-V0.3.0`        | `mpc-core / CORE-001`–`CORE-007` release gate | `CS-003`–`CS-005`, `CS-009a`                    | Exact Go module tag after real 2-of-3 DKG/signing, single-use preparams, codec, race, subprocess, and bounded fuzz gates.                    |

### Outputs

- A verified co-signer binary compatible with `CONTRACT-BUNDLE-V1`,
  `COSIGNER-HTTP-V1`, and exactly `mpc-core v0.3.0`.
- Co-signer-owned artifact-v1 documentation and golden vectors. Artifact hashes
  are not added to the signer manifest.
- A tagged `_test.go` recovery proof executable and invocation contract for the
  backend-owned `COORDINATED-E2E`; no production recovery capability or binary.
- Safe process, heap, goroutine, queue, filesystem, and DKG-state metrics consumed
  by the backend-owned E2E benchmark mode.

## Responsibility Map

| Component/file                              | Primary responsibility                                                                                  | Boundary                                                        |
| ------------------------------------------- | ------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------- |
| `contracts/mpc-2of3/v1/`                    | Vendored signer-owned shared bundle                                                                     | No artifact or backend-only HTTP corpus                          |
| `testdata/mpc-co-signer-http/v1/`           | Vendored backend-owned HTTP/mailbox fixtures                                                            | Strict tests only; backend remains normative owner               |
| `internal/contract/mpc2of3/`                | Exact descriptor, terminal, digest, JCS, and bundle verification                                       | Does not create descriptors or rewrite input values              |
| `internal/config/`                          | Stable deployment ID, B/C bindings, directories, key/keyRef, lock/free-space/preparams profile         | No secret logging                                                |
| `internal/sharestore/`                      | Encrypted v1 envelope, Linux publish, inspect, primary load, routing writer, one immutable active pair | Recovery has no production SIGN capability                       |
| `internal/localrouter/`                     | B↔C validated frames and A/local route split                                                            | Same authenticated envelope checks                               |
| `internal/worker/dkg_coordinator.go`        | Two handles, pair lease, common start barrier, sibling cancellation, evidence comparison               | One scheduler job and one production core DKG service            |
| `internal/preparams/`                       | Shared target-two pool profile and refill exclusion                                                    | Handles remain sealed, service-bound, and single-use              |
| `internal/worker/scheduler.go`              | Existing general permits, fixed binary DKG guard, `dkgAdmissionOpen`, one DKG attempt per batch        | SIGN continues when DKG is blocked                               |
| `internal/terminal/`                        | Single-slot lifecycle publisher, immutable request bytes, retry policy, typed authoritative response   | No maximum attempts; startup job owns no general permit           |
| `internal/reconcile/`                       | Actionable-only DKG classification and terminal-job construction                                       | No directory scan or inventory cleanup                           |
| `internal/lifecycle/` and `cmd/co-signer/`  | Lock-first startup, publisher handoff, intake/readiness, drain, lock-last shutdown                      | One local filesystem; not distributed fencing                    |
| `internal/health/`, `internal/metrics/`     | Process/signing/provisioning health and safe benchmark/operations metrics                               | Queue age alerts do not stop admission                            |

## Requirement-to-Step Mapping

| Design items                                                                                                                                                                                                                           | Ownership                     | Steps                                         | Tests                 | Evidence                           |
| -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------- | --------------------------------------------- | --------------------- | ---------------------------------- |
| `REQ-001`, `002`, `024`, `034`, `044`, `047`, `048`, `065`, `078`, `081`; `INV-001`, `002`, `014`, `026`, `029`, `041`, `053`, `056`                                                                                                           | contracts and dependency pins | `PLAN-STEP-CS-001`, `PLAN-STEP-CS-003`        | `TEST-CS-001`–`009`   | vendored manifests/module graph    |
| `REQ-008`–`014`, `REQ-021`, `REQ-025`, `REQ-026`, `REQ-049`, `REQ-060`–`063`, `REQ-069`; `INV-008`, `INV-013`, `INV-015`, `INV-017`–`019`, `INV-026`, `INV-035`, `INV-037`, `INV-039`, `INV-040`, `INV-044`    | store/config                  | `PLAN-STEP-CS-002`, `PLAN-STEP-CS-003`        | `TEST-CS-010`–`019`   | filesystem/fuzz/readback           |
| `REQ-004`–`007`, `REQ-014`, `REQ-019`, `REQ-029`, `REQ-032`, `REQ-036`, `REQ-046`, `REQ-053`–`055`, `REQ-065`, `REQ-071`, `REQ-072`; `INV-003`, `004`, `010`, `016`, `018`, `023`, `032`, `033`, `038`–`040`, `046`, `047`            | one-service coordinator       | `PLAN-STEP-CS-003`–`PLAN-STEP-CS-005`        | `TEST-CS-020`–`034`   | local integration/race             |
| `REQ-016`–`018`, `REQ-038`, `REQ-052`, `REQ-056`–`058`, `REQ-064`, `REQ-068`, `REQ-076`; `INV-009`, `INV-011`, `INV-012`, `INV-034`, `INV-038`, `INV-041`, `INV-045`, `INV-051`                                           | scheduler/terminal publisher  | `PLAN-STEP-CS-006`, `PLAN-STEP-CS-007`, `PLAN-STEP-CS-009b`        | `TEST-CS-035`–`045`, `TEST-CS-068`–`072`   | stateful HTTP/admission/redaction            |
| `REQ-050`, `REQ-051`, `REQ-057`, `REQ-065`, `REQ-070`, `REQ-076`; `INV-030`, `INV-031`, `INV-035`, `INV-041`, `INV-045`, `INV-051`                                                                                                                        | lifecycle/reconciliation      | `PLAN-STEP-CS-008a`, `PLAN-STEP-CS-008b`      | `TEST-CS-046`–`060`   | exact-path matrix/subprocess       |
| `REQ-020`, `REQ-030`, `REQ-041`, `REQ-059`, `REQ-077`; `INV-006`, `INV-007`, `INV-028`, `INV-036`, `INV-044`, `INV-052`                                                                                                           | recovery proof                | `PLAN-STEP-CS-009a`                           | `TEST-CS-061`–`067`   | isolated tagged test binary        |
| `REQ-068`–`REQ-070`, `REQ-079`, `REQ-080`; `INV-041`, `INV-045`, `INV-054`, `INV-055`                                                                                                                                                | operations and verification   | `PLAN-STEP-CS-009b`                           | `TEST-CS-068`–`072`   | safe metrics/docs/full verify      |
| `REQ-073`–`REQ-075`; `INV-048`–`INV-050`                                                                                                                                                                                             | upstream state integration    | `PLAN-STEP-CS-007`, `PLAN-STEP-CS-008a`, `PLAN-STEP-CS-009a` | coordinated E2E | A evidence/typed backend outcomes  |
| `REQ-082`–`REQ-084`; `INV-057`–`INV-059`                                                                                                                                                                                             | normal SIGN HTTP contract     | `PLAN-STEP-CS-009c`                           | `TEST-CS-073`–`077`   | vendored fixtures/client routing   |

## Implementation Steps

### PLAN-STEP-CS-001: Add strict shared contract codecs

**Sources:** `REQ-001`, `REQ-002`, `REQ-024`, `REQ-034`, `REQ-044`,
`REQ-047`, `REQ-048`, `REQ-058`, `REQ-065`, `REQ-078`, `REQ-081`,
`DECISION-036`, `DECISION-047`, `INV-053`, `INV-056`

**Depends on:** `CONTRACT-BUNDLE-V1`, `COSIGNER-HTTP-V1`

**Files:**

- Create: `Makefile`
- Create: `cmd/mpc-contracts/main.go`
- Create: `internal/contract/bundle/verifier.go`
- Create: `internal/contract/bundle/verifier_test.go`
- Create: `internal/contract/mpc2of3/descriptor.go`
- Create: `internal/contract/mpc2of3/terminal.go`
- Create: `internal/contract/mpc2of3/digest.go`
- Create: `internal/contract/mpc2of3/jcs.go`
- Create: `internal/contract/mpc2of3/corpus_test.go`
- Create: `contracts/mpc-2of3/v1/`
- Create: `testdata/mpc-co-signer-http/v1/`
- Create: `scripts/sync-contracts.sh`
- Modify: `go.mod`
- Modify: `go.sum`

**Interfaces:**

- Parses exact canonical descriptor bytes without repair/reserialization.
- Produces branded descriptor/artifact/terminal/chain-code digest types.
- Serializes minimal `COMPLETED`/`FAILED` terminal payload once.
- Strictly validates the shared backend-created `TIMED_OUT` vector but never
  originates that status from co-signer.
- Verifies the complete signer-owned bundle and backend-owned HTTP fixture bundle,
  including absence of unexpected files. The signer bundle does not contain
  codec-v2 or artifact-v1 hashes.
- Computes the signer bundle identity as SHA-256 of exact closed-schema JCS
  `manifest.json` bytes, encoded as 43-character unpadded base64url; the
  canonical path-ordered manifest has per-file hashes and no self-hash.
- Provides one explicit consumer-local command:

  ```bash
  make sync-contracts \
    CONTRACT_SOURCE=/absolute/path/to/mpc-signer/contracts/mpc-2of3/v1 \
    HTTP_SOURCE=/absolute/path/to/back-end/contracts/mpc-co-signer-http/v1
  ```

  It validates each source, copies whole bundles through temporary directories,
  atomically replaces only the fixed vendored destinations, and verifies the
  final copies. It performs no network access, clone, code generation, or source
  discovery. CI runs verification only.

- [ ] Add failing bundle and shared-corpus tests for missing/changed/unexpected
      files, canonical/noncanonical descriptor and `COMPLETED`/`FAILED`/
      `TIMED_OUT` values, rejection of co-signer-originated `TIMED_OUT`,
      duplicate/unknown keys, party order, ASCII/numbers, digest encodings, and
      strict backend HTTP/mailbox fixture shapes and status codes.
- [ ] Run `GOWORK=off go test ./internal/contract/mpc2of3 -count=1`; expect the
      module/corpus to be absent.
- [ ] Pin RFC 8785 behind a local adapter, implement strict token-first parsing,
      fixed roster semantics, exact key ID regex, and canonical terminal encoding.
- [ ] Implement the verifier and `sync-contracts` command. Prove invalid sources
      leave both fixed destinations unchanged, valid sources replace whole
      bundles, and final hashes match their producer manifests.
- [ ] Run `GOWORK=off go test ./internal/contract/... ./cmd/mpc-contracts -count=1`
      and the fixed-destination verifier; expect byte-identical shared contract
      bytes and backend fixture hashes.
- [ ] Commit with `feat(contract): add strict 2-of-3 wire codecs`.

### PLAN-STEP-CS-002: Replace configuration with two immutable store profiles

**Sources:** `REQ-008`–`REQ-011`, `REQ-025`, `REQ-044`, `REQ-062`, `REQ-069`,
`ASSUMPTION-004`

**Depends on:** `PLAN-STEP-CS-001`

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
`REQ-060`–`REQ-063`, `REQ-072`, `REQ-078`, `DECISION-024`,
`DECISION-025`, `DECISION-026`, `DECISION-037`, `DECISION-042`, `INV-037`,
`INV-047`, `INV-053`

**Depends on:** `PLAN-STEP-CS-002`, `MPC-CORE-V0.3.0`

**Files:**

- Replace: `internal/sharestore/file_store.go`
- Create: `internal/sharestore/artifact.go`
- Create: `internal/sharestore/fs_linux.go`
- Create: `internal/sharestore/fs_unsupported.go`
- Create: `internal/sharestore/reader.go`
- Create: `internal/sharestore/publisher.go`
- Create: `internal/sharestore/runtime_loader.go`
- Create: `internal/sharestore/primary_reader.go`
- Create: `internal/sharestore/active_pair.go`
- Create: `internal/sharestore/active_pair_test.go`
- Create: `internal/sharestore/routing_writer.go`
- Create: `internal/sharestore/routing_writer_test.go`
- Create: `internal/sharestore/artifact_test.go`
- Create: `internal/sharestore/fs_linux_test.go`
- Create: `internal/sharestore/artifact_fuzz_test.go`
- Create: `testdata/artifact-v1/`
- Modify: `cmd/co-signer/main.go`
- Modify: `cmd/co-signer/main_test.go`
- Modify: `internal/worker/session_worker.go`
- Modify: `internal/worker/session_worker_test.go`
- Modify: `go.mod`
- Modify: `go.sum`

**Interfaces:**

- Produces `PublishAndInspect` and `InspectExisting` returning only
  `ArtifactEvidence`.
- Produces a separate primary-only SIGN reader; recovery store does not satisfy it.
- Adapts every current application caller to the released core
  `ShareReader`/`ShareWriter` capabilities in the same commit that pins
  `mpc-core v0.3.0`; removes `WithShareStore`, mutable status,
  `DisableShare`, `ErrShareDisabled`, and their result mapping without adding a
  no-op or application-local disabler.
- Keeps exact canonical descriptor bytes inside the encrypted payload and uses the
  `mpc-core v0.3.0` evidence inspector to validate codec, canonical compressed
  SEC1 public key, and chain-code hash. Artifact vectors remain co-signer-owned.
- Centralizes decrypt, descriptor/product binding, evidence inspection, and
  best-effort decrypted-buffer cleanup in non-exported
  `loadValidatedRuntimeShare`; only the production primary reader can call it in
  non-test builds.
- Uses temp/write/sync/close/`RENAME_NOREPLACE`/dir-sync/no-follow/readback/
  decrypt/inspect ordering.
- Adds the final generation-bound active-pair primitive and routing writer now,
  so the split core writer has a real fail-closed product adapter. Until the
  coordinator registers a complete B/C pair in `CS-004`/`CS-005`, a DKG write
  returns a typed unregistered-pair error before any filesystem mutation. There
  is no legacy writer fallback, synthetic descriptor, or releaseable
  intermediate mode.

- [ ] Add failing golden, syscall-recorder, real-filesystem, corruption, size,
      symlink/nonregular, `ErrArtifactExists`, readback, entropy, and crash-boundary
      tests.
- [ ] On Linux, run `GOWORK=off go test ./internal/sharestore -count=1`;
      expect overwrite and incomplete durability behavior to fail.
- [ ] Implement the closed envelope/payload, strict padded-base64 readers,
      Linux-only filesystem adapter, exact final-byte hashing, core evidence
      inspection, and best-effort secret-buffer clearing.
- [ ] Implement the active-pair/routing-writer boundary and update bootstrap and
      worker error classification to the split core capabilities. Prove an
      unregistered writer fails before filesystem access and normal primary
      reads remain available.
- [ ] Pin exactly `mpc-core v0.3.0`, remove every committed `replace`, and prove
      `GOWORK=off go list -m` resolves that tag. Local uncommitted `go.work` or
      `replace` may be used before the tag exists but cannot enter a commit or CI.
- [ ] Run normal tests, a bounded 10-second fuzz run, and helper-process crash
      tests; every final file must strictly inspect and all published bytes remain
      unchanged.
- [ ] Add every discovered fuzz failure to the permanent Go fuzz regression
      corpus before considering the step complete.
- [ ] Run `GOWORK=off go test ./... -count=1` before commit; every current
      consumer of the removed core `ShareStore`/status API must compile against
      `v0.3.0`.
- [ ] Commit with `feat(store): publish immutable recovery artifacts`.

### PLAN-STEP-CS-004: Add one-service B/C routing and an immutable active pair

**Sources:** `REQ-005`–`REQ-007`, `REQ-014`, `REQ-019`, `REQ-029`, `REQ-046`,
`REQ-071`, `REQ-072`, `DECISION-039`, `DECISION-042`, `INV-004`, `INV-015`,
`INV-039`, `INV-040`, `INV-046`, `INV-047`

**Depends on:** `PLAN-STEP-CS-003`, `MPC-CORE-V0.3.0`,
`CONTRACT-BUNDLE-V1`, `COSIGNER-HTTP-V1`

**Files:**

- Create: `internal/localrouter/router.go`
- Create: `internal/localrouter/validation.go`
- Create: `internal/localrouter/router_test.go`
- Modify: `internal/sharestore/active_pair.go`
- Modify: `internal/sharestore/active_pair_test.go`
- Modify: `internal/sharestore/routing_writer.go`
- Modify: `internal/sharestore/routing_writer_test.go`
- Create: `internal/worker/dkg_coordinator.go`
- Create: `internal/worker/dkg_coordinator_test.go`
- Refactor: `internal/worker/session_worker.go`
- Modify: `internal/transport/http_transport.go`
- Modify: `internal/transport/http_transport_test.go`
- Modify: `cmd/co-signer/runtime_helpers.go`

**Interfaces:**

- The process creates exactly one production DKG `mpc-core.Service` with one
  shared preparams pool. B and C invoke it concurrently with the same wire
  `sessionId` and distinct per-run party ID, transport, writer, and cancellation
  dependencies. No service-global mutable local party or transport is allowed.
- Both B and C adapters pass the public product threshold `2` unchanged into
  `mpc-core`; co-signer never supplies the library-specific threshold value
  `1`. The only conversion remains inside the core runtime adapter.
- Local frames use the same session/sender/recipient/round/sequence/dedupe
  validation; only A-bound frames use HTTP.
- Co-signer defines its own internal `PersistenceRunKey{SessionID,
  LocalPartyID}`; the core `DKGRunKey` remains internal to `mpc-core`.
- The generic core writer boundary is limited to:

  ```go
  SaveShareInput{
      SessionID,
      KeyID,
      LocalPartyID,
      OpaqueDescriptorFingerprint,
      CodecBlob,
  }
  ```

  Product purpose, exact descriptor bytes, and destination directories never
  enter `mpc-core`.
- A fixed-capacity active-pair slot holds exactly two copied immutable contexts:
  session ID, key ID, B/C party IDs, exact descriptor bytes, and descriptor
  fingerprint. `RegisterPair` is atomic and returns an opaque
  generation-bound lease.
- The routing writer resolves core `SaveShareInput` against the active pair,
  requires exact session/key/party/fingerprint agreement and an allowlisted B/C
  party, then routes B to the primary store and C to the recovery store. Each
  final store repeats its immutable `ExpectedPartyID`, purpose, key, descriptor,
  and fingerprint checks.
- Coordinator compares post-runtime `InspectExisting` evidence only.

- [ ] Add failing tests for two concurrent calls through one service, no global
      party/transport state, B↔C local delivery, A routing,
      spoofed/wrong-round/duplicate frames, duplicate/colliding pair registration,
      wrong session/key/party/fingerprint, copied descriptor immutability, stale
      lease release, independent B/C cleanup, post-runtime evidence mismatch,
      and adapter spies observing threshold `2` independently for B and C while
      rejecting a caller-supplied library threshold `1`.
- [ ] Run `GOWORK=off go test -race ./internal/localrouter ./internal/sharestore ./internal/worker ./internal/transport -run 'DKG|Local|Party|ActivePair|RoutingWriter' -count=1`;
      expect the single-party worker to fail.
- [ ] Split SIGN and DKG worker paths, instantiate one production DKG service,
      add the local router and generation-bound routing writer, and keep the
      active-pair slot independent from the core-internal composite run state.
- [ ] Run the full worker/transport/localrouter/sharestore suites with `-race`.
- [ ] Commit with `feat(dkg): add party-bound local routing`.

### PLAN-STEP-CS-005: Acquire two handles and execute one dual-party DKG job

**Sources:** `REQ-032`, `REQ-053`–`REQ-055`, `REQ-065`, `DECISION-023`,
`INV-032`, `INV-033`, `REQ-071`, `REQ-072`, `DECISION-039`, `DECISION-042`,
`INV-038`, `INV-041`, `INV-046`, `INV-047`

**Depends on:** `PLAN-STEP-CS-004`, `MPC-CORE-V0.3.0`

**Files:**

- Modify: `internal/worker/dkg_coordinator.go`
- Modify: `internal/sharestore/active_pair.go`
- Create: `internal/preparams/profile.go`
- Create: `internal/preparams/controller.go`
- Create: `internal/preparams/controller_test.go`
- Modify: `cmd/co-signer/main.go`
- Modify: `cmd/co-signer/main_test.go`

**Interfaces:**

- Acquire B and then C from the one service-bound pool. After both handles exist,
  atomically register the immutable pair and only then open the common start
  barrier. Failed second acquire leaves no pair and discards B; failed pair
  registration discards both handles.
- Run B and C concurrently via `RunDKGSessionWithPreParams` on the same service.
  Each call receives per-run dependencies; handles remain generic and are not
  pre-bound to party IDs. Duplicate same-session/same-party execution is rejected
  by core, while same-session/different-party execution is required.
- B and C receive the same authenticated just-in-time chain code, independently
  validate its descriptor hash before runtime start, and must inspect to the same
  chain-code hash and account public key while retaining distinct share blobs.
- The coordinator threads the intent's one immutable absolute deadline into both
  runtime contexts. Any local safety timeout is `min(localTimeout,
  remainingAbsoluteBudget)`; claim, acquisition, start, publication retry, and
  restart never move the deadline later.
- Cancel the sibling on failure, join both goroutines including every
  `SaveShare` return, then idempotently release only this pair generation. A stale
  lease cannot clear a newer pair. Coordinator-owned descriptor bytes are a
  separate immutable copy used for authoritative `InspectExisting`.
- Target=2, worker=1, explicit parallelism, no sync fallback, no auto-refill;
  refill resumes only after both runtimes stop.
- The controller exposes an admission hint only when inventory is at least two
  and generation-inflight is zero, plus a scheduler wakeup when that becomes
  true. This is not a reservation; both post-claim acquisitions remain
  fail-closed.

- [ ] Add failing state/race tests for acquisition order, second-acquire failure,
      pair-registration failure, both-handle discard, no early party start,
      same-session B/C concurrency, duplicate B rejection, sibling failure,
      independent cleanup, impossible early lease release, stale release token,
      distinct B/C codec blobs with equal public key and chain code, no refill
      while active, and refill after every outcome.
- [ ] Add missing chain-code, wrong descriptor-hash, divergent B/C chain-code,
      and near-deadline cases. The start barrier must remain closed, neither
      runtime may start after validation/deadline failure, acquired handles and
      any pair lease are safely discarded/released, and no timeout may extend the
      original absolute deadline.
- [ ] Run `GOWORK=off go test -race ./internal/preparams ./internal/worker ./cmd/co-signer -run 'PreParams|Barrier|Refill' -count=1`;
      expect missing handle integration.
- [ ] Wire sealed service-bound core handles, active-pair registration, start/join
      barriers, and the controller without exposing or reserving preparams.
- [ ] Run the same packages; expect both handles consumed/discarded exactly once.
- [ ] Commit with `feat(dkg): execute dual-party job from one service`.

### PLAN-STEP-CS-006: Add the binary DKG scheduler guard

**Sources:** `REQ-056`–`REQ-058`, `REQ-063`, `REQ-068`, `DECISION-022`,
`REQ-076`, `DECISION-043`, `RISK-014`, `INV-051`

**Depends on:** `PLAN-STEP-CS-005`

**Files:**

- Modify: `internal/worker/scheduler.go`
- Modify: `internal/worker/scheduler_test.go`
- Create: `internal/worker/permits.go`

**Interfaces:**

- The existing ordered batch loop checks `dkgAdmissionOpen` and provisioning
  capability hints, then nonblocking `TryAcquire` on the fixed-capacity-one DKG
  guard, then acquires one general session slot before claim. There is no second
  lane or new admission framework.
- SIGN acquires only a general slot. An occupied/closed DKG guard skips DKG and
  continues the existing SIGN traversal.
- `maxConcurrentSessions > 1` permits a DKG and SIGN to run in parallel;
  `maxConcurrentSessions = 1` intentionally serializes all MPC work. The B/C
  pair is one scheduler job and consumes one general slot.
- At most one DKG claim per ordered batch; skipped DKG never blocks visible SIGN.
- For a normally running DKG, both permits remain held through confirmed
  `ACCEPTED`, `EXACT_REPLAY`, or typed conflict. A startup reconciliation
  publication holds neither permit.

- [ ] Add failing deterministic tests for guard-first/general-second order,
      no claim without both permits, claim conflict release, one attempt per batch,
      continued SIGN, low-disk/preparams-not-ready DKG skip, concurrent wakeups,
      and accepted head-of-line behavior.
- [ ] Run `GOWORK=off go test -race ./internal/worker -run Scheduler -count=1`;
      expect current general-only dispatcher to fail.
- [ ] Add the fixed nonblocking guard, `dkgAdmissionOpen`, typed permit ownership,
      ordered batch traversal, and safe wakeups without a second polling lane.
- [ ] Run deterministic scheduler tests; expect no duplicate DKG, at most one DKG
      claim attempt per batch, and continued SIGN admission. Queue age is an SLO
      signal rather than a correctness/fairness guarantee.
- [ ] Commit with `feat(scheduler): serialize dkg admission`.

### PLAN-STEP-CS-007: Publish immutable terminal results until authoritative

**Sources:** `REQ-018`, `REQ-038`, `REQ-052`, `REQ-058`, `REQ-064`,
`REQ-074`–`REQ-076`, `DECISION-040`, `DECISION-043`, `DECISION-045`,
`INV-011`, `INV-012`, `INV-034`, `INV-049`–`INV-051`

**Depends on:** `PLAN-STEP-CS-006`, `COSIGNER-HTTP-V1`

**Files:**

- Modify: `internal/monolith/types.go`
- Refactor: `internal/monolith/client.go`
- Create: `internal/terminal/publisher.go`
- Create: `internal/terminal/retry.go`
- Create: `internal/terminal/single_slot.go`
- Create: `internal/terminal/publisher_test.go`
- Modify: `internal/worker/dkg_coordinator.go`

**Interfaces:**

- Serializes request bytes once; retries with `RetryPolicy.NextDelay` and
  context-aware `Sleeper.Sleep` without `maxAttempts`.
- Typed 200/409 response always carries authoritative status/fingerprint.
- Network errors, timeouts, EOF, and 5xx retry with capped jitter. Malformed
  200/409 or unexpected 4xx emit a protocol alert, retain the applicable DKG
  gate/permits, and retry with a slow capped delay. Only a validated typed
  terminal outcome releases them.
- A lifecycle-owned single-slot publisher can accept one immutable startup job
  and start its retry worker before intake opens. Handoff failure is explicit.
- Normal DKG publication holds DKG/general permits until accepted, exact replay,
  or typed conflict. Startup publication owns no scheduler permit.
- The v1 co-signer terminal body omits optional diagnostic fields completely;
  redacted diagnostics remain local only. Canonical IDs plus durable
  `InspectExisting` evidence are sufficient to reconstruct byte-identical
  request bytes after restart only while backend still returns the intent as
  actionable. Neither terminal bytes nor local diagnostics contain share
  material, descriptor bytes, decrypted material, or encryption keys.

- [ ] Add stateful HTTP tests for commit-then-EOF, byte-identical replay,
      malformed 200/409, 5xx, unexpected 4xx, long failure sequences, conflict,
      cancellation, strict unknown-field rejection, single-slot handoff/start
      failure, omitted wire diagnostics, actionable-only deterministic restart
      reconstruction, no publication for an already-terminal intent, and
      concurrent SIGN.
- [ ] Run `GOWORK=off go test ./internal/terminal ./internal/monolith ./internal/worker -run 'Terminal|PostResult|Retry' -count=1`;
      expect current three-attempt client to fail.
- [ ] Isolate generic bounded HTTP operations from lifecycle terminal
      publication, add typed responses and capped jittered retry, and retain permits
      through all unconfirmed outcomes.
- [ ] Run the stateful tests with injected no-sleep policy; verify no retry limit
      and no attempt after lifecycle cancellation. Typed conflict preserves the
      backend winner and emits an integrity signal.
- [ ] Commit with `feat(terminal): retry authoritative dkg results`.

### PLAN-STEP-CS-008a: Implement actionable DKG reconciliation

**Sources:** `REQ-050`, `REQ-051`, `REQ-057`, `REQ-065`, `REQ-070`,
`REQ-074`–`REQ-076`, `DECISION-019`, `DECISION-021`, `DECISION-030`,
`DECISION-043`, `INV-030`, `INV-031`, `INV-035`, `INV-041`, `INV-045`,
`INV-049`–`INV-051`

**Depends on:** `PLAN-STEP-CS-007`, `COSIGNER-HTTP-V1`

**Files:**

- Create: `internal/reconcile/reconciler.go`
- Create: `internal/reconcile/reconciler_test.go`
- Modify: `internal/monolith/client.go`
- Modify: `internal/monolith/client_test.go`

**Interfaces:**

- Reconciliation accepts only backend-addressed actionable DKG intents:
  unexpired PENDING and own CLAIMED for the stable deployment ID. The backend
  listing must not return terminal or foreign-CLAIMED intents. It never scans
  directories, inspects SIGN, or classifies files outside exact paths derived
  from actionable intents.
- For an own CLAIMED intent, matching B+C produces a byte-stable COMPLETED job;
  missing/partial/mismatched material produces the minimal canonical FAILED job.
- If the recovery store or strict inspection capability is unavailable, the
  reconciler returns a typed capability-deferred result before inspecting or
  mutating any actionable artifact. It constructs no terminal job and leaves
  every addressed and unaddressed file byte-for-byte unchanged.
- PENDING without material remains eligible for normal claim. PENDING with
  associated material must win CAS claim before a FAILED job can be constructed;
  claim loss or expiry leaves artifacts unchanged.
- Reconciliation preserves the backend-created absolute deadline byte-for-byte,
  never derives a fresh TTL, and never resumes protocol rounds. A restart at or
  after that deadline can only reconstruct a deterministic terminal publication
  or observe the backend timeout winner; it cannot extend execution.
- Terminal statuses returned in the actionable listing are protocol violations
  and do not authorize inspection or mutation. COMPLETED artifacts are preserved.
  Cleanup of artifacts addressed by this process is permitted only after a typed
  authoritative FAILED/TIMED_OUT outcome; unclassified files are byte-for-byte
  untouched.
- The reconciler returns at most one immutable startup publication job and a
  provisioning disposition. Multiple own CLAIMED DKG, a foreign CLAIMED intent,
  or an impossible matrix entry is a protocol-integrity result that aborts
  startup before scheduler/intake/readiness. It keeps both `processReady` and
  `signingReady` false; no SIGN is admitted.

- [ ] Add the full table-driven matrix for PENDING/own CLAIMED with
      zero/one/two/mismatched B/C, claim conflicts, typed authoritative cleanup
      evidence, multiple own CLAIMED, and defensive foreign-CLAIMED or terminal
      listing contract violations. Include restart immediately before/after the
      absolute deadline and prove the deadline never changes or reopens runtime.
- [ ] Add capability-deferred cases for unavailable recovery store/inspection.
      Prove no claim, publish, cleanup, artifact read, or filesystem mutation,
      and return an explicit disposition that cannot open DKG admission.
- [ ] Run `GOWORK=off go test -race ./internal/reconcile ./internal/monolith -count=1`;
      expect the actionable classifier and immutable publication job to be absent.
- [ ] Implement exact-path classification, claim-then-fail, aggregate
      reconstruction from `InspectExisting`, byte-stable terminal bodies, and a
      narrow publisher-handoff port. Do not add a journal, inventory endpoint,
      directory scan, or post-activation reconciliation.
- [ ] Snapshot unrelated file bytes, mode, and mtime before every matrix case and
      require equality afterward. Do not assert `atime`.
- [ ] Commit with `feat(reconcile): classify actionable dkg intents`.

### PLAN-STEP-CS-008b: Wire lock-first lifecycle and asynchronous startup publication

**Sources:** `REQ-050`, `REQ-051`, `REQ-057`, `REQ-070`, `REQ-076`,
`DECISION-019`, `DECISION-021`, `DECISION-030`, `DECISION-043`, `INV-030`,
`INV-031`, `INV-035`, `INV-045`, `INV-051`, `RISK-019`

**Depends on:** `PLAN-STEP-CS-008a`

**Files:**

- Create: `internal/lifecycle/coordinator.go`
- Create: `internal/lifecycle/lock_unix.go`
- Create: `internal/lifecycle/lock_test.go`
- Create: `internal/lifecycle/subprocess_test.go`
- Modify: `cmd/co-signer/main.go`
- Modify: `internal/worker/scheduler.go`
- Modify: `internal/terminal/single_slot.go`
- Modify: `internal/health/server.go`
- Modify: `cmd/co-signer/main_test.go`
- Modify: `internal/health/server_test.go`

**Interfaces:**

- Startup order is:

  ```text
  validate config
  -> acquire fail-fast lifetime FD lock
  -> open stores/core capabilities
  -> start lifecycle-owned single-slot publisher
  -> reconcile actionable DKG
  -> hand off any immutable startup job
  -> start scheduler/intake
  -> expose readiness
  ```

- Reconciliation is complete when every actionable DKG is classified and any
  required job has been accepted into the lifecycle-owned slot and its retry loop
  is running. It does not wait for backend acknowledgement. Publisher
  handoff/start failure prevents intake and readiness.
- Any protocol-integrity reconciliation result aborts startup before
  scheduler/intake/readiness and keeps `processReady=false` and
  `signingReady=false`. Signing-only startup is allowed for provisioning
  capability failure or a successfully handed-off but unconfirmed terminal job,
  not for a foreign claim or impossible reconciliation state.
- A capability-deferred startup latches `dkgAdmissionOpen=false` for the process
  lifetime. Capability restoration may improve diagnostics but cannot
  dynamically reopen provisioning: a new process must reacquire the lifetime
  lock, validate stores, and complete actionable reconciliation before DKG
  admission can open.
- While startup publication is unconfirmed,
  `dkgAdmissionOpen=false`, `provisioningReady=false` with reason
  `dkg_terminal_unconfirmed`, and no general permit is held. `processReady` and
  `signingReady` can become true; the existing scheduler continues SIGN.
- ACCEPTED, EXACT_REPLAY, or typed conflict clears the gate, emits a scheduler
  wakeup, and requires a fresh authoritative poll before another DKG is
  considered. Typed conflict also emits an integrity alert without changing the
  backend winner. Malformed/unexpected responses keep DKG closed.
- Shutdown closes readiness/intake, drains or cancels workers, publisher, refill,
  service, and stores, then releases the FD lock. The lock is local-filesystem
  fencing only and is held while any worker or retry loop remains alive.

- [ ] Add lifecycle-order unit tests and real helper-process lock tests for
      contention, no backend access by loser, graceful/SIGKILL release,
      controlled drain, close-on-exec, and lock-last shutdown. Synchronize
      subprocess boundaries only through pipes, files, or channels with bounded
      timeouts; fixed sleeps are forbidden.
- [ ] Add tests for publisher handoff/start failure, terminal endpoint outage with
      successful SIGN, no DKG claim/permit during startup publication, malformed
      response, typed conflict alert, confirmation wakeup plus fresh poll,
      lifecycle cancellation, and protocol-integrity reconciliation abort with
      no scheduler, intake, readiness, or SIGN.
- [ ] Add a capability-restoration test proving signing-only intake may remain
      available but DKG admission stays latched closed until restart and a fresh
      lock-first reconciliation succeeds.
- [ ] Run `GOWORK=off go test -race ./internal/lifecycle ./internal/reconcile ./internal/terminal ./internal/worker ./internal/health ./cmd/co-signer -count=1`;
      expect missing lifecycle ordering and split readiness behavior.
- [ ] Implement the lifecycle coordinator, fail-fast FD lock, readiness split,
      DKG admission gate, scheduler wakeup, and publisher ownership without
      adding a standby process, distributed lease, application feature flag, or
      general-permit reservation.
- [ ] On Linux, prove shutdown stops terminal retry before releasing the lock and
      that restart either reconstructs identical request bytes for a still
      actionable intent or performs no publication after backend terminalization.
- [ ] Commit with `feat(lifecycle): gate dkg during startup replay`.

### PLAN-STEP-CS-009a: Prove isolated B+C recovery through a test-only reader

**Sources:** `REQ-020`, `REQ-030`, `REQ-041`, `REQ-059`, `REQ-073`, `REQ-077`,
`DECISION-004`, `DECISION-031`, `DECISION-044`, `INV-006`, `INV-007`,
`INV-028`, `INV-036`, `INV-044`, `INV-048`, `INV-052`, `RISK-018`

**Depends on:** `PLAN-STEP-CS-008b`, `MPC-CORE-V0.3.0`

**Files:**

- Create: `internal/sharestore/recovery_proof_integration_test.go`
- Modify: `internal/sharestore/runtime_loader.go`
- Modify: `internal/sharestore/primary_reader.go`
- Modify: `Makefile`

**Interfaces:**

- `recovery_proof_integration_test.go` begins with:

  ```go
  //go:build mpc_recovery_test

  package sharestore
  ```

  It defines a package-local `recoveryProofReader` in `_test.go` only. No
  non-test file, production interface, command, endpoint, or binary gains a
  recovery reader. Even `go build -tags=mpc_recovery_test ./cmd/co-signer`
  cannot include this capability.
- B opens through the production `PrimaryReader`; C opens through the test-local
  reader. Both reuse the same non-exported
  `loadValidatedRuntimeShare` decoder/validator and all artifact, descriptor,
  key, party, purpose, public-key, chain-code, and codec checks.
- The isolated proof uses two signing-only `mpc-core.Service` instances, one for
  B and one for C. This is test signing topology, not the one-service production
  DKG topology.
- The test process receives only copied artifacts and the test key via an
  inherited FD or a private 0600 input file. It has no backend client, signer A,
  original stores, coordinator, DKG transport, or original workflow references.
  Output is a redacted pass/fail result.

- [ ] Add failing isolated tests for real B+C signing, independent secp256k1
      verification, modified digest rejection, root and one supported non-root
      derivation, wrong key/keyRef, corrupt/missing C, corrupt/missing B, and
      descriptor/public-output mismatch. Never reconstruct a private key.
- [ ] Build the proof exactly as:

  ```bash
  GOWORK=off go test -c \
    -tags=mpc_recovery_test \
    -o "$MPC_RECOVERY_TEST_BIN" \
    ./internal/sharestore
  ```

  Then run `"$MPC_RECOVERY_TEST_BIN" -test.run '^TestIsolatedRecoveryProof$'`.
- [ ] Implement the `_test.go`-only reader and proof using copied files, two
      signing-only core services, fresh signing randomness, bounded timeouts, and
      best-effort decrypted-buffer cleanup.
- [ ] Prove a normal production build without the tag succeeds and contains no
      recovery reader symbol or command; test output and failure diagnostics
      contain no key, blob, share, chain code, or preparams.
- [ ] Commit with `test(recovery): prove isolated B-C signing`.

### PLAN-STEP-CS-009b: Add observability, security docs, and full verification

**Sources:** `REQ-068`–`REQ-070`, `REQ-079`, `REQ-080`, `DECISION-034`,
`DECISION-038`, `DECISION-046`, `INV-012`, `INV-019`, `INV-041`, `INV-045`,
`INV-054`, `INV-055`, `AC-016`, `RISK-020`, `RISK-021`

**Depends on:** `PLAN-STEP-CS-009a`

**Files:**

- Create: `internal/metrics/filesystem.go`
- Create: `internal/metrics/runtime.go`
- Create: `internal/metrics/{scheduler,lifecycle,terminal,preparams,artifact,jobs,relay}.go`
- Create: `internal/sharestore/immutability_integration_test.go`
- Modify: `internal/sharestore/{publisher,reader}.go`
- Modify: `internal/preparams/controller.go`
- Modify: `internal/worker/{scheduler,dkg_coordinator,session_worker}.go`
- Modify: `internal/terminal/publisher.go`
- Modify: `internal/reconcile/reconciler.go`
- Modify: `internal/lifecycle/coordinator.go`
- Modify: `internal/localrouter/validation.go`
- Modify: `internal/health/server.go`
- Modify: `README.md`
- Modify: `SECURITY.md`
- Create: `docs/artifact-format-v1.md`
- Create: `docs/runbooks/recovery-artifact.md`
- Modify: `Makefile`

**Interfaces:**

- Safe metrics expose the complete DESIGN-owned co-signer contract: lifecycle
  lock/readiness, reconciliation duration/failures, DKG guard/admission/claim
  conflicts, observed pending batch composition/ages/claim outcomes, terminal
  unconfirmed/attempt/conflict state, every preparams generation/acquire/
  consume/discard/failure/skip transition, artifact publish/inspect outcomes and
  filesystem inventory, active DKG/SIGN jobs, MPC session duration, relay
  integrity conflicts, bounded HTTP/local-router queue overflow/drop counters,
  process RSS, Go heap, and goroutines.
- Instrumentation call sites remain at their owning store, preparams/runtime,
  scheduler, terminal, lifecycle, and reconciliation boundaries, but their
  concrete metric modules and wiring are added atomically in `CS-009b`. Earlier
  steps define behavior and tests without claiming partial production
  instrumentation.
- Metrics never decrypt or classify unaddressed artifacts. Labels use an exact
  bounded allowlist and contain no key/session IDs, paths, public keys, shares,
  ciphertext, descriptor bytes, diagnostics, or customer-controlled values.
- `processReady`, `signingReady`, and `provisioningReady` reflect independent
  capability failures. Queue age alerts do not automatically stop admission;
  primary-store/key-provider failure blocks signing, while recovery-store,
  preparams, disk-space, or terminal-publication failure blocks only new DKG
  where possible. A recovery-inspection capability deferred at startup keeps
  DKG admission latched closed until restart and successful reconciliation.
- Artifact format, one-key lifetime, host-compromise assumption, customer backup
  responsibility, no post-activation C guarantee, local-filesystem locking, and
  the absence of a supported recovery tool are documented. `FUTURE-001` remains
  DESIGN-only.
- Deployment documentation and assertions require one replica, no overlapping
  old/new writable processes, one writable state filesystem shared by both B/C
  stores, and one stable lock path inside that state directory. The lifetime
  OS lock is documented as local-filesystem fencing, not cross-host fencing.
- `make verify-mpc-2of3` is the single repository release command. It sets
  `GOWORK=off` internally, rejects non-Linux execution for required
  filesystem/lock suites, verifies both vendored bundles and exact
  `mpc-core v0.3.0` with no `replace`, runs `go test -race ./...`, helper-process
  suites, bounded fuzzing, the tagged recovery proof, and a production build
  without tags. Any missing/skipped mandatory suite is an error.

- [ ] Add failing metrics/readiness tests, secret-redaction tests, documentation
      assertions, contract/core-pin verifier tests, production-build capability
      checks, and a smoke test that `make verify-mpc-2of3` propagates every child
      failure.
- [ ] Add documentation contract tests for one replica, no-overlap rollout, one
      writable B/C state filesystem, stable lock path, and the explicit
      cross-host/distributed-fencing non-goal.
- [ ] Add separate immutability scenarios that snapshot exact final B/C bytes,
      mode, and mtime immediately after publication: an activated key through
      primary-share loading/A+B SIGN and graceful shutdown; a live lost-response
      retry through exact replay; and a pre-activation own-CLAIMED restart
      through completion reconstruction. Every retained file remains identical;
      authoritative FAILED/TIMED_OUT cleanup may remove or atomically quarantine
      it but never rewrite it, and any quarantined bytes remain identical. Do
      not assert `atime`.
- [ ] Prove a corrupt B for one key fails only that SIGN and raises a critical
      alert while unrelated keys, `signingReady`, and `processReady` remain
      healthy. Separately prove a systemic primary-store or shared-key-provider
      outage makes signing unavailable and closes process readiness.
- [ ] Add a metric-contract table test covering every co-signer-owned metric
      name, increment/observe site, bounded label value, overflow/drop counter,
      and redaction canary. Missing or unexpectedly labeled metrics fail.
- [ ] Run targeted metrics/health/docs tests; expect missing safe benchmark
      surfaces and capability distinctions.
- [ ] Implement safe metrics, security/artifact documentation, custody runbook,
      and the one release verification target. Do not add Dockerfiles, ECR/OCI
      publication, Kubernetes manifests/verifiers, GitHub deployment workflows,
      multi-arch builds, ingress control, or replica automation.
- [ ] On a real Linux host using the supported production filesystem topology
      run `make verify-mpc-2of3`; require non-zero on any skipped capability
      suite, failed threshold/test, or secret-bearing output. The backend-owned
      coordinated E2E and benchmark consume the provided binary, test executable,
      and metrics but are not reimplemented here.
- [ ] Commit with `docs(operations): define recovery artifact support`.

### PLAN-STEP-CS-009c: Pin and prove normal SIGN claim/result fixtures

**Sources:** `REQ-082`-`REQ-084`, `DECISION-048`, `INV-057`-`INV-059`,
`AC-005`, `AC-014`, `AC-038`

**Depends on:** `PLAN-STEP-CS-009b`, `SIGN-CLAIM-HTTP-V1`

**Files:**

- Modify: `testdata/mpc-co-signer-http/v1/` vendored fixture bundle
- Modify: HTTP contract manifest verifier and golden tests
- Modify: backend HTTP client claim/result tests
- Modify: repository verification/sync tests as required by the new manifest

**Interfaces:**

- `make sync-contracts` copies the complete updated backend-owned HTTP bundle
  from an explicit local source, verifies the temporary copy, atomically
  replaces the fixed destination, and verifies the final destination.
- The production worker continues to use the existing shared claim and result
  routes for DKG and SIGN. The client strictly validates the SIGN claim fixture,
  preserves the immutable `intentId`, `sessionId`, deadline, deployment owner,
  and payload, and posts only the existing minimal SIGN `COMPLETED`/`FAILED`
  result shape.
- The strict listing exposes discovery-only `ownClaimedSign[]`. Normal intake
  combines those entries with `pending[]` and calls the same claim endpoint, so
  a restarted stable deployment receives the authoritative same-owner payload
  and deadline replay. Startup reconciliation continues to consume only
  `ownClaimedDkg[]`.
- DKG terminal publication remains canonical and fingerprinted. SIGN must never
  enter the DKG terminal publisher, while DKG cannot use the generic SIGN result
  path.
- This step adds no scheduler lane, generic intent API, recovery capability,
  feature flag, or compatibility mode.

- [ ] Vendor the exact `SIGN-CLAIM-HTTP-V1` producer bundle with source commit
      and per-file hashes; prove missing, altered, or unexpected files fail.
- [ ] Add fixture-driven client tests for SIGN claim success, same-owner replay,
      restart rediscovery through `ownClaimedSign[]`, typed conflict, immutable
      payload/deadline preservation, completed result, failed result, and strict
      unknown-field rejection.
- [ ] Add routing tests proving SIGN uses generic `PostResult`, DKG uses the
      canonical terminal publisher, and neither parser accepts the other
      intent kind.
- [ ] Run contract verification, focused HTTP/worker tests, `go test -race
      ./...`, `go vet ./...`, and `make verify-mpc-2of3` on Linux.
- [ ] Commit with `test(contract): pin sign claim lifecycle fixtures`.

## Test Matrix

| Test IDs            | Behavior                                                                 | Step                    | Evidence                         |
| ------------------- | ------------------------------------------------------------------------ | ----------------------- | -------------------------------- |
| `TEST-CS-001`–`005` | Shared bundle/HTTP fixture sync, strict JCS/digest/terminal validation   | `PLAN-STEP-CS-001`      | producer manifests/golden corpus |
| `TEST-CS-006`–`009` | Exact core pin, `GOWORK=off`, no replace, contract bundle verification   | `PLAN-STEP-CS-001`, `PLAN-STEP-CS-003` | module/manifest verifier         |
| `TEST-CS-010`–`012` | Key/path/store/purpose/party binding                                     | `PLAN-STEP-CS-002`      | config tests                     |
| `TEST-CS-013`–`019` | Publish order, no-replace, readback, corruption, fuzz, crash boundaries, split core caller compile | `PLAN-STEP-CS-003`      | recorder/real FS/subprocess/full compile |
| `TEST-CS-020`–`027` | One DKG service, local routing, active-pair isolation, generation lease  | `PLAN-STEP-CS-003`, `PLAN-STEP-CS-004` | race/integration                 |
| `TEST-CS-028`–`034` | Two handles, registration/start/join barriers, sibling failure, refill  | `PLAN-STEP-CS-005`      | state/race tests                 |
| `TEST-CS-035`–`039` | Guard, DKG gate, permits, batch admission, SIGN concurrency              | `PLAN-STEP-CS-006`      | deterministic scheduler          |
| `TEST-CS-040`–`045` | Lost response, immutable retries, typed conflict, publisher lifecycle    | `PLAN-STEP-CS-007`      | stateful HTTP                    |
| `TEST-CS-046`–`052` | Full actionable reconciliation matrix and unrelated-file nonmutation     | `PLAN-STEP-CS-008a`     | exact-path tests                 |
| `TEST-CS-053`–`060` | Lock/process death, startup handoff, split readiness, SIGN during replay | `PLAN-STEP-CS-008b`     | helper process/lifecycle         |
| `TEST-CS-061`–`067` | Tagged `_test.go` B+C proof, derivation, negative digest, no prod reader | `PLAN-STEP-CS-009a`     | isolated compiled test binary    |
| `TEST-CS-068`–`072` | Safe metrics, docs, redaction, production build, full verify target      | `PLAN-STEP-CS-009b`     | Linux release command            |
| `TEST-CS-073`–`077` | SIGN fixture pin, strict claim/replay/conflict, result-kind separation   | `PLAN-STEP-CS-009c`     | vendored manifest and client tests |

## Failure and Recovery Matrix

| Failure or race                                      | Required behavior                                                                                                                                               | Proof |
| ---------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----- |
| Shared contract or HTTP source is invalid            | `sync-contracts` fails before replacing either fixed destination; CI verification never syncs.                                                                  | `TEST-CS-001`–`006` |
| Core tag missing, changed, workspace-shadowed, or replaced | Build/release verification fails before DKG admission.                                                                                                      | `TEST-CS-006`–`009` |
| Second preparams acquire fails                       | Discard B handle, register no pair, start neither party, resume asynchronous refill.                                                                             | `TEST-CS-028`–`030` |
| Active-pair registration fails                       | Discard both handles, preserve prior slot generation, start neither party.                                                                                       | `TEST-CS-020`–`021`, `TEST-CS-030` |
| Duplicate B run or stale cleanup token               | Reject duplicate same-session/same-party execution; stale release cannot clear a newer pair.                                                                     | `TEST-CS-022`–`024` |
| B or C runtime/store fails                           | Cancel sibling, join both, keep pair until all writer calls return, release only this generation, publish FAILED, never activate/reuse key ID.                    | `TEST-CS-025`–`034` |
| Terminal HTTP response is lost for a running DKG     | Retain normal DKG/general permits and retry byte-identical request indefinitely; available general capacity continues SIGN.                                       | `TEST-CS-040`–`043` |
| SIGN claim/result HTTP contract drifts                | Reject the fixture bundle or response before running/terminalizing the session; DKG terminal state is untouched.                                                  | `TEST-CS-073`–`077` |
| Startup terminal endpoint is unavailable/malformed  | Complete safe handoff, open SIGN intake/readiness, hold no general permit, keep `dkgAdmissionOpen=false` and provisioning unready.                                 | `TEST-CS-053`–`057` |
| Startup publisher cannot start or accept handoff     | Do not open intake or readiness; retain lifecycle lock until clean shutdown.                                                                                     | `TEST-CS-054` |
| Startup terminal result is confirmed                 | Record authoritative result, alert on typed conflict, wake scheduler, and consider DKG only after a fresh poll.                                                   | `TEST-CS-056`–`058` |
| Process crashes before terminal acknowledgement      | If backend still returns the intent, rebuild identical canonical bytes; if already terminal, publish nothing and leave local files unchanged without cleanup evidence. | `TEST-CS-050`, `TEST-CS-059` |
| Multiple own CLAIMED or foreign CLAIMED is returned  | Treat as protocol-integrity startup failure, leave local artifacts untouched, start no scheduler/intake, keep process/signing/provisioning unready, and admit no SIGN. | `TEST-CS-049`, `TEST-CS-057` |
| Lock is already held                                 | Fail fast before stores/backend/intake; lock owner remains sole local-filesystem coordinator.                                                                     | `TEST-CS-053` |
| Disk/recovery/preparams capability is unavailable    | Close provisioning only; continue SIGN if primary store and common key provider are healthy.                                                                     | `TEST-CS-035`, `TEST-CS-068` |
| One key's B artifact is corrupt                       | Fail only that key's SIGN with a critical alert; keep unrelated keys and global signing/process readiness healthy.                                               | `TEST-CS-068`–`069` |
| Primary store or common key provider is systemically unavailable | Mark signing unavailable and fail process readiness according to health policy; do not expose partial recovery behavior.                              | `TEST-CS-068`–`069` |
| C is missing/corrupt after activation                | Tagged B+C recovery proof fails closed; normal production A+B remains outside C and is validated by coordinated E2E.                                              | `TEST-CS-063`–`067` |

## Migration, Compatibility, and Recovery

- **Configuration migration:** replace single party/share directory and hashed
  secret behavior with explicit deployment ID, two party/directory bindings,
  strict standard-base64 key, keyRef, state/lock path, free-space threshold,
  and generation parallelism. Startup fails closed if any mandatory value or
  capability is absent.
- **Artifact migration:** N/A — clean cut starts before legacy shares require
  preservation. Existing mutable `.json` shares are not read or converted.
- **Dependency cut:** implementation may begin independently, but core-dependent
  co-signer commits cannot merge until `MPC-CORE-V0.3.0` exists.
  `CONTRACT-BUNDLE-V1` and `COSIGNER-HTTP-V1` are copied only with the explicit
  consumer-local sync command. The canonical implementation graph is in the
  DESIGN; this PLAN does not define a competing forward order.
- **Module cut:** committed `go.mod/go.sum` pin exactly `mpc-core v0.3.0`.
  Consumer CI and release verification use `GOWORK=off`; no `replace` is
  committed. Local workspace development before the tag is not release evidence.
- **Rollback:** safe only before v1 activation. After activation retain the
  original key/keyRef and perform coordinated forward recovery.
- **Crash recovery:** no protocol resume. Own actionable intents replay
  completion from exact B/C or publish FAILED; unrelated files remain untouched.
- **Customer recovery:** backup/restore B, C, original key, and keyRef. V1
  provides format and test proof, not a supported user recovery tool.
- **Maintenance cutover:** the backend-owned manual runbook stops provisioning
  and SIGN, verifies all old backend/signer/co-signer processes are gone, takes
  backups, runs backend and signer one-shot migrations, starts compatible
  binaries, verifies contract hashes and core version, runs coordinated E2E, and
  only then restores traffic. This repository adds no deployment automation.
- **Packaging and deployment:** Docker/ECR/OCI, Kubernetes, GitHub Actions,
  multi-arch builds, routing changes, and replica automation require a separate
  owner/specification and are not implementation blockers here.

## Completion Criteria

### Repository implementation

- `make verify-mpc-2of3` passes on a real Linux host and internally enforces
  `GOWORK=off`.
- Race, Linux lock/filesystem/helper-process, bounded fuzz, tagged recovery
  proof, and untagged production build suites all execute rather than skip.
- Shared contract and backend HTTP fixture manifests equal their producer
  versions. Artifact-v1 vectors remain local.
- `go.mod/go.sum` resolve exactly `mpc-core v0.3.0` without `replace`.
- `rg` finds no share-status mutation, passphrase hashing, overwrite rename,
  production recovery-store SIGN wiring, fixed terminal retry count, artifact
  inventory scan, Docker/Kubernetes deployment implementation, or test reader in
  a non-`_test.go` file.

### Behavioral validation

- One production core DKG service and shared pool run B and C only after two
  handles and generation-bound pair registration. The coordinator joins both
  parties, releases only its pair generation, and re-inspects both artifacts.
- SIGN continues while DKG is active/unconfirmed when a general slot remains.
- Startup publication closes only DKG, owns no general slot, and permits SIGN
  after safe lifecycle handoff.
- The compiled tagged test uses a production B reader plus `_test.go`-only C
  reader and two signing-only core services to prove isolated copied B+C signing.
- Missing/corrupt C fails recovery proof and does not add C to the production
  signing path.

### External rollout

- Backend activation/CAS, signer dual streams, core signing matrix, A+B
  production signing, isolated B+C proof, and negative digest verification pass
  `npm run test:e2e:mpc-2of3`.
- The backend-owned `npm run benchmark:mpc-2of3` reuses that harness, measures
  the co-signer safe metrics, and always runs SIGN-only, DKG-only, refill-only,
  refill+SIGN, and DKG→refill→DKG. With `maxConcurrentSessions > 1` it also runs
  DKG + one SIGN and DKG + `maxConcurrentSessions-1` SIGN; a value of one emits
  explicit serialized-policy `N/A` results rather than silently skipping mixed
  checks. DKG+refill+SIGN is forbidden by the repository-local refill invariant
  rather than benchmarked. The benchmark is manual before cutover and is not
  part of this repository verification target.
- This repository alone does not claim product activation or rollout readiness.

## Structural Readiness

All design IDs and residual risks are inventoried; every co-signer-owned
requirement/invariant maps to files, steps, tests, and evidence. Compatibility,
configuration migration, crash recovery, startup publication, core/module
dependency, custody, external deployment ownership, and the absence of a
user-facing recovery tool are explicit. No durable journal, inventory control
plane, or deployment subsystem is introduced. Status:
`STRUCTURALLY_READY`.

## GSTACK REVIEW REPORT

| Review | Trigger | Why | Runs | Status | Findings |
| --- | --- | --- | --- | --- | --- |
| CEO Review | `/plan-ceo-review` | Scope and strategy | 0 | NOT RUN | Approved scope was already reduced interactively |
| Codex Review | `/codex review` | Independent second opinion | 2 | INCORPORATED | Outside-voice findings were folded into the contracts and plans |
| Eng Review | `/plan-eng-review` | Architecture, failure modes, tests, and performance | 4 | CLEAR (PLAN) | 21 issues, 0 critical gaps; all accepted changes were folded |
| Design Review | `/plan-design-review` | UI/UX gaps | 0 | N/A | Backend and cryptographic feature with no UI scope |
| DX Review | `/plan-devex-review` | Developer experience gaps | 0 | NOT RUN | Not required for the architecture gate |

**VERDICT:** ENG CLEARED — ready to implement against the pinned DESIGN revision.

NO UNRESOLVED DECISIONS
