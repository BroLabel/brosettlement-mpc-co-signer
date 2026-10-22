# Signing policy validation

This package owns transaction signing rules for supported blockchain protocols.
It does not access storage, execute MPC sessions, or implement RPC transports.

- `protocol.go` explicitly selects the supported protocol. The journal, tuple
  validator, and prepared-byte verifier use the same payload/hash pairing.
- `context.go` decodes the existing closed policy JSON variants. Their wire
  discriminator is unchanged; unknown networks are rejected during validation so
  an authenticated claim can still receive a policy rejection.
- `validate.go` checks common identity bindings and delegates protocol rules.
- `prepared.go` verifies the signed authorization, economic identity, fee cap,
  canonical bytes, and digest returned by the selected protocol validator.
  Protocol-specific fee evidence remains opaque until that validator checks it.
- `ethereum.go` and `ethereum_prepared.go` own Ethereum networks, policy fields,
  addresses, EIP-1559 bytes, fee budgets, and ETH/ERC-20 rules.
- `tron.go` and `tron_prepared.go` own TRON networks, policy fields, protobuf bytes,
  TAPOS shape, freshness margin, bandwidth fees, and TRX/TRC-20 rules.
- `network.go` exposes a chain-independent evidence port. A verified transaction
  may require trusted network membership and set a validity deadline. The
  concrete TRON RPC implementation lives in `../chainrpc/tron`.

The approval gate first verifies the prepared bytes and claim identity, then
rejects any replacement of an existing execution binding. Only then does it
request network evidence for the exact verified bytes and recheck the protocol's
validity deadline. Missing or failed required evidence prevents binding and MPC.
Ethereum does not request TRON evidence. Network requirements are derived locally
from the verified bytes; they are not accepted from the backend.

Authorization wire checks follow the same separation in `../contract/approval`:
`contracts.go` owns the common envelope and derivation identity; `ethereum.go` and
`tron.go` own chain-specific address, asset, numeric, and fee-parameter rules.
The composition root and configuration select concrete providers and endpoints.

The shared producer/hostile corpora validate unchanged wire compatibility and
rejection behavior. Workflow tests cover identity isolation, exact-byte network
reads, conflict rejection before RPC, outages, and expiration during RPC.

For another supported network, extend the explicit network list and add positive
and cross-network rejection tests. For a new protocol, add its validator and an
explicit routing case. Do not accept unknown networks by prefix or fall back to
an existing protocol.
