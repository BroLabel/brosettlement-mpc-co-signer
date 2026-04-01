# Contributing to BroSettlement MPC

Thank you for your interest in contributing. This project implements cryptographic MPC infrastructure — **correctness and security take absolute priority over features or performance**.

If you are unsure whether a change is appropriate, open an issue first.

---

## Table of Contents

- [Before You Start](#before-you-start)
- [Development Setup](#development-setup)
- [Making Changes](#making-changes)
- [Pull Request Process](#pull-request-process)
- [Commit Conventions](#commit-conventions)
- [Testing Requirements](#testing-requirements)
- [Code Standards](#code-standards)
- [What We Will Not Merge](#what-we-will-not-merge)
- [DCO — Sign Your Commits](#dco--sign-your-commits)

---

## Before You Start

1. **Search existing issues and PRs** — your idea or bug may already be tracked.
2. **Open an issue** for non-trivial changes before writing code. This avoids wasted effort if the direction isn't aligned.
3. **Security issues** — do not open a public issue. See [SECURITY.md](SECURITY.md).

---

## Development Setup

**Requirements:**
- Go 1.22+
- Docker (for `mpc-co-signer` integration tests)
- `golangci-lint` for linting

```bash
# Clone
git clone https://github.com/brolabel/<repo>.git  # або brosettlement/<repo>
cd <repo>

# Install dependencies
go mod download

# Run tests
go test ./...

# Run linter
golangci-lint run
```

For `mpc-co-signer`, build the Docker image locally:

```bash
docker build -t mpc-co-signer:dev .
```

---

## Making Changes

1. **Fork** the repository and create a branch from `main`:
   ```bash
   git checkout -b fix/describe-the-fix
   ```

2. Branch naming:
   - `fix/` — bug fix
   - `feat/` — new feature
   - `refactor/` — internal refactor with no behavior change
   - `test/` — test-only changes
   - `docs/` — documentation only

3. **Write tests first** for any behavioral change (see [Testing Requirements](#testing-requirements)).

4. Make sure `go test ./...` and `golangci-lint run` pass before opening a PR.

---

## Pull Request Process

1. Open the PR against `main`.
2. Fill in the PR template — describe *what* changed and *why*.
3. Link the related issue (`Closes #123`).
4. A maintainer will review within **5 business days**.
5. All CI checks must pass.
6. At least **one maintainer approval** is required to merge.
7. Maintainers may request changes or close PRs that don't fit the project direction.

**PRs modifying cryptographic logic** (DKG, signing protocol, key encryption, memory zeroing) require review from at least one cryptography-focused maintainer and will take longer — this is intentional.

---

## Commit Conventions

We follow [Conventional Commits](https://www.conventionalcommits.org/):

```
<type>(<scope>): <short description>

[optional body]

[optional footer — DCO sign-off, issue refs]
```

**Types:** `fix`, `feat`, `refactor`, `test`, `docs`, `chore`, `ci`

**Examples:**
```
fix(keygen): zero Share B bytes on SIGTERM before process exit
feat(cosigner): add gRPC health check endpoint
docs(readme): add Docker volume mount example
```

Keep the subject line under 72 characters. Use the body to explain *why*, not *what*.

---

## Testing Requirements

| Change type | Required tests |
|-------------|----------------|
| Bug fix | Regression test that fails before the fix |
| New feature | Unit tests covering the happy path and error cases |
| Cryptographic change | Vector tests against known-good outputs |
| Protocol change | Integration test with both signer and co-signer |

**Minimum coverage**: do not reduce overall test coverage. CI will fail if coverage drops.

For cryptographic code, prefer **test vectors from published standards or reference implementations** over self-generated vectors.

---

## Code Standards

- Standard Go formatting — `gofmt` is enforced in CI.
- `golangci-lint` must pass with the project's `.golangci.yml` config.
- Avoid external dependencies for cryptographic primitives — use the standard library or `golang.org/x/crypto`. New crypto dependencies require explicit justification.
- **Key material handling rules** (enforced in review):
  - Sensitive byte slices must be zeroed with `crypto/subtle` or `golang.org/x/crypto/...` before GC, not just set to `nil`
  - Key material must not be logged, even at debug level
  - Key material must not appear in error messages
- No `panic()` in library code — return errors instead.

---

## What We Will Not Merge

- Changes that reconstruct the full private key on either side
- Logging or persisting key material in any form beyond what is documented
- Dependencies that introduce supply chain risk without strong justification
- Changes that break the 2-of-2 / 2-of-3 noncustodial guarantee
- Large refactors without a prior discussion in an issue
- Generated code committed without the generator script

---

## DCO — Sign Your Commits

This project uses the [Developer Certificate of Origin (DCO)](https://developercertificate.org/) instead of a CLA.

Add a `Signed-off-by` line to every commit:

```bash
git commit -s -m "fix(cosigner): zero key material on shutdown"
# produces: Signed-off-by: Your Name <your@email.com>
```

By signing off, you certify that you have the right to submit the contribution under the Apache 2.0 license. CI will reject commits without a sign-off.

---

## Questions?

Open a [GitHub Discussion](../../discussions) or reach out at **dev@brolabel.io**.
