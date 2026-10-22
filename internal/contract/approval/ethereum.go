package approval

import (
	"encoding/hex"
	"math/big"
	"regexp"

	"golang.org/x/crypto/sha3"
)

// Both ERC-20 and TRC-20 authorization contracts use this canonical encoding.
var evmAddress = regexp.MustCompile(`^0x[0-9a-f]{40}$`)

// Ethereum and TRON derive the same address bytes from an uncompressed public key.
func publicKeyAddressBytes(publicKey []byte) []byte {
	h := sha3.NewLegacyKeccak256()
	_, _ = h.Write(publicKey[1:])
	return h.Sum(nil)[12:]
}

func validateEthereumAuthorization(v Object, publicKey []byte) error {
	d := rawObject(v, "derivationContext")
	expected := "0x" + hex.EncodeToString(publicKeyAddressBytes(publicKey))
	if str(v, "fromAddress") != expected || str(d, "addressEncoding") != "evm_hex" || !evmAddress.MatchString(str(v, "toAddress")) || !evmAddress.MatchString(str(v, "fromAddress")) {
		return invalid()
	}
	if err := validateTransferAsset(v, "ETH", "erc20"); err != nil {
		return err
	}
	if !isNull(v, "requestedChainParams") {
		p := rawObject(v, "requestedChainParams")
		q := rawObject(p, "ethereum")
		if !closed(p, "ethereum") || !closed(q, "maxFeePerGas,maxPriorityFeePerGas") || !uintPattern.MatchString(str(q, "maxFeePerGas")) || !uintPattern.MatchString(str(q, "maxPriorityFeePerGas")) {
			return invalid()
		}
		a, _ := new(big.Int).SetString(str(q, "maxFeePerGas"), 10)
		b, _ := new(big.Int).SetString(str(q, "maxPriorityFeePerGas"), 10)
		if b.Cmp(a) > 0 {
			return invalid()
		}
	}
	return nil
}
