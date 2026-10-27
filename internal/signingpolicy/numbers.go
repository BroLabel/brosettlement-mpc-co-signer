package signingpolicy

import "math/big"

// Transaction producers encode these fields using JavaScript safe integers.
const maxProducerSafeInteger int64 = 1<<53 - 1

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
