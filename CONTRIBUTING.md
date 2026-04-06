# Contributing to BroSettlement MPC Co-Signer

Thank you for your interest in contributing. This project implements cryptographic MPC infrastructure, so correctness and security take priority over features or performance.

If you are unsure whether a change is appropriate, open an issue first.

## Before You Start

1. Search existing issues and pull requests. Your idea or bug may already be tracked.
2. Open an issue for non-trivial changes before writing code. This helps avoid wasted effort if the direction is not aligned.
3. Do not open public issues for security problems. See [SECURITY.md](SECURITY.md).

## Development Setup

Requirements:

- Go 1.24+
- Docker for local integration work
- `golangci-lint` for linting

```bash
git clone https://github.com/BroLabel/brosettlement-mpc-co-signer.git
cd brosettlement-mpc-co-signer

go mod download
go test ./...
golangci-lint run
```

If your change needs a local container image, build it with:

```bash
docker build -t mpc-co-signer:dev .
```

## Making Changes

1. Fork the repository and create a branch from `main`.

```bash
git checkout -b fix/describe-the-fix
```

2. Use a descriptive branch prefix:

- `fix/` for bug fixes
- `feat/` for new features
- `refactor/` for internal refactors without behavior change
- `test/` for test-only changes
- `docs/` for documentation-only changes

3. Write tests first for any behavioral change.
4. Make sure `go test ./...` and `golangci-lint run` pass before opening a PR.

## Pull Request Process

1. Open the PR against `main`.
2. Describe what changed and why.
3. Link the related issue when there is one.
4. Make sure all CI checks pass.
5. Expect review feedback before merge.

Changes that touch cryptographic logic, key handling, or signing flows may require deeper review and can take longer to approve.

## Commit Conventions

We follow [Conventional Commits](https://www.conventionalcommits.org/):

```text
<type>(<scope>): <short description>
```

Common types: `fix`, `feat`, `refactor`, `test`, `docs`, `chore`, `ci`

Examples:

```text
fix(keygen): zero share bytes on shutdown
feat(cosigner): add health check endpoint
docs(readme): add local run example
```

Keep the subject line under 72 characters. Use the body to explain why the change is needed.

## Testing Requirements

| Change type | Expected tests |
|-------------|----------------|
| Bug fix | Regression test that fails before the fix |
| New feature | Unit tests for the happy path and error cases |
| Protocol change | Integration coverage for both sides of the flow |
| Cryptographic change | Deterministic checks or known-good vectors when applicable |

Do not reduce confidence in touched code. If a change affects security-sensitive behavior, add or strengthen tests before asking for review.

## Code Standards

- Use standard Go formatting. Run `gofmt` on changed files.
- Make sure `golangci-lint` passes with the repository configuration.
- Avoid new cryptographic dependencies unless they are clearly justified.
- Do not log key material, even at debug level.
- Do not include secrets or key material in error messages.
- Prefer returning errors instead of calling `panic()` in library code.

## What We Will Not Merge

- Changes that reconstruct the full private key on either side
- Logging or persisting key material beyond the documented design
- Large refactors without prior discussion
- Generated code without the generator source or command

## DCO Sign-Off

This project uses the [Developer Certificate of Origin](https://developercertificate.org/).

Add a `Signed-off-by` line to every commit:

```bash
git commit -s -m "fix(cosigner): zero key material on shutdown"
```

## Questions

Open an issue or start a discussion in the repository if you need clarification before contributing.
