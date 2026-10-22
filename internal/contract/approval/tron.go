package approval

import (
	"math/big"

	"github.com/btcsuite/btcutil/base58"
)

func canonicalTronAddress(s string) bool {
	payload, version, err := base58.CheckDecode(s)
	return err == nil && version == 0x41 && len(payload) == 20 && base58.CheckEncode(payload, version) == s
}

func validateTronAuthorization(v Object, publicKey []byte) error {
	d := rawObject(v, "derivationContext")
	expected := base58.CheckEncode(publicKeyAddressBytes(publicKey), 0x41)
	if str(v, "fromAddress") != expected || str(d, "addressEncoding") != "tron_base58" || !canonicalTronAddress(str(v, "toAddress")) || !canonicalTronAddress(str(v, "fromAddress")) {
		return invalid()
	}
	if err := validateTransferAsset(v, "TRX", "trc20"); err != nil {
		return err
	}
	if isNull(v, "tokenStandard") {
		amount, _ := new(big.Int).SetString(str(v, "amountAtomic"), 10)
		if amount.Cmp(big.NewInt(9007199254740991)) > 0 {
			return invalid()
		}
	}
	if !isNull(v, "requestedChainParams") {
		p := rawObject(v, "requestedChainParams")
		q := rawObject(p, "tron")
		if !closed(p, "tron") || !closed(q, "feeLimitSun") || !isNull(q, "feeLimitSun") && !uintPattern.MatchString(str(q, "feeLimitSun")) {
			return invalid()
		}
	}
	return nil
}
