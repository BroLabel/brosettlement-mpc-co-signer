package signingpolicy

import (
	"encoding/hex"
	"strings"
)

// transferCall encodes the transfer(address,uint256) ABI shared by ERC-20 and TRC-20.
func transferCall(address, amount string) []byte {
	addr, e := hex.DecodeString(strings.TrimPrefix(address, "0x"))
	n := number(amount)
	if e != nil || len(addr) != 20 || n == nil || n.Sign() < 0 || n.BitLen() > 256 {
		return nil
	}
	b := make([]byte, 68)
	copy(b, []byte{0xa9, 0x05, 0x9c, 0xbb})
	copy(b[16:36], addr)
	n.FillBytes(b[36:])
	return b
}
