package signingpolicy

import "errors"

const tronPayloadType = "tron-transaction"

var tronPolicyFields = []string{
	"amountAtomic", "asset", "chain", "feeLimitSun", "fromAddress", "toAddress",
	"tokenContractCanonical", "tokenDecimals", "tokenStandard",
}

func isTronChain(chain string) bool {
	return chain == "tron:mainnet" || chain == "tron:nile"
}

func validateTronContext(policy *Context) error {
	if !isTronChain(policy.Chain) {
		return errors.New("SIGN claim TRON network is unsupported")
	}
	if policy.Version != 0 || policy.TransactionType != 0 || policy.ChainID != "" || policy.Nonce != "" || policy.GasLimit != "" ||
		policy.MaxFeePerGas != "" || policy.MaxPriorityFeePerGas != "" {
		return errors.New("SIGN claim policy context mismatch")
	}
	return nil
}

func validTronTuple(tuple Tuple) bool {
	return isTronChain(tuple.Chain) && tuple.HashAlgorithm == "sha256" &&
		(tuple.AddressEncoding == "tron_base58" || tuple.AddressEncoding == "base58check" || tuple.AddressEncoding == "tron_base58check")
}
