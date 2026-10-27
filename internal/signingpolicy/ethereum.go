package signingpolicy

import (
	"errors"
	"strings"
)

const ethereumPayloadType = "ethereum-transaction"
const ethereumHashAlgorithm = "keccak256"

func isEthereumChain(chain string) bool { return ethereumChainID(chain) != "" }

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

func validateEthereumAddresses(policy *Context) error {
	for _, address := range []struct{ field, value string }{
		{"fromAddress", policy.FromAddress},
		{"toAddress", policy.ToAddress},
	} {
		if !canonicalEVMAddress(address.value) {
			return &AddressValidationError{Field: "payload.policyContext." + address.field, Reason: "noncanonical_address"}
		}
	}
	if policy.TokenStandard != nil && policy.TokenContractCanonical != nil && !canonicalEVMAddress(*policy.TokenContractCanonical) {
		return &AddressValidationError{Field: "payload.policyContext.tokenContractCanonical", Reason: "noncanonical_address"}
	}
	return nil
}

func validateEthereumContext(policy *Context) error {
	if err := validateEthereumAddresses(policy); err != nil {
		return err
	}
	wantChainID := ethereumChainID(policy.Chain)
	if policy.Version != 1 || policy.TransactionType != 2 || wantChainID == "" || policy.ChainID != wantChainID ||
		!canonicalUint(policy.AmountAtomic) || !canonicalUint(policy.Nonce) || !canonicalPositiveUint(policy.GasLimit) ||
		!canonicalPositiveUint(policy.MaxFeePerGas) || !canonicalUint(policy.MaxPriorityFeePerGas) ||
		compareUint(policy.MaxPriorityFeePerGas, policy.MaxFeePerGas) > 0 {
		return errors.New("SIGN claim Ethereum policy context is invalid")
	}
	if policy.TokenStandard == nil {
		if policy.Asset != "ETH" || policy.TokenContractCanonical != nil || policy.TokenDecimals != nil {
			return errors.New("SIGN claim Ethereum native asset context is invalid")
		}
		return nil
	}
	if *policy.TokenStandard != "erc20" || policy.TokenContractCanonical == nil ||
		policy.TokenDecimals == nil || *policy.TokenDecimals < 0 || *policy.TokenDecimals > 255 || policy.Asset == "ETH" {
		return errors.New("SIGN claim Ethereum token context is invalid")
	}
	return nil
}

func validEthereumTuple(tuple Tuple) bool {
	return isEthereumChain(tuple.Chain) && tuple.AddressEncoding == "evm_hex"
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
