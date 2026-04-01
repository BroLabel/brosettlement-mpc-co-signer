# Minimal Bootstrap Design

## Goal

Create the smallest possible Go bootstrap for `brosettlement-mpc-co-signer` that uses the external module `github.com/BroLabel/brosettlement-mpc-core`.

## Scope

This first step is intentionally minimal:

- add a root `go.mod`;
- add a root `main.go`;
- wire `github.com/BroLabel/brosettlement-mpc-core` as an external dependency;
- make the project build successfully with no extra service architecture.

Out of scope for this step:

- `cmd/` layout;
- `internal/` packages;
- runtime configuration;
- HTTP/gRPC servers;
- persistence;
- background workers;
- production logging and observability.

## Architecture

The repository will become a single-module Go application with one entrypoint in the project root. `main.go` will be the only executable file and will integrate `brosettlement-mpc-core` in the most conservative way possible.

If the external module exposes a clear public entrypoint that can be called without extra infrastructure, `main.go` will call it. If not, `main.go` will still import the module in a compile-safe way so the bootstrap proves dependency integration without inventing behavior that is not yet designed.

## Files

### `go.mod`

Defines the local module and records the dependency on `github.com/BroLabel/brosettlement-mpc-core`.

### `main.go`

Contains a minimal `main` function. Its responsibility is only to establish a runnable application entrypoint and reference `brosettlement-mpc-core`.

## Data Flow

The bootstrap has no meaningful runtime data flow yet. Control flow is:

1. the binary starts in `main`;
2. the program references `brosettlement-mpc-core`;
3. the process exits successfully unless the chosen public API requires explicit error handling.

## Error Handling

The bootstrap should avoid speculative behavior. If the core package exposes an initialization call that returns an error, `main.go` should fail fast with a clear message. If there is no such stable entrypoint, the bootstrap should avoid fake initialization logic and remain a compile-time integration only.

## Testing

Testing is limited to basic verification that the module resolves and the application builds. This step does not need broad unit-test coverage because there is almost no behavior yet; the main proof is successful dependency wiring and successful build or run.
