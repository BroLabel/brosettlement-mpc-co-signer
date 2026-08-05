# Recovery artifact operations runbook

## Supported topology and stop conditions

Operate exactly one co-signer replica on Linux. The primary, recovery, and
state directories must be pre-created with private permissions on the intended
local writable filesystem. The stable lock path must be inside the state
directory. The advisory lock is local-filesystem fencing only; cross-host,
shared-NFS, and active-active operation are unsupported.
It is not distributed fencing. Run with no overlap and one writable local
state topology.

Keep provisioning closed and do not start a replacement process when any of
the following is true:

- the previous process has not exited or still holds the lifetime lock;
- the filesystem lacks no-replace publication, file `fsync`, or directory
  `fsync` support;
- either artifact directory is absent, unsafe, read-only, or low on space;
- the shared encryption key or `CO_SIGNER_SHARE_ENCRYPTION_KEY_ID` is missing;
- startup reconciliation reports protocol-integrity failure;
- a terminal result is unconfirmed or conflicts without an authoritative
  backend outcome.

Normal A+B signing may continue through provisioning-only failures when
`signingReady` remains true. A primary-store or shared-key-provider failure
makes signing unavailable and requires immediate escalation.

## Lock contention and shutdown

Lock acquisition is fail-fast. Treat `lock already held` as evidence of another
writer, not as a stale PID file. Verify the old process and every child worker
have stopped; do not delete the lock file or start a standby writer. During
shutdown, close readiness and intake, drain or cancel workers, wait for the
terminal publisher and pre-parameter generator, close stores, and only then let
the process release the lock. If drain is stuck, terminate the old process and
wait for the OS to release its file descriptor before restarting.

## Filesystem capability and capacity incidents

Do not replace a missing mount by creating local directories. Restore the
intended volume and its private ownership/permissions, then restart so startup
capability checks run again. Low free space closes new DKG admission but must
not trigger automatic artifact deletion. Observe file count, total bytes, free
space, temporary-file count, and oldest-file age without logging paths or key
identifiers.

Temporary or unclassified files are not authoritative evidence of an orphan.
Never move, overwrite, decrypt, classify, or delete them solely by age or
filename. Obtain an actionable backend intent and follow reconciliation before
changing any artifact.

## Pre-parameter failures

New DKG is eligible only when two durable pre-parameter entries are available
and generation is idle. Acquisition, unlink, or directory-sync failure keeps
DKG closed; it does not permit synchronous fallback or material reuse. If the
second acquisition fails, discard the first handle and wait for asynchronous
refill. No refill may start while either local B/C runtime is active. Escalate
repeated generation failure or inventory that cannot reach two; keep SIGN
running when its independent capabilities remain healthy.

## Actionable startup reconciliation

Reconcile only DKG intents returned by backend as `PENDING` or owned
`CLAIMED`. Do not scan unrelated artifact inventory.

| Backend state | Addressed B/C state | Required action |
| --- | --- | --- |
| Unexpired `PENDING` | Neither artifact exists | Claim and run a new DKG. |
| `PENDING` | Either addressed artifact exists | Claim, publish `FAILED`, retire the key, then clean up only after authoritative acknowledgement. |
| Own `CLAIMED` | Both exist and strict inspection agrees | Reconstruct byte-identical terminal completion and hand it to the retry publisher. |
| Own `CLAIMED` | Missing, partial, or mismatched | Publish `FAILED`; never reuse the key. |
| Foreign `CLAIMED` returned defensively | Any | Change nothing, keep DKG closed, and raise a protocol-integrity alert. |
| `COMPLETED` | Both exist and agree | Preserve both immutable artifacts. |
| `FAILED` or `TIMED_OUT` | Addressed artifacts exist | Clean up only after authoritative evidence permits it; quarantine or delete best-effort. |

Reconciliation completes before normal intake. If terminal publication is
handed to the lifecycle-owned publisher, SIGN may open while DKG remains closed
until an accepted replay or typed conflict is confirmed.

## Terminal uncertainty and conflicts

Network errors, timeouts, EOF, `5xx`, malformed typed responses, and unexpected
`4xx` keep the DKG guard occupied and retry with capped backoff. They do not
authorize another DKG. `SIGN` continues through the general scheduler when
signing capability is healthy.

A typed terminal conflict preserves the backend winner, raises an integrity
alert, releases the DKG gate, wakes the scheduler, and requires a fresh poll.
Never synthesize cleanup authority from a lost response. If backend no longer
returns the intent as actionable after restart, leave local bytes unchanged.

## Partial publication and cleanup authority

Failure to publish or inspect either B or C prevents product activation. B+C
may still be cryptographically capable, so deletion is cleanup rather than a
security boundary. Never activate or reuse the failed `keyId`.

Cleanup is allowed only after an authoritative outcome of `FAILED` or
`TIMED_OUT` for the addressed intent. `COMPLETED` artifacts must remain intact.
Files outside the actionable intent's exact paths are never changed. Preserve
before/after hashes when investigating publication failures and escalate any
unexpected byte mutation.

## Primary and recovery degradation

- Missing or corrupt primary B blocks only SIGN operations for that key and
  emits a critical alert. A systemic primary-store or key-provider failure
  makes `signingReady` false.
- Missing or corrupt recovery C after activation does not disable A+B signing.
  It removes the customer's recovery capability and requires a custody alert;
  backend key state remains `ACTIVE`.
- Recovery-store, free-space, or pre-parameter failure closes
  `provisioningReady` only. Do not report signing unavailable unless its own
  dependencies fail.

## Queue and relay incidents

Monitor oldest pending DKG age, oldest pending SIGN age, pending batch
composition, typed claim throughput, DKG-guard occupancy, relay conflicts, and
terminal-unconfirmed state. Queue age is an SLO alert, not automatic admission
authority. A blocked DKG guard must not consume the general SIGN permit.

Wrong party, session, round, sender sequence, or stream binding is a protocol
integrity incident. Fail the affected session, preserve redacted diagnostics,
and investigate backend/co-signer contract compatibility before reopening DKG.

## Backup, restore, and forward recovery

Back up immutable B/C files together with the original 32-byte encryption key
and exact `CO_SIGNER_SHARE_ENCRYPTION_KEY_ID`. Restore exact bytes to their
original purpose-bound directories. Do not rename, decrypt/re-encrypt, rotate,
or adopt an artifact under another key filename.

V1 has no component-level rollback after activation or destructive migration.
For rollout failure, keep traffic closed and use a coordinated forward fix, or
restore the pre-v1 snapshot before any v1 key was activated. Do not restart old
binaries against migrated schemas.

## Security response

Treat co-signer host compromise as a quorum-bearing incident: the host, or the
shared key plus both artifacts, can authorize B+C signing without platform A.
Preserve evidence and contact security support. The service offers no supported
post-activation C guarantee and no recovery command. `FUTURE-001` may define a
future product only after a separate DESIGN.
