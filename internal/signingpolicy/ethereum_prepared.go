package signingpolicy

import (
	"bytes"
	"encoding/hex"
	"math/big"
	"time"

	wire "github.com/BroLabel/brosettlement-mpc-co-signer/internal/contract/approval"
	"github.com/ethereum/go-ethereum/rlp"
	"golang.org/x/crypto/sha3"
)

func verifyEthereumPrepared(raw []byte, prepared *PreparedTransfer, policy *Context, auth wire.Object, _ time.Time) (preparedVerification, error) {
	if prepared.NetworkFeeAsset != "ETH" || !nullEqual(prepared.FeeEvidence, nil) || !nullEqual(auth["tokenStandard"], policy.TokenStandard) {
		return preparedVerification{}, ErrPrepared
	}
	budget, err := verifyEthereumBytes(raw, policy)
	if err != nil {
		return preparedVerification{}, err
	}
	params := authObject(auth, "requestedChainParams")
	if len(params) > 0 {
		p := authObject(params, "ethereum")
		if authString(p, "maxFeePerGas") != policy.MaxFeePerGas || authString(p, "maxPriorityFeePerGas") != policy.MaxPriorityFeePerGas {
			return preparedVerification{}, ErrPrepared
		}
	}
	h := sha3.NewLegacyKeccak256()
	_, _ = h.Write(raw)
	return preparedVerification{digest: h.Sum(nil), budget: budget}, nil
}

type unsignedType2 struct {
	ChainID    *big.Int
	Nonce      *big.Int
	Priority   *big.Int
	MaxFee     *big.Int
	Gas        *big.Int
	To         []byte
	Value      *big.Int
	Data       []byte
	AccessList []rlp.RawValue
}

func verifyEthereumBytes(raw []byte, p *Context) (string, error) {
	var tx unsignedType2
	if len(raw) < 2 || raw[0] != 2 || rlp.DecodeBytes(raw[1:], &tx) != nil {
		return "", ErrPrepared
	}
	encoded, e := rlp.EncodeToBytes(tx)
	if e != nil || !bytes.Equal(encoded, raw[1:]) || len(tx.AccessList) != 0 || len(tx.To) != 20 || tx.ChainID.String() != ethereumChainID(p.Chain) || tx.ChainID.String() != p.ChainID || tx.Nonce.String() != p.Nonce || tx.Gas.String() != p.GasLimit || tx.MaxFee.String() != p.MaxFeePerGas || tx.Priority.String() != p.MaxPriorityFeePerGas || tx.Gas.Sign() <= 0 || tx.MaxFee.Sign() <= 0 || tx.Priority.Cmp(tx.MaxFee) > 0 || tx.Value.BitLen() > 256 || tx.MaxFee.BitLen() > 256 || tx.Priority.BitLen() > 256 || tx.Gas.BitLen() > 64 || tx.Nonce.Cmp(big.NewInt(maxProducerSafeInteger)) > 0 {
		return "", ErrPrepared
	}
	to := "0x" + hex.EncodeToString(tx.To)
	if p.TokenStandard == nil {
		if p.Asset != "ETH" || to != p.ToAddress || tx.Value.String() != p.AmountAtomic || len(tx.Data) != 0 {
			return "", ErrPrepared
		}
	} else {
		if *p.TokenStandard != "erc20" || p.TokenContractCanonical == nil || to != *p.TokenContractCanonical || tx.Value.Sign() != 0 || !bytes.Equal(tx.Data, transferCall(p.ToAddress, p.AmountAtomic)) {
			return "", ErrPrepared
		}
	}
	return new(big.Int).Mul(tx.Gas, tx.MaxFee).String(), nil
}
