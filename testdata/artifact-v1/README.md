# Artifact v1 golden vector

`golden.primary.json` is the exact compact artifact produced with:

- AES key: 32 bytes of `0x01`;
- key reference: `customer-co-signer-key-v1`;
- nonce: 12 bytes of `0x11`;
- session ID: `dkg-session-123`;
- key ID: `mpc_key_123e4567-e89b-42d3-a456-426614174000`;
- party ID: `co-signer-primary`;
- descriptor and codec material constructed by `artifact_test.go`.

The file intentionally contains encrypted test-only key material and no
production secret. The repository text file's final line feed is transport
formatting and is excluded from the artifact bytes asserted by the test.
