package testingHelpers

import (
	"context"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient/simulated"
	"github.com/stretchr/testify/require"
)

const (
	// DefaultChainID is the chain ID used for testing
	DefaultChainID = int64(1337)
	// DefaultBalance is the default balance for test accounts (100k ETH)
	DefaultBalance = "100000000000000000000000"
)

// Account represents a test account with its auth and balance
type Account struct {
	Auth    *bind.TransactOpts
	Balance *big.Int
}

// SimBackendConfig holds configuration for creating a simulated backend
type SimBackendConfig struct {
	ChainID *big.Int
	// Number of pre-funded accounts to create
	NumAccounts int
	// Optional custom balance for accounts
	Balance *big.Int
}

// NewSimulatedBackendWithAccounts creates a new simulated backend with the specified number of pre-funded accounts
func NewSimulatedBackendWithAccounts(t *testing.T, cfg *SimBackendConfig) (*simulated.Backend, map[common.Address]Account) {
	t.Helper()

	if cfg == nil {
		cfg = &SimBackendConfig{
			ChainID:     big.NewInt(DefaultChainID),
			NumAccounts: 1,
		}
	}

	if cfg.Balance == nil {
		var ok bool
		cfg.Balance, ok = new(big.Int).SetString(DefaultBalance, 10)
		require.True(t, ok, "failed to set default balance")
	}

	accounts := make(map[common.Address]Account)
	ethAccounts := make(map[common.Address]types.Account)

	// Create accounts
	for i := 0; i < cfg.NumAccounts; i++ {
		pk, err := crypto.GenerateKey()
		require.NoError(t, err)

		auth, err := bind.NewKeyedTransactorWithChainID(pk, cfg.ChainID)
		require.NoError(t, err)

		accounts[auth.From] = Account{
			Auth:    auth,
			Balance: cfg.Balance,
		}
		ethAccounts[auth.From] = types.Account{Balance: cfg.Balance}
	}

	// Create and return the backend
	backend := simulated.NewBackend(ethAccounts)
	return backend, accounts
}

// DeployContract is a generic helper to deploy a contract
func DeployContract[T any](
	ctx context.Context,
	t *testing.T,
	backend *simulated.Backend,
	auth *bind.TransactOpts,
	deployFn func(*bind.TransactOpts, bind.ContractBackend) (common.Address, *types.Transaction, T, error),
) (common.Address, T, error) {
	t.Helper()

	addr, tx, contract, err := deployFn(auth, backend.Client())
	if err != nil {
		return common.Address{}, contract, err
	}

	err = waitForTx(ctx, backend, tx)
	return addr, contract, err
}

/* ---------------------------------- Utils --------------------------------- */

// waitForTx waits for a transaction to be mined and indexed
func waitForTx(ctx context.Context, backend *simulated.Backend, tx *types.Transaction) error {
	backend.Commit()
	_, err := bind.WaitMined(ctx, backend.Client(), tx)
	return err
}
