# BROSET-123: Report Co-Signer Build Identity Through Polling

Date: 2026-10-19

Issue: https://brolabel.atlassian.net/browse/BROSET-123

Status: Implemented across backend, Co-Signer and UI; locally verified on 2026-10-19. No release, shared migration or deployment has been performed. Actual container image/runtime equality remains pending after a local registry metadata fetch stalled. See [verification and release handoff](../verification/2026-10-19-broset-123/README.md) for source revisions, passing evidence and coverage limits.

## Goal

Display the last reported Co-Signer version and source revision alongside its existing presence status in the client and administrative applications. Preserve compatibility with clients that do not report build identity.

## Verified source baseline

These refs were checked against their remote branch heads on 2026-10-19. Recheck them before execution; these are source snapshots, not proof of deployment.

| Repository | Reference | Commit |
| --- | --- | --- |
| `mpc-co-signer` | `origin/main` | `5df5ac25953debeb94f2c37d89a2ad458a73057c` |
| `back-end` | `origin/eks-staging` | `6488cbcde5a07c87963ebc0d43041e83586ce1dd` |
| `front-end` | `origin/eks-staging` | `419336df1b7d5cfa1d27d3c66cffdbbddf3dd323` |

The original local Co-Signer and backend checkouts lag behind these refs. Implement from an appropriate refreshed integration branch, not those stale working files.

Current behavior:

- `cmd/co-signer/build_identity.go` already resolves version and revision; `main.go`, `Dockerfile`, and `Makefile` already supply build identity. The health response already exposes both fields.
- `internal/monolith/discovery.go` polls `GET /api/v1/co-signer/intents/pending`.
- `GetPendingIntentsUseCase.execute()` refreshes presence after listing actionable intents. There is no dedicated heartbeat endpoint.
- `CoSignerInstance` is unique on `(orgId, partyId)`. The party is server-configured; it does not identify an OS process or replica.
- The API-key signature covers the exact request target, including the encoded query string.
- Client MPC status and administrative organization list/detail APIs currently expose `status` and `lastSeenAt`.

## Confirmed product decisions

1. One logical Co-Signer per organization is sufficient for this task. Preserve `(orgId, partyId)` and the existing configured party.
2. Extend the existing polling request. Do not add a heartbeat endpoint, timer, instance registration, or per-process identifier.
3. Tagged release builds report their exact tag version without the leading `v`; branch builds report `X.Y.Z-dev+<12-character SHA>`. Keep the full commit SHA in `revision`.

## Constraints

- Keep the existing polling response body and MPC signing contracts unchanged.
- Keep authentication, scopes, organization isolation, and signing decisions unchanged.
- Build metadata is observational; it is not a compatibility gate or proof of which code is executing.
- Use the same resolved build identity for health and polling. Do not introduce runtime version configuration.
- Published branch builds use `X.Y.Z-dev+<12-character SHA>`; tagged releases use the exact tag version without `v`. The full commit SHA remains in `revision`.
- Accept legacy polls without build metadata for the lifetime of this API contract; removing that support requires a separate compatibility decision.
- Persist and return unknown values as JSON `null`, never the strings `unknown`, `null`, or an empty string.
- Never log raw rejected metadata, request credentials, configuration, or key material.
- Do not merge, release, deploy, or migrate a shared environment as part of preparing the implementation.

## Build identity and GitHub tags

The following rules are the agreed target behavior, not a claim that the current branch publication script already implements development suffixes:

| Build source | Reported `version` | Reported `revision` |
| --- | --- | --- |
| Verified release tag `v2.2.0` | `2.2.0` | Full SHA of the tag's commit |
| Verified candidate tag `v2.2.0-rc.1` | `2.2.0-rc.1` | Full SHA of the tag's commit |
| `main` or `eks-staging` branch publication with `VERSION=2.2.0` | `2.2.0-dev+0123456789ab` | Full SHA of the built commit, starting with `0123456789ab` |

`VERSION` continues to contain only the base `X.Y.Z`. For branch publications, `scripts/check-release-identity.sh` appends `-dev+` and the first 12 lowercase hexadecimal characters of the full `REVISION`. Validate the full revision as a 40-character lowercase SHA before deriving the short value. Tag publications preserve the existing version/tag and branch ownership checks and do not append development metadata.

Classification follows the publication ref. A branch-triggered build remains a development build even if its commit is also referenced by a release tag. A matching base version alone never makes a branch build a release.

The existing workflow passes the resolved identity into the binary and OCI labels, so health, polling, and the image's version label must agree. Keep existing image aliases and publication channels unchanged; no GitHub release is created for a branch publication. The suffix is generated at build time and is not a runtime setting.

The documented general-purpose `make build` path must also produce `X.Y.Z-dev+<12-character SHA>`, adding `.dirty` to build metadata for a modified checkout. Keep the explicit README installation recipe for a verified release tag aligned with that tag. Plain `go build` without injected metadata retains the existing `dev+<12-character SHA>[.dirty]` or `dev` fallback. Manually overriding linker metadata is outside the official build guarantees.

Build metadata after `+` is not a release-order counter. Use the full revision to identify source; do not infer upgrade order from lexical SHA comparisons. The UI displays the complete version, preserving the visible `-dev` or `-rc.N` distinction.

## Polling contract

New clients include the following optional structured query parameters on every discovery poll, including startup/reconciliation calls using the same client:

```http
GET /api/v1/co-signer/intents/pending?revision=0123456789abcdef0123456789abcdef01234567&version=2.1.1
```

Construct the query with `url.Values.Encode()` before invoking existing signing code. For example, the plus in `dev+0123456789ab.dirty` must be encoded as `%2B`. Sign the exact encoded request target. Do not place metadata in a GET body or rely on unsigned headers.

The endpoint's authorization and response stay unchanged. Infer organization from the authenticated context and party from the existing backend configuration. A caller-supplied organization or party must not select the heartbeat row.

Pass an immutable `monolith.BuildIdentity{Version, Revision}` to the client at construction. An optional constructor argument can preserve current test call sites. The production composition root must always pass the resolved identity. Omit revision when the existing build fallback is `unknown`; local `dev` version remains explicit.

## Validation and failure behavior

Normalize the two fields independently at the HTTP boundary. Use the existing `class-validator` pure `isSemVer()` function, not a throwing DTO decorator for this metadata.

| Input | Stored value | Diagnostic |
| --- | --- | --- |
| Missing parameter | `null` | None |
| Version: strict SemVer, at most 128 ASCII characters | Exact version, without adding/removing prefixes | None |
| Version: `dev` or `dev+` followed by 12 lowercase hex characters and optional `.dirty` | Exact value | None |
| Revision: exactly 40 lowercase hex characters | Exact value | None |
| Empty, repeated/array, object, invalid format, whitespace-padded, or overlong input | `null` for that field | Fixed field name only |

For example, `2.1.1`, `2.2.0-rc.1`, `2.2.0-rc.1+build.7`, and `2.2.0-dev+0123456789ab` are accepted. The new branch format is valid SemVer and needs no special backend exception. `v2.1.1`, `2.1`, `latest`, and `02.1.1` are rejected as metadata. Local dirty builds are clearly marked development observations; their SHA does not identify uncommitted edits reproducibly. Official build identity is the deterministic `(version, revision)` pair supplied by the build pipeline with the agreed publication-ref classification.

Within the existing HTTP request-size and request-format limits, invalid metadata does not generate an HTTP 400 or prevent an otherwise authorized poll. Existing transport limits, authentication, discovery, and database failures retain their current behavior. Do not turn a persistence failure into a successful poll.

Count invalid fields in the existing metrics service with `mpc_co_signer_build_metadata_invalid_total{field="version|revision"}`. Missing legacy values are not errors. No raw value, organization, revision, or version is a metric label. Metrics failures must not affect polling.

## Persistence semantics

Add nullable columns to `mpc.co_signer_instances`:

```prisma
version  String? @db.VarChar(128)
revision String? @db.VarChar(40)
```

Keep the existing uniqueness constraint and server-generated heartbeat time. Extend the existing atomic upsert so both create and update write `lastSeenAt`, `version`, and `revision` from the same poll.

This is the last persisted polling snapshot, using the existing last-writer-wins behavior. It is not a version history or chronological reconciliation service. A downgrade is a valid newer observation: never compare SemVer precedence when deciding whether to update.

Explicitly write nulls on legacy or invalid input. Do not omit these update properties, use `undefined`, or use `COALESCE` to preserve stale metadata. For example, `2.1.1 -> legacy client` must become `null`, not remain `2.1.1`.

An offline transition caused by elapsed time does not erase metadata: retain the last observation and its `lastSeenAt`. Two simultaneous processes using the same logical party remain indistinguishable, as in the current system; this task does not claim per-instance reporting.

The migration only adds nullable columns, without a version default or historical backfill.

## Status API

Extend these existing outputs:

1. Client `MpcCoSignerStatusDto` and `GetMpcStatusResult.coSigner`.
2. Administrative `OrganizationCoSignerStatusDto`, including both organization list and detail queries and their status mapper.

```json
{
  "status": "ONLINE",
  "lastSeenAt": "2026-10-19T10:00:00.000Z",
  "version": "2.1.1",
  "revision": "0123456789abcdef0123456789abcdef01234567"
}
```

Both properties are present and nullable in the new API schema. No heartbeat means `OFFLINE` with all three nullable properties set to `null`. A legacy heartbeat may be `ONLINE` with both build properties null.

Select metadata together with timestamps. The administrative batch loader must map complete heartbeat snapshots rather than only `Date` values. Do not add per-organization queries to the list endpoint.

Do not add version properties to the strict pending-intent response: old Co-Signers reject unknown response fields.

## UI

- Client MPC readiness card: show `Version {{version}}` under Co-Signer presence; show `Version unknown` for null or absent fields. Display a short 12-character revision as secondary diagnostic detail when available.
- Client organization setup: show the same version and optional revision beside the existing connection information.
- Admin organization detail: show version and optional short revision in the existing Co-Signer section.
- Admin organization list: include version and revision in the existing status tooltip, without adding a new table column.
- Preserve version when OFFLINE/STALE and retain the existing last-seen copy so it is understood as the last reported value.
- Update client normalization and onboarding snapshot types; do not drop the new fields during projection.
- Add English, Ukrainian, and Russian client translations, and English administrative translations following existing catalogs. Render values as escaped text.
- Missing fields from an older backend render as unknown. Do not introduce update warnings, compatibility decisions, or changes to readiness/initialization rules.

## Verification

1. Go transport: valid release, RC, development plus encoding, revision omission, each polling call, unchanged response decoding, exact request signature, and rejection of a signature after query tampering.
2. Backend parsing: all validation table cases, including repeated parameters; invalid metadata still yields the existing successful discovery response.
3. Persistence: create, metadata update, downgrade, legacy clearing, partial invalid clearing, organization isolation, and offline retention.
4. API serialization: explicit nulls and exact values in client status, admin list, and admin detail; unchanged ONLINE/STALE/OFFLINE thresholds.
5. UI: known version, unknown/absent metadata, development value, and OFFLINE with a last reported version. Assert readiness actions remain unchanged.
6. OpenAPI-derived frontend types, relevant type checks, and existing MPC HTTP contract checks must pass.
7. Before release, verify the new client against an old backend and an old client against the new backend using test instances. Planned tests are not completed evidence.
8. Build identity: verify stable and RC tag equality, branch development suffixes on both branches, unchanged full revisions, and the same identity in binary health/polling and OCI labels. A branch ref at a tagged commit remains a development build. Verify `make build` marks a clean/dirty local build as development.

## Rollout and rollback

1. Deliver additive database migration and backend behavior. Complete the backend rollout before deploying a reporting Co-Signer so old backend writers cannot keep refreshing timestamps without build metadata.
2. Deliver UI with absent-field fallback.
3. Release a new Co-Signer using the existing release process; install the approved build explicitly.
4. Verify a reported version and revision match the installed build and flow through both status APIs into the UI.

For Co-Signer rollback, an older reporting build overwrites the snapshot with its own version, while a legacy build clears it. Keep the additive columns during application rollback. If rolling the backend back to a version unaware of these columns, also roll back or hide the new administrative/client display until reporting support is restored; old writers do not clear previously stored metadata. Do not remove columns as an emergency rollback step.

## Scope boundary

This task does not introduce automatic updates, release discovery, minimum supported versions, build attestation, per-instance presence, version history, or separate heartbeat scheduling. Jira wording should describe optional signed polling query fields as the heartbeat transport when the task is updated.
