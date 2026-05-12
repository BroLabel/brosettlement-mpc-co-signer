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
does not carry `orgId`, `chainCode`, `derivationScheme`, or `derivationContext`. With the local core
API, that means DKG fails with `ErrInvalidSessionDescriptor` or `ErrChainCodeMissing`, and SIGN fails with
`ErrDerivationContextRequired`.

---

## Contract

### Shared intent payload

`internal/monolith.IntentPayload` is extended with these fields:

```go
type IntentPayload struct {
    Type              string             `json:"type,omitempty"`
    OrgID             string             `json:"orgId"`
    WalletID          string             `json:"walletId,omitempty"`
    KeyID             string             `json:"keyId"`
    ProfileID         string             `json:"profileId,omitempty"`
    ProfileVersion    uint32             `json:"profileVersion,omitempty"`
    ProfileTemplateID string             `json:"profileTemplateId,omitempty"`
    Parties           []string           `json:"parties"`
    Threshold         uint32             `json:"threshold"`
    Algorithm         string             `json:"algorithm"`
    Curve             string             `json:"curve"`
    Chain             string             `json:"chain,omitempty"`
    Digest            []byte             `json:"digest"`
    DigestType        string             `json:"digestType,omitempty"`
    HashAlgorithm     string             `json:"hashAlgorithm,omitempty"`
    SigningPayloadType string            `json:"signingPayloadType,omitempty"`
    ChainCode         string             `json:"chainCode,omitempty"`
    ChainCodeHash     string             `json:"chainCodeHash,omitempty"`
    DerivationScheme  string             `json:"derivationScheme,omitempty"`
    DerivationContextHash string          `json:"derivationContextHash,omitempty"`
    PartyID           string             `json:"partyId,omitempty"`
    DerivationContext *DerivationContext `json:"derivationContext,omitempty"`
}

type DkgParticipantResult struct {
    KeyID            string `json:"keyId"`
    AccountPublicKey string `json:"accountPublicKey"`
    ChainCodeHash    string `json:"chainCodeHash"`
    ChainCodePresent bool   `json:"chainCodePresent"`
    PublicKeyFormat  string `json:"publicKeyFormat"`
    DerivationScheme string `json:"derivationScheme"`
}

type IntentResult struct {
    Status       string                `json:"status"`
    ErrorCode    string                `json:"errorCode,omitempty"`
    ErrorMessage string                `json:"errorMessage,omitempty"`
    DkgMaterial  *DkgParticipantResult `json:"dkgMaterial,omitempty"`
}

type DerivationContext struct {
    ProfileID         string `json:"profileId"`
    ProfileTemplateID string `json:"profileTemplateId,omitempty"`
    Chain             string `json:"chain"`
    Algorithm         string `json:"algorithm"`
    Curve             string `json:"curve"`
    Scheme            string `json:"scheme"`
    AccountPath       string `json:"accountPath"`
    ChildPath         string `json:"childPath"`
    FullPath          string `json:"fullPath"`
    AddressEncoding   string `json:"addressEncoding,omitempty"`
    ExpectedAddress   string `json:"expectedAddress,omitempty"`
    ExpectedPublicKey string `json:"expectedPublicKey,omitempty"`
    PublicKeyFormat   string `json:"publicKeyFormat,omitempty"`
    DescriptorVersion uint32 `json:"descriptorVersion,omitempty"`
    ProfileVersion    uint32 `json:"profileVersion,omitempty"`
    KeyVersion        uint32 `json:"keyVersion,omitempty"`
}
```

`payload.type` mirrors the top-level intent `type` field returned by the monolith. If both are
present, the co-signer validates that they match after trimming and uppercasing.
`chainCode` and top-level `derivationScheme` are DKG material fields. They are not SIGN fields.
The canonical wire value for DKG `derivationScheme` and SIGN `derivationContext.scheme` is
`bip32_secp256k1`; Go constant names are implementation details, not wire values.
`derivationContext.scheme` is the SIGN derivation scheme field.
`expectedPublicKey` is the wire name from the monolith wallet snapshot. The co-signer maps it into
`coretss.DerivationContext.DerivedPublicKey` because core names the same child public key from the
cryptographic point of view.
`orgId` is required for DKG and SIGN because local core treats it as part of the MPC session
descriptor. The monolith is the source of `orgId`; the co-signer must not derive it from API key,
environment, party ID, or any other local default.
The co-signer keeps this wire type separate from `coretss.DerivationContext` because the public
core type has no JSON tags and the monolith contract uses camelCase JSON names.

### DKG payload

For DKG, the payload must include explicit HD derivation material:

```json
{
  "type": "DKG",
  "orgId": "org-1",
  "keyId": "key-1",
  "parties": ["p1", "p2"],
  "threshold": 2,
  "algorithm": "ECDSA",
  "curve": "secp256k1",
  "chainCode": "1111111111111111111111111111111111111111111111111111111111111111",
  "chainCodeHash": "base64url-sha256-chain-code",
  "derivationScheme": "bip32_secp256k1"
}
```

Rules:

- `orgId` is required for DKG.
- `payload.type`, when present, must match top-level intent type `DKG`.
- `keyId` is required for DKG.
- `chainCode` is required for DKG.
- `chainCode` must be lowercase hex, exactly 64 characters, matching `^[0-9a-f]{64}$`.
- `chainCodeHash` is required for DKG and must match the monolith's canonical
  `base64url(sha256(decoded_chain_code))` value.
- `derivationScheme` is required for DKG.
- The only supported `derivationScheme` at launch is `bip32_secp256k1`.
- DKG is chain-agnostic. `chain` is optional for DKG payload compatibility and ignored for DKG
  material, key metadata, result verification, and profile creation.
- The co-signer does not default `derivationScheme` when `chainCode` is present.
- The co-signer does not generate, rewrite, or normalize chain code.
- A non-nil `derivationContext` in DKG payload is `INVALID_INTENT`.
- A non-empty `digest` in DKG payload is `INVALID_INTENT`.

The co-signer executes only pending/deliverable DKG payloads that still include raw `chainCode`.
The monolith may keep a cleaned DKG payload after delivery with `chainCodeHash` and
`derivationScheme` but without raw `chainCode`; that cleaned payload is audit/delivery state, not
an executable co-signer session payload. If such a cleaned payload reaches claim/run, the co-signer
reports `FAILED / INVALID_INTENT`.

This design treats all DKG intents as HD-aware. The current supported runtime scope remains
ECDSA/secp256k1; unsupported algorithm/curve combinations continue to fail validation before core.

### SIGN payload

For SIGN, the payload must include a derivation context in the shape expected by the public core
facade:

```json
{
  "type": "SIGN",
  "orgId": "org-1",
  "keyId": "key-1",
  "parties": ["p1", "p2"],
  "threshold": 2,
  "algorithm": "ECDSA",
  "curve": "secp256k1",
  "chain": "ethereum",
  "digest": "AQID",
  "walletId": "wallet-1",
  "profileId": "profile-1",
  "profileVersion": 3,
  "profileTemplateId": "tron-default",
  "digestType": "transaction_hash",
  "hashAlgorithm": "sha256",
  "signingPayloadType": "tron_transaction",
  "derivationContextHash": "coretss-context-hash",
  "partyId": "co-signer",
  "derivationContext": {
    "profileId": "profile-1",
    "profileTemplateId": "tron-default",
    "chain": "ethereum",
    "algorithm": "ecdsa",
    "curve": "secp256k1",
    "scheme": "bip32_secp256k1",
    "accountPath": "m/44'/60'/0'",
    "childPath": "/0/15",
    "fullPath": "m/44'/60'/0'/0/15",
    "addressEncoding": "",
    "expectedAddress": "",
    "expectedPublicKey": "",
    "publicKeyFormat": "uncompressed_hex",
    "descriptorVersion": 7,
    "profileVersion": 3,
    "keyVersion": 1
  }
}
```

Rules:

- `orgId` is required for SIGN.
- `payload.type`, when present, must match top-level intent type `SIGN`.
- `keyId` is required for SIGN.
- Top-level `chain` is required for SIGN and must match `derivationContext.chain` after core
  normalization.
- `digest` is required for SIGN and must decode to non-empty bytes.
- `derivationContext` is required for SIGN.
- SIGN wire metadata includes `walletId`, `profileId`, `profileVersion`, `profileTemplateId`,
  `digestType`, `hashAlgorithm`, `signingPayloadType`, `derivationContextHash`, and `partyId`.
  The co-signer validates this monolith contract metadata at the boundary and keeps core mapping
  focused on the fields required by `coretss.SignSessionRequest`.
- SIGN `derivationContext` uses the monolith signer/core signing context shape:
  `profileId`, `profileTemplateId`, `profileVersion`, `chain`, `algorithm`, `curve`, `scheme`,
  `accountPath`, `childPath`, `fullPath`, `addressEncoding`, `expectedAddress`,
  `expectedPublicKey`, `publicKeyFormat`, `descriptorVersion`, and `keyVersion`.
- `derivationContextHash` is verified with `coretss.NormalizeDerivationContext` and
  `coretss.DerivationContextHashV1`. The co-signer must not compute this hash through an
  independent JSON canonicalization path.
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

For DKG, `intent.Payload.Chain` may be empty. When present, it is compatibility context only and
must not affect DKG material validation, key metadata, or monolith material consistency checks.

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
            ProfileTemplateID: ctx.ProfileTemplateID,
            Chain:             ctx.Chain,
            Algorithm:         ctx.Algorithm,
            Curve:             ctx.Curve,
            Scheme:            ctx.Scheme,
            PublicKeyFormat:   ctx.PublicKeyFormat,
            AccountPath:       ctx.AccountPath,
            ChildPath:         ctx.ChildPath,
            FullPath:          ctx.FullPath,
            AddressEncoding:   ctx.AddressEncoding,
            ExpectedAddress:   ctx.ExpectedAddress,
            DerivedPublicKey:  ctx.ExpectedPublicKey,
            DescriptorVersion: ctx.DescriptorVersion,
            ProfileVersion:    ctx.ProfileVersion,
            KeyVersion:        ctx.KeyVersion,
        },
        Transport: tr,
    }
}
```

During SIGN validation, the co-signer maps every wire `payload.derivationContext` field that exists
in `coretss.DerivationContext`, then calls `coretss.NormalizeDerivationContext` and
`coretss.DerivationContextHashV1` to fail invalid contexts before creating session transport. That
check must also compare the computed hash with top-level `payload.derivationContextHash`. Runtime
still passes the original mapped context to `RunSignSession`, and core remains the source of runtime
normalization and hashing. `derivationContextHash` is an opaque core-owned value; the co-signer must
not compute it with JSON canonicalization or assume a base64url/hex representation outside the core
API.

### DKG result

On successful DKG, the co-signer posts the common `DkgParticipantResult` material snapshot to the
monolith:

```json
{
  "status": "COMPLETED",
  "dkgMaterial": {
    "keyId": "key-1",
    "accountPublicKey": "uncompressed-account-public-key",
    "chainCodeHash": "base64url-sha256-chain-code",
    "chainCodePresent": true,
    "publicKeyFormat": "uncompressed_hex",
    "derivationScheme": "bip32_secp256k1"
  }
}
```

This result shape is shared with signer participant material reporting. It must not include raw
`chainCode`, root addresses, derivation paths, profile ids, or profile versions. On failure,
`dkgMaterial` is omitted and the result contains only `FAILED` plus the error fields.

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
- `validateIntent` rejects DKG and SIGN when `payload.type` conflicts with the top-level intent
  type.
- `validateIntent` rejects DKG and SIGN without `keyId`.
- `validateIntent` rejects DKG without `chainCode`.
- `validateIntent` rejects DKG without `chainCodeHash`.
- `validateIntent` rejects DKG when `chainCodeHash` does not match `chainCode`.
- `validateIntent` rejects DKG with malformed, uppercase, non-hex, or non-64-character
  `chainCode`.
- `validateIntent` rejects DKG without `derivationScheme`.
- `validateIntent` rejects DKG with unsupported `derivationScheme`.
- `validateIntent` rejects DKG with non-nil `derivationContext`.
- `validateIntent` rejects DKG with non-empty `digest`.
- `buildDKGRequest` passes `DerivationMaterial` into the core request.
- Successful DKG posts `DkgParticipantResult` with `keyId`, `accountPublicKey`,
  `chainCodeHash`, `chainCodePresent`, `publicKeyFormat`, and `derivationScheme`, and never raw
  `chainCode`.
- `validateIntent` rejects SIGN without `digest`.
- `validateIntent` rejects SIGN without top-level `chain` or when top-level `chain` conflicts with
  `derivationContext.chain`.
- `validateIntent` rejects SIGN without `derivationContext`.
- `validateIntent` rejects SIGN without monolith metadata fields such as `walletId`, `profileId`,
  `profileVersion`, `profileTemplateId`, `digestType`, `hashAlgorithm`, `signingPayloadType`,
  `derivationContextHash`, and `partyId`, while still keeping unrelated metadata out of core.
- `validateIntent` rejects SIGN with a derivation context that fails
  `coretss.NormalizeDerivationContext` or `coretss.DerivationContextHashV1`.
- `validateIntent` rejects SIGN when top-level `derivationContextHash` does not match the hash
  computed by `coretss.DerivationContextHashV1`.
- `validateIntent` rejects SIGN with non-empty top-level `chainCode`.
- `validateIntent` rejects SIGN with non-empty top-level `derivationScheme`.
- `validateIntent` allows empty top-level `chainCode` and `derivationScheme` strings for the
  SIGN-specific top-level check, while still requiring a valid `derivationContext`.
- `buildDKGRequest` and `buildSignRequest` pass `OrgID` into the core session descriptor.
- `buildSignRequest` maps `payload.derivationContext` into `coretss.DerivationContext`, including
  `profileTemplateId`, `publicKeyFormat`, `keyVersion`, and `expectedPublicKey -> DerivedPublicKey`.
- `BuildResult` maps the new core derivation sentinels to `INVALID_INTENT` via `errors.Is`.
- Monolith JSON decoding covers `orgId`, `chainCode`, `chainCodeHash`, `derivationScheme`, SIGN
  metadata fields, and nested `derivationContext`.

---

## Acceptance Criteria

- Co-signer implements a strict monolith payload contract with no fallback, defaulting, or legacy
  mode.
- DKG and SIGN intents require explicit `orgId` from the monolith.
- DKG and SIGN intents reject conflicting top-level and payload intent types.
- DKG and SIGN intents require explicit `keyId` from the monolith.
- DKG intents require explicit `chainCode`, `chainCodeHash`, and `derivationScheme`.
- DKG `chain` is optional and ignored for DKG material; SIGN still requires chain in the signing
  derivation context.
- Successful DKG results use the shared `DkgParticipantResult` material snapshot and never include
  raw `chainCode`.
- Cleaned DKG payloads without raw `chainCode` are not executable co-signer session payloads and
  fail as `INVALID_INTENT` if claimed for execution.
- SIGN intents require explicit non-empty `digest`.
- SIGN intents require top-level `chain` matching `derivationContext.chain`.
- SIGN intents require explicit `derivationContext`.
- SIGN intents require monolith wallet/profile/digest metadata fields: `walletId`, `profileId`,
  `profileVersion`, `profileTemplateId`, `digestType`, `hashAlgorithm`, `signingPayloadType`,
  `derivationContextHash`, and `partyId`.
- Non-nil `derivationContext` and non-empty `digest` in DKG payload are rejected as
  `INVALID_INTENT`.
- Non-empty top-level `chainCode` or `derivationScheme` in SIGN payload is rejected as
  `INVALID_INTENT`.
- Contract errors are detected before `FrameContext`, `HTTPTransport`, or core session creation.
- New core derivation sentinels map to `FAILED / INVALID_INTENT` through `errors.Is`.
- Final `go.mod` does not contain a local `replace` for `brosettlement-mpc-core`.
