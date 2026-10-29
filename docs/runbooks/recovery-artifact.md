# Recovery artifact operations runbook

## Supported topology and stop conditions

Operate exactly one co-signer replica on Linux. The primary and recovery
directories must be pre-created with private permissions on the intended local
writable filesystem. The process uses the fixed lifetime-lock path
`<primary directory>/.co-signer.lock`. The advisory lock is local-filesystem
fencing only; cross-host, shared-NFS, and active-active operation are
unsupported. It is not distributed fencing. Run with no overlap and one
writable local artifact topology for DKG publication. These writer constraints
do not require permanent local retention of C files for activated keys. Keep
the configured recovery directory available for new DKG even after existing C
artifacts have been transferred to independent custody.

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

## Filesystem capability incidents

Do not replace a missing mount by creating local directories. Restore the
intended volume and its private ownership/permissions, then restart so startup
capability checks run again. Observe file count, total bytes, free space,
temporary-file count, and oldest-file age without logging paths or key
identifiers. Free-space telemetry is observational and does not close DKG
admission or trigger automatic artifact deletion.

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
`TIMED_OUT` for the addressed intent. `COMPLETED` artifacts must retain their
exact bytes and identity. A verified post-activation C custody transfer follows
the separate procedure below; it is not failed-DKG cleanup.
Files outside the actionable intent's exact paths are never changed. Preserve
before/after hashes when investigating publication failures and escalate any
unexpected byte mutation.

## Primary and recovery degradation

- Missing or corrupt primary B blocks only SIGN operations for that key and
  emits a critical alert. A systemic primary-store or key-provider failure
  makes `signingReady` false.
- Absence of local C after a verified transfer to independent custody is
  expected and does not remove recovery capability. Loss or corruption of all
  recoverable C copies requires a custody alert and removes B+C recovery;
  normal A+B signing and backend `ACTIVE` state remain unchanged.
- Recovery-store or pre-parameter failure closes
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

## Post-activation Share C custody

C may be retained in independent customer-controlled storage, including offline
storage, after DKG is authoritatively terminal `COMPLETED` and the key is
activated. Do not transfer C while DKG, activation or terminal delivery remains
unresolved.

1. Identify the exact activated key's `.recovery.json` artifact. Preserve its
   original filename, exact bytes, key/session identity and encryption key
   reference; never modify or re-encrypt it.
2. Establish a durable independent copy outside the Co-Signer's trust domain.
   Verify byte equality and recoverability with the original 32-byte key and
   exact `CO_SIGNER_SHARE_ENCRYPTION_KEY_ID` using a reviewed custody procedure.
   Keep the encryption key protected separately. Do not log share material or
   expose it through chat, tickets or command arguments.
3. Only after that verification may the operator remove the active-host copy.
   Never destroy the last recoverable copy. Retain protected evidence of the
   custody handover without placing secrets in service logs.
4. Keep primary B, the configured recovery directory, the primary initialization
   marker and approval journal intact. This is a file custody transfer, not
   removal of the storage mount or replacement of journal history. C is not
   needed for normal A+B signing. New DKG still publishes a new C locally and
   requires a new custody handover for that key.

The service does not export C or verify external custody automatically. Local
absence alone is not proof of a completed transfer. This procedure does not
introduce remote DKG, a remote share-store backend or a new recovery CLI.

## Backup, restore, and forward recovery

Preserve immutable B and C artifacts in independently controlled backup
locations, plus the original 32-byte encryption key and exact
`CO_SIGNER_SHARE_ENCRYPTION_KEY_ID` in separately protected key custody. Restore
B to its purpose-bound directory; C may remain in verified independent custody
unless an approved recovery procedure needs it. Preserve exact bytes and
original filenames. Do not decrypt/re-encrypt, rotate, or adopt an artifact
under another key filename. Preserve approval-journal and initialization-marker
evidence under the existing coordinated restore procedure.

V1 has no component-level rollback after activation or destructive migration.
For rollout failure, keep traffic closed and use a coordinated forward fix, or
restore the pre-v1 snapshot before any v1 key was activated. Do not restart old
binaries against migrated schemas.

## Security response

Treat co-signer host compromise as quorum-bearing when both artifacts and their
decryption material were accessible to that host, including during DKG or before
C separation. The shared key plus both artifacts can authorize B+C signing
without platform A. Later C removal does not revoke previously obtained copies;
independent custody reduces continuing exposure, not past compromise.
Preserve evidence and contact security support. The service offers no supported
post-activation C guarantee and no recovery command. `FUTURE-001` may define a
future product only after a separate DESIGN.
