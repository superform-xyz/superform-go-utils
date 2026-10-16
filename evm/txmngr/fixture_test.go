package txmngr

import (
	"cmp"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"

	"github.com/superform-xyz/superform-go-utils/evm/txmngr/mock"
	"github.com/superform-xyz/superform-go-utils/evm/txmngr/txmngr_provider"
	"github.com/superform-xyz/superform-go-utils/evm/txmngr/txmngr_provider/mem"
)

// fixture is the (store, client, signer, manager) quadruple every manager test
// needs. Assembling it by hand in each test was the single largest source of
// duplication in this package.
type fixture struct {
	t    *testing.T
	db   txmngr_provider.TransactionsQ
	cl   *mock.EthClient
	auth *bind.TransactOpts
	mngr *txManager
	lane *nonceLane
}

// testRPCError models the structured error returned by go-ethereum's JSON-RPC
// client without coupling tests to its unexported jsonError implementation.
type testRPCError struct {
	code    int
	message string
}

func (e testRPCError) Error() string  { return e.message }
func (e testRPCError) ErrorCode() int { return e.code }

type fixtureOpts struct {
	// seed pre-populates the store, for tests that start from persisted state.
	seed map[string]txmngr_provider.Transaction
	// auth overrides the generated signer, for key-rotation tests.
	auth *bind.TransactOpts
	// sender overrides the manager's sender; defaults to auth.From.
	sender *common.Address
	// client replaces the generated mock, for tests that need a real backend.
	client EthClient
	// noClients builds a manager with no chain clients configured.
	noClients bool
	// nilTimings leaves the bump and block-time maps nil, so the manager falls
	// back to its built-in defaults.
	nilTimings   bool
	bumpInterval time.Duration
	blockTime    time.Duration
	managerOpts  []Option
}

// newSigner returns a fresh keyed transactor for the test chain.
func newSigner(t *testing.T) *bind.TransactOpts {
	t.Helper()

	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	auth, err := bind.NewKeyedTransactorWithChainID(key, big.NewInt(chainId))
	require.NoError(t, err)
	return auth
}

// signLegacy signs a minimal legacy transaction at the given nonce.
func signLegacy(t *testing.T, auth *bind.TransactOpts, nonce uint64) *types.Transaction {
	t.Helper()

	signed, err := auth.Signer(auth.From, types.NewTx(&types.LegacyTx{
		To:       ptr(common.HexToAddress("0x0000000000000000000000000000000000000001")),
		Nonce:    nonce,
		GasPrice: big.NewInt(100),
		Gas:      100_000,
		Value:    big.NewInt(0),
	}))
	require.NoError(t, err)
	return signed
}

// legacyTx builds an unsigned legacy transaction, for tests that only need a
// stable hash.
func legacyTx() *types.Transaction {
	return types.NewTx(&types.LegacyTx{
		To:       ptr(common.Address{}),
		GasPrice: big.NewInt(1),
		Value:    big.NewInt(0),
	})
}

// terminalRow is a settled intent-backed row whose notification is already due.
func terminalRow(id string, at time.Time) txmngr_provider.Transaction {
	intentID := "intent"
	return txmngr_provider.Transaction{
		ID:                 id,
		IntentID:           &intentID,
		Status:             txmngr_provider.SucceedTxStatus,
		NextNotificationAt: at.Add(-time.Second),
		CreatedAt:          at,
		UpdatedAt:          at,
	}
}

func newFixture(t *testing.T, opts fixtureOpts) *fixture {
	t.Helper()

	auth := cmp.Or(opts.auth, newSigner(t))
	sender := auth.From
	if opts.sender != nil {
		sender = *opts.sender
	}
	seed := opts.seed
	if seed == nil {
		seed = make(map[string]txmngr_provider.Transaction)
	}

	f := &fixture{t: t, db: mem.NewTransactionsQ(seed), auth: auth, lane: &nonceLane{}}
	clients := map[uint64]EthClient{}
	switch {
	case opts.client != nil:
		clients[uint64(chainId)] = opts.client
	case !opts.noClients:
		f.cl = mock.NewEthClient(t)
		clients[uint64(chainId)] = f.cl
	}

	var bumpMap, blockMap map[uint64]time.Duration
	if !opts.nilTimings {
		bumpMap = map[uint64]time.Duration{uint64(chainId): cmp.Or(opts.bumpInterval, time.Millisecond)}
		blockMap = map[uint64]time.Duration{uint64(chainId): cmp.Or(opts.blockTime, time.Millisecond)}
	}
	f.mngr = New(
		f.db,
		clients,
		sender,
		bumpMap,
		blockMap,
		func(uint64) (*bind.TransactOpts, error) { return auth, nil },
		opts.managerOpts...,
	).(*txManager)
	return f
}

// get reloads a transaction, failing the test if it is missing.
func (f *fixture) get(id string) *txmngr_provider.Transaction {
	f.t.Helper()

	tx, err := f.db.New().FilterByIds(id).Get()
	require.NoError(f.t, err)
	return tx
}

// seedAttempt persists signed as a durable attempt of transactionID.
func (f *fixture) seedAttempt(transactionID string, signed *types.Transaction, createdAt time.Time) {
	f.t.Helper()

	require.NoError(f.t, f.db.InsertAttempt(txmngr_provider.TransactionAttempt{
		TransactionID: transactionID,
		Hash:          signed.Hash(),
		Nonce:         signed.Nonce(),
		Tx:            signed,
		CreatedAt:     createdAt,
	}))
}

// newRejectedMarkerFixture restores the durable state written after a first
// submission received an explicit rejection but reconciliation did not finish.
func newRejectedMarkerFixture(t *testing.T, id string, nonce uint64) (*fixture, *types.Transaction) {
	t.Helper()

	now := time.Now()
	auth := newSigner(t)
	signed := signLegacy(t, auth, nonce)
	f := newFixture(t, fixtureOpts{auth: auth, seed: map[string]txmngr_provider.Transaction{
		id: {
			ID:                  id,
			ChainID:             uint64(chainId),
			Sender:              auth.From,
			AssignedNonce:       ptr(nonce),
			Tx:                  signed,
			Status:              txmngr_provider.ConfirmingTxStatus,
			Msg:                 "broadcast rejected: gas limit too high",
			RejectedAttemptHash: ptr(signed.Hash().Hex()),
			NextActionAt:        now.Add(-time.Second),
			CreatedAt:           now,
			UpdatedAt:           now,
		},
	}})
	f.seedAttempt(id, signed, now)
	return f, signed
}

// step runs one processing step against the stored row.
func (f *fixture) step(id string) error {
	f.t.Helper()

	return f.mngr.processTransactionStep(f.t.Context(), *f.get(id), f.lane)
}

// stepDue forces the row overdue and runs one step, requiring it to succeed.
func (f *fixture) stepDue(id string) {
	f.t.Helper()

	tx := f.get(id)
	tx.NextActionAt = time.Now().Add(-time.Second)
	require.NoError(f.t, f.db.Insert(*tx))
	require.NoError(f.t, f.mngr.processTransactionStep(f.t.Context(), *tx, f.lane))
}
