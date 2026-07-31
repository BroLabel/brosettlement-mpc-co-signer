# Artifact format v1

Artifact v1 is an encrypted, immutable local recovery format. It is not a
customer-facing recovery API or a supported signing tool.

Each final file is created exactly once as either `<keyId>.primary.json` or
`<keyId>.recovery.json`; `keyId` is the canonical descriptor key ID matching
`^mpc_key_[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`.
No filename is sanitized, adopted, or overwritten.

## Normative wire format

The outer UTF-8 JSON object is closed and compact (unknown/duplicate keys are
invalid):

```json
{"version":1,"encryption":{"algorithm":"AES-256-GCM","keyRef":"customer-co-signer-key-v1","nonce":"...","ciphertext":"...","tag":"..."}}
```

`version` is JSON number `1`; `algorithm` is exactly `AES-256-GCM`; `keyRef`
is the configured printable ASCII binding; `nonce` is exactly 12 bytes, `tag`
is exactly 16 bytes, and `ciphertext` excludes the tag. All binary fields use
canonical RFC 4648 standard padded base64. AES-256-GCM encrypts with the one
configured 32-byte key and empty AAD; nonce generation is cryptographically
random and reuse under that key is prohibited.

After decrypting, the closed plaintext object is:

```json
{"artifactPayloadVersion":1,"sessionId":"dkg-123","partyId":"co-signer-primary","descriptorBytesBase64":"...","shareBlob":"..."}
```

`artifactPayloadVersion` is JSON number `1`; `sessionId` and `partyId` are the
exact descriptor/runtime bindings; `descriptorBytesBase64` and `shareBlob` are
canonical standard padded base64. Descriptor bytes are the exact canonical bytes
created by the backend—not a reconstructed JSON object. `shareBlob` is owned by
the pinned `mpc-core` codec v2 decoder and evidence inspector; this repository
does not redefine that codec.

The artifact fingerprint is SHA-256 over exact final file bytes. Descriptor,
chain-code, artifact, and terminal wire fingerprints use their respective
strict `Sha256DigestV1` domains: 32 SHA-256 bytes encoded as 43-character
unpadded base64url. No envelope JSON reserialization is a fingerprint domain.

Normative formulas are: `descriptorFingerprint = base64urlNoPad(SHA-256(exact
canonical descriptor bytes))`; `artifactFingerprint =
base64urlNoPad(SHA-256(exact final artifact bytes))`; `terminalResultFingerprint
= base64urlNoPad(SHA-256(exact canonical terminal payload bytes))`; and
`chainCodeHash = base64urlNoPad(SHA-256(raw 32-byte chain code))`.

The exact final bytes are the artifact fingerprint. Publication uses a private
`0600` temporary file, file sync, Linux `renameat2(RENAME_NOREPLACE)`, parent
directory sync, no-follow readback, decrypt, and strict inspection. Existing
final paths are never replaced or adopted.

Readers bound the envelope to 16 MiB, ciphertext plus tag to 12 MiB, and
plaintext/share blob to 8 MiB before allocation/decode. They verify the final
path is a no-follow regular file, decode the closed envelope, decrypt, parse
the exact descriptor, check session/key/party/purpose bindings, inspect the
codec blob, and compare public-key and chain-code evidence. Publication follows
write, file `fsync`, close, `RENAME_NOREPLACE`, directory `fsync`, no-follow
readback, decrypt, and inspect. These operations never decrypt or classify
unaddressed inventory files.

## Key lifetime and custody

Both B and C use one configured 32-byte AES key and one `keyRef` for the full
lifetime of every v1 artifact. Preserve the original key, `keyRef`, B artifact,
and C artifact in backups. A replacement key or key reference fails closed.
V1 has no key ring, online rotation, or re-encryption path.

The customer owns recovery custody. A missing or corrupt C after activation
does not change backend state and does not stop normal A+B signing; it removes
recovery capability. No supported CLI, SDK, endpoint, or distributed recovery
binary exists in v1. `FUTURE-001` is DESIGN-only.
