package txmngr

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient/simulated"
	"github.com/holiman/uint256"
	"github.com/stretchr/testify/assert"
	mock2 "github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	testingHelpers "github.com/superform-xyz/superform-go-utils/evm/internal/testutil"
	"github.com/superform-xyz/superform-go-utils/evm/txmngr/test"
	"github.com/superform-xyz/superform-go-utils/evm/txmngr/txmngr_provider"
)

const chainId = int64(1337)

var (
	simpleStorageABI *abi.ABI
)

func init() {
	var err error
	if simpleStorageABI, err = test.SimpleStorageMetaData.GetAbi(); err != nil {
		panic(err)
	}
}

// createSimulatedBackend creates a simulated backend with the basic setup
func createSimulatedBackend(t *testing.T) (*simulated.Backend, common.Address, *test.SimpleStorage, *bind.TransactOpts) {
	t.Helper()

	// Create simulated backend with 2 accounts
	cfg := &testingHelpers.SimBackendConfig{
		ChainID:     big.NewInt(testingHelpers.DefaultChainID),
		NumAccounts: 2,
	}
	backend, accounts := testingHelpers.NewSimulatedBackendWithAccounts(t, cfg)

	// Get the two accounts
	var userAuth, deployerAuth *bind.TransactOpts
	for _, acc := range accounts {
		if userAuth == nil {
			userAuth = acc.Auth
		} else {
			deployerAuth = acc.Auth
		}
	}

	// Deploy SimpleStorage contract
	addr, contract, err := testingHelpers.DeployContract(
		context.Background(),
		t,
		backend,
		deployerAuth,
		test.DeploySimpleStorage,
	)
	require.NoError(t, err)

	// Get the current state
	val, err := contract.Get(&bind.CallOpts{})
	require.NoError(t, err)
	require.Equal(t, "0", val.String())

	return backend, addr, contract, userAuth
}

func Test_txMngr_Stress(t *testing.T) {
	const blockTime = time.Millisecond * 100

	backend, ssAddr, ss, userAuth := createSimulatedBackend(t)

	// Commit blocks every 100 milliseconds. Stopping the committer and waiting
	// for it to finish keeps it off the backend once cleanup closes it.
	stopCommitting, committed := make(chan struct{}), make(chan struct{})
	defer func() { close(stopCommitting); <-committed }()
	go func() {
		defer close(committed)
		for {
			select {
			case <-stopCommitting:
				return
			case <-time.After(blockTime):
				backend.Commit()
			}
		}
	}()

	// Create transaction manager
	mngr := newFixture(t, fixtureOpts{
		auth:         userAuth,
		client:       backend.Client(),
		bumpInterval: blockTime * 3, // Bump after 3 blocks
		blockTime:    blockTime,
	}).mngr

	// Start transaction manager
	mngr.Start(context.Background())
	defer mngr.Stop()

	var wg sync.WaitGroup

	// Send 1000 transactions
	ids := make([]string, 100)
	for i := int64(1); i <= 100; i++ {
		wg.Add(1)
		go func(i int64) {
			defer wg.Done()

			// Prepare tx data
			data, err := simpleStorageABI.Pack("add", big.NewInt(100*i))
			require.NoError(t, err)

			// Send transaction
			ids[i-1], err = mngr.SendTxAsync(context.Background(), uint64(chainId), ssAddr, data, 900000, big.NewInt(0), nil, nil)
			require.NoError(t, err)
		}(i)
	}
	wg.Wait()

	// Wait for the transactions to be mined
	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()

			tx, err := mngr.WaitTxCompleted(context.Background(), id)
			require.NoError(t, err)
			require.Equal(t, txmngr_provider.SucceedTxStatus, tx.Status)
			require.Equal(t, uint64(1), tx.Receipt.Status)
		}(id)
	}
	wg.Wait()

	// Get the state after
	val, err := ss.Get(&bind.CallOpts{})
	require.NoError(t, err)
	require.Equal(t, "505000", val.String())
}

func TestProcessTxsCallsStatusCallbackAfterPersistingTerminalStatus(t *testing.T) {
	t.Parallel()

	callbackCh := make(chan txmngr_provider.Transaction, 1)
	f := newFixture(t, fixtureOpts{
		bumpInterval: time.Second,
		managerOpts: []Option{WithOnStatusChange(func(_ context.Context, tx txmngr_provider.Transaction) error {
			callbackCh <- tx
			return nil
		})},
	})
	intentID := "0xintent"
	toAddr := common.HexToAddress("0x0000000000000000000000000000000000000001")
	f.cl.On("PendingNonceAt", mock2.Anything, f.auth.From).Once().Return(uint64(0), nil)
	f.cl.On("SuggestGasPrice", mock2.Anything).Once().Return(big.NewInt(100), nil)
	f.cl.On("SendTransaction", mock2.Anything, mock2.Anything).Once().
		Run(func(args mock2.Arguments) {
			sent := args.Get(1).(*types.Transaction)
			require.Equal(t, []byte("test"), sent.Data())
			require.Equal(t, toAddr, *sent.To())
			require.EqualValues(t, 115, sent.GasPrice().Uint64(), "the first attempt bumps the suggestion by 15%")
		}).Return(nil)
	f.cl.On("TransactionReceipt", mock2.Anything, mock2.Anything).
		Return(func(_ context.Context, hash common.Hash) (*types.Receipt, error) {
			return &types.Receipt{TxHash: hash, Status: types.ReceiptStatusSuccessful}, nil
		})
	txID, err := f.mngr.SendTxAsync(t.Context(), uint64(chainId), toAddr, []byte("test"), 21_000, big.NewInt(0), nil, &intentID)
	require.NoError(t, err)

	// The first pass signs and broadcasts; a hash accepted microseconds ago
	// cannot be mined yet, so the receipt is picked up on the following pass.
	f.mngr.processTxs(t.Context())
	require.Eventually(t, func() bool {
		f.mngr.processTxs(t.Context())
		return f.get(txID).IsCompleted()
	}, time.Second, 5*time.Millisecond, "transaction never reached a terminal status")

	f.mngr.deliverStatusNotifications(t.Context())

	select {
	case tx := <-callbackCh:
		require.Equal(t, txID, tx.ID)
		require.Equal(t, txmngr_provider.SucceedTxStatus, tx.Status)
		require.Equal(t, intentID, *tx.IntentID)
		require.Equal(t, tx.Status, f.get(txID).Status, "the callback must observe the persisted status")
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for tx status callback")
	}
}

func TestFixedNonceSurvivesDelayedReceiptAndNonceTooLow(t *testing.T) {
	t.Parallel()

	const sourceNonce = uint64(468)
	toAddr := common.HexToAddress("0x0000000000000000000000000000000000000001")
	f := newFixture(t, fixtureOpts{})
	f.cl.On("PendingNonceAt", mock2.Anything, f.auth.From).Once().Return(sourceNonce, nil)
	f.cl.On("SuggestGasPrice", mock2.Anything).Twice().Return(big.NewInt(100), nil)

	var sent []*types.Transaction
	f.cl.On("SendTransaction", mock2.Anything, mock2.Anything).
		Twice().
		Return(func(_ context.Context, tx *types.Transaction) error {
			sent = append(sent, tx)
			if len(sent) == 2 {
				return errors.New("nonce too low")
			}
			return nil
		})

	revealFirstReceipt := false
	f.cl.On("TransactionReceipt", mock2.Anything, mock2.Anything).
		Maybe().
		Return(func(_ context.Context, hash common.Hash) (*types.Receipt, error) {
			if revealFirstReceipt && len(sent) > 0 && hash == sent[0].Hash() {
				return &types.Receipt{TxHash: hash, Status: types.ReceiptStatusSuccessful}, nil
			}
			return nil, ethereum.NotFound
		})

	txID, err := f.mngr.SendTxAsync(t.Context(), uint64(chainId), toAddr, []byte("handleOps"), 100_000, big.NewInt(0), nil, nil)
	require.NoError(t, err)
	require.NoError(t, f.step(txID))

	time.Sleep(2 * time.Millisecond)
	f.stepDue(txID)

	tx := f.get(txID)
	require.Equal(t, txmngr_provider.ConfirmingTxStatus, tx.Status)
	require.NotNil(t, tx.NonceConsumedAt)
	require.Len(t, sent, 2)
	require.Equal(t, sourceNonce, sent[0].Nonce())
	require.Equal(t, sourceNonce, sent[1].Nonce())
	attempts, err := f.db.SelectAttempts(txID)
	require.NoError(t, err)
	require.Len(t, attempts, 2)

	// Even when the consumed nonce's receipt is still unavailable, the logical
	// transaction remains confirming and must never be replayed at nonce 469.
	f.stepDue(txID)
	require.Len(t, sent, 2, "nonce consumption must disable every later send")

	revealFirstReceipt = true
	f.stepDue(txID)

	completed := f.get(txID)
	require.Equal(t, txmngr_provider.SucceedTxStatus, completed.Status)
	require.Equal(t, sent[0].Hash(), completed.Tx.Hash())
	require.Equal(t, sent[0].Hash(), completed.Receipt.TxHash)
	require.Len(t, sent, 2, "nonce-too-low must never trigger a third send at a new nonce")
	f.cl.AssertNumberOfCalls(t, "PendingNonceAt", 1)
}

func TestAmbiguousSendRetriesExactPersistedHash(t *testing.T) {
	t.Parallel()

	f := newFixture(t, fixtureOpts{})
	f.cl.On("PendingNonceAt", mock2.Anything, f.auth.From).Once().Return(uint64(23), nil)
	f.cl.On("SuggestGasPrice", mock2.Anything).Once().Return(big.NewInt(100), nil)
	var sent []*types.Transaction
	f.cl.On("SendTransaction", mock2.Anything, mock2.Anything).Times(3).
		Return(func(_ context.Context, tx *types.Transaction) error {
			sent = append(sent, tx)
			if len(sent) < 3 {
				return errors.New("temporary upstream gateway failure")
			}
			return nil
		})
	f.cl.On("TransactionReceipt", mock2.Anything, mock2.Anything).Maybe().Return(nil, ethereum.NotFound)

	txID, err := f.mngr.SendTxAsync(t.Context(), uint64(chainId), common.Address{}, []byte("payload"), 21_000, big.NewInt(0), nil, nil)
	require.NoError(t, err)
	for range 3 {
		f.stepDue(txID)
	}

	require.Len(t, sent, 3)
	require.Equal(t, sent[0].Hash(), sent[1].Hash())
	require.Equal(t, sent[0].Hash(), sent[2].Hash())
	attempts, err := f.db.SelectAttempts(txID)
	require.NoError(t, err)
	require.Len(t, attempts, 1, "ambiguous sends must not manufacture fee replacements")
	require.False(t, f.get(txID).BroadcastPending)
}

func TestRejectedFirstSubmissionReleasesTipNonceAndNextRowReusesIt(t *testing.T) {
	t.Parallel()

	const nonce = uint64(31)
	now := time.Now()
	auth := newSigner(t)
	first := txmngr_provider.Transaction{
		ID:           "rejected",
		ChainID:      uint64(chainId),
		Sender:       auth.From,
		Address:      common.HexToAddress("0x0000000000000000000000000000000000000001"),
		Value:        big.NewInt(0),
		GasLimit:     100_000,
		Status:       txmngr_provider.PendingTxStatus,
		NextActionAt: now.Add(-2 * time.Second),
		CreatedAt:    now.Add(-2 * time.Second),
		UpdatedAt:    now,
	}
	second := first
	second.ID = "next"
	second.Data = []byte("next logical operation")
	second.NextActionAt = now.Add(-time.Second)
	second.CreatedAt = now.Add(-time.Second)

	f := newFixture(t, fixtureOpts{
		auth: auth,
		seed: map[string]txmngr_provider.Transaction{first.ID: first, second.ID: second},
	})
	f.cl.On("PendingNonceAt", mock2.Anything, auth.From).Times(3).Return(nonce, nil)
	f.cl.On("SuggestGasPrice", mock2.Anything).Twice().Return(big.NewInt(100), nil)
	f.cl.On("TransactionReceipt", mock2.Anything, mock2.Anything).Once().Return(nil, ethereum.NotFound)
	f.cl.On("TransactionByHash", mock2.Anything, mock2.Anything).Once().Return(nil, false, ethereum.NotFound)

	var sent []*types.Transaction
	f.cl.On("SendTransaction", mock2.Anything, mock2.Anything).Twice().
		Return(func(_ context.Context, tx *types.Transaction) error {
			sent = append(sent, tx)
			if len(sent) == 1 {
				return testRPCError{code: -32000, message: "gas limit too high: have 17597473, max 16777216"}
			}
			return nil
		})

	f.mngr.processTxs(t.Context())

	rejected := f.get(first.ID)
	require.Equal(t, txmngr_provider.ErroredTxStatus, rejected.Status)
	require.Nil(t, rejected.AssignedNonce, "a proven-unadmitted tip nonce must be released")
	require.False(t, rejected.BroadcastPending)
	require.Equal(t, txmngr_provider.PendingTxStatus, f.get(second.ID).Status)
	require.Nil(t, f.get(second.ID).AssignedNonce, "the batch must reload before reusing a released nonce")
	attempts, err := f.db.SelectAttempts(first.ID)
	require.NoError(t, err)
	require.Len(t, attempts, 1, "the rejected signed envelope remains immutable audit evidence")
	require.Equal(t, nonce, attempts[0].Nonce)

	f.mngr.processTxs(t.Context())

	require.Len(t, sent, 2)
	require.Equal(t, nonce, sent[0].Nonce())
	require.Equal(t, nonce, sent[1].Nonce(), "the next row must reuse the released nonce")
	require.NotEqual(t, sent[0].Hash(), sent[1].Hash(), "different logical operations must retain distinct hash ownership")
	require.Equal(t, txmngr_provider.ConfirmingTxStatus, f.get(second.ID).Status)
	secondAttempts, err := f.db.SelectAttempts(second.ID)
	require.NoError(t, err)
	require.Len(t, secondAttempts, 1)
}

func TestPermanentBroadcastRejectionMessageIsBounded(t *testing.T) {
	t.Parallel()

	const nonce = uint64(41)
	now := time.Now()
	auth := newSigner(t)
	row := txmngr_provider.Transaction{
		ID:           "long-permanent-rejection",
		ChainID:      uint64(chainId),
		Sender:       auth.From,
		Address:      common.HexToAddress("0x0000000000000000000000000000000000000001"),
		Value:        big.NewInt(0),
		GasLimit:     100_000,
		Status:       txmngr_provider.PendingTxStatus,
		NextActionAt: now.Add(-time.Second),
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	f := newFixture(t, fixtureOpts{auth: auth, seed: map[string]txmngr_provider.Transaction{row.ID: row}})
	f.cl.On("PendingNonceAt", mock2.Anything, auth.From).Twice().Return(nonce, nil)
	f.cl.On("SuggestGasPrice", mock2.Anything).Once().Return(big.NewInt(100), nil)
	f.cl.On("SendTransaction", mock2.Anything, mock2.Anything).Once().
		Return(testRPCError{code: -32000, message: "gas limit too high: " + strings.Repeat("z", 1024)})
	f.cl.On("TransactionReceipt", mock2.Anything, mock2.Anything).Once().Return(nil, ethereum.NotFound)
	f.cl.On("TransactionByHash", mock2.Anything, mock2.Anything).Once().Return(nil, false, ethereum.NotFound)

	f.mngr.processTxs(t.Context())

	stored := f.get(row.ID)
	require.Equal(t, txmngr_provider.ErroredTxStatus, stored.Status)
	require.LessOrEqual(t, len(stored.Msg), maxTransactionMessageBytes)
	require.True(t, utf8.ValidString(stored.Msg))
	attempts, err := f.db.SelectAttempts(row.ID)
	require.NoError(t, err)
	require.Len(t, attempts, 1)
	require.Contains(t, stored.Msg, attempts[0].Hash.Hex())
	require.Contains(t, stored.Msg, "gas limit too high")
	require.True(t, strings.HasSuffix(stored.Msg, strings.Repeat("z", 64)))
	require.Nil(t, stored.RejectedAttemptHash)
}

func TestLongPermanentRejectionKeepsIdentityAcrossReceiptRetry(t *testing.T) {
	t.Parallel()

	const nonce = uint64(42)
	now := time.Now()
	auth := newSigner(t)
	row := txmngr_provider.Transaction{
		ID:           "long-rejection-receipt-retry",
		ChainID:      uint64(chainId),
		Sender:       auth.From,
		Address:      common.HexToAddress("0x0000000000000000000000000000000000000001"),
		Value:        big.NewInt(0),
		GasLimit:     100_000,
		Status:       txmngr_provider.PendingTxStatus,
		NextActionAt: now.Add(-time.Second),
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	f := newFixture(t, fixtureOpts{auth: auth, seed: map[string]txmngr_provider.Transaction{row.ID: row}})
	f.cl.On("PendingNonceAt", mock2.Anything, auth.From).Twice().Return(nonce, nil)
	f.cl.On("SuggestGasPrice", mock2.Anything).Once().Return(big.NewInt(100), nil)
	f.cl.On("SendTransaction", mock2.Anything, mock2.Anything).Once().
		Return(testRPCError{code: -32000, message: "gas limit too high: " + strings.Repeat("z", 1024)})
	f.cl.On("TransactionReceipt", mock2.Anything, mock2.Anything).Once().
		Return(nil, errors.New("receipt lookup temporarily unavailable"))

	f.mngr.processTxs(t.Context())

	retrying := f.get(row.ID)
	require.Equal(t, txmngr_provider.ConfirmingTxStatus, retrying.Status)
	require.LessOrEqual(t, len(retrying.Msg), maxTransactionMessageBytes)
	require.True(t, utf8.ValidString(retrying.Msg))
	require.NotNil(t, retrying.RejectedAttemptHash)
	require.Contains(t, retrying.Msg, *retrying.RejectedAttemptHash)
	require.Contains(t, retrying.Msg, "gas limit too high")
	require.Contains(t, retrying.Msg, reconciliationSeparator)
	require.Contains(t, retrying.Msg, "rpc transport error")

	retrying.NextActionAt = time.Now().Add(-time.Second)
	require.NoError(t, f.db.Insert(*retrying))
	f.cl.On("TransactionReceipt", mock2.Anything, mock2.Anything).Once().Return(nil, ethereum.NotFound)
	f.cl.On("TransactionByHash", mock2.Anything, mock2.Anything).Once().Return(nil, false, ethereum.NotFound)
	f.mngr.processTxs(t.Context())

	terminal := f.get(row.ID)
	require.Equal(t, txmngr_provider.ErroredTxStatus, terminal.Status)
	require.LessOrEqual(t, len(terminal.Msg), maxTransactionMessageBytes)
	require.True(t, utf8.ValidString(terminal.Msg))
	require.Contains(t, terminal.Msg, *retrying.RejectedAttemptHash)
	require.Contains(t, terminal.Msg, "gas limit too high")
	require.Contains(t, terminal.Msg, "rpc transport error")
	require.Nil(t, terminal.RejectedAttemptHash)
}

func TestRejectedFirstSubmissionResumesReconciliationAfterRestart(t *testing.T) {
	t.Parallel()

	const nonce = uint64(32)
	now := time.Now()
	auth := newSigner(t)
	f := newFixture(t, fixtureOpts{auth: auth, seed: map[string]txmngr_provider.Transaction{
		"reconcile-after-restart": {
			ID:           "reconcile-after-restart",
			ChainID:      uint64(chainId),
			Sender:       auth.From,
			Address:      common.HexToAddress("0x0000000000000000000000000000000000000001"),
			Value:        big.NewInt(0),
			GasLimit:     100_000,
			Status:       txmngr_provider.PendingTxStatus,
			NextActionAt: now.Add(-time.Second),
			CreatedAt:    now,
			UpdatedAt:    now,
		},
	}})
	f.cl.On("PendingNonceAt", mock2.Anything, auth.From).Twice().Return(nonce, nil)
	f.cl.On("SuggestGasPrice", mock2.Anything).Once().Return(big.NewInt(100), nil)
	f.cl.On("SendTransaction", mock2.Anything, mock2.Anything).Once().
		Return(testRPCError{code: -32000, message: "gas limit too high: have 17597473, max 16777216"})
	f.cl.On("TransactionReceipt", mock2.Anything, mock2.Anything).Twice().Return(nil, ethereum.NotFound)
	f.cl.On("TransactionByHash", mock2.Anything, mock2.Anything).Once().
		Return(nil, false, errors.New("transaction lookup temporarily unavailable"))
	f.cl.On("TransactionByHash", mock2.Anything, mock2.Anything).Once().
		Return(nil, false, ethereum.NotFound)

	f.mngr.processTxs(t.Context())

	pending := f.get("reconcile-after-restart")
	require.Equal(t, txmngr_provider.ConfirmingTxStatus, pending.Status)
	require.NotNil(t, pending.RejectedAttemptHash, "explicit rejection evidence must survive failed reconciliation")
	require.NotNil(t, pending.AssignedNonce)

	pending.NextActionAt = time.Now().Add(-time.Second)
	require.NoError(t, f.db.Insert(*pending))
	restarted := New(
		f.db,
		map[uint64]EthClient{uint64(chainId): f.cl},
		auth.From,
		map[uint64]time.Duration{uint64(chainId): time.Millisecond},
		map[uint64]time.Duration{uint64(chainId): time.Millisecond},
		func(uint64) (*bind.TransactOpts, error) { return auth, nil },
	).(*txManager)
	restarted.processTxs(t.Context())

	stored := f.get("reconcile-after-restart")
	require.Equal(t, txmngr_provider.ErroredTxStatus, stored.Status)
	require.Nil(t, stored.AssignedNonce)
	require.Nil(t, stored.RejectedAttemptHash)
	f.cl.AssertNumberOfCalls(t, "SendTransaction", 1)
}

func TestRejectedMarkerKnownHashPreservesNonceAfterRestart(t *testing.T) {
	t.Parallel()

	const (
		id    = "rejected-marker-known"
		nonce = uint64(34)
	)
	f, signed := newRejectedMarkerFixture(t, id, nonce)
	staleParent := f.get(id)
	staleParent.Tx = nil
	require.NoError(t, f.db.Insert(*staleParent))
	f.cl.On("TransactionReceipt", mock2.Anything, signed.Hash()).Once().Return(nil, ethereum.NotFound)
	f.cl.On("TransactionByHash", mock2.Anything, signed.Hash()).Once().Return(signed, true, nil)

	require.NoError(t, f.step(id))

	stored := f.get(id)
	require.Equal(t, txmngr_provider.ConfirmingTxStatus, stored.Status)
	require.Equal(t, ptr(nonce), stored.AssignedNonce)
	require.Equal(t, signed.Hash(), stored.Tx.Hash())
	require.Nil(t, stored.RejectedAttemptHash)
	require.Nil(t, stored.NonceConsumedAt)
	require.False(t, stored.BroadcastPending)
	f.cl.AssertNotCalled(t, "PendingNonceAt", mock2.Anything, mock2.Anything)
	f.cl.AssertNotCalled(t, "SendTransaction", mock2.Anything, mock2.Anything)
}

func TestRejectedMarkerAdvancedNonceBecomesConsumedAfterRestart(t *testing.T) {
	t.Parallel()

	const (
		id    = "rejected-marker-consumed"
		nonce = uint64(35)
	)
	f, signed := newRejectedMarkerFixture(t, id, nonce)
	f.cl.On("TransactionReceipt", mock2.Anything, signed.Hash()).Once().Return(nil, ethereum.NotFound)
	f.cl.On("TransactionByHash", mock2.Anything, signed.Hash()).Once().Return(nil, false, ethereum.NotFound)
	f.cl.On("PendingNonceAt", mock2.Anything, f.auth.From).Once().Return(nonce+1, nil)

	require.NoError(t, f.step(id))

	stored := f.get(id)
	require.Equal(t, txmngr_provider.ConfirmingTxStatus, stored.Status)
	require.Equal(t, ptr(nonce), stored.AssignedNonce)
	require.Nil(t, stored.RejectedAttemptHash)
	require.NotNil(t, stored.NonceConsumedAt)
	require.False(t, stored.BroadcastPending)
	f.cl.AssertNotCalled(t, "SendTransaction", mock2.Anything, mock2.Anything)
}

func TestRejectedMarkerLaggingNonceRemainsReservedAfterRestart(t *testing.T) {
	t.Parallel()

	const (
		id    = "rejected-marker-node-behind"
		nonce = uint64(36)
	)
	f, signed := newRejectedMarkerFixture(t, id, nonce)
	f.cl.On("TransactionReceipt", mock2.Anything, signed.Hash()).Once().Return(nil, ethereum.NotFound)
	f.cl.On("TransactionByHash", mock2.Anything, signed.Hash()).Once().Return(nil, false, ethereum.NotFound)
	f.cl.On("PendingNonceAt", mock2.Anything, f.auth.From).Once().Return(nonce-1, nil)

	require.NoError(t, f.step(id))

	stored := f.get(id)
	require.Equal(t, txmngr_provider.ConfirmingTxStatus, stored.Status)
	require.Equal(t, ptr(nonce), stored.AssignedNonce)
	require.Equal(t, ptr(signed.Hash().Hex()), stored.RejectedAttemptHash)
	require.Nil(t, stored.NonceConsumedAt)
	require.False(t, stored.BroadcastPending)
	f.cl.AssertNotCalled(t, "SendTransaction", mock2.Anything, mock2.Anything)
}

func TestRejectedMarkerReceiptErrorDefersReconciliationAfterRestart(t *testing.T) {
	t.Parallel()

	const (
		id    = "rejected-marker-receipt-error"
		nonce = uint64(37)
	)
	f, signed := newRejectedMarkerFixture(t, id, nonce)
	f.cl.On("TransactionReceipt", mock2.Anything, signed.Hash()).Once().
		Return(nil, errors.New("receipt lookup temporarily unavailable"))

	require.NoError(t, f.step(id))

	stored := f.get(id)
	require.Equal(t, txmngr_provider.ConfirmingTxStatus, stored.Status)
	require.Equal(t, ptr(nonce), stored.AssignedNonce)
	require.Equal(t, ptr(signed.Hash().Hex()), stored.RejectedAttemptHash)
	require.Regexp(t, "^broadcast rejected: gas limit too high; reconciliation:", stored.Msg,
		"receipt errors must retain the permanent rejection as the primary reason")
	require.Contains(t, stored.Msg, "rpc transport error")
	require.NotContains(t, stored.Msg, "receipt lookup temporarily unavailable",
		"unstructured transport details must not be persisted")
	require.False(t, stored.BroadcastPending)
	f.cl.AssertNotCalled(t, "TransactionByHash", mock2.Anything, mock2.Anything)
	f.cl.AssertNotCalled(t, "PendingNonceAt", mock2.Anything, mock2.Anything)
	f.cl.AssertNotCalled(t, "SendTransaction", mock2.Anything, mock2.Anything)

	stored.NextActionAt = time.Now().Add(-time.Second)
	require.NoError(t, f.db.Insert(*stored))
	f.cl.On("TransactionReceipt", mock2.Anything, signed.Hash()).Once().Return(nil, ethereum.NotFound)
	f.cl.On("TransactionByHash", mock2.Anything, signed.Hash()).Once().Return(nil, false, ethereum.NotFound)
	f.cl.On("PendingNonceAt", mock2.Anything, f.auth.From).Once().Return(nonce, nil)

	require.ErrorIs(t, f.step(id), errLaneStateChanged)

	terminal := f.get(id)
	require.Equal(t, txmngr_provider.ErroredTxStatus, terminal.Status)
	require.Regexp(t, "^broadcast rejected: gas limit too high; reconciliation:", terminal.Msg,
		"terminalization must not replace the rejection with a transient lookup error")
}

func TestRejectedMarkerReceiptErrorsKeepPersistedMessageBounded(t *testing.T) {
	t.Parallel()

	const (
		id      = "rejected-marker-bounded-message"
		nonce   = uint64(38)
		retries = 20
	)
	f, signed := newRejectedMarkerFixture(t, id, nonce)
	f.cl.On("TransactionReceipt", mock2.Anything, signed.Hash()).Times(retries).
		Return(nil, errors.New("receipt lookup temporarily unavailable"))

	for range retries {
		require.NoError(t, f.step(id))
		stored := f.get(id)
		require.LessOrEqual(t, len(stored.Msg), maxTransactionMessageBytes)
		require.True(t, utf8.ValidString(stored.Msg))
		require.Equal(t, ptr(signed.Hash().Hex()), stored.RejectedAttemptHash)
	}

	stored := f.get(id)
	require.True(t, strings.HasPrefix(stored.Msg, "broadcast rejected: gas limit too high; reconciliation:"))
	require.Contains(t, stored.Msg, "rpc transport error")
	f.cl.AssertNotCalled(t, "TransactionByHash", mock2.Anything, mock2.Anything)
	f.cl.AssertNotCalled(t, "PendingNonceAt", mock2.Anything, mock2.Anything)
	f.cl.AssertNotCalled(t, "SendTransaction", mock2.Anything, mock2.Anything)

	f.cl.On("TransactionReceipt", mock2.Anything, signed.Hash()).Once().Return(nil, ethereum.NotFound)
	f.cl.On("TransactionByHash", mock2.Anything, signed.Hash()).Once().Return(nil, false, ethereum.NotFound)
	f.cl.On("PendingNonceAt", mock2.Anything, f.auth.From).Once().Return(nonce, nil)
	require.ErrorIs(t, f.step(id), errLaneStateChanged)

	terminal := f.get(id)
	require.Equal(t, txmngr_provider.ErroredTxStatus, terminal.Status)
	require.LessOrEqual(t, len(terminal.Msg), maxTransactionMessageBytes)
	require.True(t, strings.HasPrefix(terminal.Msg, "broadcast rejected: gas limit too high; reconciliation:"),
		"bounded retries must retain the permanent rejection as the primary reason")
}

func TestRejectedMarkerPendingNonceErrorKeepsMessageBounded(t *testing.T) {
	t.Parallel()

	const (
		id    = "rejected-marker-bounded-pending-nonce"
		nonce = uint64(39)
	)
	f, signed := newRejectedMarkerFixture(t, id, nonce)
	f.cl.On("TransactionReceipt", mock2.Anything, signed.Hash()).Once().Return(nil, ethereum.NotFound)
	f.cl.On("TransactionByHash", mock2.Anything, signed.Hash()).Once().Return(nil, false, ethereum.NotFound)
	f.cl.On("PendingNonceAt", mock2.Anything, f.auth.From).Once().
		Return(uint64(0), testRPCError{code: -32000, message: strings.Repeat("z", 1024)})

	require.NoError(t, f.step(id))

	stored := f.get(id)
	require.Len(t, stored.Msg, maxTransactionMessageBytes)
	require.True(t, strings.HasPrefix(stored.Msg, "broadcast rejected: gas limit too high; reconciliation:"))
	require.True(t, strings.HasSuffix(stored.Msg, strings.Repeat("z", 128)))
	require.Equal(t, ptr(signed.Hash().Hex()), stored.RejectedAttemptHash)
	require.False(t, stored.BroadcastPending)
	f.cl.AssertNotCalled(t, "SendTransaction", mock2.Anything, mock2.Anything)
}

func TestRejectedMarkerReceiptErrorDoesNotBypassIntegrityChecks(t *testing.T) {
	t.Parallel()

	const nonce = uint64(40)
	tests := []struct {
		name              string
		want              string
		wrongNonce        bool
		wrongStoredSender bool
		rotatedManager    bool
		wrongMarker       bool
	}{
		{name: "stored nonce", want: "assigned nonce", wrongNonce: true},
		{name: "stored sender", want: "persisted sender", wrongStoredSender: true},
		{name: "manager sender", want: "differs from manager sender", rotatedManager: true},
		{name: "rejection marker", want: "persisted rejection does not match", wrongMarker: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			attemptAuth := newSigner(t)
			managerAuth := attemptAuth
			signed := signLegacy(t, attemptAuth, nonce)
			rejectedHash := signed.Hash().Hex()
			row := txmngr_provider.Transaction{
				ID:                  "rejected-integrity-" + test.name,
				ChainID:             uint64(chainId),
				Sender:              attemptAuth.From,
				AssignedNonce:       ptr(nonce),
				Tx:                  signed,
				Status:              txmngr_provider.ConfirmingTxStatus,
				Msg:                 "broadcast rejected: gas limit too high",
				RejectedAttemptHash: &rejectedHash,
				NextActionAt:        time.Now().Add(-time.Second),
				CreatedAt:           time.Now(),
				UpdatedAt:           time.Now(),
			}
			if test.wrongNonce {
				row.AssignedNonce = ptr(nonce + 1)
			}
			if test.wrongStoredSender {
				row.Sender = newSigner(t).From
			}
			if test.rotatedManager {
				managerAuth = newSigner(t)
			}
			if test.wrongMarker {
				wrongHash := common.HexToHash("0xdead").Hex()
				row.RejectedAttemptHash = &wrongHash
			}

			f := newFixture(t, fixtureOpts{auth: managerAuth, seed: map[string]txmngr_provider.Transaction{row.ID: row}})
			f.seedAttempt(row.ID, signed, row.CreatedAt)
			f.cl.On("TransactionReceipt", mock2.Anything, signed.Hash()).Once().
				Return(nil, errors.New("receipt lookup temporarily unavailable"))

			err := f.step(row.ID)
			require.ErrorIs(t, err, errTransactionUnprocessable)
			require.Contains(t, err.Error(), test.want)

			stored := f.get(row.ID)
			require.Equal(t, row.Sender, stored.Sender)
			require.Equal(t, row.AssignedNonce, stored.AssignedNonce)
			require.Equal(t, row.RejectedAttemptHash, stored.RejectedAttemptHash)
			require.Equal(t, row.Msg, stored.Msg)
			f.cl.AssertNotCalled(t, "TransactionByHash", mock2.Anything, mock2.Anything)
			f.cl.AssertNotCalled(t, "PendingNonceAt", mock2.Anything, mock2.Anything)
			f.cl.AssertNotCalled(t, "SendTransaction", mock2.Anything, mock2.Anything)
		})
	}
}

func TestAppendReconciliationErrorKeepsBoundedUTF8Tail(t *testing.T) {
	t.Parallel()

	require.Equal(t, "original; reconciliation: latest",
		appendReconciliationError("original", errors.New("latest")))
	require.Equal(t, "ab; reconciliation: latest",
		appendReconciliationError(string([]byte{'a', 0xff, 'b'}), errors.New("latest")))
	require.Equal(t, strings.Repeat("x", maxTransactionMessageBytes),
		appendReconciliationError("", errors.New(strings.Repeat("x", maxTransactionMessageBytes+100))))

	message := strings.Repeat("界", maxTransactionMessageBytes)
	for retry := range 1800 {
		latest := fmt.Sprintf("retry %04d: %s", retry, strings.Repeat("界", 40))
		message = appendReconciliationError(message, errors.New(latest))
		require.LessOrEqual(t, len(message), maxTransactionMessageBytes)
		require.True(t, utf8.ValidString(message))
		require.True(t, strings.HasSuffix(message, latest))
	}
}

func TestReleasedNonceHashCollisionIsResignedWithoutSharingOwnership(t *testing.T) {
	t.Parallel()

	const nonce = uint64(33)
	now := time.Now()
	auth := newSigner(t)
	address := common.HexToAddress("0x0000000000000000000000000000000000000001")
	conflicting, err := auth.Signer(auth.From, types.NewTx(&types.LegacyTx{
		To:       &address,
		Nonce:    nonce,
		GasPrice: big.NewInt(115),
		Gas:      100_000,
		Value:    big.NewInt(0),
	}))
	require.NoError(t, err)

	f := newFixture(t, fixtureOpts{auth: auth, seed: map[string]txmngr_provider.Transaction{
		"old-owner": {
			ID:        "old-owner",
			ChainID:   uint64(chainId),
			Sender:    auth.From,
			Status:    txmngr_provider.ErroredTxStatus,
			CreatedAt: now.Add(-time.Hour),
			UpdatedAt: now.Add(-time.Hour),
		},
		"new-owner": {
			ID:           "new-owner",
			ChainID:      uint64(chainId),
			Sender:       auth.From,
			Address:      address,
			Value:        big.NewInt(0),
			GasLimit:     100_000,
			Status:       txmngr_provider.PendingTxStatus,
			NextActionAt: now.Add(-time.Second),
			CreatedAt:    now,
			UpdatedAt:    now,
		},
	}})
	f.seedAttempt("old-owner", conflicting, now.Add(-time.Hour))
	f.cl.On("PendingNonceAt", mock2.Anything, auth.From).Once().Return(nonce, nil)
	f.cl.On("SuggestGasPrice", mock2.Anything).Twice().Return(big.NewInt(100), nil)
	f.cl.On("SendTransaction", mock2.Anything, mock2.Anything).Once().Return(nil)

	f.mngr.processTxs(t.Context())

	attempts, err := f.db.SelectAttempts("new-owner")
	require.NoError(t, err)
	require.Len(t, attempts, 1)
	require.NotEqual(t, conflicting.Hash(), attempts[0].Hash)
	require.Greater(t, attempts[0].Tx.GasPrice().Uint64(), conflicting.GasPrice().Uint64())
}

func TestReleasedNonceHashCollisionJumpsDurableHistoryAfterRestart(t *testing.T) {
	t.Parallel()

	const nonce = uint64(38)
	now := time.Now()
	auth := newSigner(t)
	address := common.HexToAddress("0x0000000000000000000000000000000000000001")
	historicalFees := []int64{115, 149, 216, 345, 603, 1145, 2347, 5163}
	seed := make(map[string]txmngr_provider.Transaction, len(historicalFees)+1)
	seed["new-owner-after-restart"] = txmngr_provider.Transaction{
		ID:           "new-owner-after-restart",
		ChainID:      uint64(chainId),
		Sender:       auth.From,
		Address:      address,
		Value:        big.NewInt(0),
		GasLimit:     100_000,
		Status:       txmngr_provider.PendingTxStatus,
		NextActionAt: now.Add(-time.Second),
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	f := newFixture(t, fixtureOpts{auth: auth, seed: seed})
	for i, fee := range historicalFees {
		id := fmt.Sprintf("released-owner-%d", i)
		owner := txmngr_provider.Transaction{
			ID:        id,
			ChainID:   uint64(chainId),
			Sender:    auth.From,
			Status:    txmngr_provider.ErroredTxStatus,
			CreatedAt: now.Add(-time.Duration(len(historicalFees)-i) * time.Hour),
			UpdatedAt: now,
		}
		require.NoError(t, f.db.Insert(owner))
		signed, err := auth.Signer(auth.From, types.NewTx(&types.LegacyTx{
			To:       &address,
			Nonce:    nonce,
			GasPrice: big.NewInt(fee),
			Gas:      100_000,
			Value:    big.NewInt(0),
		}))
		require.NoError(t, err)
		f.seedAttempt(id, signed, owner.CreatedAt)
	}

	f.cl.On("PendingNonceAt", mock2.Anything, auth.From).Once().Return(nonce, nil)
	f.cl.On("SuggestGasPrice", mock2.Anything).Twice().Return(big.NewInt(100), nil)
	f.cl.On("SendTransaction", mock2.Anything, mock2.Anything).Once().Return(nil)

	f.mngr.processTxs(t.Context())

	attempts, err := f.db.SelectAttempts("new-owner-after-restart")
	require.NoError(t, err)
	require.Len(t, attempts, 1)
	require.Greater(t, attempts[0].Tx.GasPrice().Int64(), historicalFees[len(historicalFees)-1],
		"one durable lookup must jump past every hash signed by earlier processes")
}

func TestRecoveredAttemptBeforeRejectionGraceKeepsNonce(t *testing.T) {
	t.Parallel()

	const nonce = uint64(17)
	now := time.Now()
	auth := newSigner(t)
	signed := signLegacy(t, auth, nonce)
	f := newFixture(t, fixtureOpts{auth: auth, seed: map[string]txmngr_provider.Transaction{
		"recovered": {
			ID:               "recovered",
			ChainID:          uint64(chainId),
			Sender:           auth.From,
			AssignedNonce:    ptr(nonce),
			Tx:               signed,
			Status:           txmngr_provider.ConfirmingTxStatus,
			BroadcastPending: true,
			NextActionAt:     now.Add(-time.Second),
			CreatedAt:        now,
			UpdatedAt:        now,
		},
	}})
	f.seedAttempt("recovered", signed, now)
	f.cl.On("TransactionReceipt", mock2.Anything, signed.Hash()).Twice().Return(nil, ethereum.NotFound)
	f.cl.On("SendTransaction", mock2.Anything, signed).Once().
		Return(testRPCError{code: -32000, message: "gas limit too high: have 17597473, max 16777216"})

	require.NoError(t, f.step("recovered"))

	stored := f.get("recovered")
	require.Equal(t, txmngr_provider.ConfirmingTxStatus, stored.Status)
	require.NotNil(t, stored.AssignedNonce)
	require.Equal(t, nonce, *stored.AssignedNonce)
	require.True(t, stored.BroadcastPending)
	require.Nil(t, stored.RejectedAttemptHash)
	require.False(t, stored.NextActionAt.Before(now.Add(recoveredAttemptRejectionGrace)),
		"a recovered hash must not be retried before the durable ambiguity grace expires")
	f.cl.AssertNotCalled(t, "TransactionByHash", mock2.Anything, mock2.Anything)
	f.cl.AssertNotCalled(t, "PendingNonceAt", mock2.Anything, mock2.Anything)
}

func TestRecoveredReplacementUsesLatestAttemptForRejectionGrace(t *testing.T) {
	t.Parallel()

	const nonce = uint64(19)
	now := time.Now()
	auth := newSigner(t)
	first := signLegacy(t, auth, nonce)
	address := common.HexToAddress("0x0000000000000000000000000000000000000001")
	latest, err := auth.Signer(auth.From, types.NewTx(&types.LegacyTx{
		To:       &address,
		Nonce:    nonce,
		GasPrice: big.NewInt(200),
		Gas:      100_000,
		Value:    big.NewInt(0),
	}))
	require.NoError(t, err)

	f := newFixture(t, fixtureOpts{auth: auth, seed: map[string]txmngr_provider.Transaction{
		"recovered-replacement": {
			ID:               "recovered-replacement",
			ChainID:          uint64(chainId),
			Sender:           auth.From,
			AssignedNonce:    ptr(nonce),
			Tx:               latest,
			Status:           txmngr_provider.ConfirmingTxStatus,
			BroadcastPending: true,
			NextActionAt:     now.Add(-time.Second),
			CreatedAt:        now.Add(-2 * recoveredAttemptRejectionGrace),
			UpdatedAt:        now,
		},
	}})
	f.seedAttempt("recovered-replacement", first, now.Add(-2*recoveredAttemptRejectionGrace))
	f.seedAttempt("recovered-replacement", latest, now)
	f.cl.On("TransactionReceipt", mock2.Anything, mock2.Anything).Times(4).Return(nil, ethereum.NotFound)
	f.cl.On("SendTransaction", mock2.Anything, latest).Once().
		Return(testRPCError{code: -32000, message: "gas limit too high: have 17597473, max 16777216"})

	require.NoError(t, f.step("recovered-replacement"))

	stored := f.get("recovered-replacement")
	require.Equal(t, ptr(nonce), stored.AssignedNonce)
	require.True(t, stored.BroadcastPending)
	require.Nil(t, stored.RejectedAttemptHash)
	require.False(t, stored.NextActionAt.Before(now.Add(recoveredAttemptRejectionGrace)),
		"an old predecessor must not shorten the latest hash's ambiguity grace")
	f.cl.AssertNotCalled(t, "TransactionByHash", mock2.Anything, mock2.Anything)
	f.cl.AssertNotCalled(t, "PendingNonceAt", mock2.Anything, mock2.Anything)
}

func TestRecoveredPermanentRejectionAfterGraceReleasesTipNonce(t *testing.T) {
	t.Parallel()

	const nonce = uint64(18)
	now := time.Now()
	auth := newSigner(t)
	signed := signLegacy(t, auth, nonce)
	head := txmngr_provider.Transaction{
		ID:               "recovered-rejected-head",
		ChainID:          uint64(chainId),
		Sender:           auth.From,
		AssignedNonce:    ptr(nonce),
		Tx:               signed,
		Status:           txmngr_provider.ConfirmingTxStatus,
		BroadcastPending: true,
		NextActionAt:     now.Add(-time.Second),
		CreatedAt:        now.Add(-recoveredAttemptRejectionGrace),
		UpdatedAt:        now,
	}
	follower := txmngr_provider.Transaction{
		ID:           "recovered-rejected-follower",
		ChainID:      uint64(chainId),
		Sender:       auth.From,
		Address:      common.HexToAddress("0x0000000000000000000000000000000000000002"),
		Data:         []byte("different logical operation"),
		Value:        big.NewInt(0),
		GasLimit:     100_000,
		Status:       txmngr_provider.PendingTxStatus,
		NextActionAt: now.Add(-time.Second),
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	f := newFixture(t, fixtureOpts{
		auth: auth,
		seed: map[string]txmngr_provider.Transaction{head.ID: head, follower.ID: follower},
	})
	f.seedAttempt(head.ID, signed, now.Add(-recoveredAttemptRejectionGrace-time.Second))
	f.cl.On("TransactionReceipt", mock2.Anything, signed.Hash()).Twice().Return(nil, ethereum.NotFound)
	f.cl.On("TransactionByHash", mock2.Anything, signed.Hash()).Once().Return(nil, false, ethereum.NotFound)
	f.cl.On("PendingNonceAt", mock2.Anything, auth.From).Twice().Return(nonce, nil)
	f.cl.On("SuggestGasPrice", mock2.Anything).Once().Return(big.NewInt(100), nil)

	var sent []*types.Transaction
	f.cl.On("SendTransaction", mock2.Anything, mock2.Anything).Twice().
		Return(func(_ context.Context, tx *types.Transaction) error {
			sent = append(sent, tx)
			if len(sent) == 1 {
				return testRPCError{code: -32000, message: "gas limit too high: have 17597473, max 16777216"}
			}
			return nil
		})

	f.mngr.processTxs(t.Context())

	rejected := f.get(head.ID)
	require.Equal(t, txmngr_provider.ErroredTxStatus, rejected.Status)
	require.Nil(t, rejected.AssignedNonce)
	require.Nil(t, rejected.RejectedAttemptHash)
	require.Nil(t, f.get(follower.ID).AssignedNonce,
		"the lane must reload its high-water mark before reusing the released nonce")

	f.mngr.processTxs(t.Context())

	require.Len(t, sent, 2)
	require.Equal(t, nonce, sent[0].Nonce())
	require.Equal(t, nonce, sent[1].Nonce())
	require.NotEqual(t, sent[0].Hash(), sent[1].Hash())
	require.Equal(t, txmngr_provider.ConfirmingTxStatus, f.get(follower.ID).Status)
}

func TestNonTipRejectionLetsAssignedSuccessorRunButBlocksUnassignedWork(t *testing.T) {
	t.Parallel()

	const nonce = uint64(40)
	now := time.Now()
	auth := newSigner(t)
	rejectedAttempt := signLegacy(t, auth, nonce)
	successorAttempt := signLegacy(t, auth, nonce+1)
	unassigned := txmngr_provider.Transaction{
		ID:           "unassigned-after-rejection",
		ChainID:      uint64(chainId),
		Sender:       auth.From,
		Value:        big.NewInt(0),
		GasLimit:     21_000,
		Status:       txmngr_provider.PendingTxStatus,
		NextActionAt: now.Add(-time.Second),
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	f := newFixture(t, fixtureOpts{auth: auth, seed: map[string]txmngr_provider.Transaction{
		"rejected-non-tip": {
			ID:                  "rejected-non-tip",
			ChainID:             uint64(chainId),
			Sender:              auth.From,
			AssignedNonce:       ptr(nonce),
			Tx:                  rejectedAttempt,
			Status:              txmngr_provider.ConfirmingTxStatus,
			Msg:                 "broadcast rejected: gas limit too high",
			RejectedAttemptHash: ptr(rejectedAttempt.Hash().Hex()),
			NextActionAt:        now.Add(-time.Second),
			CreatedAt:           now,
			UpdatedAt:           now,
		},
		"assigned-successor": {
			ID:               "assigned-successor",
			ChainID:          uint64(chainId),
			Sender:           auth.From,
			AssignedNonce:    ptr(nonce + 1),
			Tx:               successorAttempt,
			Status:           txmngr_provider.ConfirmingTxStatus,
			BroadcastPending: true,
			NextActionAt:     now.Add(-time.Second),
			CreatedAt:        now,
			UpdatedAt:        now,
		},
		unassigned.ID: unassigned,
	}})
	f.seedAttempt("rejected-non-tip", rejectedAttempt, now)
	f.seedAttempt("assigned-successor", successorAttempt, now)
	f.cl.On("TransactionReceipt", mock2.Anything, mock2.Anything).Twice().Return(nil, ethereum.NotFound)
	f.cl.On("TransactionByHash", mock2.Anything, rejectedAttempt.Hash()).Once().Return(nil, false, ethereum.NotFound)
	f.cl.On("PendingNonceAt", mock2.Anything, auth.From).Twice().Return(nonce, nil)
	f.cl.On("SendTransaction", mock2.Anything, successorAttempt).Once().Return(nil)

	f.mngr.processLaneBatch(t.Context(), uint64(chainId))

	rejected := f.get("rejected-non-tip")
	require.Equal(t, ptr(nonce), rejected.AssignedNonce)
	require.Equal(t, ptr(rejectedAttempt.Hash().Hex()), rejected.RejectedAttemptHash)
	require.False(t, rejected.NextActionAt.Before(now), "the unresolved head must be rescheduled")
	require.False(t, f.get("assigned-successor").BroadcastPending,
		"a non-tip rejection must not prevent already-assigned successor work")
	require.Nil(t, f.get(unassigned.ID).AssignedNonce,
		"the high-water barrier must still prevent assigning new work across the gap")
}

func TestAmbiguousHeadPreventsFollowerAssignmentAcrossBatches(t *testing.T) {
	t.Parallel()

	const nonce = uint64(9)
	now := time.Now()
	auth := newSigner(t)
	first := txmngr_provider.Transaction{
		ID:           "ambiguous",
		ChainID:      uint64(chainId),
		Sender:       auth.From,
		Value:        big.NewInt(0),
		GasLimit:     21_000,
		Status:       txmngr_provider.PendingTxStatus,
		NextActionAt: now.Add(-2 * time.Second),
		CreatedAt:    now.Add(-2 * time.Second),
		UpdatedAt:    now,
	}
	second := first
	second.ID = "follower"
	second.NextActionAt = now.Add(-time.Second)
	second.CreatedAt = now.Add(-time.Second)

	f := newFixture(t, fixtureOpts{
		auth:         auth,
		blockTime:    time.Hour,
		bumpInterval: time.Hour,
		seed:         map[string]txmngr_provider.Transaction{first.ID: first, second.ID: second},
	})
	f.cl.On("PendingNonceAt", mock2.Anything, auth.From).Times(3).Return(nonce, nil)
	f.cl.On("SuggestGasPrice", mock2.Anything).Once().Return(big.NewInt(100), nil)
	f.cl.On("SendTransaction", mock2.Anything, mock2.Anything).Once().
		Return(errors.New("temporary upstream gateway failure"))
	f.cl.On("TransactionReceipt", mock2.Anything, mock2.Anything).Once().Return(nil, ethereum.NotFound)

	f.mngr.processTxs(t.Context())
	require.True(t, f.get(first.ID).BroadcastPending)
	require.Nil(t, f.get(second.ID).AssignedNonce)

	// The ambiguous head is not due on this pass, but its durable nonce still
	// gates the ready follower through MaxAssignedNonce + PendingNonceAt.
	f.mngr.processTxs(t.Context())
	require.Nil(t, f.get(second.ID).AssignedNonce)
	f.cl.AssertNumberOfCalls(t, "SendTransaction", 1)
}

func TestDueAssignedHeadIsRetriedAheadOfMoreThanOneBatchOfFollowers(t *testing.T) {
	t.Parallel()

	const nonce = uint64(10)
	now := time.Now()
	auth := newSigner(t)
	signed := signLegacy(t, auth, nonce)
	seed := map[string]txmngr_provider.Transaction{
		"assigned-head": {
			ID:               "assigned-head",
			ChainID:          uint64(chainId),
			Sender:           auth.From,
			AssignedNonce:    ptr(nonce),
			Tx:               signed,
			Status:           txmngr_provider.ConfirmingTxStatus,
			BroadcastPending: true,
			NextActionAt:     now.Add(-time.Second),
			CreatedAt:        now,
			UpdatedAt:        now,
		},
	}
	for i := range maxTransactionsPerLane + 5 {
		id := fmt.Sprintf("older-follower-%02d", i)
		seed[id] = txmngr_provider.Transaction{
			ID:           id,
			ChainID:      uint64(chainId),
			Sender:       auth.From,
			Value:        big.NewInt(0),
			GasLimit:     21_000,
			Status:       txmngr_provider.PendingTxStatus,
			NextActionAt: now.Add(-time.Hour),
			CreatedAt:    now.Add(-time.Hour),
			UpdatedAt:    now,
		}
	}

	f := newFixture(t, fixtureOpts{auth: auth, seed: seed})
	f.seedAttempt("assigned-head", signed, now)
	f.cl.On("TransactionReceipt", mock2.Anything, signed.Hash()).Once().Return(nil, ethereum.NotFound)
	f.cl.On("SendTransaction", mock2.Anything, signed).Once().Return(nil)
	f.cl.On("PendingNonceAt", mock2.Anything, auth.From).Once().Return(nonce, nil)

	f.mngr.processTxs(t.Context())

	retried := f.get("assigned-head")
	require.False(t, retried.BroadcastPending, "the due nonce owner must not be excluded by follower backlog")
	for i := range maxTransactionsPerLane + 5 {
		id := fmt.Sprintf("older-follower-%02d", i)
		require.Nil(t, f.get(id).AssignedNonce, "followers must remain unassigned while the node is waiting at the head nonce")
	}
}

func TestExhaustedAttemptsRebroadcastLatestExactHash(t *testing.T) {
	t.Parallel()

	const nonce = uint64(42)
	now := time.Now()
	auth := newSigner(t)
	f := newFixture(t, fixtureOpts{seed: map[string]txmngr_provider.Transaction{
		"exhausted": {
			ID:            "exhausted",
			ChainID:       uint64(chainId),
			Sender:        auth.From,
			AssignedNonce: ptr(nonce),
			Status:        txmngr_provider.ConfirmingTxStatus,
			NextActionAt:  now.Add(-time.Second),
			CreatedAt:     now,
			UpdatedAt:     now,
		},
	}, auth: auth})

	var latest *types.Transaction
	for attemptNumber := 1; attemptNumber <= maxFeeReplacements; attemptNumber++ {
		signed, signErr := auth.Signer(auth.From, types.NewTx(&types.LegacyTx{
			Nonce:    nonce,
			To:       ptr(common.Address{}),
			Gas:      21_000,
			GasPrice: big.NewInt(int64(attemptNumber)),
			Value:    big.NewInt(0),
		}))
		require.NoError(t, signErr)
		latest = signed
		f.seedAttempt("exhausted", signed, now.Add(-time.Minute).Add(time.Duration(attemptNumber)*time.Millisecond))
	}

	f.cl.On("TransactionReceipt", mock2.Anything, mock2.Anything).Maybe().Return(nil, ethereum.NotFound)
	f.cl.On("SendTransaction", mock2.Anything, latest).Once().Return(nil)
	require.NoError(t, f.step("exhausted"))

	attempts, err := f.db.SelectAttempts("exhausted")
	require.NoError(t, err)
	require.Len(t, attempts, maxFeeReplacements, "the cap must block a sixth signature")
	require.Equal(t, latest.Hash(), attempts[len(attempts)-1].Hash)
}

// The lane reads its rows only after taking the lock, so a row that reached a
// terminal status in the meantime is never selected for work at all.
func TestProcessLaneBatchSkipsTerminalRows(t *testing.T) {
	t.Parallel()

	now := time.Now()
	terminal := txmngr_provider.Transaction{
		ID:           "already-mined",
		ChainID:      uint64(chainId),
		Status:       txmngr_provider.SucceedTxStatus,
		NextActionAt: now.Add(-time.Second),
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	f := newFixture(t, fixtureOpts{
		seed:      map[string]txmngr_provider.Transaction{terminal.ID: terminal},
		noClients: true,
	})

	f.mngr.processLaneBatch(t.Context(), uint64(chainId))

	stored := f.get(terminal.ID)
	require.Equal(t, txmngr_provider.SucceedTxStatus, stored.Status)
	require.Empty(t, stored.Msg, "a terminal row must not be touched by the lane")
}

// A row that can never advance is parked, not left blocking the lane. Because
// the batch is ordered by next_action_at and a parked row is always overdue, an
// aborting lane would let one bad row starve every transaction behind it.
func TestProcessLaneBatchParksUnprocessableRowsAndContinues(t *testing.T) {
	t.Parallel()

	now := time.Now()
	first := txmngr_provider.Transaction{
		ID:           "first",
		ChainID:      uint64(chainId),
		Status:       txmngr_provider.PendingTxStatus,
		NextActionAt: now.Add(-2 * time.Second),
		CreatedAt:    now.Add(-2 * time.Second),
		UpdatedAt:    now,
	}
	second := first
	second.ID = "second"
	second.NextActionAt = now.Add(-time.Second)
	second.CreatedAt = now.Add(-time.Second)

	// No client is configured for the chain, which is a permanent per-row defect.
	f := newFixture(t, fixtureOpts{
		seed:      map[string]txmngr_provider.Transaction{first.ID: first, second.ID: second},
		noClients: true,
	})

	f.mngr.processLaneBatch(t.Context(), uint64(chainId))

	for _, id := range []string{first.ID, second.ID} {
		stored := f.get(id)
		require.Contains(t, stored.Msg, "no client configured", "row %s should have been attempted", id)
		require.True(t, stored.NextActionAt.After(now), "row %s should have been rescheduled", id)
	}
}

func TestLaneBatchFiltersOldSignerRowsBeforeLimit(t *testing.T) {
	t.Parallel()

	originalAuth := newSigner(t)
	currentAuth := newSigner(t)
	now := time.Now()
	current := txmngr_provider.Transaction{
		ID:           "current-sender-work",
		ChainID:      uint64(chainId),
		Sender:       currentAuth.From,
		Address:      common.HexToAddress("0x0000000000000000000000000000000000000001"),
		Value:        big.NewInt(0),
		GasLimit:     100_000,
		Status:       txmngr_provider.PendingTxStatus,
		NextActionAt: now.Add(-time.Second),
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	seed := map[string]txmngr_provider.Transaction{current.ID: current}
	for i := range maxTransactionsPerLane + 5 {
		nonce := uint64(i)
		id := fmt.Sprintf("old-sender-%02d", i)
		seed[id] = txmngr_provider.Transaction{
			ID:            id,
			ChainID:       uint64(chainId),
			Sender:        originalAuth.From,
			AssignedNonce: &nonce,
			Status:        txmngr_provider.ConfirmingTxStatus,
			NextActionAt:  now.Add(-time.Hour),
			CreatedAt:     now.Add(-time.Hour),
			UpdatedAt:     now,
		}
	}

	f := newFixture(t, fixtureOpts{auth: currentAuth, seed: seed})
	f.cl.On("PendingNonceAt", mock2.Anything, currentAuth.From).Once().Return(uint64(7), nil)
	f.cl.On("SuggestGasPrice", mock2.Anything).Once().Return(big.NewInt(100), nil)
	f.cl.On("SendTransaction", mock2.Anything, mock2.Anything).Once().Return(nil)

	f.mngr.processTxs(t.Context())

	stored := f.get(current.ID)
	require.Equal(t, txmngr_provider.ConfirmingTxStatus, stored.Status)
	require.Equal(t, ptr(uint64(7)), stored.AssignedNonce,
		"foreign assigned rows must not consume the current sender's work limit")
	for i := range maxTransactionsPerLane + 5 {
		old := f.get(fmt.Sprintf("old-sender-%02d", i))
		require.Empty(t, old.Msg)
		require.Equal(t, originalAuth.From, old.Sender)
	}
}

func TestLaneBatchAdoptsLegacyUnsetSenderRow(t *testing.T) {
	t.Parallel()

	now := time.Now()
	const nonce = uint64(9)
	tx := txmngr_provider.Transaction{
		ID:           "legacy-unset-sender",
		ChainID:      uint64(chainId),
		Address:      common.HexToAddress("0x0000000000000000000000000000000000000001"),
		Value:        big.NewInt(0),
		GasLimit:     100_000,
		Status:       txmngr_provider.PendingTxStatus,
		NextActionAt: now.Add(-time.Second),
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	f := newFixture(t, fixtureOpts{seed: map[string]txmngr_provider.Transaction{tx.ID: tx}})
	f.cl.On("PendingNonceAt", mock2.Anything, f.auth.From).Once().Return(nonce, nil)
	f.cl.On("SuggestGasPrice", mock2.Anything).Once().Return(big.NewInt(100), nil)
	f.cl.On("SendTransaction", mock2.Anything, mock2.Anything).Once().Return(nil)

	f.mngr.processTxs(t.Context())

	stored := f.get(tx.ID)
	require.Equal(t, f.auth.From, stored.Sender, "legacy rows must be adopted by the configured sender")
	require.Equal(t, ptr(nonce), stored.AssignedNonce)
	require.Equal(t, txmngr_provider.ConfirmingTxStatus, stored.Status)
	attempts, err := f.db.SelectAttempts(tx.ID)
	require.NoError(t, err)
	require.Len(t, attempts, 1)
	attemptSender, attemptNonce, err := validatePersistedAttempts(attempts)
	require.NoError(t, err)
	require.Equal(t, f.auth.From, attemptSender)
	require.Equal(t, nonce, attemptNonce)
}

func TestSendTxAsyncOperationKeyIsIdempotent(t *testing.T) {
	t.Parallel()

	f := newFixture(t, fixtureOpts{})
	send := func(data string) (string, error) {
		return f.mngr.SendTxAsync(t.Context(), uint64(chainId), common.Address{}, []byte(data), 1, big.NewInt(0),
			nil, nil, WithOperationKey("intent:user-op"))
	}

	firstID, err := send("same")
	require.NoError(t, err)
	secondID, err := send("same")
	require.NoError(t, err)
	require.Equal(t, firstID, secondID)

	_, err = send("different")
	require.ErrorIs(t, err, ErrIdempotencyConflict)
}

func TestSetCodeFeeReplacementKeepsAssignedNonce(t *testing.T) {
	t.Parallel()

	userPK, err := crypto.GenerateKey()
	require.NoError(t, err)
	userAuth, err := bind.NewKeyedTransactorWithChainID(userPK, big.NewInt(chainId))
	require.NoError(t, err)
	authorization, err := types.SignSetCode(userPK, types.SetCodeAuthorization{
		ChainID: *uint256.NewInt(uint64(chainId)),
		Address: userAuth.From,
	})
	require.NoError(t, err)

	f := newFixture(t, fixtureOpts{auth: userAuth})
	f.cl.On("HeaderByNumber", mock2.Anything, mock2.Anything).
		Once().Return(&types.Header{BaseFee: big.NewInt(100)}, nil)
	f.cl.On("HeaderByNumber", mock2.Anything, mock2.Anything).
		Once().Return(&types.Header{BaseFee: big.NewInt(1)}, nil)
	f.cl.On("SuggestGasTipCap", mock2.Anything).Twice().Return(big.NewInt(10), nil)

	nonce := uint64(42)
	tx := txmngr_provider.Transaction{
		ChainID:           uint64(chainId),
		Address:           common.HexToAddress("0x0000000000000000000000000000000000000001"),
		Value:             big.NewInt(0),
		GasLimit:          100_000,
		RawAuthorizations: []types.SetCodeAuthorization{authorization},
		AssignedNonce:     &nonce,
	}
	first, err := f.mngr.signAttempt(t.Context(), tx, nil, nil)
	require.NoError(t, err)
	second, err := f.mngr.signAttempt(t.Context(), tx, []txmngr_provider.TransactionAttempt{{Tx: first}}, nil)
	require.NoError(t, err)
	require.EqualValues(t, types.SetCodeTxType, first.Type())
	require.EqualValues(t, types.SetCodeTxType, second.Type())
	require.Equal(t, nonce, first.Nonce())
	require.Equal(t, nonce, second.Nonce())
	require.Greater(t, second.GasTipCap().Uint64(), first.GasTipCap().Uint64())
	require.Greater(t, second.GasFeeCap().Uint64(), first.GasFeeCap().Uint64())
}

func TestLegacyFeeReplacementStrictlyIncreases(t *testing.T) {
	t.Parallel()

	f := newFixture(t, fixtureOpts{})
	f.cl.On("SuggestGasPrice", mock2.Anything).Twice().Return(big.NewInt(1), nil)

	nonce := uint64(42)
	tx := txmngr_provider.Transaction{
		ChainID:       uint64(chainId),
		Address:       common.HexToAddress("0x0000000000000000000000000000000000000001"),
		Value:         big.NewInt(0),
		GasLimit:      21_000,
		AssignedNonce: &nonce,
	}
	first, err := f.mngr.signAttempt(t.Context(), tx, nil, nil)
	require.NoError(t, err)
	second, err := f.mngr.signAttempt(t.Context(), tx, []txmngr_provider.TransactionAttempt{{Tx: first}}, nil)
	require.NoError(t, err)
	require.Equal(t, nonce, first.Nonce())
	require.Equal(t, nonce, second.Nonce())
	require.Greater(t, second.GasPrice().Uint64(), first.GasPrice().Uint64())
}

func TestSignAttemptRejectsNonceLaneSignerMismatch(t *testing.T) {
	t.Parallel()

	managerAuth := newSigner(t)
	f := newFixture(t, fixtureOpts{auth: newSigner(t), sender: &managerAuth.From, noClients: true})

	nonce := uint64(1)
	_, err := f.mngr.signAttempt(t.Context(), txmngr_provider.Transaction{
		ChainID:       uint64(chainId),
		Value:         big.NewInt(0),
		AssignedNonce: &nonce,
	}, nil, nil)
	require.ErrorContains(t, err, "differs from nonce-lane sender")
}

func TestRestartResumesPersistedAttemptWithExactHash(t *testing.T) {
	t.Parallel()

	const nonce = uint64(7)
	now := time.Now()
	auth := newSigner(t)
	signed := signLegacy(t, auth, nonce)
	f := newFixture(t, fixtureOpts{
		auth:         auth,
		bumpInterval: time.Second,
		blockTime:    time.Second,
		seed: map[string]txmngr_provider.Transaction{
			"restart-tx": {
				ID:            "restart-tx",
				ChainID:       uint64(chainId),
				Sender:        auth.From,
				AssignedNonce: ptr(nonce),
				Status:        txmngr_provider.ConfirmingTxStatus,
				Msg:           "stale restart error",
				NextActionAt:  now,
				CreatedAt:     now,
				UpdatedAt:     now,
			},
		},
	})
	f.seedAttempt("restart-tx", signed, now)
	f.cl.On("TransactionReceipt", mock2.Anything, signed.Hash()).Maybe().Return(nil, ethereum.NotFound)
	f.cl.On("SendTransaction", mock2.Anything, signed).Once().Return(nil)

	require.NoError(t, f.step("restart-tx"))

	recovered := f.get("restart-tx")
	require.Equal(t, signed.Hash(), recovered.Tx.Hash())
	require.False(t, recovered.BroadcastPending)
	require.Empty(t, recovered.Msg)
}

func TestLegacyRawTransactionIsRecoveredAsAmbiguousAttempt(t *testing.T) {
	t.Parallel()

	now := time.Now()
	auth := newSigner(t)
	signed := signLegacy(t, auth, 11)
	// nilTimings leaves the bump and block-time maps unset, so this is also the
	// test that covers the manager's built-in timing defaults.
	f := newFixture(t, fixtureOpts{auth: auth, nilTimings: true, seed: map[string]txmngr_provider.Transaction{
		"legacy-raw": {
			ID:           "legacy-raw",
			ChainID:      uint64(chainId),
			Tx:           signed,
			Status:       txmngr_provider.ProcessingTxStatus,
			NextActionAt: now,
			CreatedAt:    now,
			UpdatedAt:    now,
		},
	}})
	f.cl.On("TransactionReceipt", mock2.Anything, signed.Hash()).Maybe().Return(nil, ethereum.NotFound)
	f.cl.On("SendTransaction", mock2.Anything, signed).Once().Return(nil)

	require.NoError(t, f.step("legacy-raw"))

	attempts, err := f.db.SelectAttempts("legacy-raw")
	require.NoError(t, err)
	require.Len(t, attempts, 1)
	require.Equal(t, signed.Hash(), attempts[0].Hash)
	recovered := f.get("legacy-raw")
	require.Equal(t, auth.From, recovered.Sender)
	require.False(t, recovered.BroadcastPending)
}

func TestMinedOldSignerAttemptFinalizesAfterKeyRotation(t *testing.T) {
	t.Parallel()

	const wrongNonce = uint64(99)
	now := time.Now()
	originalAuth := newSigner(t)
	replacementAuth := newSigner(t)
	signed := signLegacy(t, originalAuth, 5)
	f := newFixture(t, fixtureOpts{auth: replacementAuth, seed: map[string]txmngr_provider.Transaction{
		"mined-before-key-rotation": {
			ID:            "mined-before-key-rotation",
			ChainID:       uint64(chainId),
			Sender:        replacementAuth.From,
			AssignedNonce: ptr(wrongNonce),
			Status:        txmngr_provider.ConfirmingTxStatus,
			CreatedAt:     now,
			UpdatedAt:     now,
		},
	}})
	f.seedAttempt("mined-before-key-rotation", signed, now)
	f.cl.On("TransactionReceipt", mock2.Anything, signed.Hash()).Once().
		Return(&types.Receipt{TxHash: signed.Hash(), Status: types.ReceiptStatusSuccessful}, nil)

	require.NoError(t, f.step("mined-before-key-rotation"))

	stored := f.get("mined-before-key-rotation")
	require.Equal(t, txmngr_provider.SucceedTxStatus, stored.Status)
	require.Equal(t, originalAuth.From, stored.Sender, "the winning attempt repairs the parent sender")
	require.NotNil(t, stored.AssignedNonce)
	require.Equal(t, signed.Nonce(), *stored.AssignedNonce, "and its nonce")
	require.Equal(t, signed.Hash(), stored.Receipt.TxHash)
	f.cl.AssertNotCalled(t, "SendTransaction", mock2.Anything, mock2.Anything)
}

func TestNonceLaneReservation(t *testing.T) {
	t.Parallel()

	t.Run("waits when the node is behind the lane mark", func(t *testing.T) {
		t.Parallel()

		lane := &nonceLane{}
		lane.observe(11)
		_, ready := lane.reserve(9)
		require.False(t, ready)
	})

	t.Run("follows the node when it is ahead", func(t *testing.T) {
		t.Parallel()

		lane := &nonceLane{}
		lane.observe(3)
		nonce, ready := lane.reserve(20)
		require.True(t, ready)
		require.Equal(t, uint64(20), nonce)
	})

	// reserve must be idempotent until observe confirms the assignment reached
	// the database: a nonce burned by a failed write would leave a permanent gap
	// that stalls every later transaction on the lane.
	t.Run("advances only once the assignment is durable", func(t *testing.T) {
		t.Parallel()

		lane := &nonceLane{}
		first, ready := lane.reserve(9)
		require.True(t, ready)
		require.Equal(t, uint64(9), first)
		retry, ready := lane.reserve(9)
		require.True(t, ready)
		require.Equal(t, first, retry, "a failed persist must reuse the nonce")

		lane.observe(first)
		_, ready = lane.reserve(9)
		require.False(t, ready, "the node must observe the durable assignment before the lane advances")
		next, ready := lane.reserve(first + 1)
		require.True(t, ready)
		require.Equal(t, first+1, next)
	})

	t.Run("never moves backwards", func(t *testing.T) {
		t.Parallel()

		lane := &nonceLane{}
		lane.observe(30)
		lane.observe(4)
		_, ready := lane.reserve(0)
		require.False(t, ready)
		require.Equal(t, uint64(31), lane.next)
	})
}

// The lane's high-water mark is scoped to (sender, chain), so a different
// signer's much higher nonce must not push this manager's assignment forward.
func TestLaneIgnoresOtherPersistedSenders(t *testing.T) {
	t.Parallel()

	other := common.HexToAddress("0x00000000000000000000000000000000000000ff")
	otherNonce := uint64(100)
	auth := newSigner(t)
	managerNonce := uint64(7)
	f := newFixture(t, fixtureOpts{auth: auth, seed: map[string]txmngr_provider.Transaction{
		"manager": {ID: "manager", ChainID: uint64(chainId), Sender: auth.From, AssignedNonce: &managerNonce, Status: txmngr_provider.ConfirmingTxStatus},
		"other":   {ID: "other", ChainID: uint64(chainId), Sender: other, AssignedNonce: &otherNonce, Status: txmngr_provider.ConfirmingTxStatus},
	}})

	mark, found, err := f.db.MaxAssignedNonce(t.Context(), auth.From, uint64(chainId))
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, managerNonce, mark, "the other sender's nonce must not leak into this lane")
}

func TestLaneHandoffWaitsForNodeToReachPersistedNonceHighWaterMark(t *testing.T) {
	t.Parallel()

	auth := newSigner(t)

	const assignedByFirstManager = uint64(12)
	now := time.Now()
	// The first replica already consumed nonce 12 and its row is terminal. The
	// second replica's RPC endpoint is still one nonce behind, so only the
	// durable high-water mark can stop it reusing 12.
	f := newFixture(t, fixtureOpts{
		auth:         auth,
		bumpInterval: time.Second,
		blockTime:    time.Second,
		seed: map[string]txmngr_provider.Transaction{
			"completed-by-first-manager": {
				ID:            "completed-by-first-manager",
				ChainID:       uint64(chainId),
				Sender:        auth.From,
				AssignedNonce: ptr(assignedByFirstManager),
				Status:        txmngr_provider.SucceedTxStatus,
				CreatedAt:     now,
				UpdatedAt:     now,
			},
			"queued-for-second-manager": {
				ID:           "queued-for-second-manager",
				ChainID:      uint64(chainId),
				Sender:       auth.From,
				Value:        big.NewInt(0),
				GasLimit:     21_000,
				Status:       txmngr_provider.PendingTxStatus,
				NextActionAt: now.Add(-time.Second),
				CreatedAt:    now,
				UpdatedAt:    now,
			},
		},
	})

	f.cl.On("PendingNonceAt", mock2.Anything, auth.From).Once().Return(assignedByFirstManager, nil)
	f.cl.On("PendingNonceAt", mock2.Anything, auth.From).Once().Return(assignedByFirstManager+1, nil)
	f.cl.On("SuggestGasPrice", mock2.Anything).Once().Return(big.NewInt(1), nil)
	var sent *types.Transaction
	f.cl.On("SendTransaction", mock2.Anything, mock2.Anything).
		Once().
		Run(func(args mock2.Arguments) { sent = args.Get(1).(*types.Transaction) }).
		Return(nil)
	f.cl.On("TransactionReceipt", mock2.Anything, mock2.Anything).Maybe().Return(nil, ethereum.NotFound)

	f.mngr.processTxs(t.Context())
	require.Nil(t, sent, "a lagging node must not make the manager skip a durable nonce")
	require.Nil(t, f.get("queued-for-second-manager").AssignedNonce)

	f.mngr.processTxs(t.Context())
	require.NotNil(t, sent)
	require.Equal(t, assignedByFirstManager+1, sent.Nonce(), "a terminal row's nonce stays consumed")
	stored := f.get("queued-for-second-manager")
	require.NotNil(t, stored.AssignedNonce)
	require.Equal(t, assignedByFirstManager+1, *stored.AssignedNonce)
}

func TestRecoveredLegacyAttemptAdvancesNonceCache(t *testing.T) {
	t.Parallel()

	const legacyNonce = uint64(7)
	now := time.Now()
	auth := newSigner(t)
	signed := signLegacy(t, auth, legacyNonce)
	f := newFixture(t, fixtureOpts{
		auth:         auth,
		bumpInterval: time.Second,
		blockTime:    time.Second,
		seed: map[string]txmngr_provider.Transaction{
			"legacy": {
				ID:           "legacy",
				ChainID:      uint64(chainId),
				Tx:           signed,
				Status:       txmngr_provider.ProcessingTxStatus,
				NextActionAt: now.Add(-time.Second),
				CreatedAt:    now,
				UpdatedAt:    now,
			},
		},
	})
	// The recovered attempt already carries its nonce, so no nonce is requested.
	f.cl.On("TransactionReceipt", mock2.Anything, signed.Hash()).Maybe().Return(nil, ethereum.NotFound)
	f.cl.On("SendTransaction", mock2.Anything, signed).Once().Return(errors.New("already known"))

	reserved, ready := f.lane.reserve(legacyNonce)
	require.True(t, ready)
	require.Equal(t, legacyNonce, reserved)
	require.NoError(t, f.step("legacy"))

	// Recovering the legacy attempt reserves its nonce for the rest of the batch,
	// so a later row cannot reuse it while the node is still lagging.
	_, ready = f.lane.reserve(legacyNonce)
	require.False(t, ready)
	f.cl.AssertNotCalled(t, "PendingNonceAt", mock2.Anything, mock2.Anything)
}

func TestWorkChainsRotateInBoundedBatches(t *testing.T) {
	t.Parallel()

	clients := make(map[uint64]EthClient)
	for chainID := uint64(1); chainID <= 6; chainID++ {
		clients[chainID] = nil
	}
	mngr := New(nil, clients, common.Address{}, nil, nil, nil).(*txManager)
	require.Equal(t, []uint64{1, 2, 3, 4}, mngr.nextWorkChainIDs(4))
	require.Equal(t, []uint64{5, 6, 1, 2}, mngr.nextWorkChainIDs(4))
	require.Equal(t, []uint64{3, 4, 5, 6}, mngr.nextWorkChainIDs(4))
}

func TestTerminalNotificationRetriesUntilAcknowledged(t *testing.T) {
	t.Parallel()

	now := time.Now()
	callbackCalls := 0
	f := newFixture(t, fixtureOpts{
		noClients: true,
		seed:      map[string]txmngr_provider.Transaction{"terminal": terminalRow("terminal", now)},
		managerOpts: []Option{WithOnStatusChange(func(_ context.Context, tx txmngr_provider.Transaction) error {
			callbackCalls++
			require.Equal(t, txmngr_provider.SucceedTxStatus, tx.Status)
			if callbackCalls == 1 {
				return errors.New("publisher unavailable")
			}
			return nil
		})},
	})

	f.mngr.deliverStatusNotifications(t.Context())
	terminal := f.get("terminal")
	require.Nil(t, terminal.NotifiedAt)
	require.Equal(t, uint32(1), terminal.NotificationAttempts)
	terminal.NextNotificationAt = time.Now().Add(-time.Second)
	require.NoError(t, f.db.Insert(*terminal))

	f.mngr.deliverStatusNotifications(t.Context())
	require.NotNil(t, f.get("terminal").NotifiedAt)
	require.Equal(t, 2, callbackCalls)
}

func TestNotificationDeliveryUsesBoundedWorkerPool(t *testing.T) {
	t.Parallel()

	const notificationCount = 12
	now := time.Now()
	seed := make(map[string]txmngr_provider.Transaction, notificationCount)
	for i := range notificationCount {
		id := fmt.Sprintf("terminal-%02d", i)
		seed[id] = terminalRow(id, now)
	}

	started := make(chan struct{}, notificationCount)
	release := make(chan struct{})
	var active atomic.Int32
	var maxActive atomic.Int32
	var calls atomic.Int32
	f := newFixture(t, fixtureOpts{
		noClients: true,
		seed:      seed,
		managerOpts: []Option{WithOnStatusChange(func(ctx context.Context, _ txmngr_provider.Transaction) error {
			calls.Add(1)
			current := active.Add(1)
			for {
				previous := maxActive.Load()
				if current <= previous || maxActive.CompareAndSwap(previous, current) {
					break
				}
			}
			started <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
			}
			active.Add(-1)
			return nil
		})},
	})

	done := make(chan struct{})
	go func() {
		f.mngr.deliverStatusNotifications(context.Background())
		close(done)
	}()
	for range maxNotificationWorkers {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for notification workers")
		}
	}
	require.EqualValues(t, maxNotificationWorkers, active.Load())
	require.EqualValues(t, maxNotificationWorkers, maxActive.Load())
	require.EqualValues(t, maxNotificationWorkers, calls.Load())
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for notification delivery")
	}
	require.EqualValues(t, notificationCount, calls.Load())
	require.LessOrEqual(t, maxActive.Load(), int32(maxNotificationWorkers))
}

func TestStopCancelsInFlightNotificationAndStartIsIdempotent(t *testing.T) {
	t.Parallel()

	now := time.Now()
	started := make(chan struct{}, 2)
	var calls atomic.Int32
	f := newFixture(t, fixtureOpts{
		noClients: true,
		seed: map[string]txmngr_provider.Transaction{
			"cancel-notification": terminalRow("cancel-notification", now),
		},
		managerOpts: []Option{WithOnStatusChange(func(ctx context.Context, _ txmngr_provider.Transaction) error {
			calls.Add(1)
			started <- struct{}{}
			<-ctx.Done()
			return ctx.Err()
		})},
	})
	mngr := f.mngr
	mngr.Start(context.Background())
	mngr.Start(context.Background())
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for notification callback")
	}

	stopped := make(chan struct{})
	go func() {
		mngr.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop did not cancel the in-flight callback")
	}
	require.EqualValues(t, 1, calls.Load())
	mngr.Stop()
}

func TestTerminalIntentNotificationSurvivesMissingCallback(t *testing.T) {
	t.Parallel()

	now := time.Now()
	record := terminalRow("terminal-without-publisher", now)
	record.Status = txmngr_provider.ConfirmingTxStatus
	f := newFixture(t, fixtureOpts{
		noClients: true,
		seed:      map[string]txmngr_provider.Transaction{record.ID: record},
	})

	winner := legacyTx()
	receipt := &types.Receipt{TxHash: winner.Hash(), Status: types.ReceiptStatusSuccessful}
	require.NoError(t, f.mngr.finalizeTransaction(record, winner, receipt))

	terminal := f.get(record.ID)
	require.Nil(t, terminal.NotifiedAt, "missing callback must not acknowledge an intent notification")
	require.False(t, terminal.NextNotificationAt.After(time.Now()))

	deliveries := 0
	withPublisher := New(f.db, nil, common.Address{}, nil, nil, nil, WithOnStatusChange(
		func(_ context.Context, tx txmngr_provider.Transaction) error {
			deliveries++
			require.Equal(t, record.ID, tx.ID)
			return nil
		},
	)).(*txManager)
	withPublisher.deliverStatusNotifications(t.Context())

	require.NotNil(t, f.get(record.ID).NotifiedAt)
	require.Equal(t, 1, deliveries)
}

func TestFindKnownReceiptRejectsMismatchedHashAndStatus(t *testing.T) {
	t.Parallel()

	tx := legacyTx()
	attempt := txmngr_provider.TransactionAttempt{Hash: tx.Hash(), Tx: tx}
	for _, tt := range []struct {
		name    string
		receipt *types.Receipt
	}{
		{name: "zero hash", receipt: &types.Receipt{Status: types.ReceiptStatusSuccessful}},
		{name: "mismatched hash", receipt: &types.Receipt{TxHash: common.HexToHash("0x01"), Status: types.ReceiptStatusSuccessful}},
		{name: "unsupported status", receipt: &types.Receipt{TxHash: tx.Hash(), Status: 2}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, fixtureOpts{})
			f.cl.On("TransactionReceipt", mock2.Anything, tx.Hash()).Once().Return(tt.receipt, nil)
			receipt, winner, err := f.mngr.findKnownReceipt(t.Context(), uint64(chainId),
				[]txmngr_provider.TransactionAttempt{attempt})
			require.Error(t, err)
			require.Nil(t, receipt)
			require.Nil(t, winner)
		})
	}
}

func ptr[T any](value T) *T {
	return &value
}

func Test_bumpGasPrice(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		gasPrice *big.Int
		want     *big.Int
	}{
		{
			name:     "nil gas price",
			gasPrice: nil,
			want:     big.NewInt(0),
		},
		{
			name:     "zero gas price",
			gasPrice: big.NewInt(0),
			want:     big.NewInt(0),
		},
		{
			name:     "positive gas price",
			gasPrice: big.NewInt(100),
			want:     big.NewInt(115),
		},
		{
			name:     "small positive gas price still increases",
			gasPrice: big.NewInt(1),
			want:     big.NewInt(2),
		},
		{
			name:     "positive gas price #2",
			gasPrice: big.NewInt(200),
			want:     big.NewInt(230),
		},
		{
			name:     "negative gas price",
			gasPrice: big.NewInt(-100),
			want:     big.NewInt(-115),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equalf(t, tt.want, bumpGasPrice(tt.gasPrice, 15), "bumpGasPrice(%v)", tt.gasPrice)
		})
	}
}
