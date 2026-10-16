package txmngr

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rpc"
	mock2 "github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/superform-xyz/superform-go-utils/evm/txmngr/mock"
	"github.com/superform-xyz/superform-go-utils/evm/txmngr/txmngr_provider"
	"github.com/superform-xyz/superform-go-utils/evm/txmngr/txmngr_provider/mem"
)

// A crash between InsertAttempt and the parent-row update leaves a durable
// signed attempt whose parent still says pending with no assigned nonce. That
// inconsistent state is the whole reason attempts are committed first, so
// recovery must adopt the attempt's nonce rather than signing a new one.
func TestRestartRepairsParentRowFromDurableAttempt(t *testing.T) {
	t.Parallel()

	const nonce = uint64(21)
	now := time.Now()
	auth := newSigner(t)
	signed := signLegacy(t, auth, nonce)
	f := newFixture(t, fixtureOpts{auth: auth, seed: map[string]txmngr_provider.Transaction{
		"half-written": {
			ID:      "half-written",
			ChainID: uint64(chainId),
			// Parent never got its status/nonce written before the crash.
			Status:       txmngr_provider.PendingTxStatus,
			NextActionAt: now.Add(-time.Second),
			CreatedAt:    now,
			UpdatedAt:    now,
		},
	}})
	f.seedAttempt("half-written", signed, now)
	f.cl.On("TransactionReceipt", mock2.Anything, signed.Hash()).Maybe().Return(nil, ethereum.NotFound)
	f.cl.On("SendTransaction", mock2.Anything, signed).Once().Return(nil)

	require.NoError(t, f.step("half-written"))

	stored := f.get("half-written")
	require.Equal(t, txmngr_provider.ConfirmingTxStatus, stored.Status)
	require.NotNil(t, stored.AssignedNonce)
	require.Equal(t, nonce, *stored.AssignedNonce, "recovery must adopt the durable attempt's nonce")
	require.Equal(t, auth.From, stored.Sender)

	attempts, err := f.db.SelectAttempts("half-written")
	require.NoError(t, err)
	require.Len(t, attempts, 1, "recovery must not sign a second candidate")
	f.cl.AssertNotCalled(t, "PendingNonceAt", mock2.Anything, mock2.Anything)
}

// A nonce consumed by somebody else can never be mined by this payload. Polling
// it forever would hang WaitTxCompleted and hold its notification slot, so the
// row is abandoned once receipts have had time to catch up.
func TestConsumedNonceIsAbandonedAfterGracePeriod(t *testing.T) {
	t.Parallel()

	const nonce = uint64(5)

	for _, tt := range []struct {
		name          string
		consumedSince time.Duration
		wantStatus    txmngr_provider.TxStatus
	}{
		{
			name:          "within grace period keeps polling",
			consumedSince: time.Second,
			wantStatus:    txmngr_provider.ConfirmingTxStatus,
		},
		{
			name:          "past grace period is abandoned",
			consumedSince: 2 * consumedNonceGrace,
			wantStatus:    txmngr_provider.ErroredTxStatus,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Signed per subtest: types.Transaction caches its hash lazily and is
			// not safe to share across parallel tests.
			now := time.Now()
			auth := newSigner(t)
			signed := signLegacy(t, auth, nonce)
			intentID := "0xintent"
			f := newFixture(t, fixtureOpts{auth: auth, seed: map[string]txmngr_provider.Transaction{
				"consumed": {
					ID:            "consumed",
					ChainID:       uint64(chainId),
					IntentID:      &intentID,
					Sender:        auth.From,
					AssignedNonce: ptr(nonce),
					// The broadcast that reported nonce-too-low already completed.
					Tx:              signed,
					Status:          txmngr_provider.ConfirmingTxStatus,
					NonceConsumedAt: ptr(now.Add(-tt.consumedSince)),
					NextActionAt:    now.Add(-time.Second),
					CreatedAt:       now,
					UpdatedAt:       now,
				},
			}})
			f.seedAttempt("consumed", signed, now)
			f.cl.On("TransactionReceipt", mock2.Anything, signed.Hash()).Maybe().Return(nil, ethereum.NotFound)

			require.NoError(t, f.step("consumed"))

			stored := f.get("consumed")
			require.Equal(t, tt.wantStatus, stored.Status)
			f.cl.AssertNotCalled(t, "SendTransaction", mock2.Anything, mock2.Anything)

			if tt.wantStatus == txmngr_provider.ErroredTxStatus {
				require.Nil(t, stored.NotifiedAt, "an abandoned intent row must still be delivered")
				require.Contains(t, stored.Msg, "consumed by another transaction")
			}
		})
	}
}

// A reverted transaction is a settled outcome, not a failure to keep retrying.
func TestRevertedReceiptFinalizesAsFailed(t *testing.T) {
	t.Parallel()

	const nonce = uint64(8)
	now := time.Now()
	auth := newSigner(t)
	signed := signLegacy(t, auth, nonce)
	f := newFixture(t, fixtureOpts{auth: auth, seed: map[string]txmngr_provider.Transaction{
		"reverted": {
			ID:            "reverted",
			ChainID:       uint64(chainId),
			Sender:        auth.From,
			AssignedNonce: ptr(nonce),
			Status:        txmngr_provider.ConfirmingTxStatus,
			NextActionAt:  now.Add(-time.Second),
			CreatedAt:     now,
			UpdatedAt:     now,
		},
	}})
	f.seedAttempt("reverted", signed, now)
	f.cl.On("TransactionReceipt", mock2.Anything, signed.Hash()).Once().
		Return(&types.Receipt{TxHash: signed.Hash(), Status: types.ReceiptStatusFailed}, nil)

	require.NoError(t, f.step("reverted"))

	stored := f.get("reverted")
	require.Equal(t, txmngr_provider.FailedTxStatus, stored.Status)
	require.Equal(t, signed.Hash(), stored.Receipt.TxHash)
	require.NotNil(t, stored.NotifiedAt, "a row without an intent has no consumer to notify")
}

// The lane lock exists so two replicas cannot both broadcast for one sender and
// chain. Without it each would allocate the same nonce from the same node.
func TestConcurrentManagersAssignDistinctNonces(t *testing.T) {
	t.Parallel()

	const replicas = 4
	auth := newSigner(t)
	now := time.Now()
	seed := make(map[string]txmngr_provider.Transaction, replicas)
	for i := range replicas {
		id := string(rune('a' + i))
		seed[id] = txmngr_provider.Transaction{
			ID:           id,
			ChainID:      uint64(chainId),
			Sender:       auth.From,
			Value:        big.NewInt(0),
			GasLimit:     21_000,
			Status:       txmngr_provider.PendingTxStatus,
			NextActionAt: now.Add(-time.Second),
			CreatedAt:    now,
			UpdatedAt:    now,
		}
	}
	db := mem.NewTransactionsQ(seed)

	var mu sync.Mutex
	sentNonces := map[uint64]int{}
	newManager := func() *txManager {
		cl := mock.NewEthClient(t)
		// Every replica's node reports the same stale nonce, so only the durable
		// high-water mark taken under the lane lock can keep them apart. Every
		// expectation is optional: replicas that lose the lane do no work at all,
		// and which one wins is deliberately nondeterministic.
		cl.On("PendingNonceAt", mock2.Anything, auth.From).Maybe().Return(uint64(0), nil)
		cl.On("SuggestGasPrice", mock2.Anything).Maybe().Return(big.NewInt(100), nil)
		cl.On("TransactionReceipt", mock2.Anything, mock2.Anything).Maybe().Return(nil, ethereum.NotFound)
		cl.On("SendTransaction", mock2.Anything, mock2.Anything).Maybe().
			Run(func(args mock2.Arguments) {
				tx := args.Get(1).(*types.Transaction)
				mu.Lock()
				sentNonces[tx.Nonce()]++
				mu.Unlock()
			}).
			Return(nil)
		return New(
			db,
			map[uint64]EthClient{uint64(chainId): cl},
			auth.From,
			map[uint64]time.Duration{uint64(chainId): time.Hour},
			map[uint64]time.Duration{uint64(chainId): time.Hour},
			func(uint64) (*bind.TransactOpts, error) { return auth, nil },
		).(*txManager)
	}

	var wg sync.WaitGroup
	for range replicas {
		wg.Add(1)
		go func() {
			defer wg.Done()
			newManager().processTxs(context.Background())
		}()
	}
	wg.Wait()

	assigned := map[uint64]string{}
	for id := range seed {
		stored, err := db.New().FilterByIds(id).Get()
		require.NoError(t, err)
		if stored.AssignedNonce == nil {
			continue
		}
		previous, clash := assigned[*stored.AssignedNonce]
		require.False(t, clash, "nonce %d assigned to both %s and %s", *stored.AssignedNonce, previous, id)
		assigned[*stored.AssignedNonce] = id
	}
	require.NotEmpty(t, assigned, "at least one transaction should have been assigned a nonce")

	mu.Lock()
	defer mu.Unlock()
	for nonce, count := range sentNonces {
		require.Equal(t, 1, count, "nonce %d was broadcast %d times", nonce, count)
	}
}

func TestNotificationBackoffGrowsAndIsCapped(t *testing.T) {
	t.Parallel()

	require.Equal(t, 2*notificationMinBackoff, notificationBackoff(1))
	require.Equal(t, 4*notificationMinBackoff, notificationBackoff(2))
	require.Equal(t, notificationMaxBackoff, notificationBackoff(6), "the shift is capped")
	require.Equal(t, notificationMaxBackoff, notificationBackoff(30), "the shift must not overflow")
}

// A panicking callback must not take down the notification worker.
func TestPanickingStatusCallbackIsRecovered(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	m := &txManager{onStatusChange: func(context.Context, txmngr_provider.Transaction) error {
		calls.Add(1)
		panic("callback exploded")
	}}

	err := m.callStatusCallback(context.Background(), txmngr_provider.Transaction{ID: "boom"})
	require.ErrorContains(t, err, "panicked")
	require.Equal(t, int32(1), calls.Load())
}

func TestSendErrorClassificationCoversEveryPredicate(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		message            string
		code               int
		alreadyKnown       bool
		nonceTooLow        bool
		feePolicy          bool
		permanentRejection bool
		retryableRejection bool
		disposition        broadcastErrorDisposition
	}{
		{message: "already known", code: -32000, alreadyKnown: true, disposition: broadcastAlreadyKnown},
		{message: "known transaction: 0xabc", alreadyKnown: true, disposition: broadcastAlreadyKnown},
		{message: "unknown transaction", disposition: broadcastAmbiguous},
		{message: "nonce too low", nonceTooLow: true, disposition: broadcastNonceConsumed},
		{message: "Nonce Too Low: next nonce 5", code: -32000, nonceTooLow: true, disposition: broadcastNonceConsumed},
		{message: "standard nonce error", code: 1, nonceTooLow: true, disposition: broadcastNonceConsumed},
		{message: "replacement transaction underpriced", feePolicy: true, disposition: broadcastFeeRejected},
		{message: "replacement underpriced", feePolicy: true, disposition: broadcastFeeRejected},
		{message: "transaction underpriced", feePolicy: true, disposition: broadcastFeeRejected},
		{message: "fee too low to replace", feePolicy: true, disposition: broadcastFeeRejected},
		{message: "max fee per gas less than block base fee", feePolicy: true, disposition: broadcastFeeRejected},
		{message: "max fee per gas below block base fee", feePolicy: true, disposition: broadcastFeeRejected},
		{message: "fee cap less than block base fee", feePolicy: true, disposition: broadcastFeeRejected},
		{message: "fee cap below base fee", feePolicy: true, disposition: broadcastFeeRejected},
		{message: "standard base-fee error", code: 806, feePolicy: true, disposition: broadcastFeeRejected},
		{message: "standard known transaction", code: 1000, alreadyKnown: true, disposition: broadcastAlreadyKnown},
		{message: "execution client rejected transaction", code: -32000, disposition: broadcastAmbiguous},
		{message: "invalid params", code: -32602, permanentRejection: true, disposition: broadcastTransactionRejectedPermanent},
		{message: "standard transaction gas limit error", code: 803, permanentRejection: true, disposition: broadcastTransactionRejectedPermanent},
		{message: "gas limit too high: have 17597473, max 16777216", code: -32000, permanentRejection: true, disposition: broadcastTransactionRejectedPermanent},
		{message: "legacy block gas limit error", code: -38015, permanentRejection: true, disposition: broadcastTransactionRejectedPermanent},
		{message: "legacy intrinsic gas error", code: -38013, permanentRejection: true, disposition: broadcastTransactionRejectedPermanent},
		{message: "standard nonce gap", code: 2, retryableRejection: true, disposition: broadcastTransactionRejectedRetryable},
		{message: "legacy nonce gap", code: -38011, retryableRejection: true, disposition: broadcastTransactionRejectedRetryable},
		{message: "standard insufficient funds", code: 809, retryableRejection: true, disposition: broadcastTransactionRejectedRetryable},
		{message: "insufficient funds for gas * price + value", code: -38014, retryableRejection: true, disposition: broadcastTransactionRejectedRetryable},
		{message: "gas limit too high", disposition: broadcastAmbiguous},
		{message: "transaction rejected", code: -32003, disposition: broadcastAmbiguous},
		{message: "gas limit too high", code: -32003, permanentRejection: true, disposition: broadcastTransactionRejectedPermanent},
		{message: "internal error", code: -32603, disposition: broadcastAmbiguous},
		{message: "request timed out", code: -32002, disposition: broadcastAmbiguous},
		{message: "capacity exceeded", code: -32005, disposition: broadcastAmbiguous},
		{message: "vendor-specific failure", code: -32099, disposition: broadcastAmbiguous},
		{message: "connection reset by peer", disposition: broadcastAmbiguous},
	} {
		name := fmt.Sprintf("%d/%s", tt.code, tt.message)
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var err error = errors.New(tt.message)
			if tt.code != 0 {
				err = testRPCError{code: tt.code, message: tt.message}
			}
			require.Equal(t, tt.alreadyKnown, isAlreadyKnownError(err))
			require.Equal(t, tt.nonceTooLow, isNonceTooLowError(err))
			require.Equal(t, tt.feePolicy, isFeePolicyRejectionError(err))
			require.Equal(t, tt.permanentRejection, isPermanentTransactionRejectionError(err))
			require.Equal(t, tt.retryableRejection, isRetryableTransactionRejectionError(err))
			require.Equal(t, tt.disposition, classifyBroadcastError(err))
		})
	}

	require.False(t, isAlreadyKnownError(nil))
	require.False(t, isNonceTooLowError(nil))
	require.False(t, isFeePolicyRejectionError(nil))
	require.False(t, isPermanentTransactionRejectionError(nil))
	require.False(t, isRetryableTransactionRejectionError(nil))
	require.Equal(t, broadcastAmbiguous, classifyBroadcastError(nil))
}

func TestRPCErrorSummaryRedactsCredentialsAndTransportDetails(t *testing.T) {
	t.Parallel()

	transportErr := &url.Error{
		Op:  "Post",
		URL: "https://rpc-user:rpc-password@rpc.example/v2/path-secret?token=query-secret",
		Err: errors.New("connection reset by peer"),
	}
	transportSummary := rpcErrorSummary(transportErr)
	require.Equal(t, "rpc transport Post failed", transportSummary)
	for _, secret := range []string{"rpc-user", "rpc-password", "path-secret", "query-secret"} {
		require.NotContains(t, transportSummary, secret)
	}

	responseErr := testRPCError{
		code: -32000,
		message: "gas limit too high via https://rpc.example/v2/url-secret " +
			"Bearer bearer-secret eyJheader.payload.signature",
	}
	responseSummary := rpcErrorSummary(responseErr)
	require.Contains(t, responseSummary, "rpc error -32000")
	require.Contains(t, responseSummary, "gas limit too high")
	for _, secret := range []string{"url-secret", "bearer-secret", "eyJheader", "payload", "signature"} {
		require.NotContains(t, responseSummary, secret)
	}

	httpSummary := rpcErrorSummary(rpc.HTTPError{
		StatusCode: 401,
		Status:     "Unauthorized",
		Body:       []byte("credential-bearing response body"),
	})
	require.Equal(t, "rpc HTTP status 401", httpSummary)
	require.NotContains(t, httpSummary, "credential-bearing")
}
