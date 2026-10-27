# Approval review follow-up — 2026-10-20

## Changes and review

- A valid saved APPROVED receipt without a delivery ACK is a pending authorization condition. The owned SIGN waits within its existing deadline while the existing dispatcher recovers delivery. MPC still requires a locally durable APPLIED ACK and exact execution binding. Missing/corrupt admission or receipt, conflicting evidence and missing ACK after binding remain fail-closed integrity errors.
- HTTP 400 with `INVALID_CURSOR` restarts authenticated pending discovery on the next scheduled step. Other transport/authentication failures preserve cursor position. Saved receipts and retry hints survive the restart; no organization reevaluation or replacement decision is introduced.
- The paired backend serializes admin withdrawal holds with SIGN creation and unifies safe persisted execution failure projection.

Repeat review covered owned-session deadlines, no-MPC-before-ACK ordering, durable receipt recovery, missing-history handling, typed HTTP errors, cursor retry boundaries, organization isolation and dependency direction. No remaining actionable findings in this follow-up. The main agent performed the review without subagents or an outside reviewer.

## Validation

Executed from the existing Co-Signer review copy over source `31a474d02082c7c6e1eb2ce50e87b014c752923c`, using Go 1.24.0 on macOS arm64:

- Full `go test -race -p 2 ./... -count=1`: all 19 test packages passed; one command package has no tests.
- `go build ./...`: passed.
- `go run ./cmd/mpc-contracts verify`: all four pinned bundles passed; no bundle or corpus changes.
- `gofmt` and whitespace checks: passed.

New regressions failed before the fixes: delayed ACK ended SIGN before Core admission, a missing ACK produced INTERNAL_ERROR instead of waiting to the deadline, and an invalid cursor never restarted discovery. Final tests cover those cases, non-reset on ordinary outage, no duplicate saved decision after a cursor reset, HTTP error classification and the existing hostile-evidence checks. The full suite also caught a test-package import cycle; the pending error was moved into the existing signing-policy package before the final successful run.

This is local runtime/HTTP-fixture and race-test evidence. Coordinated MPC/Anvil settlement, live organization endpoints and Linux filesystem behavior were not rerun. No merge, release publication or deployment is included. Hosted CI is reported separately for the pushed commit.
