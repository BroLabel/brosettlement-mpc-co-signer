package signingpolicy

import (
	"encoding/json"
	"errors"
)

// Context is the immutable transaction-policy snapshot authorized by
// the backend for one SIGN intent. It is retained by the co-signer so claim
// validation can bind the cryptographic request to the authorized chain and
// source address.
type Context struct {
	AmountAtomic           string  `json:"amountAtomic"`
	Asset                  string  `json:"asset"`
	Chain                  string  `json:"chain"`
	ChainID                string  `json:"chainId,omitempty"`
	FeeLimitSun            *string `json:"feeLimitSun"`
	FromAddress            string  `json:"fromAddress"`
	GasLimit               string  `json:"gasLimit,omitempty"`
	MaxFeePerGas           string  `json:"maxFeePerGas,omitempty"`
	MaxPriorityFeePerGas   string  `json:"maxPriorityFeePerGas,omitempty"`
	Nonce                  string  `json:"nonce,omitempty"`
	ToAddress              string  `json:"toAddress"`
	TokenContractCanonical *string `json:"tokenContractCanonical"`
	TokenDecimals          *int64  `json:"tokenDecimals"`
	TokenStandard          *string `json:"tokenStandard"`
	TransactionType        uint32  `json:"transactionType,omitempty"`
	Version                uint32  `json:"version,omitempty"`
}

func (c *Context) UnmarshalJSON(raw []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	expected := tronPolicyFields
	if _, ok := fields["version"]; ok {
		expected = ethereumPolicyFields
	}
	if len(fields) != len(expected) {
		return errors.New("SIGN policy context has invalid fields")
	}
	for _, name := range expected {
		if _, ok := fields[name]; !ok {
			return errors.New("SIGN policy context has invalid fields")
		}
	}
	type wire Context
	var decoded wire
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	*c = Context(decoded)
	return nil
}
