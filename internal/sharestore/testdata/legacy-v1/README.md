# Frozen encrypted legacy fixtures

**TEST ONLY — NEVER FUND.** All encryption keys and shares in this corpus are
public synthetic material. They are not production custody or provenance evidence.

The envelopes were produced on Linux by Co-Signer
`cf48fe4a51cc1adcff0c3994d124a0d59f7ae12c`, Core `v0.5.0` and TSS `v1.5.0`,
using its unchanged `RoutingWriter.SaveShare` and immutable publisher. The old
producer ran in a separate process and exited before candidate recovery. No
candidate writer, replacement DKG, Gob re-encoding or fabricated save data was
used. `manifest.json` records hashes of the old producer sources/manifests,
envelopes, decoded ciphertext, descriptor and original K01 Gob bytes, together
with the public AES-256 test key and its key reference. `core-manifest.json` is
the unchanged K01 provenance/public identity record.

K01's logical store key `synthetic-legacy-v1-never-fund` is not a canonical Co-Signer
artifact ID. Only the new synthetic envelope binds it to canonical test ID
`mpc_key_123e4567-e89b-42d3-a456-426614174000`; the Gob does not encode the logical
store key. Its exact B/C bytes, product party IDs, root public key, chain code and
derived identities remain unchanged. The session is `synthetic-legacy-dkg`.
This test-fixture wrapping is not a migration of any existing artifact.

## Use frozen copies for isolated recovery

From the repository root, run:

```sh
python3 internal/sharestore/testdata/legacy-v1/prepare-proof-input.py /tmp/new-legacy-proof
MPC_RECOVERY_TEST_BIN=/tmp/recovery-proof GOWORK=off make build-recovery-proof
MPC_RECOVERY_PROOF_INPUT=/tmp/new-legacy-proof/proof-input.json /tmp/recovery-proof -test.run '^TestIsolatedRecoveryProof$'
```

The helper refuses an existing destination, copies bytes unchanged, sets private
store/input permissions and prints the absolute input path. It only uses this
checked-in corpus; it cannot read arbitrary custody data. The tagged gate also
runs this legacy case automatically before its existing fresh-DKG recovery cases.
The explicit input branch cannot fall back to fresh DKG.

## Reproduce the old-writer procedure

Reproduction creates new random AES-GCM nonces, so new envelopes differ. Never
replace the frozen fixtures to accommodate a candidate. Use a source-only
archive, not a running baseline service checkout, and a new Linux output path:

```sh
old_source=$(mktemp -d)
git archive cf48fe4a51cc1adcff0c3994d124a0d59f7ae12c | tar -x -C "$old_source"
cp internal/sharestore/testdata/legacy-v1/producer/legacy_fixture_test.go.txt "$old_source/internal/sharestore/legacy_fixture_test.go"
cd "$old_source"
LEGACY_CORE_CORPUS=/absolute/mpc-core/testdata/legacy-v1 \
LEGACY_ARTIFACT_OUTPUT=/tmp/new-old-writer-output \
GOWORK=off go test ./internal/sharestore -run '^TestProduceFrozenLegacyArtifacts$' -count=1
```

The generator verifies K01's warning and blob hashes, registers the production
B/C active pair, and passes each untouched blob to the old routing writer. It
uses the pinned old module graph without a replacement and records the writer
source hashes. Original baseline runtime checkouts are not modified.
