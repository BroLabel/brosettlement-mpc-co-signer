# HD Intent Contract Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development
> (recommended) or superpowers:executing-plans to implement this plan task-by-task.
> Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the strict HD-aware monolith intent contract for DKG and SIGN sessions.

**Architecture:** Extend the monolith wire payload first, then enforce the contract in
`internal/worker` before session transport creation. Valid DKG intents pass chain code through
`coretss.DKGDerivationMaterial` with no DKG chain. Valid SIGN intents pass
`coretss.DerivationContext`; core owns normalization, context hashing, and signing-key derivation.

**Tech Stack:** Go 1.24, `brosettlement-mpc-core/tss`, standard `testing`, monolith HTTP JSON
contract.

---

## File Structure

- Modify `go.mod`: add a dev-only local `replace` while implementing against the local core branch.
- Modify `internal/monolith/types.go`: add strict HD wire fields and result shape.
- Modify `internal/monolith/client_test.go`: add JSON decoding coverage for the new payload fields.
- Modify `internal/worker/session_worker.go`: add validation helpers, request mapping, DKG result
  mapping, and new error mapping.
- Modify `internal/worker/session_worker_test.go`: add contract validation, request mapping, result,
  and error mapping tests.
- Final readiness check: remove local `replace` once a tagged core version is available.

---

## Task 1: Use Local Core During Development

**Files:**
- Modify: `go.mod`

- [ ] **Step 1: Add the local core replace**

Run:

```bash
go mod edit -replace github.com/BroLabel/brosettlement-mpc-core=../brosettlement-mpc-core
```

- [ ] **Step 2: Verify the replace points at the local branch**

Run:

```bash
go list -m -json github.com/BroLabel/brosettlement-mpc-core
```

Expected: output contains `"Replace": {"Path": "../brosettlement-mpc-core"}`.

- [ ] **Step 3: Commit**

Do not commit this task by itself. This is a development-only dependency setup and must be removed
before final merge/release.

---

## Task 2: Add Monolith Wire Types

**Files:**
- Modify: `internal/monolith/types.go`
- Test: `internal/monolith/client_test.go`

- [ ] **Step 1: Write the JSON decoding test**

Add `TestGetPendingIntentsDecodesHDIntentPayload` covering:

- `payload.type`
- `orgId`
- `walletId`
- `keyId`
- `profileId`
- `profileVersion`
- `profileTemplateId`
- `digestType`
- `hashAlgorithm`
- `signingPayloadType`
- `chainCode`
- `chainCodeHash`
- `derivationScheme`
- `derivationContextHash`
- `partyId`
- nested `derivationContext.profileId`
- nested `derivationContext.profileTemplateId`
- nested `derivationContext.chain`
- nested `derivationContext.algorithm`
- nested `derivationContext.curve`
- nested `derivationContext.scheme`
- nested `derivationContext.accountPath`
- nested `derivationContext.childPath`
- nested `derivationContext.fullPath`
- nested `derivationContext.expectedPublicKey`
- nested `derivationContext.publicKeyFormat`
- nested `derivationContext.descriptorVersion`
- nested `derivationContext.profileVersion`
- nested `derivationContext.keyVersion`

Use the wire name `expectedPublicKey`, not `derivedPublicKey`.

- [ ] **Step 2: Run the test and verify it fails**

Run:

```bash
go test ./internal/monolith -run TestGetPendingIntentsDecodesHDIntentPayload -count=1
```

Expected: FAIL because the wire fields are not present yet.

- [ ] **Step 3: Add the wire fields**

Update `IntentPayload` and add `DerivationContext`, `DkgParticipantResult`, and `IntentResult`
fields to match the spec in `docs/superpowers/specs/2026-05-11-hd-intent-contract-design.md`.

Numeric version fields remain `uint32`; validation rejects required versions when they are zero.

- [ ] **Step 4: Run the test and verify it passes**

Run:

```bash
go test ./internal/monolith -run TestGetPendingIntentsDecodesHDIntentPayload -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/monolith/types.go internal/monolith/client_test.go
git commit -m "feat: add hd intent payload fields"
```

---

## Task 3: Validate Strict HD Intent Contract

**Files:**
- Modify: `internal/worker/session_worker.go`
- Test: `internal/worker/session_worker_test.go`

- [ ] **Step 1: Add valid intent test helpers**

Add `validDKGIntent()` with:

- `Type: "DKG"`
- `OrgID`
- `KeyID`
- `Parties`
- `Threshold`
- `Algorithm: "ECDSA"`
- `Curve: "secp256k1"`
- empty `Chain`
- lowercase 64-character hex `ChainCode`
- matching `ChainCodeHash`, using `base64.RawURLEncoding(sha256(decoded_chain_code))`
- `DerivationScheme: "bip32_secp256k1"`

Add `validSignIntent()` with:

- `Type: "SIGN"`
- `OrgID`
- `WalletID`
- `KeyID`
- `ProfileID`
- `ProfileVersion > 0`
- `ProfileTemplateID`
- `Parties`
- `Threshold`
- `Algorithm: "ECDSA"`
- `Curve: "secp256k1"`
- `Chain`
- non-empty `Digest`
- `DigestType`
- `HashAlgorithm`
- `SigningPayloadType`
- `DerivationContextHash` computed with `coretss.NormalizeDerivationContext` and
  `coretss.DerivationContextHashV1`
- `PartyID`
- nested `DerivationContext` with matching `profileId`, `profileTemplateId`, `profileVersion`,
  `chain`, `algorithm`, `curve`, valid paths, `publicKeyFormat`, `descriptorVersion > 0`, and
  `keyVersion > 0`

- [ ] **Step 2: Add DKG validation tests**

Add table tests asserting `validateIntent` rejects:

- missing `orgId`
- missing `keyId`
- non-empty `chain`
- missing `chainCode`
- malformed, uppercase, non-hex, or non-64-character `chainCode`
- missing `chainCodeHash`
- mismatched `chainCodeHash`
- missing `derivationScheme`
- unsupported `derivationScheme`
- non-nil `derivationContext`
- non-empty `digest`
- conflicting `payload.type`

Add positive tests asserting:

- DKG payload type `" dkg "` matches top-level `DKG` after trimming and uppercasing.
- DKG chain code `1111111111111111111111111111111111111111111111111111111111111111` accepts expected
  hash `AtRJox-7JnyPNS6ZaKeePl_JXBu-qlAv1kVOveWkvtw`.

- [ ] **Step 3: Add SIGN validation tests**

Add table tests asserting `validateIntent` rejects:

- missing `orgId`
- missing `keyId`
- missing `walletId`
- missing `profileId`
- zero top-level `profileVersion`
- missing `profileTemplateId`
- missing `digestType`
- missing `hashAlgorithm`
- missing `signingPayloadType`
- missing `derivationContextHash`
- missing `partyId`
- missing or empty `chain`
- missing or empty `digest`
- missing `derivationContext`
- invalid derivation context
- zero nested `profileVersion`, `descriptorVersion`, or `keyVersion`
- top-level `chain`, `algorithm`, `curve`, `profileId`, `profileTemplateId`, or `profileVersion`
  conflicts with the normalized derivation context
- top-level `derivationContextHash` does not match `coretss.DerivationContextHashV1`
- non-empty top-level `chainCode`
- non-empty top-level `derivationScheme`
- conflicting `payload.type`

Add a positive test showing SIGN allows empty top-level `chainCode` and `derivationScheme` strings.
Add a positive test showing SIGN payload type `" sign "` matches top-level `SIGN` after trimming and
uppercasing.

- [ ] **Step 4: Run worker validation tests and verify they fail**

Run:

```bash
go test ./internal/worker -run 'TestValidateIntent' -count=1
```

Expected: FAIL because validation does not yet enforce the new contract.

- [ ] **Step 5: Implement validation helpers**

Implement:

- `validateCommonPayload`
- `validatePayloadType`
- `validateDKGPayload`
- `validateSignPayload`
- `validateSignMetadata`
- `validateChainCodeHash`
- `isLowerHex64`
- `toCoreDerivationContext`
- `derivationContextPtr`

Use `coretss.NormalizeDerivationContext` and `coretss.DerivationContextHashV1` for SIGN validation.
Compare SIGN top-level metadata with the normalized derivation context using core-compatible
semantics: trim strings, lowercase protocol identifiers such as `algorithm` and `curve`, and compare
profile identifiers after trimming.
Do not implement an independent JSON canonicalization path.

- [ ] **Step 6: Wire the helpers into `validateIntent`**

Validation must run after claim and before `FrameContext`, `HTTPTransport`, or core session request
creation.

- [ ] **Step 7: Run validation tests and verify they pass**

Run:

```bash
go test ./internal/worker -run 'TestValidateIntent' -count=1
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/worker/session_worker.go internal/worker/session_worker_test.go
git commit -m "feat: validate hd intent contract"
```

---

## Task 4: Map HD Payload Into Core Requests And Results

**Files:**
- Modify: `internal/worker/session_worker.go`
- Test: `internal/worker/session_worker_test.go`

- [ ] **Step 1: Add DKG request mapping test**

Assert `buildDKGRequest` maps:

- `Session.OrgID`
- `Session.KeyID`
- `Session.Parties`
- `Session.Threshold`
- `Session.Algorithm`
- `Session.Curve`
- empty `Session.Chain`
- `DerivationMaterial.ChainCode`
- `DerivationMaterial.DerivationScheme`

- [ ] **Step 2: Add SIGN request mapping test**

Assert `buildSignRequest` maps:

- `Session.OrgID`
- `Session.KeyID`
- `Session.Chain`
- `Digest`
- `DerivationContext.ProfileID`
- `DerivationContext.ProfileTemplateID`
- `DerivationContext.Chain`
- `DerivationContext.Algorithm`
- `DerivationContext.Curve`
- `DerivationContext.Scheme`
- `DerivationContext.PublicKeyFormat`
- `DerivationContext.FullPath`
- `DerivationContext.DerivedPublicKey` from wire `expectedPublicKey`
- `DerivationContext.DescriptorVersion`
- `DerivationContext.ProfileVersion`
- `DerivationContext.KeyVersion`

- [ ] **Step 3: Add successful DKG result mapping test**

Assert successful DKG posts `DkgParticipantResult` with:

- `keyId`
- `accountPublicKey`
- `chainCodeHash` from the validated claimed payload
- `chainCodePresent: true`
- `publicKeyFormat`
- `derivationScheme`

Assert the posted result does not include raw `chainCode`.

Add a failed DKG result test asserting `DkgMaterial == nil` and the posted result contains only
`FAILED` plus `errorCode`/`errorMessage`.

- [ ] **Step 4: Run mapping tests and verify they fail**

Run:

```bash
go test ./internal/worker -run 'TestBuild(DKG|Sign)RequestMaps|TestRunSessionPostsDkgMaterial' -count=1
```

Expected: FAIL because HD fields and DKG material result mapping are not implemented yet.

- [ ] **Step 5: Update request and result mapping**

Update `buildDKGRequest` to pass `OrgID`, empty `Chain`, and `DKGDerivationMaterial`.

Update `buildSignRequest` to pass `OrgID`, `Digest`, and the mapped `coretss.DerivationContext`.

Update successful DKG handling so `BuildResult` or the worker receives the core DKG output and posts
`DkgParticipantResult` using the validated payload `chainCodeHash`, not raw core `ChainCode`.

- [ ] **Step 6: Run mapping tests and verify they pass**

Run:

```bash
go test ./internal/worker -run 'TestBuild(DKG|Sign)RequestMaps|TestRunSessionPostsDkgMaterial' -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/worker/session_worker.go internal/worker/session_worker_test.go
git commit -m "feat: map hd intent payload to core"
```

---

## Task 5: Map New Core Derivation Errors

**Files:**
- Modify: `internal/worker/session_worker.go`
- Test: `internal/worker/session_worker_test.go`

- [ ] **Step 1: Add error mapping test**

Add `TestBuildResultMapsDerivationErrorsToInvalidIntent` for wrapped errors:

- `coretss.ErrChainCodeMissing`
- `coretss.ErrChainCodeInvalid`
- `coretss.ErrDerivationContextRequired`
- `coretss.ErrInvalidDerivationContext`
- `coretss.ErrUnsupportedDerivationScheme`
- `coretss.ErrDerivedSigningUnsupported`
- `coretss.ErrDerivationPathInvalid`
- `coretss.ErrDerivationContextMismatch`
- `coretss.ErrUnsupportedAlgorithmCurve`

- [ ] **Step 2: Run the test and verify it fails**

Run:

```bash
go test ./internal/worker -run TestBuildResultMapsDerivationErrorsToInvalidIntent -count=1
```

Expected: FAIL for the new derivation errors not currently mapped.

- [ ] **Step 3: Extend `BuildResult`**

Map all new derivation sentinels to `INVALID_INTENT` using `errors.Is`.

- [ ] **Step 4: Run the test and verify it passes**

Run:

```bash
go test ./internal/worker -run TestBuildResultMapsDerivationErrorsToInvalidIntent -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/worker/session_worker.go internal/worker/session_worker_test.go
git commit -m "fix: map derivation errors to invalid intent"
```

---

## Task 6: Full Verification And Release Dependency Cleanup

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`

- [ ] **Step 1: Run all tests with local core**

Run:

```bash
go test ./...
```

Expected: PASS.

- [ ] **Step 2: Replace local core with a tagged version**

After `brosettlement-mpc-core` publishes a tagged release containing `DKGDerivationMaterial`,
`DerivationContext`, `OrgID`, and derivation error sentinels, run:

```bash
go mod edit -dropreplace github.com/BroLabel/brosettlement-mpc-core
go get github.com/BroLabel/brosettlement-mpc-core@<tag>
go mod tidy
```

- [ ] **Step 3: Verify no local replace remains**

Run:

```bash
go mod edit -json | rg '"Replace"|"brosettlement-mpc-core"'
```

Expected: no `Replace` entry for `github.com/BroLabel/brosettlement-mpc-core`; the required version
is the tagged release.

- [ ] **Step 4: Run final full tests**

Run:

```bash
go test ./...
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum
git commit -m "chore: use tagged hd mpc core"
```

---

## Self-Review

- Spec coverage: covered `orgId`, `keyId`, DKG `chainCode`, DKG `chainCodeHash`, DKG
  `derivationScheme`, DKG rejection of `chain`, SIGN metadata consistency, required numeric
  versions, SIGN `digest`, SIGN `derivationContext`, strict forbidden fields, core error mapping,
  DKG result material, and final removal of local `replace`.
- Placeholder scan: no `TODO`, `TBD`, or unspecified implementation steps.
- Type consistency: plan uses `monolith.DerivationContext` for JSON wire input and
  `coretss.DerivationContext` for core requests.
