# HD Intent Contract Design

## Goal

Define the strict monolith intent payload contract that lets `mpc-co-signer` drive the
HD-wallet-aware `brosettlement-mpc-core` API without legacy defaults, fallback behavior, or
implicit derivation material.

The monolith remains the owner of intent creation. The co-signer validates the HTTP payload before
starting local MPC session machinery, maps valid payloads into the core public API, and reports
contract failures as `FAILED / INVALID_INTENT`.

---

## Context

The local `brosettlement-mpc-core` branch now requires HD derivation inputs:

- ECDSA DKG requires `tss.DKGDerivationMaterial` containing an upstream-supplied chain code and
  derivation scheme.
- SIGN requires `tss.DerivationContext` so core can normalize the derivation context, compute the
  derivation context hash, bind that hash into protocol frames, and derive the child signing key.

The current co-signer payload model already passes `chain` through `coretss.SessionDescriptor`, but
does not carry `chainCode`, `derivationScheme`, or `derivationContext`. With the local core API,
that means DKG fails with `ErrChainCodeMissing` and SIGN fails with
`ErrDerivationContextRequired`.

---

## Contract

### Shared intent payload

`internal/monolith.IntentPayload` is extended with these fields:

```go
type IntentPayload struct {
    KeyID             string             `json:"keyId"`
    Parties           []string           `json:"parties"`
    Threshold         uint32             `json:"threshold"`
    Algorithm         string             `json:"algorithm"`
    Curve             string             `json:"curve"`
    Chain             string             `json:"chain"`
    Digest            []byte             `json:"digest"`
    ChainCode         string             `json:"chainCode,omitempty"`
    DerivationScheme  string             `json:"derivationScheme,omitempty"`
    DerivationContext *DerivationContext `json:"derivationContext,omitempty"`
}
```

`chainCode` and top-level `derivationScheme` are DKG material fields. They are not SIGN fields.
`derivationContext.scheme` is the SIGN derivation scheme field.

### DKG payload

For DKG, the payload must include explicit HD derivation material:

```json
{
  "keyId": "key-1",
  "parties": ["p1", "p2"],
  "threshold": 2,
  "algorithm": "ECDSA",
  "curve": "secp256k1",
  "chain": "ethereum",
  "chainCode": "1111111111111111111111111111111111111111111111111111111111111111",
  "derivationScheme": "bip32_secp256k1"
}
```

Rules:

- `chainCode` is required for DKG.
- `chainCode` must be lowercase hex, exactly 64 characters, matching `^[0-9a-f]{64}$`.
- `derivationScheme` is required for DKG.
- The only supported `derivationScheme` at launch is `bip32_secp256k1`.
- The co-signer does not default `derivationScheme` when `chainCode` is present.
- The co-signer does not generate, rewrite, or normalize chain code.

This design treats all DKG intents as HD-aware. The current supported runtime scope remains
ECDSA/secp256k1; unsupported algorithm/curve combinations continue to fail validation before core.

### SIGN payload

For SIGN, the payload must include a derivation context in the shape expected by the public core
facade:

```json
{
  "keyId": "key-1",
  "parties": ["p1", "p2"],
  "threshold": 2,
  "algorithm": "ECDSA",
  "curve": "secp256k1",
  "chain": "ethereum",
  "digest": "base64-encoded-digest",
  "derivationContext": {
    "profileId": "profile-1",
    "chain": "ethereum",
    "algorithm": "ecdsa",
    "curve": "secp256k1",
    "scheme": "bip32_secp256k1",
    "accountPath": "m/44'/60'/0'",
    "childPath": "/0/15",
    "fullPath": "m/44'/60'/0'/0/15",
    "addressEncoding": "",
    "expectedAddress": "",
    "derivedPublicKey": "",
    "descriptor": "",
    "descriptorVersion": 7,
    "profileVersion": 3
  }
}
```

Rules:

- `derivationContext` is required for SIGN.
- A non-empty top-level `chainCode` in SIGN payload is `INVALID_INTENT`.
- A non-empty top-level `derivationScheme` in SIGN payload is `INVALID_INTENT`.
- SIGN derives scheme from `derivationContext.scheme`; top-level `derivationScheme` belongs only
  to DKG material.
- Empty top-level `chainCode` or `derivationScheme` strings do not fail the SIGN-specific
  top-level-field check, but SIGN still requires a valid `derivationContext`.
- SIGN never receives DKG chain code from the monolith and never passes chain code to core.

---

## Worker Data Flow

The session worker keeps contract failures at the co-signer boundary:

```text
ClaimIntent
  -> validateIntent / mapping check
  -> create FrameContext
  -> create and start HTTPTransport
  -> call core RunDKGSession or RunSignSession
  -> PostResult
```

Validation runs after a successful claim because only claimed intents should receive terminal
results from this worker. Validation runs before `FrameContext`, `HTTPTransport`, and core calls so
contract failures have no MPC session side effects.

All pre-core validation failures post:

```json
{
  "status": "FAILED",
  "errorCode": "INVALID_INTENT",
  "errorMessage": "..."
}
```

---

## Core Mapping

### DKG

`buildDKGRequest` maps monolith DKG material into `coretss.DKGDerivationMaterial`:

```go
func buildDKGRequest(intent monolith.Intent, localPartyID string, tr coretss.Transport) coretss.DKGSessionRequest {
    return coretss.DKGSessionRequest{
        Session: coretss.SessionDescriptor{
            SessionID: intent.SessionID,
            KeyID:     intent.Payload.KeyID,
            Parties:   intent.Payload.Parties,
            Threshold: intent.Payload.Threshold,
            Algorithm: intent.Payload.Algorithm,
            Curve:     intent.Payload.Curve,
            Chain:     intent.Payload.Chain,
        },
        LocalPartyID: localPartyID,
        DerivationMaterial: &coretss.DKGDerivationMaterial{
            ChainCode:        intent.Payload.ChainCode,
            DerivationScheme: intent.Payload.DerivationScheme,
        },
        Transport: tr,
    }
}
```

### SIGN

`buildSignRequest` maps `payload.derivationContext` into `coretss.DerivationContext`:

```go
func buildSignRequest(intent monolith.Intent, localPartyID string, tr coretss.Transport) coretss.SignSessionRequest {
    ctx := intent.Payload.DerivationContext
    return coretss.SignSessionRequest{
        Session: coretss.SessionDescriptor{
            SessionID: intent.SessionID,
            KeyID:     intent.Payload.KeyID,
            Parties:   intent.Payload.Parties,
            Threshold: intent.Payload.Threshold,
            Algorithm: intent.Payload.Algorithm,
            Curve:     intent.Payload.Curve,
            Chain:     intent.Payload.Chain,
        },
        LocalPartyID: localPartyID,
        Digest:       intent.Payload.Digest,
        DerivationContext: &coretss.DerivationContext{
            ProfileID:         ctx.ProfileID,
            Chain:             ctx.Chain,
            Algorithm:         ctx.Algorithm,
            Curve:             ctx.Curve,
            Scheme:            ctx.Scheme,
            AccountPath:       ctx.AccountPath,
            ChildPath:         ctx.ChildPath,
            FullPath:          ctx.FullPath,
            AddressEncoding:   ctx.AddressEncoding,
            ExpectedAddress:   ctx.ExpectedAddress,
            DerivedPublicKey:  ctx.DerivedPublicKey,
            Descriptor:        ctx.Descriptor,
            DescriptorVersion: ctx.DescriptorVersion,
            ProfileVersion:    ctx.ProfileVersion,
        },
        Transport: tr,
    }
}
```

The co-signer may call `coretss.NormalizeDerivationContext` and
`coretss.DerivationContextHashV1` during validation to fail early. That check is only a boundary
validation check. Runtime still passes the original mapped context to `RunSignSession`, and core
remains the source of runtime normalization and hashing.

---

## Error Mapping

`BuildResult` must map new core derivation sentinels to `INVALID_INTENT` with `errors.Is`, so
wrapping inside core does not break the wire contract:

- `coretss.ErrChainCodeMissing`
- `coretss.ErrChainCodeInvalid`
- `coretss.ErrDerivationContextRequired`
- `coretss.ErrInvalidDerivationContext`
- `coretss.ErrUnsupportedDerivationScheme`
- `coretss.ErrDerivationPathInvalid`
- `coretss.ErrDerivationContextMismatch`
- `coretss.ErrUnsupportedAlgorithmCurve`

Existing share, runtime, protocol, timeout, and worker shutdown mappings remain unchanged.

---

## Local Core Development

During implementation, the co-signer may use the local core checkout:

```go
replace github.com/BroLabel/brosettlement-mpc-core => ../brosettlement-mpc-core
```

This is a development-only step for working against the local `feat-hd-wallets` core API. It must
not ship. Before final merge or release, `go.mod` must depend on a tagged core version that contains
the HD derivation API, and `go.mod` must not contain a local `replace`.

---

## Test Plan

Focused tests should cover the new boundary contract and mapping:

- `validateIntent` rejects DKG without `chainCode`.
- `validateIntent` rejects DKG with malformed, uppercase, non-hex, or non-64-character
  `chainCode`.
- `validateIntent` rejects DKG without `derivationScheme`.
- `validateIntent` rejects DKG with unsupported `derivationScheme`.
- `buildDKGRequest` passes `DerivationMaterial` into the core request.
- `validateIntent` rejects SIGN without `derivationContext`.
- `validateIntent` rejects SIGN with non-empty top-level `chainCode`.
- `validateIntent` rejects SIGN with non-empty top-level `derivationScheme`.
- `validateIntent` allows empty top-level `chainCode` and `derivationScheme` strings for the
  SIGN-specific top-level check, while still requiring a valid `derivationContext`.
- `buildSignRequest` maps `payload.derivationContext` into `coretss.DerivationContext`.
- `BuildResult` maps the new core derivation sentinels to `INVALID_INTENT` via `errors.Is`.
- Monolith JSON decoding covers `chainCode`, `derivationScheme`, and nested `derivationContext`.

---

## Acceptance Criteria

- Co-signer implements a strict monolith payload contract with no fallback, defaulting, or legacy
  mode.
- DKG intents require explicit `chainCode` and `derivationScheme`.
- SIGN intents require explicit `derivationContext`.
- Non-empty top-level `chainCode` or `derivationScheme` in SIGN payload is rejected as
  `INVALID_INTENT`.
- Contract errors are detected before `FrameContext`, `HTTPTransport`, or core session creation.
- New core derivation sentinels map to `FAILED / INVALID_INTENT` through `errors.Is`.
- Final `go.mod` does not contain a local `replace` for `brosettlement-mpc-core`.
