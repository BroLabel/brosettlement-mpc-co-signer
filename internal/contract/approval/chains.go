package approval

import "math/big"

// Only explicit supported networks select a validator. A chain prefix must
// never select another protocol's rules or a default protocol.
func authorizationValidator(chain string) (func(Object, []byte) error, bool) {
	switch chain {
	case "ethereum:mainnet", "ethereum:sepolia":
		return validateEthereumAuthorization, true
	case "tron:mainnet", "tron:nile":
		return validateTronAuthorization, true
	default:
		return nil, false
	}
}

func validateTransferAsset(v Object, native, standard string) error {
	amount := str(v, "amountAtomic")
	cap := str(v, "maxNetworkFeeAtomic")
	if !uintPattern.MatchString(amount) || amount == "0" || !uintPattern.MatchString(cap) || !bounded(str(v, "asset"), 128) || str(v, "networkFeeAsset") != native {
		return invalid()
	}
	amountN, _ := new(big.Int).SetString(amount, 10)
	if amountN.BitLen() > 256 {
		return invalid()
	}
	if isNull(v, "tokenStandard") {
		if !isNull(v, "tokenContractCanonical") || !isNull(v, "tokenDecimals") || str(v, "asset") != native {
			return invalid()
		}
	} else if str(v, "tokenStandard") != standard || !evmAddress.MatchString(str(v, "tokenContractCanonical")) || integer(v, "tokenDecimals") < 0 || integer(v, "tokenDecimals") > 255 {
		return invalid()
	}
	return nil
}
