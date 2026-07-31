# Recovery artifact operations runbook

## Supported topology

Operate exactly one co-signer replica. B and C stores must share one writable
local state filesystem, and the stable lock path must be inside that state
directory. Do not run old and new writable processes concurrently: stop and
verify the old process is gone before starting the replacement. The OS advisory
lock is local-filesystem fencing only, not distributed or cross-host fencing;
NFS/shared-filesystem active-active operation is unsupported.

## Backup and restore

Back up the immutable B/C files together with the original 32-byte encryption
key and its exact `keyRef`. Restore them without changing file bytes. Do not
rename, decrypt/re-encrypt, or rotate an artifact. A new key or `keyRef` makes
v1 artifacts unreadable and must be treated as a fail-closed incident.

## Security response

Treat co-signer host compromise as a quorum-bearing incident: the host, or the
shared key plus both artifacts, can authorize B+C signing without platform A.
Preserve evidence and contact security support. The service offers no supported
post-activation C guarantee and no recovery command. `FUTURE-001` may define a
future product only after a separate DESIGN.
