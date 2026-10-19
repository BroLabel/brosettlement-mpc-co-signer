# BROSET-123 local verification and release handoff

Date: 2026-10-19. Status: implemented and locally verified, with the container-image limitation below. This is not a release, deployment, shared migration, or live version readback record.

## Source identities

| Repository | Pre-change source | Implemented source |
| --- | --- | --- |
| Backend | `6488cbcde5a07c87963ebc0d43041e83586ce1dd` | ingestion `e77a5cf62d845aa284910516a655cca4ab5e972b`; API `b8002d98cb31a79fe5a3e9f39dcb2d0f862173a3`; Task 5 integration `a26ece1a` |
| Co-Signer | `5df5ac25953debeb94f2c37d89a2ad458a73057c` | build identity `c49f70f`; polling `99b0685327d637fad851cac30eede5a56ae58ff1`; verification helper and exact local-build assertion are in the commit adding this document |
| Frontend | `419336df1b7d5cfa1d27d3c66cffdbbddf3dd323` | `97349f7` |

The backend fixture `apps/client-api/src/mpc/co-signer/http/co-signer.controller.legacy.fixture.ts` preserves the complete old controller byte for byte, with a fixed SHA-256 regression assertion. Authentication guards, raw-request capture, and RBAC source are unchanged from the backend baseline. This is an isolated old-controller compatibility fixture, not a complete old-backend deployment. The actual legacy Go client was compiled from `git archive` of the exact pre-change Co-Signer revision above.

## New integration evidence

Node commands used Node 22 at `/Users/vlas/.npm/_npx/52027bd8fc0022aa/node_modules/node/bin/node`; prepend that binary's directory to PATH when reproducing on this machine. Commands are run from the named repository root.

| Repository / command | Actual result |
| --- | --- |
| Backend: `npm run test -w brosettlement-client-api -- --runInBand co-signer-signed-polling` | PASS: 9 tests, 1 explicitly skipped opt-in Go test. No sibling checkout, Go toolchain, Git history, external service, or database is required. |
| Backend: same command with `POLLING_NEW_CLIENT` and `POLLING_LEGACY_CLIENT` set to locally built helper binaries | PASS: 10/10 tests, including six actual Go requests over TCP. |
| Backend: `npm run typecheck:client` | PASS. |
| Backend: `MPC_HEARTBEAT_TEST_ADMIN_URL=postgresql://broset123@127.0.0.1:55483/postgres node apps/client-api/scripts/test-co-signer-heartbeat-postgres.mjs` | PASS: migrations applied only to a unique disposable database, 2 PostgreSQL tests, database removed afterward. UTC is configured only on that disposable database. |
| Co-Signer: `./scripts/check-release-identity.test.sh` | PASS: stable/RC tag equality, main/staging development versions, full revisions, tagged-commit branch classification, rejected malformed revisions, preserved publication boundaries. |
| Co-Signer: `./scripts/check-local-build-identity.test.sh` | PASS: exact clean development identity and exact dirty identity. Clean version matching now includes its argument boundary, so `.dirty` cannot pass the clean assertion. |
| Co-Signer: `GOWORK=off go test ./cmd/co-signer ./internal/health ./internal/monolith ./scripts/polling-http-compatibility-client` | PASS: three tested packages; helper compiles and has no test files. Existing packages used Go's cache. |
| Backend pre-commit: `npx lint-staged` and `npm run lint` | PASS: formatting and 9/9 turbo build/lint tasks (7 cached). |
| Both repositories: `git diff --check` | PASS. |

The real Nest/Fastify controller, `RequestContextMiddleware`, `ApiKeySigningGuard`, `RbacScopeGuard`, `ClientApiExceptionFilter`, pending-intents use case and client-status use case run in the new suite. No guard returns unconditional success. Only key lookup/nonce persistence, intent persistence, heartbeat storage, unrelated use cases, and observability dependencies are replaced. The nonce fixture rejects reuse. Generated Ed25519 credentials exist only for the local fixture.

The tests establish:

- `%2B` survives exact-target signing and is decoded to a literal plus for storage. A correctly signed malformed/unescaped/repeated version remains a successful poll with null version and a valid independent revision.
- Changing version, revision, query ordering, `%2B` to `+`, or even `%2B` to `%2b` after signing produces `401 INVALID_SIGNATURE`, with no heartbeat write. Replayed nonces produce `401 REPLAY_DETECTED`; insufficient scopes produce `403 INSUFFICIENT_SCOPE`.
- The authenticated key selects `org-123`, and server configuration selects `server-party`; signed attacker-supplied org/party fields cannot redirect storage.
- The complete `contracts/mpc-co-signer-http/v1/listing-response.json` is unchanged after real HTTP serialization and real use-case mapping. Go clients decode it with their existing strict decoders.
- New Go -> new controller reports development identity, then a lower reporting version, then old Go -> new controller clears both fields. All remain ONLINE. New Go -> old controller accepts and ignores the extra query fields without weakening signatures. Old Go -> old controller also succeeds.
- The client-status use case and the actual mapper shared by admin list/detail consume the same sequence and return the same ONLINE version/revision snapshot. Complete admin service serialization and actual UI rendering are covered by the earlier tests below; the HTTP fixture does not start authenticated client/admin applications.

The old controller uses the current pending use case in this fixture. Its exact old behavior is to pass only authenticated orgId; the extra query fields are ignored. Real old use-case persistence and a complete old application are not claimed.

### Reproduce actual Go compatibility without shared infrastructure

The following assumes sibling `mpc-co-signer` and `back-end` checkouts at the implemented sources. Run in the Co-Signer root; the temporary directory contains source and binaries only. Do not use a deployed API URL.

```sh
compat_dir=$(mktemp -d)
git archive 5df5ac25953debeb94f2c37d89a2ad458a73057c | tar -x -C "$compat_dir"
GOWORK=off go build -o "$compat_dir/new-client" ./scripts/polling-http-compatibility-client
python3 - "$compat_dir" <<'PY'
import pathlib, sys
source = pathlib.Path('scripts/polling-http-compatibility-client/main.go').read_text()
# Only adapt the helper call to the old four-argument constructor; client source stays pinned.
source = source.replace(', monolith.BuildIdentity{Version: os.Getenv("POLLING_TEST_VERSION"), Revision: os.Getenv("POLLING_TEST_REVISION")}', '')
(pathlib.Path(sys.argv[1]) / 'compatibility-client.go').write_text(source)
PY
(cd "$compat_dir" && GOWORK=off go build -o old-client compatibility-client.go)
(cd ../back-end && POLLING_NEW_CLIENT="$compat_dir/new-client" POLLING_LEGACY_CLIENT="$compat_dir/old-client" npm run test -w brosettlement-client-api -- --runInBand co-signer-signed-polling)
```

The helper only lists and strictly decodes intents; it never claims or executes MPC work. Remove the temporary source/binaries after inspection. Ordinary backend tests intentionally skip this cross-repository part.

## Earlier verified evidence reused

These results are from the completed Tasks 1-4, not fresh broad-suite reruns during Task 5:

| Layer | Commands / results |
| --- | --- |
| Backend parsing, persistence, client status | `npm run test -w brosettlement-client-api -- --runInBand co-signer-build-identity co-signer.controller get-pending-intents.use-case co-signer-instance.store mpc-2of3-metrics.service get-mpc-status.use-case`: 89 passed, 2 PostgreSQL tests intentionally skipped in ordinary invocation. Disposable PostgreSQL separately passed 2/2. |
| Admin list/detail serialization | `npm run test -w brosettlement-admin-api -- --runInBand mpc-co-signer-status organizations.service`: 32 passed. Client/admin typechecks passed. |
| HTTP contract | `npm run verify:mpc-co-signer-http-contract`: `COSIGNER-HTTP-V1 verified: FTDwFxMF6H-johDYeeyMVxb2kxPcszVN8crqaKoqClA`. |
| Go transport/build | `GOWORK=off go test ./...`: 17 tested packages passed before Task 3 commits. Eight build-metadata transport subtests each exercised two requests, query tampering rejection, identity value-copy behavior, and strict response decoding. Release/local/registry-protection scripts passed. |
| Frontend | Client affected suites: 69/69 across seven files; admin detail/tooltip: 14/14. `npm run typecheck`: 6/6 tasks. Scoped ESLint passed. |

UI tests include `2.2.0 -> 2.1.1 -> null` rerenders in actual readiness/admin-detail components, missing-field fallback, escaped values, long development and RC identities, offline/stale retention and unchanged readiness/initialization behavior. UI is observational and does not compare versions or gate signing.

### OpenAPI provenance

The adjacent `client-status.openapi.json` and `admin-status.openapi.json` were exported from actual backend DTO classes at `b8002d98` using public Nest `SwaggerModule.createDocument()` with `extraModels` and an empty module. Their `paths` are empty: these are partial component schema exports, not deployed or full API snapshots.

| Input | SHA-256 |
| --- | --- |
| `client-status.openapi.json` | `b674f28b34a39587e7dc33e221ad618d2e7e11c91a5c0bd02819a18921eb956d` |
| `admin-status.openapi.json` | `46edd51d421f8a79dbcb1e6bb407fe06abddf9defe6456c13de9bc95c84e7a62` |

Installed `openapi-typescript 7.13.0` generated the adjacent partial `.generated.ts` files. Exactly the complete affected DTO blocks were carried into the existing frontend generated files; each frontend generated-file diff adds only required nullable `version` and `revision`. The generated blocks were compared exactly. This is selective schema generation, not full-service regeneration.

Re-export from the backend root (output directory must exist):

```sh
NODE_PATH="$PWD/node_modules" TS_NODE_PROJECT=apps/client-api/tsconfig.json STATUS_SCHEMA_OUTPUT_DIR=../mpc-co-signer/docs/superpowers/verification/2026-10-19-broset-123 node -r ts-node/register/transpile-only ../mpc-co-signer/docs/superpowers/verification/2026-10-19-broset-123/export-status-openapi.cjs
```

From the frontend root, run its installed `node_modules/.bin/openapi-typescript` against each adjacent JSON to regenerate a temporary partial output. Compare the named `MpcCoSignerStatusDto` / `OrganizationCoSignerStatusDto` blocks before replacing those blocks in `apps/{client,admin}/src/types/generated-types.ts`. Do not replace full generated files with partial output.

### Visual provenance

Task 4 used real application/UI components, i18n, React Query and Router providers in temporary isolated Vite pages, then Playwright screenshots and visual inspection. Desktop was 1440x1100; narrow was 390x844. The fixtures covered ONLINE long development identity (123 characters), ONLINE unknown, and OFFLINE known identity with last-seen/revision. Both narrow documents had scrollWidth 390; identity wrapped, status badges remained readable, revisions used 12 characters, and list metadata stayed in the existing tooltip. Known/unknown/offline tooltip states were inspected at both widths.

Adjacent `client-visual.{html,tsx}` and `admin-visual.{html,tsx}` preserve the harness sources. To reproduce, temporarily copy them to `apps/{client,admin}/broset123-visual.{html,tsx}`, run the existing Vite configs on isolated ports 33123/43123 and open `/broset123-visual.html`. Remove temporary routes afterward. Screenshots were copied outside Git to the local user artifact directory `/Users/vlas/.codex/visualizations/2026/10/19/01a153ac-d46c-7e42-9d5e-0b0945e36781/broset-123/`; they are optional illustrations, not required to interpret this record. The only browser console error was the harness favicon 404. No authenticated shared environment was used; the harness/servers/browser were stopped.

## Build identity evidence and remaining container check

The checked branch resolver produced `version=2.1.1-dev+99b0685327d6` and `revision=99b0685327d637fad851cac30eede5a56ae58ff1` for `REF=refs/heads/main`, base VERSION `2.1.1`, and that exact source commit. Stable/RC tests use verified disposable Git refs and preserve the exact tag without `v`; branch-at-tag tests stay development builds. Local-build fixtures check both clean and `.dirty` identities.

Source review confirms `.github/workflows/docker-publish.yml` derives version/revision once and passes those same values to Docker build arguments and `org.opencontainers.image.version` / `.revision` labels. `Dockerfile` requires both and injects linker globals. `main()` resolves them before startup; `resources.go` passes those same globals to polling and health. Resolver, health and signed Go transport tests passed. No runtime version setting was introduced.

A local Dockerfile build from a clean archive of `99b0685` was attempted with the exact branch identity and identical OCI labels. It stalled at `load metadata for docker.io/library/golang:1.24-alpine` and was explicitly canceled (exit 130). No successful image build, image-label inspection, container health/polling readback, registry publication, or multi-architecture build is claimed. Docker/global network settings were not changed. The OCI wiring is source-reviewed, and actual image/runtime equality remains a release verification step.

## Scope review

Review of final changed paths and diffs found no changes to MPC admission/readiness decisions, key generation/signing operations, credentials, API-key verification, authorization scopes, or signing/terminal response contracts. Release source changes are confined to identity classification, its tests, and invoking the additional local-build test. Existing image aliases, branch ownership checks, tag immutability checks, GitHub release triggers and publication channels remain intact. Task 5 changes are tests, a local verification helper, disposable-runner cleanup, and documentation. The frontend remained read-only during Task 5.

## Final review and local cleanup

An independent whole-change review on 2026-10-19 covered Co-Signer `96c8d74`, backend `a26ece1a`, and frontend `97349f7`, following the task-scoped reviews. No critical, important, or new minor findings remained. The reviewer confirmed the cross-repository contract, exact query signing, nullable snapshot semantics, build-ref classification, unchanged MPC decisions, and the evidence limits above. This is code-review readiness, not authorization to merge or deploy.

All three feature branches and isolated worktrees are retained for review. The dedicated temporary PostgreSQL cluster was stopped after verification; temporary local HTTP/Vite servers and browser sessions were closed. Task scratch is removed after the durable verification record and optional screenshots are preserved. No shared environment was modified.

## Release handoff, requiring separate authorization

1. Review/merge through the normal process only when separately authorized. Apply additive migration `20261019000000_add_co_signer_build_identity` and complete backend rollout first. Confirm all backend writers support build snapshots before installing reporting clients. A disposable migration test is not evidence of a shared migration.
2. Deploy the UI with required nullable schema fields and absent-field fallback. Verify client status and admin list/detail with known, legacy and absent snapshots.
3. Use the existing publication process for the approved Co-Signer source/ref. Verify stable or RC version equals its exact GitHub tag without `v`; branch versions retain `-dev+<12 SHA>` and the full source revision. Compare resolver output, image OCI labels and installed binary health before relying on the display. The local registry-fetch limitation above must be closed with a successful real image/build check.
4. Explicitly install the approved build. Read back health, signed polling snapshot, both status APIs and UI. Record installed image digest/source SHA and matching version/revision separately from CI or merge evidence. Metadata is a last reported observation, not attestation or a compatibility gate.

Rollback: an older reporting client overwrites metadata with its lower version; a legacy client explicitly clears both fields on the new backend while remaining ONLINE. Keep additive columns during application rollback. If backend writers are rolled back to a pre-reporting version, hide/roll back the new displays too: old writers refresh presence without clearing previously stored metadata. Do not drop columns as an emergency rollback. Offline expiry preserves the last observation. Simultaneous processes sharing one party remain indistinguishable.

No Jira update, push, merge, release creation/publication, shared database mutation, deployment, or installed/live version proof was performed by this work.
