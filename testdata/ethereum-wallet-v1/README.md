# Ethereum Wallet v1 Co-Signer Vectors

This consumer subset retains the canonical vector IDs, source hash, derivation
context hashes, and opaque Keccak-256 signing digests from the shared
`contracts/ethereum-wallet-v1/vectors.json` fixture. It contains no private
material and is test data only.

`sign-claim-eth.json` and `sign-claim-erc20.json` are exact closed Client API
claim responses for the native Mainnet and ERC-20 Sepolia vector policies. They
exercise the same HTTP decoder used by the worker before any Core call.
