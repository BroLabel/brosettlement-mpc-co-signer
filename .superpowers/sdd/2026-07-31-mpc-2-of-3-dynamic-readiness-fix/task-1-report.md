# Task 1 Report — Refresh provisioning capability readiness

## Status

DONE

## Root cause

Lifecycle startup stored a process/signing-ready snapshot while the production
pre-parameter pool was still warming. The scheduler used the live
`applicationResources.provisioningReady` predicate, but the health handler only
refreshed the signing predicate. As a result, `/health` continued returning the
startup `dkg_capability_unavailable` snapshot after the pool reached two items
with no generation in flight.

## Implementation

- Added a dynamic provisioning probe to the lifecycle health handler.
- Restricted dynamic refreshes to process/signing-ready snapshots whose reason
  is either empty or `dkg_capability_unavailable`.
- Re-read the authoritative lifecycle snapshot after the live probe so a
  concurrent shutdown, `dkg_terminal_unconfirmed`, or
  `dkg_capability_deferred` transition cannot be overwritten.
- Kept the systemic signing probe authoritative over process, signing, and
  provisioning readiness.
- Added an application health-server factory that passes the exact production
  `applicationResources.provisioningReady` predicate used by scheduler
  admission to the health handler.
- Did not add a pre-parameter wakeup receiver. The scheduler remains the only
  consumer of `PreparamsController.Wakeups()`.

## Tests added

- Startup `dkg_capability_unavailable` snapshot transitions to provisioning
  ready when the live predicate becomes true.
- A later false live predicate closes only provisioning readiness.
- Terminal-unconfirmed and capability-deferred reasons bypass and cannot be
  overridden by a true provisioning probe.
- A false signing probe closes process, signing, and provisioning readiness.
- A probe racing with shutdown cannot reopen readiness.
- The application health-server factory uses the live production provisioning
  predicate rather than only a directly constructed test readiness value.

## TDD evidence

- The initial health regression failed with provisioning still false after the
  live predicate became true.
- The complete health regression set failed without dynamic evaluation on the
  false-to-true transition, the later true-to-false transition, and the
  shutdown/probe ordering case.
- The application regression first failed because the production health-server
  factory did not exist, then passed after the factory wired the shared live
  predicate.

## Verification

- `GOWORK=off go test -race ./internal/health ./internal/lifecycle ./internal/preparams ./cmd/co-signer -run 'TestHealth|TestCoordinator|TestPreParamsController|TestApplicationHealthServer' -count=1`
- `GOWORK=off go test -race ./...`
- `GOWORK=off go vet ./...`
- `git diff --check`
- Structural diff checks confirmed no changes under `internal/lifecycle`,
  `internal/preparams`, `internal/worker`, platform free-space files, or
  deployment surfaces.

All commands completed successfully.

## Self-review

- Lifecycle gate precedence is explicit and fail-closed for unknown future
  provisioning reasons.
- The dynamic probe changes only the per-request health view; it does not mutate
  the lifecycle snapshot or reopen DKG admission.
- Scheduler admission and health use one product predicate covering retained
  store capability, two pre-parameters with generation idle, and both artifact
  directories' free-space threshold.
- No fake pre-parameters, platform bypass, HTTP compatibility shim,
  cryptographic behavior change, reviewer agent, quality-gate workflow, or
  deployment change was introduced.

## Concerns

None.
