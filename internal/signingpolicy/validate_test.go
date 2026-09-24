package signingpolicy

import "testing"

func TestValidateContextRejectsUnboundOrUnsupportedIdentity(t *testing.T) {
	policy := Context{Chain: "tron:mainnet", Asset: "TRX", AmountAtomic: "100", FromAddress: "sender", ToAddress: "recipient"}
	tests := []struct {
		name, chain, payloadType, expectedAddress string
	}{
		{"different network", "tron:nile", "tron-transaction", "sender"},
		{"different sender", "tron:mainnet", "tron-transaction", "other-sender"},
		{"unknown payload", "tron:mainnet", "unknown-transaction", "sender"},
		{"cross protocol", "tron:mainnet", "ethereum-transaction", "sender"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateContext(&policy, tt.chain, tt.payloadType, tt.expectedAddress); err == nil {
				t.Fatal("accepted an unbound or unsupported signing identity")
			}
		})
	}
	if err := ValidateContext(&policy, "tron:mainnet", "tron-transaction", "sender"); err != nil {
		t.Fatal(err)
	}
	policy.Chain = "tron:unknown"
	if err := ValidateContext(&policy, "tron:unknown", "tron-transaction", "sender"); err == nil {
		t.Fatal("accepted an unsupported TRON network")
	}
}
