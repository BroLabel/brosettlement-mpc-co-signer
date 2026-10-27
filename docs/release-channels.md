# Co-Signer release channels

Co-Signer and the backend must use compatible MPC contracts. The production
channel advances only after compatibility with the production backend is
confirmed. A successful staging deployment alone is not production approval.

| Publication | Source | Container tags | GitHub release |
| --- | --- | --- | --- |
| Development on main | `main` | `sha-<commit>` | None |
| Staging development | `eks-staging` | `staging`, `sha-<commit>` | None |
| Staging candidate | `vX.Y.Z-rc.N` at the current `eks-staging` tip | Exact version | Pre-release, never Latest |
| Production release | `vX.Y.Z` at the current `main` tip | Exact version, then `latest` | Stable, Latest |

`VERSION` contains the base version (`X.Y.Z`). RC numbering starts at 1.
The tagged RC suffix is included in the container binary version. Versions and
Git tags are never reused for different code. Stable releases must advance from
the previous production version; rollback uses an existing digest explicitly,
then a new corrective release if clients need a new default.

## Existing releases

`v2.2.0` is the stable release published on 27 October 2026. Historical tags,
including `v2.0.2`, `v2.1.0` and `v2.1.1`, remain occupied and must not be moved
or reused. Before starting the next candidate/stable cycle, bump `VERSION`
above the published stable version, for example to `2.2.1`.

Clients resolve GitHub Latest once, then install the exact returned tag or image
digest. Staging clients explicitly choose their approved preview. Do not select
the highest Git tag or assume the default branch is the supported release.

## Publish a candidate

1. Merge the change and base `VERSION` bump into `eks-staging`.
2. Fetch the current branch and create a fresh RC tag at its tip:

   ```bash
   git fetch origin eks-staging
   git tag v2.2.1-rc.1 origin/eks-staging
   git push origin v2.2.1-rc.1
   ```

3. The publish workflow runs tests, checks tag/branch identity, refuses an
   existing versioned container, builds both architectures, and creates a
   GitHub pre-release with the image digest. Verify the completed workflow and
   release before applying the image to staging.

## Promote to production

1. Confirm compatibility with the deployed or coordinated production backend.
   Merge the reviewed changes and matching base `VERSION` into `main`.
2. Tag the current `main` tip with the stable version and push the tag. A stable
   tag on staging-only code is rejected. Do not manually create the release;
   the workflow creates it after the image is published.
3. Verify the stable release, GitHub Latest and Docker `latest`. The workflow
   promotes the exact built digest to `latest`; ordinary branch pushes do not.
4. Update installations deliberately using the versioned image or digest,
   preserving share volumes and encryption configuration. Publication itself
   does not deploy or restart any service.

## Failed publication

Publication is serialized per ref; stable releases share a separate queue.
An existing image blocks a rebuild,
including a rerun; network or authentication errors also block publication.
If an image was pushed but GitHub release creation or `latest` promotion failed,
verify its digest and source revision from the original workflow, then finish
only the missing release metadata or alias promotion. Never delete the image or
move its Git tag to make a rerun pass. A source correction gets a new version.

Branch checks are refreshed at publication time. Fetch only the source required
by the selected channel: stable/main uses `main`, while staging and RC use
`eks-staging`. A missing staging branch cannot block a stable publication; an RC
still requires its own staging source. Older delayed tag runs are
rejected once the corresponding branch has advanced. Protect release tags and
branches with repository access controls; a workflow cannot prevent an admin
from force-moving a tag or rerunning a historical workflow that predates these
rules.
