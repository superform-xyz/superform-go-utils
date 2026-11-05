package txmngr

import (
	"context"
	"math/big"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/superform-xyz/superform-go-utils/evm/ethawskmssigner"
)

// GetTxAuthFromKMS returns transaction authorization for a caller-configured KMS
// client. The context controls public-key retrieval and subsequent signing.
func GetTxAuthFromKMS(ctx context.Context, client ethawskmssigner.KMS, keyID string) (common.Address, func(uint64) (*bind.TransactOpts, error), error) {
	publicKey, err := ethawskmssigner.GetPubKey(ctx, client, keyID)
	if err != nil {
		return common.Address{}, nil, err
	}
	auth := func(chainID uint64) (*bind.TransactOpts, error) {
		return ethawskmssigner.NewAwsKmsTransactorWithChainID(ctx, client, keyID, new(big.Int).SetUint64(chainID))
	}
	return crypto.PubkeyToAddress(*publicKey), auth, nil
}

// GetTxAuthFromPrivateKey returns transaction authorization for a hex-encoded
// private key. The caller owns key configuration and error reporting.
func GetTxAuthFromPrivateKey(privateKeyHex string) (common.Address, func(uint64) (*bind.TransactOpts, error), error) {
	privateKey, err := crypto.HexToECDSA(privateKeyHex)
	if err != nil {
		return common.Address{}, nil, err
	}
	auth := func(chainID uint64) (*bind.TransactOpts, error) {
		return bind.NewKeyedTransactorWithChainID(privateKey, new(big.Int).SetUint64(chainID))
	}
	return crypto.PubkeyToAddress(privateKey.PublicKey), auth, nil
}
