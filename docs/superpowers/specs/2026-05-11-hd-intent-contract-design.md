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
    OrgID             string             `json:"orgId"`
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

type DerivationContext struct {
    ProfileID         string `json:"profileId"`
    Chain             string `json:"chain"`
    Algorithm         string `json:"algorithm"`
    Curve             string `json:"curve"`
    Scheme            string `json:"scheme"`
    AccountPath       string `json:"accountPath"`
    ChildPath         string `json:"childPath"`
    FullPath          string `json:"fullPath"`
    AddressEncoding   string `json:"addressEncoding,omitempty"`
    ExpectedAddress   string `json:"expectedAddress,omitempty"`
    DerivedPublicKey  string `json:"derivedPublicKey,omitempty"`
    Descriptor        string `json:"descriptor,omitempty"`
    DescriptorVersion uint32 `json:"descriptorVersion,omitempty"`
    ProfileVersion    uint32 `json:"profileVersion,omitempty"`
}
```

`chainCode` and top-level `derivationScheme` are DKG material fields. They are not SIGN fields.
`derivationContext.scheme` is the SIGN derivation scheme field.
`orgId` is required for DKG and SIGN because local core treats it as part of the MPC session
descriptor. The monolith is the source of `orgId`; the co-signer must not derive it from API key,
environment, party ID, or any other local default.
The co-signer keeps this wire type separate from `coretss.DerivationContext` because the public
core type has no JSON tags and the monolith contract uses camelCase JSON names.

### DKG payload

For DKG, the payload must include explicit HD derivation material:

```json
{
  "orgId": "org-1",
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

- `orgId` is required for DKG.
- `chainCode` is required for DKG.
- `chainCode` must be lowercase hex, exactly 64 characters, matching `^[0-9a-f]{64}$`.
- `derivationScheme` is required for DKG.
- The only supported `derivationScheme` at launch is `bip32_secp256k1`.
- The co-signer does not default `derivationScheme` when `chainCode` is present.
- The co-signer does not generate, rewrite, or normalize chain code.
- A non-nil `derivationContext` in DKG payload is `INVALID_INTENT`.
- A non-empty `digest` in DKG payload is `INVALID_INTENT`.

This design treats all DKG intents as HD-aware. The current supported runtime scope remains
ECDSA/secp256k1; unsupported algorithm/curve combinations continue to fail validation before core.

### SIGN payload

For SIGN, the payload must include a derivation context in the shape expected by the public core
facade:

```json
{
  "orgId": "org-1",
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

- `orgId` is required for SIGN.
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

For DKG, a valid `chainCode` is not persisted or staged separately by the co-signer. Immediately
after validation, the worker creates the session transport and starts `RunDKGSession` with
`DKGDerivationMaterial{ChainCode, DerivationScheme}` from the claimed intent. Core owns DKG
execution and persists the chain code only as part of successful DKG key material persistence.

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
            OrgID:     intent.Payload.OrgID,
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
            OrgID:     intent.Payload.OrgID,
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

During SIGN validation, the co-signer maps `payload.derivationContext` into a
`coretss.DerivationContext`, then calls `coretss.NormalizeDerivationContext` and
`coretss.DerivationContextHashV1` to fail invalid contexts before creating session transport. That
check is only a boundary validation check. Runtime still passes the original mapped context to
`RunSignSession`, and core remains the source of runtime normalization and hashing.

---

## Chain Code Persistence

The co-signer treats `payload.chainCode` as transient DKG input:

- It is read from the claimed monolith intent.
- It is validated before session transport creation.
- It is passed directly into `coretss.RunDKGSession` through `DKGDerivationMaterial`.
- It is not stored in a separate co-signer table, file, cache, or sidecar payload.

After successful ECDSA DKG, core persists the chain code together with the generated ECDSA key
material through the configured `coretss.ShareStore`. With the current filesystem store, the outer
`<shares_dir>/<keyID>.json` envelope contains share metadata and encrypted `ciphertext`; the raw
chain code lives inside that encrypted key-material blob, not as a plaintext top-level JSON field.

SIGN intents never provide chain code. During SIGN, core loads the stored key material by `keyId`
and uses the chain code persisted from the prior successful DKG.

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

- `validateIntent` rejects DKG and SIGN without `orgId`.
- `validateIntent` rejects DKG without `chainCode`.
- `validateIntent` rejects DKG with malformed, uppercase, non-hex, or non-64-character
  `chainCode`.
- `validateIntent` rejects DKG without `derivationScheme`.
- `validateIntent` rejects DKG with unsupported `derivationScheme`.
- `validateIntent` rejects DKG with non-nil `derivationContext`.
- `validateIntent` rejects DKG with non-empty `digest`.
- `buildDKGRequest` passes `DerivationMaterial` into the core request.
- `validateIntent` rejects SIGN without `derivationContext`.
- `validateIntent` rejects SIGN with a derivation context that fails
  `coretss.NormalizeDerivationContext` or `coretss.DerivationContextHashV1`.
- `validateIntent` rejects SIGN with non-empty top-level `chainCode`.
- `validateIntent` rejects SIGN with non-empty top-level `derivationScheme`.
- `validateIntent` allows empty top-level `chainCode` and `derivationScheme` strings for the
  SIGN-specific top-level check, while still requiring a valid `derivationContext`.
- `buildDKGRequest` and `buildSignRequest` pass `OrgID` into the core session descriptor.
- `buildSignRequest` maps `payload.derivationContext` into `coretss.DerivationContext`.
- `BuildResult` maps the new core derivation sentinels to `INVALID_INTENT` via `errors.Is`.
- Monolith JSON decoding covers `orgId`, `chainCode`, `derivationScheme`, and nested
  `derivationContext`.

---

## Acceptance Criteria

- Co-signer implements a strict monolith payload contract with no fallback, defaulting, or legacy
  mode.
- DKG and SIGN intents require explicit `orgId` from the monolith.
- DKG intents require explicit `chainCode` and `derivationScheme`.
- SIGN intents require explicit `derivationContext`.
- Non-nil `derivationContext` and non-empty `digest` in DKG payload are rejected as
  `INVALID_INTENT`.
- Non-empty top-level `chainCode` or `derivationScheme` in SIGN payload is rejected as
  `INVALID_INTENT`.
- Contract errors are detected before `FrameContext`, `HTTPTransport`, or core session creation.
- New core derivation sentinels map to `FAILED / INVALID_INTENT` through `errors.Is`.
- Final `go.mod` does not contain a local `replace` for `brosettlement-mpc-core`.
