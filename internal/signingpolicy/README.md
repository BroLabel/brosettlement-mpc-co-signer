# Signing policy validation

This package owns transaction signing rules for supported blockchain protocols.
It does not perform HTTP requests, access storage, or execute MPC sessions.

- `context.go` decodes the existing closed policy JSON variants without changing
  their wire format. The version field identifies the Ethereum variant; the
  authenticated signing payload type is checked separately during validation.
- `validate.go` checks common identity bindings and selects an explicitly supported
  protocol. Unknown signing payload types are rejected.
- `tron.go` owns TRON networks, policy fields, SHA-256, and accepted address encodings.
- `ethereum.go` owns Ethereum networks and chain IDs, policy fields, Keccak-256,
  EVM address encoding, EIP-1559 fee constraints, and ETH/ERC-20 context rules.

Claim handling and contract-fixture verification both call `ValidateContext`.
The worker calls `ValidateTuple` before executing a signing session. Session
lifecycle, organization/key identity, MPC parties, and derivation-context integrity
remain with their existing callers. These checks validate supplied metadata; they
are not an independent reconstruction of a transaction digest from policy fields.

For another network of a supported protocol, extend that protocol's explicit
network list and add positive and cross-network rejection tests. For a new
protocol, add its rules and an explicit dispatch case, define its closed JSON
variant, and test it through claim decoding and worker admission. Do not accept
unknown networks by prefix or fall back to an existing protocol.
