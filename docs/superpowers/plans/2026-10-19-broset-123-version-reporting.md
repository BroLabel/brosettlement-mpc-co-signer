# BROSET-123 Co-Signer Version Reporting Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [x]`) syntax for tracking.

**Goal:** Distinguish tagged and development Co-Signer builds and carry their identity through authenticated polling into persisted presence, both status APIs, and their existing UI surfaces.

**Architecture:** Preserve one logical Co-Signer per organization and the existing polling heartbeat. Normalize optional signed query metadata without rejecting otherwise valid polls; atomically persist it with the existing timestamp and expose nullable fields through status APIs.

**Tech Stack:** Go, NestJS/TypeScript, Prisma/PostgreSQL, React, Jest, Vitest.

**Spec:** [BROSET-123 design](../specs/2026-10-19-broset-123-version-reporting-design.md).

## Global Constraints

- Keep the existing polling response body and MPC signing contracts unchanged.
- Keep authentication, scopes, organization isolation, and signing decisions unchanged.
- Build metadata is observational; it is not a compatibility gate or proof of which code is executing.
- Use the same resolved build identity for health and polling. Do not introduce runtime version configuration.
- Published branch builds use `X.Y.Z-dev+<12-character SHA>`; tagged releases use the exact tag version without `v`. The full commit SHA remains in `revision`.
- Accept legacy polls without build metadata for the lifetime of this API contract; removing that support requires a separate compatibility decision.
- Persist and return unknown values as JSON `null`, never the strings `unknown`, `null`, or an empty string.
- Never log raw rejected metadata, request credentials, configuration, or key material.
- Do not merge, release, deploy, or migrate a shared environment as part of preparing the implementation.

## Execution workspace and evidence

Repository names below are roots, not nested directories. The design lives in `mpc-co-signer`, but backend and frontend changes belong in their respective repositories. Preserve the user's existing local changes. Recheck source refs from the spec and use isolated worktrees from the selected integration refs before executing application changes. The frontend has separate production and staging branches; verify its intended integration base instead of copying unrelated changes from the original local feature branch.

Deliver three independently reviewable changes: backend, Co-Signer, and frontend. Integration/deployment order is backend, frontend, then Co-Signer installation. Source implementation may prepare the Co-Signer change before the frontend.

Record actual command outcomes and any unavailable integration coverage. All commands and examples below are planned, not execution evidence. Do not bump or publish a release while implementing this plan; release version selection follows the existing promotion process.

## Task 1: Normalize and persist polling metadata in the backend

**Files in `back-end`:**

- Modify `packages/database/prisma/schema.prisma`.
- Create `packages/database/prisma/migrations/20261019000000_add_co_signer_build_identity/migration.sql` after verifying this migration name is unused.
- Create `apps/client-api/src/mpc/co-signer/http/co-signer-build-identity.ts` and `.spec.ts`.
- Modify `apps/client-api/src/mpc/co-signer/http/co-signer.controller.ts` and `.spec.ts`.
- Modify `apps/client-api/src/mpc/co-signer/use-cases/get-pending-intents.use-case.ts` and `.spec.ts`.
- Modify `apps/client-api/src/mpc/infra/co-signer-instance.store.ts` and `.spec.ts`.
- Modify `apps/client-api/src/mpc/observability/mpc-2of3-metrics.service.ts` and `.spec.ts`.

**Interfaces:**

- `normalizeCoSignerBuildIdentity(query: Record<string, unknown>): { version: string | null; revision: string | null; invalidBuildFields: Array<'version' | 'revision'> }`.
- Extend `GetPendingIntentsInput` with optional nullable `version`, optional nullable `revision`, and optional `invalidBuildFields` of the above union array.
- Extend `UpsertCoSignerHeartbeatInput` with required nullable `version` and `revision`.
- Extend `findLatestHeartbeatByOrgAndParty()` result with required nullable `version` and `revision`.
- Add `Mpc2of3Metrics.invalidCoSignerBuildMetadata(field: 'version' | 'revision'): void`.

- [x] **Step 1: Add failing normalization and polling tests.**

Use this parser test as the initial behavior contract:

```ts
describe('normalizeCoSignerBuildIdentity', () => {
  const revision = '0123456789abcdef0123456789abcdef01234567';
  it.each(['2.1.1', '2.2.0-rc.1', '2.2.0+build.7', '2.2.0-dev+0123456789ab', '2.2.0-dev+0123456789ab.dirty', 'dev', 'dev+0123456789ab.dirty'])(
    'accepts supported version %s',
    (version) => {
      expect(normalizeCoSignerBuildIdentity({ version, revision })).toEqual({
        version, revision, invalidBuildFields: [],
      });
    },
  );
  it('accepts a legacy request', () => {
    expect(normalizeCoSignerBuildIdentity({})).toEqual({
      version: null, revision: null, invalidBuildFields: [],
    });
  });
  it.each(['', 'v2.1.1', '02.1.1', ' 2.1.1', 'latest', 'x'.repeat(129), ['2.1.1', '2.1.2'], {}])(
    'rejects metadata without throwing: %j',
    (version) => {
      expect(normalizeCoSignerBuildIdentity({ version, revision })).toEqual({
        version: null, revision, invalidBuildFields: ['version'],
      });
    },
  );
});
```

Add revision cases for `unknown`, an empty value, uppercase SHA, 39/41-character strings, duplicate arrays, and a valid SHA with missing version. In the existing use-case fixture, assert a legacy poll writes both nulls; a new poll writes both values; invalid metadata still returns the same listing. Extend the existing controller tests to assert authenticated `orgId` wins over query input.

Run from `back-end`:

```bash
npm run test -w brosettlement-client-api -- --runInBand co-signer-build-identity co-signer.controller get-pending-intents.use-case co-signer-instance.store
```

Expected first outcome: new behavior assertions fail before implementation. Separate baseline/environment failures from those expected failures.

- [x] **Step 2: Add the nullable schema and migration.**

```prisma
version  String? @db.VarChar(128)
revision String? @db.VarChar(40)
```

```sql
ALTER TABLE "mpc"."co_signer_instances"
  ADD COLUMN "version" VARCHAR(128),
  ADD COLUMN "revision" VARCHAR(40);
```

Build generated Prisma types locally with `npm run build -w @brosettlement/database`. Apply migration only to a disposable test database during this task.

- [x] **Step 3: Implement the pure parser and non-throwing HTTP boundary.**

```ts
import { isSemVer } from 'class-validator';

export function normalizeCoSignerBuildIdentity(query: Record<string, unknown>): {
  version: string | null;
  revision: string | null;
  invalidBuildFields: Array<'version' | 'revision'>;
} {
  const rawVersion = query.version;
  const rawRevision = query.revision;
  const version = typeof rawVersion === 'string' && rawVersion.length <= 128 &&
    /^[\x21-\x7e]+$/.test(rawVersion) &&
    (isSemVer(rawVersion) || /^dev(?:\+[0-9a-f]{12}(?:\.dirty)?)?$/.test(rawVersion))
    ? rawVersion : null;
  const revision = typeof rawRevision === 'string' && /^[0-9a-f]{40}$/.test(rawRevision)
    ? rawRevision : null;
  const invalidBuildFields: Array<'version' | 'revision'> = [];
  if (rawVersion !== undefined && version === null) invalidBuildFields.push('version');
  if (rawRevision !== undefined && revision === null) invalidBuildFields.push('revision');
  return { version, revision, invalidBuildFields };
}
```

Bind `@Query() query: Record<string, unknown> = {}` on `getPendingIntents()`. Do not introduce a string/array-coercing DTO or throwing validation decorators. Add optional `ApiQuery` documentation for both fields. Pass only `{ orgId: this.requireOrgId(), ...normalizeCoSignerBuildIdentity(query) }` to the use case. Confirm repeated query keys arrive as arrays in a real Fastify/Nest request test rather than relying only on direct method calls.

- [x] **Step 4: Extend the existing upsert and read projection.**

At the existing heartbeat call, pass:

```ts
await this.instanceStore.upsertHeartbeat({
  orgId: input.orgId,
  partyId: this.coSignerConfig.partyId,
  lastSeenAt: now,
  version: input.version ?? null,
  revision: input.revision ?? null,
});
```

Add those exact nullable properties to both Prisma `create` and `update`, and both `select` and the read return type. Keep one atomic upsert and the existing error propagation. Test create, changed revision, downgrade, null clearing, partial invalid metadata, and a second organization's independent row. Do not select the highest SemVer or preserve a field with `undefined` on update.

- [x] **Step 5: Add bounded diagnostics.**

Register `Counter<'field'>` named `mpc_co_signer_build_metadata_invalid_total` in the existing metrics service. Its only label values are `version` and `revision`. Implement the public method with the existing `runSafely()` wrapper and increment once for each `input.invalidBuildFields ?? []` during polling. Extend `createMetrics()` in use-case tests with the new mocked method. Assert missing input does not count, invalid input counts, and metrics exceptions do not stop polling. Do not log raw metadata.

- [x] **Step 6: Verify persistence against a disposable PostgreSQL database.**

Use the repository's existing disposable database pattern in `apps/client-api/scripts/test-mpc-mailbox-postgres.mjs` when constructing a focused heartbeat suite; do not point the suite at a shared database. Exercise the real migration and `CoSignerInstanceStore` with successive writes: valid -> newer -> lower version -> null. Read after each write and assert timestamp and both metadata fields belong to the same poll, then verify another organization remains unchanged. Confirm an old insert that omits the new columns succeeds with both columns null. Record this evidence separately from mocked unit tests.

- [x] **Step 7: Run the targeted tests and commit the independently functional backend ingestion change.**

```bash
npm run test -w brosettlement-client-api -- --runInBand co-signer-build-identity co-signer.controller get-pending-intents.use-case co-signer-instance.store mpc-2of3-metrics.service
npm run verify:mpc-co-signer-http-contract
```

Commit message: `feat(mpc): persist co-signer build identity from signed polling`.

## Task 2: Expose nullable metadata through both status APIs

**Files in `back-end`:**

- Modify `apps/client-api/src/mpc/application/get-mpc-status.use-case.ts` and `.spec.ts`.
- Modify `apps/client-api/src/mpc/api/dto/mpc-status.response.dto.ts`.
- Modify `apps/admin-api/src/organizations/dto/organization-co-signer-status.dto.ts`.
- Modify `apps/admin-api/src/organizations/mpc/mpc-co-signer-status.ts` and `.spec.ts`.
- Modify `apps/admin-api/src/organizations/organizations.service.ts` and `.spec.ts`.

**Interfaces:** Both serialized `coSigner` objects gain `version: string | null` and `revision: string | null`. Task 1's store result is the client input. Administrative queries read the same persisted columns directly.

- [x] **Step 1: Add failing serialization tests for client status, administrative list, and administrative detail.**

In the existing heartbeat fixtures, supply version `2.1.1` and a 40-character revision, then assert the complete nested response contains them. Add these expected missing and offline cases:

```ts
expect(result.coSigner).toEqual({
  status: 'OFFLINE', lastSeenAt: null, version: null, revision: null,
});
expect(offlineResult.coSigner).toMatchObject({
  status: 'OFFLINE', version: '2.1.1', revision,
});
```

Assert legacy ONLINE rows expose explicit null fields and JSON serialization retains them. Retain existing threshold boundary tests and administrative query-count expectations.

- [x] **Step 2: Add metadata to both Swagger DTOs and response mappers.**

Use `@ApiProperty({ type: String, nullable: true })` for each field. Extend client `GetMpcStatusResult.coSigner` and compose the existing status mapping with `heartbeat?.version ?? null` and `heartbeat?.revision ?? null`.

For admin, define a heartbeat snapshot input containing `lastSeenAt: Date`, `version: string | null`, and `revision: string | null`. Change `mapCoSignerStatus()` to accept that snapshot or null, keeping thresholds unchanged. Change `loadCoSignerHeartbeats()` to return a `Map<string, Snapshot>` and select all three fields in the batch query. Pass full rows in detail and list mapping. Update every existing mapper caller/test; do not perform an extra database lookup for version.

- [x] **Step 3: Verify and commit.**

```bash
npm run test -w brosettlement-client-api -- --runInBand get-mpc-status.use-case
npm run test -w brosettlement-admin-api -- --runInBand mpc-co-signer-status organizations.service
npm run typecheck:client
npm run typecheck:admin
```

Commit message: `feat(api): expose co-signer version in presence responses`.

## Task 3: Distinguish development builds and send identity on every Co-Signer poll

**Files in `mpc-co-signer`:**

- Modify `internal/monolith/client.go`.
- Modify `internal/monolith/discovery.go` and `internal/monolith/discovery_test.go`.
- Modify `cmd/co-signer/resources.go`.
- Modify `scripts/check-release-identity.sh` and `scripts/check-release-identity.test.sh`.
- Modify `Makefile` so general-purpose local builds carry the development suffix and optional dirty marker.
- Modify `README.md` and `docs/release-channels.md` to document version sources, reported metadata, and legacy compatibility.
- Extend `cmd/co-signer/build_identity_test.go` to assert injected development identities are preserved.
- Verify `.github/workflows/docker-publish.yml` and `Dockerfile` consume the single resolved version/revision for both the binary and OCI labels; modify them only if the verified flow requires it.

**Interfaces:** Add `monolith.BuildIdentity { Version string; Revision string }` and an optional variadic `identity ...BuildIdentity` constructor parameter. Existing four-argument call sites remain valid; the production composition root passes exactly one identity. Store the identity by value in the client.

### Build identity subtask

- [x] **Step A: Add failing release identity assertions.**

Extend the existing shell test immediately after each corresponding `check()` call:

```bash
# refs/heads/main
expect_line "$output" "version=3.4.5-dev+${production:0:12}"
expect_line "$output" "revision=$production"
# refs/heads/eks-staging
expect_line "$output" "version=3.4.5-dev+${staging:0:12}"
expect_line "$output" "revision=$staging"
# refs/tags/v3.4.5
expect_line "$output" 'version=3.4.5'
expect_line "$output" "revision=$production"
```

Retain the existing RC expectation `version=3.4.5-rc.1`. Add a real tag at the production fixture commit, then assert a subsequent main-branch check at that same commit still returns a development version. Add rejection cases for malformed full revisions on branch inputs. Run `./scripts/check-release-identity.test.sh` and observe the new branch-version assertions fail before implementation.

- [x] **Step B: Resolve development versions in the existing publication script.**

Validate `REVISION` with `^[0-9a-f]{40}$` before the publication case statement. Change only the branch cases to:

```bash
refs/heads/main)
  tags="$IMAGE:sha-$REVISION"
  version="$version-dev+${REVISION:0:12}"
  ;;
refs/heads/eks-staging)
  tags="$IMAGE:staging,$IMAGE:sha-$REVISION"
  version="$version-dev+${REVISION:0:12}"
  ;;
```

Keep the tagged cases, existing branch ownership checks, full revision output, image aliases, and release publication rules. Do not derive branch identity with `git describe` or the highest tag; the trigger ref determines the build class. The workflow already forwards resolved outputs to Docker build arguments and OCI labels, so reuse that path rather than constructing another version value in YAML.

- [x] **Step C: Align the local build entry point and documentation.**

Update the Makefile's identity variables so `make build` is explicitly a development build. Resolve `REVISION` before the version expression and use the same first 12 characters, not a potentially longer unique abbreviation:

```make
BASE_VERSION := $(shell cat VERSION)
REVISION := $(shell git rev-parse --verify HEAD)
SHORT_REVISION := $(shell printf '%s' '$(REVISION)' | cut -c1-12)
DIRTY_SUFFIX := $(shell test -z "$$(git status --porcelain)" || printf '.dirty')
VERSION := $(BASE_VERSION)-dev+$(SHORT_REVISION)$(DIRTY_SUFFIX)
```

Retain the existing linker flags. Verify the generated build command for a clean and a modified disposable Git fixture, and add a focused shell regression test for these two cases if the current Makefile test tooling does not cover them. Run that test in CI alongside the existing release-boundary shell tests. Keep ordinary unconfigured `go build` fallbacks unchanged. Preserve the README's explicit verified-release-tag installation recipe; document `make build` as a development build. Add the three-row stable/RC/branch mapping from the spec to `docs/release-channels.md`.

In `build_identity_test.go`, add an assertion that `resolveBuildIdentity("2.2.0-dev+0123456789ab", fullRevision, "checkout-revision", false)` retains the injected version and full revision. Verify the shell tests and build identity tests pass, then commit: `fix(build): distinguish branch builds from tagged releases`.

### Polling transport subtask

- [x] **Step 1: Add a failing transport test using the existing signature helper.**

Create a test client with the optional identity, return the existing listing fixture from an HTTP test server, and make two `ListActionableIntents()` calls. On each request assert:

```go
if got := r.URL.Query().Get("version"); got != "dev+0123456789ab.dirty" {
    t.Errorf("version = %q", got)
}
if !strings.Contains(r.URL.RawQuery, "%2B") {
    t.Error("development version plus was not URL encoded")
}
if !verifyRequestSignature(t, r, publicKey, "") {
    t.Error("query is not covered by the request signature")
}
tampered := r.Clone(r.Context())
tamperedURL := *r.URL
tampered.URL = &tamperedURL
q := tampered.URL.Query()
q.Set("version", "9.9.9")
tampered.URL.RawQuery = q.Encode()
if verifyRequestSignature(t, tampered, publicKey, "") {
    t.Error("signature unexpectedly accepts changed metadata")
}
```

Obtain the key using the existing `newTestClient()` helper, or extend that helper to forward optional identity values without changing existing callers. Table-test stable, RC, branch `2.2.0-dev+0123456789ab`, local development, no identity, and revision `unknown`. Keep strict response fixture assertions.

- [x] **Step 2: Build the encoded request target before existing request signing.**

```go
type BuildIdentity struct {
    Version string
    Revision string
}

func (c *Client) discoveryPath() string {
    const path = "/api/v1/co-signer/intents/pending"
    q := url.Values{}
    if c.buildIdentity.Version != "" {
        q.Set("version", c.buildIdentity.Version)
    }
    if c.buildIdentity.Revision != "" && c.buildIdentity.Revision != "unknown" {
        q.Set("revision", c.buildIdentity.Revision)
    }
    if len(q) == 0 {
        return path
    }
    return path + "?" + q.Encode()
}
```

Use this path in `ListActionableIntents()`. Add the `net/url` import where this method lives. Populate the client copy from the optional constructor identity. In `resources.go`, use `monolith.New(cfg.MonolithURL, cfg.APIKeyID, privateKey, cfg.HTTPTimeout, monolith.BuildIdentity{Version: version, Revision: revision})`. Do not add identity to listing responses or unrelated signing requests.

- [x] **Step 3: Verify and commit.**

```bash
GOWORK=off go test ./internal/monolith ./cmd/co-signer ./internal/health
GOWORK=off go test ./...
./scripts/check-release-identity.test.sh
```

Commit message: `feat(co-signer): report build identity in signed polling`.

## Task 4: Display the reported identity in existing client and admin UI

**Files in `front-end`:**

- Regenerate `apps/client/src/types/generated-types.ts` and `apps/admin/src/types/generated-types.ts`.
- Modify `apps/client/src/pages/mpc/components/mpc-readiness-utils.ts` and `.test.ts`.
- Modify `apps/client/src/pages/mpc/components/mpc-readiness-card.tsx` and `.test.tsx`.
- Modify `apps/client/src/pages/organization-setup/onboarding-derive.ts` and affected `.test.ts` fixtures.
- Modify `apps/client/src/pages/organization-setup/components/setup-step-cosigner.tsx`.
- Modify `apps/admin/src/pages/organizations/components/organization-co-signer-status.tsx`; add its focused `.test.tsx`.
- Modify client catalogs `apps/client/src/lib/i18n/langs/{en,uk,ru}.json` and admin catalog `apps/admin/src/lib/i18n/langs/en.json`.

**Interfaces:** Consume the new API fields as nullable values. UI normalization must also accept absent fields from old APIs. Extend `MpcCoSignerHealth` with required nullable `version` and `revision`; retain its existing status and timestamp fields and update fixtures accordingly.

- [x] **Step 1: Add failing behavioral UI tests.**

Use the existing `statusData()`/`renderCard()` fixture in the readiness card tests. For ONLINE `2.1.1`, assert `Version 2.1.1`; for null/absent metadata, assert `Version unknown`. For OFFLINE with version `2.1.1`, assert the version is retained. For branch version `2.2.0-dev+0123456789ab`, assert the full version including `-dev` is rendered as text; retain coverage for the local `dev+...` form. Assert existing initialization eligibility is unchanged by version.

For example, after extending the test fixture type:

```tsx
renderCard({
  data: statusData({ coSigner: {
    status: 'OFFLINE', lastSeenAt: '2026-10-19T10:00:00.000Z',
    version: '2.1.1', revision: '0123456789abcdef0123456789abcdef01234567',
  }}),
  isError: false, isLoading: false,
});
expect(screen.getByText('Version 2.1.1')).toBeTruthy();
expect(screen.getByText('0123456789ab')).toBeTruthy();
```

Add equivalent known/unknown tests for the admin section and tooltip. Extend normalization tests to cover explicit null and missing fields without changing health classification.

- [x] **Step 2: Regenerate types from local APIs built with Task 2.**

The current generation scripts use local client Swagger on port 8080 and admin Swagger on port 7070:

```bash
npm run generate-types:client
npm run generate-types:admin
```

Use isolated local API configuration, not shared runtime dependencies. If those services cannot run, export the same Swagger schemas from the changed backend's local application/test setup and pass the exported files to the installed `openapi-typescript` tool. Do not regenerate from a deployed old API or manually pretend generated types came from the changed schema. Inspect generated diffs for unrelated API drift.

- [x] **Step 3: Extend normalization and render the fields.**

Initialize unknown fields to null and normalize them explicitly:

```ts
version: typeof candidate.version === 'string' && candidate.version.length > 0 ? candidate.version : null,
revision: typeof candidate.revision === 'string' && candidate.revision.length > 0 ? candidate.revision : null,
```

Use existing translations for presence; add version copy separately. Display `revision.slice(0, 12)` only when present. Reuse this rendering pattern in client readiness/setup and admin detail/tooltip:

```tsx
<p className="text-xs text-muted-foreground">
  {coSigner.version
    ? t('mpc.readiness.coSigner.version', 'Version {{version}}', { version: coSigner.version })
    : t('mpc.readiness.coSigner.versionUnknown', 'Version unknown')}
</p>
{coSigner.revision && (
  <p className="text-xs text-muted-foreground">
    {t('mpc.readiness.coSigner.revision', 'Revision')}: <code>{coSigner.revision.slice(0, 12)}</code>
  </p>
)}
```

Use each application's existing translation namespace. Client English copy is `Version {{version}}`, `Version unknown`, and `Revision`; Ukrainian is `Версія {{version}}`, `Версія невідома`, and `Коміт`; Russian is `Версия {{version}}`, `Версия неизвестна`, and `Коммит`. Add these strings to all three client catalogs and English strings to the admin catalog. This task specifically includes localization of UI copy.

- [x] **Step 4: Verify tests, types, and visual placement; commit.**

```bash
npm run test -w @brosettlement/client -- src/pages/mpc/components/mpc-readiness-utils.test.ts src/pages/mpc/components/mpc-readiness-card.test.tsx src/pages/organization-setup/onboarding-derive.test.ts
npm run test -w @brosettlement/admin -- src/pages/organizations/components/organization-co-signer-status.test.tsx
npm run typecheck
```

Check client readiness/setup and admin detail/list with local known/unknown/offline fixtures at desktop and narrow widths. Verify long SemVer metadata wraps without covering the status badge. Record visual verification separately from automated tests.

Commit message: `feat(ui): display reported co-signer version and revision`.

## Task 5: Review integration and document release handoff

- [x] Verify the backend still returns the exact pending-intent response fixture and that signature verification covers encoded query parameters end to end.
- [ ] Verify tagged builds retain the exact GitHub tag version without `v`, branch builds retain their `-dev+<12-character SHA>` suffix, and full `revision` agrees with the build source. Compare resolved build output, OCI labels, and the identity used by health and polling.
- [x] Verify an old client can poll the new backend, overwriting a previous metadata observation with null while presence stays ONLINE.
- [x] Verify a new client can poll an old backend: extra query fields must be ignored and exact signature validation must still succeed. Preserve errors if that test exposes a compatibility issue; do not weaken signing validation.
- [x] Verify both status APIs and UI display a lower version after a reporting client rollback, and unknown after a legacy client rollback.
- [x] Review the final diffs for unintended changes to MPC admission, key operations, API credentials, release publishing, and other user work.
- [x] Report which tests actually passed and any remaining coverage limitations. Keep migration, source review, release publication, deployment, and live version readback as separate evidence.
- [x] Prepare the backend -> UI -> Co-Signer installation handoff using the design's rollout/rollback section. Updating Jira, publishing releases, merging, or deploying remains a separate requested action.

Build-identity status: tag/branch/local resolver and health/polling tests passed; OCI label/build-argument wiring was source-reviewed. The actual Dockerfile build stalled fetching base-image registry metadata and was canceled. Image/runtime label equality remains pending before release. Other completed boxes refer to the local and component evidence in [verification and handoff](../verification/2026-10-19-broset-123/README.md), not deployment proof.

## Plan self-review

The plan covers tagged/development build classification and identity reuse (Task 3), signed reporting (Tasks 1/3), tolerant validation and legacy behavior (Task 1), persistence (Task 1), API serialization (Task 2), UI unknown/offline/development behavior (Task 4), and cross-version integration (Task 5). No new endpoint or per-instance model is introduced. The parser, input, store, API, and UI field names are consistently `version` and `revision`.
