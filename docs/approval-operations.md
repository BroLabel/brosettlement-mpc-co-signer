# Approval dispatcher operations

Approval is mandatory for every new backend transfer and every SIGN. Upgrade backend and Co-Signer together under the coordinated cutover below; there is no organization capability flag. V1 discovery and signed receipts precede activation; v2 prepared claims verify the approved parameters and exact transaction bytes before MPC.

## First installation

Provision a **new**, empty private durable directory deliberately, owned by the user running Co-Signer, outside both primary and recovery share directories. For example, as that user:

```sh
install -d -m 0700 /var/lib/co-signer/approval
```

This is first-time provisioning only. Never run this as a repair for a missing journal after an installation has admitted work. The application requires the directory to exist; it never creates a replacement. Both lexical and resolved symlink overlap with share directories are rejected. The directory must support create-only rename, file fsync and directory fsync. Linux uses RENAME_NOREPLACE; macOS uses RENAME_EXCL and F_FULLFSYNC for files. Startup probes these operations, checks directory ownership/0700 and retains an independent lifetime writer lock. A second installation using different primary shares still cannot share this journal concurrently. Files are 0600 and read without following symlinks.

Set all trusted approval settings together:

| Setting | Value |
| --- | --- |
| `CO_SIGNER_APPROVAL_MODE` | Exactly `external` or `auto_approve` |
| `CO_SIGNER_APPROVAL_ORG_ID` | The authenticated backend organization |
| `CO_SIGNER_APPROVAL_ENVIRONMENT` | The same trusted environment used by backend receipt verification |
| `CO_SIGNER_APPROVAL_CONFIG_REVISION` | An immutable operational revision |
| `CO_SIGNER_APPROVAL_JOURNAL_DIR` | The provisioned absolute directory |
| `CO_SIGNER_APPROVAL_ENDPOINT` | For external mode, the exact HTTPS URL; no userinfo or fragment |
| `CO_SIGNER_APPROVAL_BEARER_TOKEN` | For external mode, a dedicated organization integration credential |

The bearer credential is never copied into persisted evidence, receipt envelopes, organization payloads or formatted configuration. Platform API credentials are used only for signed backend requests. Endpoint redirects are disabled. Every organization call has one transport attempt, including on reused connections; automatic HTTP body replay is disabled. Standard TLS server authentication remains required.

With no approval settings, startup supports provisioning only: the scheduler leaves SIGN work untouched, and health reports `signingReady: false` while process and DKG readiness follow their own capability checks. It does not claim or publish failures for SIGN requests. The pre-MPC authorization gate still rejects missing approval evidence. There is no non-approval SIGN mode. A partial or invalid explicit approval configuration fails startup. `CO_SIGNER_STATE_DIR` and `CO_SIGNER_LOCK_PATH` remain unsupported. Existing `CO_SIGNER_HTTP_TIMEOUT`, `CO_SIGNER_POLL_MIN_INTERVAL`, `CO_SIGNER_POLL_MAX_INTERVAL` and finite `CO_SIGNER_POLL_BACKOFF_FACTOR >= 1` control bounded attempts and full-jitter suppression. Waiting has no total deadline or maximum age.

Selecting `auto_approve` for an unseen backlog is an explicit operational decision requiring review before changing configuration. This mode produces durable signed APPROVED receipts through the same path and makes zero organization calls. Endpoint and token settings are ignored in auto mode.

TRON signing requires no blockchain RPC settings. Local transaction verification
trusts Backend for the selected TRON network and TAPOS reference. Mainnet and
Nile wallet keys/paths are not yet separated; a matching test-network approval
can therefore expose real funds at a shared mainnet address if Backend supplies
mainnet bytes. Review the [TRON network trust boundary](approval-prepared-sign-v2.md#tron-network-trust-boundary)
before operating either approval mode. The follow-up is
[BROSET-156](https://brolabel.atlassian.net/browse/BROSET-156).

## Discovery and delivery

Backend pending pages are the authoritative unresolved queue. One page of at most 50 items is processed per tick in server order. The opaque tenant-bound cursor carries the backend's fixed sweep upper bound. Suppressed items do not stop page progress. An explicit backend HTTP 400 `INVALID_CURSOR` restarts discovery from the first page on the next tick without changing saved decisions or suppression hints. Transport/authentication errors retain the current cursor. Null nextCursor wraps on the next tick; continuous arrivals join later sweeps. Empty pages and missing items are never cancellation notifications. A cancellation can race with a previously selected bounded page; backend NOT_APPLIED_CANCELLED is retained as a non-executable application result.

Each new organization attempt must come from a fresh authenticated pending page, pass immutable admission/config/hash checks, have no saved final receipt and be due after suppression. PENDING, HTTP errors, TLS failures, invalid response bindings/schema and timeouts do not create a decision. A discovery outage produces no new organization calls from local records. There is one organization call in flight, independent of SIGN permits. No transaction, MPC work or SIGN permit is allocated by this worker.

A final receipt is signed once, checked against pinned admission, and durably published before delivery. Exact wrapper bytes, canonical envelope/signature and historical public identity are retained. Lost responses retry the saved bytes with fresh HTTP authentication, even when pending discovery is empty or unavailable and after identity rotation. They never re-poll the organization. Backend HTTP 200/409 must match the closed request-bound application result before a durable ACK is saved. Permanent backend errors remain visible for reconciliation; they never fabricate a rejection or replacement receipt. TERMINAL_CONFLICT is acknowledged as non-applied evidence and surfaced for review. Only APPLIED APPROVED evidence can admit a later execution binding. If SIGN arrives while a valid local APPROVED receipt still awaits its durable ACK, the owned SIGN waits while the existing dispatcher redelivers that receipt. It does not call the organization or admit MPC early. The existing SIGN deadline still applies; missing admission/receipt, corrupt history and missing ACK after execution binding remain integrity failures.

Recovery streams at most 50 directory names and holds at most one receipt body per tick. It alternates scanning with the oldest due known delivery retry, reading exact saved evidence on demand. One failing receipt cannot consume every recovery tick or prevent new page progress. At most 1000 aggregate suppressive hints are retained; organization hints may be evicted, which only resets delay. Saved-delivery hints are protected from eviction. If all 1000 slots belong to saved deliveries, new evaluations and automatic decisions pause visibly, while discovery and saved-evidence recovery continue. No rejection, expiry or hold release results from this backpressure. An ACK frees capacity and fresh-page evaluations resume. A saturated permanent delivery outage requires operator reconciliation.

## Immutable evidence and configuration changes

Names use SHA-256 of org ID, a NUL separator and request ID; arbitrary wire identifiers are never path segments. Local records include admission, revision configuration, exact receipt plus retained public key, delivery ACK and execution evidence. Reads check exact org/request/authorization hash, closed fields, signature, source/environment/revision and application bindings. Reusing a revision for a different mode/environment/endpoint conflicts. A new revision does not migrate an existing admission; restore its pinned integration configuration to resume. The dedicated credential may rotate for the same integration. Saved final receipts are independent of current mode/environment/key and continue delivery using their retained evidence.

Publication is temp write, 0600, file fsync, create-only rename and directory fsync. Any failure prevents delivery or execution admission. If rename succeeded but directory fsync failed, finding the final record never re-evaluates the organization; file/directory durability must first be re-established. Missing required dependencies, unsafe metadata, changed bindings or bytes, unsupported filesystem operations and a replaced directory fail closed. A closed journal object cannot continue writing after another owner acquires its lock.

Execution evidence uses the frozen `ExecutionBindingV1` and one narrow create-only `execution-known` SHA-256 marker. The marker is published first; both artifacts, their exact hash and applied approval dependencies are required before returning a binding for later MPC admission. The same binding replays; a changed outgoing ID, digest or algorithm conflicts. One missing artifact or a partial two-file publication requires manual reconciliation. Neither artifact is reconstructed from a backend claim. This journal API does not verify prepared bytes or authorize MPC by itself; the prepared verifier must enforce those later gates and require these local dependencies.

## Recovery and restore

Stop admission and coordinate with the backend operator before repairing or restoring storage. Keep transfer intake and SIGN admission paused during cutover or recovery until the complete workflow is proven. Retain all journal files and historical public keys. Use a coordinated whole-volume backup/restore with the backend, preserving file ownership, permissions and all admission/receipt/ACK/execution/marker/revision evidence. Do not delete evidence, manually rewrite a decision, create an empty replacement, restore an older subset or silently switch modes to bypass a conflict. Preserve partial artifacts for investigation; external idempotency alone is not sufficient authorization to reconstruct local history.

Detected missing dependencies or a single missing execution artifact fail closed. This does not claim detection of arbitrary rollback or deletion of every related file: erasing both execution artifacts or all request evidence can remove the local fact that they existed. Preventing that event relies on the approved coordinated-volume/restore discipline; this feature does not introduce a global history registry. Investigate the original publication outcome and authoritative backend state, then restore verified exact evidence under a reviewed operational procedure. Never regenerate a business decision or execution binding from a claim echo.

## Verification boundary

Synthetic HTTP servers cover provider and backend transport behavior. Journal crash tests use subprocess termination after actual write/file-sync/create-only publication/directory-sync and receipt-send/ACK boundaries. Linux evidence requires executing the tests on a Linux filesystem, including container-native `/tmp`; macOS success or cross-compilation alone is not Linux proof. Process termination and syscall fault injection exercise recovery boundaries, but do not simulate physical power loss or establish a storage vendor's durability guarantees.

## Coordinated local validation

Task9 adds an explicitly required backend suite, `test/mpc-2of3/approval.external.e2e-spec.ts`, selected by `jest-approval-coordinated.json` through the existing owned disposable runner's `approval-full-e2e` mode. Its executed-result reporter rejects empty, skipped, failed or wrong-suite results. Harness unit tests alone do not prove cryptographic MPC or chain settlement.

The macOS synthetic organization HTTPS proof uses a Go 1.27.2 Co-Signer executable and the standard `SSL_CERT_FILE` with `GODEBUG=x509sslcertoverrideplatform=1`. The module's minimum Go 1.24 and production configuration are unchanged. A Go 1.27 build-tagged default-transport subprocess test separately proves trusted CA success, wrong CA rejection and hostname rejection. These are standard verified trust roots, not an insecure TLS bypass. Report the actual build toolchain independently from minimum-toolchain race tests.

The backend runbook `docs/runbooks/transfer-approval-rollout.md` specifies the controlled organization intake/legacy SIGN pause and drain required at cutover, mandatory approval cutover, pending-hold cancellation, exact journal restore and prohibited double-old rollback. Never treat a local synthetic capability fixture as live enablement authority.

## Mandatory approval cutover

Every SIGN requires a v2 claim with nonnull approval and prepared bytes, the configured organization, and the matching durable local receipt and application ACK. Missing configuration, journal, approval, or byte binding never permits signing. `auto_approve` remains an explicit decision mode and writes the same signed evidence.

Upgrade backend and Co-Signer together under paused transfer intake and SIGN admission. The backend migration refuses nonterminal transfers or active SIGN sessions without approval. Drain them with the compatible old release before retrying; never rewrite their origin, delete their holds, or synthesize approval evidence. Historical settled transfers remain readable.
