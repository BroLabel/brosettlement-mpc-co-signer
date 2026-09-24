package signingpolicy

import (
	"errors"
	"math/big"
	"strings"
)

const ethereumPayloadType = "ethereum-transaction"

var ethereumPolicyFields = []string{
	"amountAtomic", "asset", "chain", "chainId", "fromAddress", "gasLimit", "maxFeePerGas",
	"maxPriorityFeePerGas", "nonce", "toAddress", "tokenContractCanonical", "tokenDecimals",
	"tokenStandard", "transactionType", "version",
}

func ethereumChainID(chain string) string {
	switch chain {
	case "ethereum:mainnet":
		return "1"
	case "ethereum:sepolia":
		return "11155111"
	default:
		return ""
	}
}

func validateEthereumContext(policy *Context) error {
	wantChainID := ethereumChainID(policy.Chain)
	if policy.Version != 1 || policy.TransactionType != 2 || wantChainID == "" || policy.ChainID != wantChainID ||
		!canonicalUint(policy.AmountAtomic) || !canonicalUint(policy.Nonce) || !canonicalPositiveUint(policy.GasLimit) ||
		!canonicalPositiveUint(policy.MaxFeePerGas) || !canonicalUint(policy.MaxPriorityFeePerGas) ||
		compareUint(policy.MaxPriorityFeePerGas, policy.MaxFeePerGas) > 0 || !canonicalEVMAddress(policy.FromAddress) || !canonicalEVMAddress(policy.ToAddress) {
		return errors.New("SIGN claim Ethereum policy context is invalid")
	}
	if policy.TokenStandard == nil {
		if policy.Asset != "ETH" || policy.TokenContractCanonical != nil || policy.TokenDecimals != nil {
			return errors.New("SIGN claim Ethereum native asset context is invalid")
		}
		return nil
	}
	if *policy.TokenStandard != "erc20" || policy.TokenContractCanonical == nil || !canonicalEVMAddress(*policy.TokenContractCanonical) ||
		policy.TokenDecimals == nil || *policy.TokenDecimals < 0 || *policy.TokenDecimals > 255 || policy.Asset == "ETH" {
		return errors.New("SIGN claim Ethereum token context is invalid")
	}
	return nil
}

func validEthereumTuple(tuple Tuple) bool {
	return ethereumChainID(tuple.Chain) != "" && tuple.HashAlgorithm == "keccak256" && tuple.AddressEncoding == "evm_hex"
}

func canonicalUint(value string) bool {
	if value == "0" {
		return true
	}
	if value == "" || value[0] < '1' || value[0] > '9' {
		return false
	}
	for i := 1; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return true
}

func canonicalPositiveUint(value string) bool { return value != "0" && canonicalUint(value) }

func compareUint(left, right string) int {
	leftInt, leftOK := new(big.Int).SetString(left, 10)
	rightInt, rightOK := new(big.Int).SetString(right, 10)
	if !leftOK || !rightOK {
		return 1
	}
	return leftInt.Cmp(rightInt)
}

func canonicalEVMAddress(value string) bool {
	if len(value) != 42 || !strings.HasPrefix(value, "0x") {
		return false
	}
	for _, character := range value[2:] {
		if character < '0' || character > '9' && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
