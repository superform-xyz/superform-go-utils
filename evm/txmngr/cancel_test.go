package txmngr

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
	mock2 "github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/superform-xyz/superform-go-utils/evm/txmngr/mock"
	"github.com/superform-xyz/superform-go-utils/evm/txmngr/txmngr_provider"
)

// balanceEth supplies BalanceAt. Receipts in mined win over the mock.
type balanceEth struct {
	*mock.EthClient
	balance    *big.Int
	balErr     error
	suggestErr error
	quoted     *big.Int
	mined      map[common.Hash]*types.Receipt
}

func (c balanceEth) SuggestGasPrice(ctx context.Context) (*big.Int, error) {
	if c.suggestErr != nil {
		return nil, c.suggestErr
	}
	if c.quoted != nil {
		return new(big.Int).Set(c.quoted), nil
	}
	return c.EthClient.SuggestGasPrice(ctx)
}

func (c balanceEth) BalanceAt(context.Context, common.Address, *big.Int) (*big.Int, error) {
	if c.balErr != nil {
		return nil, c.balErr
	}
	if c.balance == nil {
		return nil, nil
	}
	return new(big.Int).Set(c.balance), nil
}

func (c balanceEth) TransactionReceipt(ctx context.Context, hash common.Hash) (*types.Receipt, error) {
	if c.mined != nil {
		if receipt, ok := c.mined[hash]; ok && receipt != nil {
			return receipt, nil
		}
	}
	return c.EthClient.TransactionReceipt(ctx, hash)
}

type exhaustedBuild struct {
	id         string
	nonce      uint64
	gas        uint64
	price      int64
	tip        int64
	feeCap     int64
	setCode    bool
	age        time.Duration
	priorAge   time.Duration
	pendingAge time.Duration
	self       bool
	zeroTip    bool
	value      *big.Int
	balance    *big.Int
	balErr     error
	suggestErr error
	quoted     *big.Int
	bump       time.Duration
	baseFee    int64
	suggested  int64
	tipQuote   int64
	optedOut   bool
}

func (b exhaustedBuild) policy() Option {
	return WithCancellationPolicy(map[uint64]CancellationPolicy{
		uint64(chainId): {PendingAge: b.pendingAge},
	})
}

func newExhaustedLane(t *testing.T, build exhaustedBuild) (*fixture, *mock.EthClient, balanceEth, *types.Transaction) {
	t.Helper()
	if build.id == "" {
		build.id = "exhausted"
	}
	if build.gas == 0 {
		build.gas = 500_000
	}
	if build.price == 0 {
		build.price = 100
	}
	if build.age == 0 {
		build.age = time.Minute
	}
	if build.suggested == 0 {
		build.suggested = build.price
	}
	if build.baseFee == 0 {
		build.baseFee = 1
	}
	auth := newSigner(t)
	now := time.Now()
	created := now.Add(-build.age)
	payloadTo := common.HexToAddress("0x00000000000000000000000000000000000000aa")
	payload := []byte{0xab, 0xcd}
	value := big.NewInt(0)
	if build.value != nil {
		value = build.value
	}
	if build.self {
		payloadTo = auth.From
		payload = nil
	}
	cl := mock.NewEthClient(t)
	mined := map[common.Hash]*types.Receipt{}
	client := balanceEth{
		EthClient:  cl,
		balance:    build.balance,
		balErr:     build.balErr,
		suggestErr: build.suggestErr,
		quoted:     build.quoted,
		mined:      mined,
	}
	if build.balance == nil && build.balErr == nil {
		client.balance = new(big.Int).Mul(big.NewInt(build.price), new(big.Int).SetUint64(build.gas))
	}
	row := txmngr_provider.Transaction{
		ID:            build.id,
		ChainID:       uint64(chainId),
		Sender:        auth.From,
		Address:       payloadTo,
		Data:          payload,
		Value:         value,
		GasLimit:      build.gas,
		AssignedNonce: ptr(build.nonce),
		Status:        txmngr_provider.ConfirmingTxStatus,
		NextActionAt:  now.Add(-time.Second),
		CreatedAt:     created,
		UpdatedAt:     now,
	}
	if build.setCode {
		row.RawAuthorizations = []types.SetCodeAuthorization{{
			Address: common.HexToAddress("0x0000000000000000000000000000000000000abc"),
			Nonce:   1,
		}}
	}
	var managerOpts []Option
	if !build.optedOut {
		managerOpts = []Option{build.policy()}
	}
	f := newFixture(t, fixtureOpts{
		auth:         auth,
		client:       client,
		managerOpts:  managerOpts,
		bumpInterval: build.bump,
		seed:         map[string]txmngr_provider.Transaction{build.id: row},
	})
	var latest *types.Transaction
	for attempt := 1; attempt <= maxFeeReplacements; attempt++ {
		// The mem store keeps one hash per logical transaction, so each bump differs by one wei.
		step := int64(maxFeeReplacements - attempt)
		var unsigned *types.Transaction
		if build.setCode {
			tip := build.tip
			capFee := build.feeCap
			if tip == 0 && !build.zeroTip {
				tip = 10
			}
			if capFee == 0 {
				capFee = build.price
			}
			// Matches signAttempt: no chain id on the unsigned tx, auth list copied through.
			unsigned = types.NewTx(&types.SetCodeTx{
				Nonce:     build.nonce,
				GasTipCap: uint256.NewInt(uint64(tip + step)),
				GasFeeCap: uint256.NewInt(uint64(capFee - step)),
				Gas:       build.gas,
				To:        payloadTo,
				Value:     uint256.MustFromBig(value),
				Data:      append([]byte(nil), payload...),
				AuthList: []types.SetCodeAuthorization{{
					Address: common.HexToAddress("0x0000000000000000000000000000000000000abc"),
					Nonce:   1,
				}},
			})
		} else {
			unsigned = types.NewTx(&types.LegacyTx{
				Nonce:    build.nonce,
				To:       &payloadTo,
				Gas:      build.gas,
				GasPrice: big.NewInt(build.price - step),
				Value:    value,
				Data:     append([]byte(nil), payload...),
			})
		}
		signed, err := auth.Signer(auth.From, unsigned)
		require.NoError(t, err)
		latest = signed
		signedAt := created.Add(time.Duration(attempt) * time.Millisecond)
		if build.priorAge > 0 && attempt < maxFeeReplacements {
			signedAt = now.Add(-build.priorAge).Add(time.Duration(attempt) * time.Millisecond)
		}
		f.seedAttempt(build.id, signed, signedAt)
	}
	// A nil parent transaction resumes the latest hash and never reaches cancellation.
	stored := f.get(build.id)
	stored.Tx = latest
	stored.BroadcastPending = false
	require.NoError(t, f.db.Insert(*stored))
	cl.On("HeaderByNumber", mock2.Anything, (*big.Int)(nil)).
		Return(&types.Header{BaseFee: big.NewInt(build.baseFee)}, nil).Maybe()
	cl.On("SuggestGasPrice", mock2.Anything).
		Return(big.NewInt(build.suggested), nil).Maybe()
	if build.setCode {
		tipQuote := build.tipQuote
		if tipQuote == 0 && !build.zeroTip {
			tipQuote = build.tip
			if tipQuote == 0 {
				tipQuote = 10
			}
		}
		cl.On("SuggestGasTipCap", mock2.Anything).Return(big.NewInt(tipQuote), nil).Maybe()
	}
	cl.On("TransactionReceipt", mock2.Anything, mock2.Anything).
		Return(nil, ethereum.NotFound).Maybe()
	return f, cl, client, latest
}

func TestMarketSpikeSignsSameNonceCancellation(t *testing.T) {
	t.Parallel()
	f, cl, _, latest := newExhaustedLane(t, exhaustedBuild{
		nonce:     7,
		suggested: 250,
		balance:   big.NewInt(1 << 60),
	})
	cl.On("SendTransaction", mock2.Anything, mock2.MatchedBy(func(tx *types.Transaction) bool {
		return tx != nil && tx.To() != nil && *tx.To() == f.auth.From && len(tx.Data()) == 0 &&
			tx.GasPrice().Cmp(big.NewInt(250)) == 0 && tx.Gas() == params.TxGas
	})).Return(nil).Once()

	require.NoError(t, f.step("exhausted"))

	attempts, err := f.db.SelectAttempts("exhausted")
	require.NoError(t, err)
	require.Len(t, attempts, maxFeeReplacements+1)
	cancel := attempts[len(attempts)-1]
	require.Equal(t, f.auth.From, *cancel.Tx.To())
	require.Empty(t, cancel.Tx.Data())
	require.Equal(t, latest.Nonce(), cancel.Tx.Nonce())
	require.NotEqual(t, latest.Hash(), cancel.Hash)
	stored := f.get("exhausted")
	require.Equal(t, txmngr_provider.ConfirmingTxStatus, stored.Status)
	require.Equal(t, CancellationReasonPending, stored.Msg)
	require.Equal(t, cancel.Hash, stored.Tx.Hash())
}

func TestCancelRepriceClearsTenPercentOverThePreviousCancel(t *testing.T) {
	t.Parallel()
	quoted := big.NewInt(250)
	f, cl, _, _ := newExhaustedLane(t, exhaustedBuild{
		nonce:     11,
		suggested: 250,
		quoted:    quoted,
		baseFee:   1,
		balance:   big.NewInt(1 << 60),
		bump:      time.Millisecond,
	})
	cl.On("SendTransaction", mock2.Anything, mock2.Anything).Return(nil).Maybe()

	require.NoError(t, f.step("exhausted"))
	first, err := f.db.SelectAttempts("exhausted")
	require.NoError(t, err)
	require.Len(t, first, maxFeeReplacements+1)
	require.Equal(t, int64(250), first[len(first)-1].Tx.GasPrice().Int64())

	// The bump gate is one millisecond; an earlier second pass only reschedules.
	time.Sleep(5 * time.Millisecond)
	quoted.SetInt64(260)
	require.NoError(t, f.step("exhausted"))
	second, err := f.db.SelectAttempts("exhausted")
	require.NoError(t, err)
	require.Len(t, second, maxFeeReplacements+2)
	floor := big.NewInt(275)
	require.GreaterOrEqual(t, second[len(second)-1].Tx.GasPrice().Cmp(floor), 0)
	require.Equal(t, first[len(first)-1].Tx.Nonce(), second[len(second)-1].Tx.Nonce())
}

func TestAgedPayloadSignsCancellationWithoutAHigherMarket(t *testing.T) {
	t.Parallel()
	f, cl, _, _ := newExhaustedLane(t, exhaustedBuild{
		nonce:      4,
		suggested:  100,
		baseFee:    50,
		pendingAge: time.Millisecond,
		age:        time.Hour,
		balance:    new(big.Int).Mul(big.NewInt(100), big.NewInt(500_000)),
	})
	cl.On("SendTransaction", mock2.Anything, mock2.MatchedBy(func(tx *types.Transaction) bool {
		return tx.GasPrice().Cmp(big.NewInt(110)) == 0 && *tx.To() == f.auth.From
	})).Return(nil).Once()

	require.NoError(t, f.step("exhausted"))
	attempts, err := f.db.SelectAttempts("exhausted")
	require.NoError(t, err)
	require.Len(t, attempts, maxFeeReplacements+1)
}

func TestIncludablePayloadIsRebroadcastUntilTheAgeBound(t *testing.T) {
	t.Parallel()
	f, cl, _, latest := newExhaustedLane(t, exhaustedBuild{
		nonce:      4,
		suggested:  100,
		baseFee:    50,
		pendingAge: time.Hour,
		age:        time.Minute,
		balance:    new(big.Int).Mul(big.NewInt(100), big.NewInt(500_000)),
	})
	cl.On("SendTransaction", mock2.Anything, latest).Return(nil).Once()

	require.NoError(t, f.step("exhausted"))
	attempts, err := f.db.SelectAttempts("exhausted")
	require.NoError(t, err)
	require.Len(t, attempts, maxFeeReplacements)
	require.Equal(t, latest.Hash(), attempts[len(attempts)-1].Hash)
	require.NotEqual(t, txmngr_provider.SucceedTxStatus, f.get("exhausted").Status)
}

func underpricedPayload(latest *types.Transaction) string {
	return "broadcast attempt " + latest.Hash().Hex() + ": replacement transaction underpriced"
}

func TestAcceptedRebroadcastClearsAnUnderpricedPayload(t *testing.T) {
	t.Parallel()
	f, cl, _, latest := newExhaustedLane(t, exhaustedBuild{
		nonce:    50,
		optedOut: true,
		age:      time.Hour,
	})
	row := f.get("exhausted")
	row.Msg = underpricedPayload(latest)
	row.BroadcastPending = false
	require.NoError(t, f.db.Insert(*row))
	cl.On("SendTransaction", mock2.Anything, latest).Return(nil).Once()

	require.NoError(t, f.step("exhausted"))
	stored := f.get("exhausted")
	require.Empty(t, stored.Msg)
	require.False(t, stored.BroadcastPending)
	require.Equal(t, txmngr_provider.ConfirmingTxStatus, stored.Status)
	require.NotNil(t, stored.AssignedNonce)
	require.Equal(t, uint64(50), *stored.AssignedNonce)
}

func TestRetryableRejectionReplacesAnUnderpricedPayload(t *testing.T) {
	t.Parallel()
	f, cl, _, latest := newExhaustedLane(t, exhaustedBuild{
		nonce:    51,
		optedOut: true,
		age:      time.Hour,
	})
	row := f.get("exhausted")
	row.Msg = underpricedPayload(latest)
	row.BroadcastPending = false
	require.NoError(t, f.db.Insert(*row))
	cl.On("SendTransaction", mock2.Anything, latest).
		Return(testRPCError{code: -38014, message: "insufficient funds for gas * price + value"}).Once()

	require.NoError(t, f.step("exhausted"))
	stored := f.get("exhausted")
	require.Contains(t, stored.Msg, "insufficient funds")
	require.NotContains(t, stored.Msg, "underpriced")
	require.True(t, stored.BroadcastPending)
	require.NotNil(t, stored.AssignedNonce)
	require.Equal(t, uint64(51), *stored.AssignedNonce)
	attempts, err := f.db.SelectAttempts("exhausted")
	require.NoError(t, err)
	require.Len(t, attempts, maxFeeReplacements)
}

func TestPriceFloorRejectionRepricesTheCancel(t *testing.T) {
	t.Parallel()
	f, cl, _, _ := newExhaustedLane(t, exhaustedBuild{
		nonce:     52,
		suggested: 100,
		baseFee:   50,
		balance:   big.NewInt(1 << 60),
		age:       time.Hour,
	})
	cancel, err := f.auth.Signer(f.auth.From, types.NewTx(&types.LegacyTx{
		Nonce:    52,
		To:       &f.auth.From,
		Gas:      params.TxGas,
		GasPrice: big.NewInt(110),
		Value:    big.NewInt(0),
	}))
	require.NoError(t, err)
	f.seedAttempt("exhausted", cancel, time.Now().Add(-time.Minute))
	row := f.get("exhausted")
	row.Tx = cancel
	row.BroadcastPending = true
	row.Msg = ""
	require.NoError(t, f.db.Insert(*row))
	cl.On("SendTransaction", mock2.Anything, cancel).
		Return(testRPCError{
			code:    -32000,
			message: "transaction gas price below minimum: gas tip cap 110, minimum needed 200",
		}).Once()
	cl.On("SendTransaction", mock2.Anything, mock2.MatchedBy(func(tx *types.Transaction) bool {
		return tx.To() != nil && *tx.To() == f.auth.From && tx.GasPrice().Cmp(big.NewInt(110)) > 0
	})).Return(nil).Once()

	require.NoError(t, f.step("exhausted"))
	rejected := f.get("exhausted")
	require.Contains(t, rejected.Msg, "transaction gas price below minimum")
	require.False(t, rejected.BroadcastPending)
	attempts, err := f.db.SelectAttempts("exhausted")
	require.NoError(t, err)
	require.Len(t, attempts, maxFeeReplacements+1)

	require.NoError(t, f.step("exhausted"))
	repriced, err := f.db.SelectAttempts("exhausted")
	require.NoError(t, err)
	require.Len(t, repriced, maxFeeReplacements+2)
	require.Positive(t, repriced[len(repriced)-1].Tx.GasPrice().Cmp(big.NewInt(110)))
	require.Equal(t, uint64(52), repriced[len(repriced)-1].Tx.Nonce())
}

func TestPendingAgeUsesTheNewestPayloadSignature(t *testing.T) {
	t.Parallel()
	f, cl, _, latest := newExhaustedLane(t, exhaustedBuild{
		nonce:      5,
		suggested:  100,
		baseFee:    50,
		pendingAge: time.Hour,
		age:        time.Minute,
		priorAge:   2 * time.Hour,
		balance:    new(big.Int).Mul(big.NewInt(100), big.NewInt(500_000)),
	})
	cl.On("SendTransaction", mock2.Anything, latest).Return(nil).Once()

	require.NoError(t, f.step("exhausted"))
	attempts, err := f.db.SelectAttempts("exhausted")
	require.NoError(t, err)
	require.Len(t, attempts, maxFeeReplacements)
	require.Equal(t, latest.Hash(), attempts[len(attempts)-1].Hash)
	require.Empty(t, f.get("exhausted").Msg)
}

func TestCancellationReceiptIsNotStrategySuccess(t *testing.T) {
	t.Parallel()
	f, cl, client, _ := newExhaustedLane(t, exhaustedBuild{
		nonce:     9,
		suggested: 250,
		balance:   big.NewInt(1 << 60),
	})
	cl.On("SendTransaction", mock2.Anything, mock2.Anything).Return(nil).Once()
	require.NoError(t, f.step("exhausted"))
	attempts, err := f.db.SelectAttempts("exhausted")
	require.NoError(t, err)
	cancel := attempts[len(attempts)-1]
	client.mined[cancel.Hash] = &types.Receipt{
		Status:      types.ReceiptStatusSuccessful,
		TxHash:      cancel.Hash,
		BlockNumber: big.NewInt(12),
		GasUsed:     params.TxGas,
	}

	require.NoError(t, f.step("exhausted"))
	stored := f.get("exhausted")
	require.Equal(t, txmngr_provider.ErroredTxStatus, stored.Status)
	require.Equal(t, CancellationReasonMined, stored.Msg)
	require.Equal(t, cancel.Hash, stored.Tx.Hash())
	require.Equal(t, cancel.Hash, stored.Receipt.TxHash)
	require.NotEqual(t, txmngr_provider.SucceedTxStatus, stored.Status)
}

func TestCancelledGaugeUsesOldestPayloadSignature(t *testing.T) {
	t.Parallel()
	f, cl, client, _ := newExhaustedLane(t, exhaustedBuild{
		nonce:     41,
		suggested: 250,
		age:       time.Minute,
		balance:   big.NewInt(1 << 60),
	})
	row := f.get("exhausted")
	row.CreatedAt = time.Now().Add(-3 * time.Hour)
	require.NoError(t, f.db.Insert(*row))
	var cancelled *LaneRecoveryEvent
	f.mngr.laneRecovery = func(event LaneRecoveryEvent) {
		if event.Status != LaneRecoveryCancelled {
			return
		}
		copied := event
		cancelled = &copied
	}
	cl.On("SendTransaction", mock2.Anything, mock2.Anything).Return(nil).Once()
	require.NoError(t, f.step("exhausted"))
	attempts, err := f.db.SelectAttempts("exhausted")
	require.NoError(t, err)
	cancel := attempts[len(attempts)-1]
	client.mined[cancel.Hash] = &types.Receipt{
		Status:      types.ReceiptStatusSuccessful,
		TxHash:      cancel.Hash,
		BlockNumber: big.NewInt(12),
		GasUsed:     params.TxGas,
	}

	require.NoError(t, f.step("exhausted"))
	require.NotNil(t, cancelled)
	require.Less(t, cancelled.BlockedAge, 2*time.Minute)
	require.Greater(t, cancelled.BlockedAge, 30*time.Second)
	require.Equal(t, maxFeeReplacements, cancelled.PayloadAttempts)
}

func TestRevertedCancellationReceiptStillConsumesTheNonce(t *testing.T) {
	t.Parallel()
	f, cl, client, _ := newExhaustedLane(t, exhaustedBuild{
		nonce:     9,
		suggested: 250,
		balance:   big.NewInt(1 << 60),
	})
	cl.On("SendTransaction", mock2.Anything, mock2.Anything).Return(nil).Once()
	require.NoError(t, f.step("exhausted"))
	attempts, err := f.db.SelectAttempts("exhausted")
	require.NoError(t, err)
	cancel := attempts[len(attempts)-1]
	client.mined[cancel.Hash] = &types.Receipt{
		Status:      types.ReceiptStatusFailed,
		TxHash:      cancel.Hash,
		BlockNumber: big.NewInt(12),
		GasUsed:     params.TxGas,
	}

	require.NoError(t, f.step("exhausted"))
	stored := f.get("exhausted")
	require.Equal(t, txmngr_provider.ErroredTxStatus, stored.Status)
	require.Equal(t, CancellationReasonMined, stored.Msg)
	require.NotNil(t, stored.AssignedNonce)
}

func TestOriginalPayloadWinsTheRaceWithCancellation(t *testing.T) {
	t.Parallel()
	f, cl, client, _ := newExhaustedLane(t, exhaustedBuild{
		nonce:     9,
		suggested: 250,
		balance:   big.NewInt(1 << 60),
	})
	cl.On("SendTransaction", mock2.Anything, mock2.Anything).Return(nil).Once()
	require.NoError(t, f.step("exhausted"))
	attempts, err := f.db.SelectAttempts("exhausted")
	require.NoError(t, err)
	original := attempts[0]
	client.mined[original.Hash] = &types.Receipt{
		Status:      types.ReceiptStatusSuccessful,
		TxHash:      original.Hash,
		BlockNumber: big.NewInt(4),
		GasUsed:     500_000,
	}

	require.NoError(t, f.step("exhausted"))
	stored := f.get("exhausted")
	require.Equal(t, txmngr_provider.SucceedTxStatus, stored.Status)
	require.Empty(t, stored.Msg)
	require.Equal(t, original.Hash, stored.Tx.Hash())
	require.Equal(t, original.Hash, stored.Receipt.TxHash)
	reloaded, err := f.db.SelectAttempts("exhausted")
	require.NoError(t, err)
	require.Len(t, reloaded, maxFeeReplacements+1)
}

func TestFundingShortageDoesNotSignOrFreeTheLane(t *testing.T) {
	t.Parallel()
	const nonce = uint64(11)
	f, cl, _, _ := newExhaustedLane(t, exhaustedBuild{
		nonce:      nonce,
		suggested:  100,
		baseFee:    50,
		pendingAge: time.Hour,
		balance:    big.NewInt(0),
	})
	now := time.Now()
	require.NoError(t, f.db.Insert(txmngr_provider.Transaction{
		ID:           "other-vault",
		ChainID:      uint64(chainId),
		Sender:       f.auth.From,
		Address:      common.HexToAddress("0x00000000000000000000000000000000000000bb"),
		Data:         []byte{0x22},
		Value:        big.NewInt(0),
		GasLimit:     21_000,
		Status:       txmngr_provider.PendingTxStatus,
		NextActionAt: now.Add(-time.Hour),
		CreatedAt:    now.Add(-time.Hour),
		UpdatedAt:    now,
	}))
	head := f.get("exhausted")
	head.BroadcastPending = true
	head.Msg = "broadcast attempt " + head.Tx.Hash().Hex() + ": insufficient funds for gas * price + value"
	require.NoError(t, f.db.Insert(*head))
	cl.On("SendTransaction", mock2.Anything, mock2.Anything).Return(nil).Maybe()
	cl.On("PendingNonceAt", mock2.Anything, f.auth.From).Return(nonce, nil).Maybe()
	calls := countSigner(f.auth)

	f.mngr.processLaneBatch(t.Context(), uint64(chainId))
	attempts, err := f.db.SelectAttempts("exhausted")
	require.NoError(t, err)
	require.Len(t, attempts, maxFeeReplacements)
	stored := f.get("exhausted")
	require.Equal(t, txmngr_provider.ConfirmingTxStatus, stored.Status)
	require.False(t, stored.BroadcastPending)
	require.Zero(t, *calls)
	require.Contains(t, stored.Msg, CancellationReasonFunding)
	required, available, ok := FundingShortfall(stored.Msg)
	require.True(t, ok)
	require.Positive(t, required.Sign())
	require.Zero(t, available.Sign())
	require.NotNil(t, stored.AssignedNonce)
	require.Equal(t, nonce, *stored.AssignedNonce)
	require.Nil(t, f.get("other-vault").AssignedNonce)

	require.NoError(t, f.step("exhausted"))
	require.Zero(t, *calls)
	reloaded, err := f.db.SelectAttempts("exhausted")
	require.NoError(t, err)
	require.Len(t, reloaded, maxFeeReplacements)
	require.Contains(t, f.get("exhausted").Msg, CancellationReasonFunding)
}

func TestSpendLimitRejectsUnboundedCancellation(t *testing.T) {
	t.Parallel()
	f, cl, _, latest := newExhaustedLane(t, exhaustedBuild{
		nonce:      3,
		gas:        params.TxGas,
		price:      100,
		suggested:  100,
		baseFee:    50,
		pendingAge: time.Millisecond,
		age:        time.Hour,
		balance:    big.NewInt(1 << 60),
	})
	calls := countSigner(f.auth)
	cl.On("SendTransaction", mock2.Anything, latest).Return(nil).Once()

	require.NoError(t, f.step("exhausted"))
	attempts, err := f.db.SelectAttempts("exhausted")
	require.NoError(t, err)
	require.Len(t, attempts, maxFeeReplacements)
	require.Equal(t, CancellationReasonFeeLimit, f.get("exhausted").Msg)
	require.Equal(t, txmngr_provider.ConfirmingTxStatus, f.get("exhausted").Status)
	require.False(t, f.get("exhausted").BroadcastPending)
	require.Zero(t, *calls)
	cl.AssertExpectations(t)
}

func TestRestartRebroadcastsTheCancellationHash(t *testing.T) {
	t.Parallel()
	f, cl, _, _ := newExhaustedLane(t, exhaustedBuild{
		nonce:     8,
		suggested: 100,
		baseFee:   50,
		balance:   big.NewInt(1 << 60),
	})
	cancel, err := f.auth.Signer(f.auth.From, types.NewTx(&types.LegacyTx{
		Nonce:    8,
		To:       &f.auth.From,
		Gas:      params.TxGas,
		GasPrice: big.NewInt(110),
		Value:    big.NewInt(0),
	}))
	require.NoError(t, err)
	f.seedAttempt("exhausted", cancel, time.Now().Add(-30*time.Second))
	row := f.get("exhausted")
	row.Tx = cancel
	row.BroadcastPending = false
	require.NoError(t, f.db.Insert(*row))
	cl.On("SendTransaction", mock2.Anything, cancel).Return(nil).Once()

	require.NoError(t, f.step("exhausted"))
	attempts, err := f.db.SelectAttempts("exhausted")
	require.NoError(t, err)
	require.Len(t, attempts, maxFeeReplacements+1)
	require.Equal(t, cancel.Hash(), attempts[len(attempts)-1].Hash)
	require.Equal(t, CancellationReasonPending, f.get("exhausted").Msg)
}

func TestForeignNonceAbandonReleasesTheLaneHead(t *testing.T) {
	t.Parallel()
	const nonce = uint64(6)
	f, cl, _, _ := newExhaustedLane(t, exhaustedBuild{
		nonce:     nonce,
		suggested: 250,
		balance:   big.NewInt(1 << 60),
	})
	var released *LaneRecoveryEvent
	f.mngr.laneRecovery = func(event LaneRecoveryEvent) {
		if event.Status != LaneRecoveryConsumed {
			return
		}
		copied := event
		released = &copied
	}
	cl.On("SendTransaction", mock2.Anything, mock2.Anything).
		Return(testRPCError{code: -38010, message: "nonce too low"}).Once()

	require.NoError(t, f.step("exhausted"))
	stored := f.get("exhausted")
	require.NotNil(t, stored.NonceConsumedAt)
	require.NotEqual(t, txmngr_provider.ErroredTxStatus, stored.Status)
	require.Nil(t, released)

	stored.NonceConsumedAt = ptr(time.Now().Add(-2 * consumedNonceGrace))
	stored.NextActionAt = time.Now().Add(-time.Second)
	require.NoError(t, f.db.Insert(*stored))

	require.NoError(t, f.step("exhausted"))
	abandoned := f.get("exhausted")
	require.Equal(t, txmngr_provider.ErroredTxStatus, abandoned.Status)
	require.NotEqual(t, CancellationReasonMined, abandoned.Msg)
	require.NotNil(t, abandoned.AssignedNonce)
	require.Equal(t, nonce, *abandoned.AssignedNonce)
	require.NotNil(t, released)
	require.NotNil(t, released.Nonce)
	require.Equal(t, nonce, *released.Nonce)
	require.Equal(t, maxFeeReplacements, released.PayloadAttempts)
}

func TestForeignNonceDuringCancellationIsNotExecutionSuccess(t *testing.T) {
	t.Parallel()
	f, cl, _, _ := newExhaustedLane(t, exhaustedBuild{
		nonce:     6,
		suggested: 250,
		balance:   big.NewInt(1 << 60),
	})
	cl.On("SendTransaction", mock2.Anything, mock2.Anything).
		Return(testRPCError{code: -38010, message: "nonce too low"}).Once()

	require.NoError(t, f.step("exhausted"))
	stored := f.get("exhausted")
	require.NotEqual(t, txmngr_provider.SucceedTxStatus, stored.Status)
	require.NotNil(t, stored.NonceConsumedAt)
	require.NotEqual(t, CancellationReasonMined, stored.Msg)
	attempts, err := f.db.SelectAttempts("exhausted")
	require.NoError(t, err)
	require.Len(t, attempts, maxFeeReplacements+1)
}

func TestBalanceReadFailureDoesNotCancelOrSucceed(t *testing.T) {
	t.Parallel()
	f, _, _, _ := newExhaustedLane(t, exhaustedBuild{
		nonce:     2,
		balErr:    ethereum.NotFound,
		baseFee:   50,
		suggested: 100,
	})

	err := f.step("exhausted")
	require.Error(t, err)
	attempts, loadErr := f.db.SelectAttempts("exhausted")
	require.NoError(t, loadErr)
	require.Len(t, attempts, maxFeeReplacements)
	require.NotEqual(t, txmngr_provider.SucceedTxStatus, f.get("exhausted").Status)
}

func TestSetCodeCancellationPricesTipAndFeeCapSeparately(t *testing.T) {
	t.Parallel()
	f, cl, _, _ := newExhaustedLane(t, exhaustedBuild{
		nonce:     15,
		setCode:   true,
		tip:       10,
		feeCap:    100,
		baseFee:   101,
		suggested: 101,
		tipQuote:  2,
		balance:   big.NewInt(1 << 62),
		gas:       500_000,
	})
	cl.On("SendTransaction", mock2.Anything, mock2.Anything).Return(nil).Once()

	require.NoError(t, f.step("exhausted"))
	attempts, err := f.db.SelectAttempts("exhausted")
	require.NoError(t, err)
	require.Len(t, attempts, maxFeeReplacements+1)
	cancel := attempts[len(attempts)-1].Tx
	require.Equal(t, byte(types.DynamicFeeTxType), cancel.Type())
	require.NotNil(t, cancel.To())
	require.Equal(t, f.auth.From, *cancel.To())
	require.Empty(t, cancel.Data())
	require.Empty(t, cancel.SetCodeAuthorizations())
	require.Equal(t, params.TxGas, cancel.Gas())
	require.GreaterOrEqual(t, cancel.GasTipCap().Cmp(big.NewInt(11)), 0)
	minimumCap := new(big.Int).Add(big.NewInt(101), cancel.GasTipCap())
	require.GreaterOrEqual(t, cancel.GasFeeCap().Cmp(minimumCap), 0)
	require.Negative(t, cancel.GasFeeCap().Cmp(big.NewInt(202)))
}

func TestSecondVaultAdvancesAfterCancellationMines(t *testing.T) {
	t.Parallel()
	const nonce = uint64(20)
	f, cl, client, _ := newExhaustedLane(t, exhaustedBuild{
		id:        "vault-a",
		nonce:     nonce,
		suggested: 250,
		balance:   big.NewInt(1 << 60),
	})
	now := time.Now()
	require.NoError(t, f.db.Insert(txmngr_provider.Transaction{
		ID:           "vault-b",
		ChainID:      uint64(chainId),
		Sender:       f.auth.From,
		Address:      common.HexToAddress("0x00000000000000000000000000000000000000bb"),
		Data:         []byte{0x22},
		Value:        big.NewInt(0),
		GasLimit:     21_000,
		Status:       txmngr_provider.PendingTxStatus,
		NextActionAt: now.Add(-time.Hour),
		CreatedAt:    now.Add(-time.Hour),
		UpdatedAt:    now,
	}))
	cl.On("SendTransaction", mock2.Anything, mock2.Anything).Return(nil).Maybe()
	cl.On("PendingNonceAt", mock2.Anything, f.auth.From).Return(nonce, nil).Once()
	cl.On("PendingNonceAt", mock2.Anything, f.auth.From).Return(nonce+1, nil).Maybe()

	f.mngr.processLaneBatch(t.Context(), uint64(chainId))
	require.Nil(t, f.get("vault-b").AssignedNonce, "the follower must wait while nonce N is unmined")
	require.Equal(t, txmngr_provider.ConfirmingTxStatus, f.get("vault-a").Status)

	attempts, err := f.db.SelectAttempts("vault-a")
	require.NoError(t, err)
	require.Len(t, attempts, maxFeeReplacements+1)
	cancel := attempts[len(attempts)-1]
	require.NotNil(t, cancel.Tx.To())
	require.Equal(t, f.auth.From, *cancel.Tx.To())
	require.Empty(t, cancel.Tx.Data())
	client.mined[cancel.Hash] = &types.Receipt{
		Status:      types.ReceiptStatusSuccessful,
		TxHash:      cancel.Hash,
		BlockNumber: big.NewInt(30),
		GasUsed:     params.TxGas,
	}
	due := f.get("vault-a")
	due.NextActionAt = time.Now().Add(-time.Second)
	require.NoError(t, f.db.Insert(*due))
	f.mngr.processLaneBatch(t.Context(), uint64(chainId))

	head := f.get("vault-a")
	require.Equal(t, txmngr_provider.ErroredTxStatus, head.Status)
	require.Equal(t, CancellationReasonMined, head.Msg)
	follower := f.get("vault-b")
	require.NotNil(t, follower.AssignedNonce)
	require.Equal(t, nonce+1, *follower.AssignedNonce)
}

func TestQuoteFailureStaysAFailure(t *testing.T) {
	t.Parallel()
	f, _, _, _ := newExhaustedLane(t, exhaustedBuild{
		nonce:      1,
		suggested:  100,
		suggestErr: ethereum.NotFound,
		balance:    big.NewInt(1 << 60),
	})

	err := f.step("exhausted")
	require.Error(t, err)
	require.NotErrorIs(t, err, ethereum.NotFound)
	attempts, loadErr := f.db.SelectAttempts("exhausted")
	require.NoError(t, loadErr)
	require.Len(t, attempts, maxFeeReplacements)
	require.NotEqual(t, txmngr_provider.SucceedTxStatus, f.get("exhausted").Status)
}

func countSigner(auth *bind.TransactOpts) *int {
	calls := 0
	inner := auth.Signer
	auth.Signer = func(address common.Address, tx *types.Transaction) (*types.Transaction, error) {
		calls++
		return inner(address, tx)
	}
	return &calls
}

func TestRetryablePayloadRejectionStillSignsCancellation(t *testing.T) {
	t.Parallel()
	const nonce = uint64(9)
	f, cl, _, latest := newExhaustedLane(t, exhaustedBuild{
		nonce:      nonce,
		suggested:  100,
		baseFee:    1,
		pendingAge: time.Hour,
		age:        time.Minute,
		balance:    big.NewInt(3_000_000),
	})
	stored := f.get("exhausted")
	stored.BroadcastPending = true
	stored.Msg = "broadcast attempt " + latest.Hash().Hex() + ": insufficient funds for gas * price + value"
	require.NoError(t, f.db.Insert(*stored))
	cl.On("SendTransaction", mock2.Anything, mock2.MatchedBy(func(tx *types.Transaction) bool {
		return tx != nil && tx.To() != nil && *tx.To() == f.auth.From && len(tx.Data()) == 0 &&
			tx.Gas() == params.TxGas
	})).Return(nil).Once()

	require.NoError(t, f.step("exhausted"))
	attempts, err := f.db.SelectAttempts("exhausted")
	require.NoError(t, err)
	require.Len(t, attempts, maxFeeReplacements+1)
	cancel := attempts[len(attempts)-1].Tx
	require.NotEqual(t, latest.Hash(), cancel.Hash())
	require.Equal(t, f.auth.From, *cancel.To())
	require.Equal(t, nonce, cancel.Nonce())
	require.Equal(t, CancellationReasonPending, f.get("exhausted").Msg)
}

func TestAlreadyKnownResendKeepsTheSpendLimit(t *testing.T) {
	t.Parallel()
	f, cl, _, _ := newExhaustedLane(t, exhaustedBuild{
		nonce:     8,
		suggested: 100,
		baseFee:   50,
		balance:   big.NewInt(1 << 60),
	})
	cancel, err := f.auth.Signer(f.auth.From, types.NewTx(&types.LegacyTx{
		Nonce:    8,
		To:       &f.auth.From,
		Gas:      params.TxGas,
		GasPrice: big.NewInt(110),
		Value:    big.NewInt(0),
	}))
	require.NoError(t, err)
	f.seedAttempt("exhausted", cancel, time.Now().Add(-30*time.Second))
	row := f.get("exhausted")
	row.Tx = cancel
	row.BroadcastPending = false
	row.Msg = CancellationReasonFeeLimit
	require.NoError(t, f.db.Insert(*row))
	cl.On("SendTransaction", mock2.Anything, cancel).
		Return(testRPCError{code: 1000, message: "already known"}).Once()

	require.NoError(t, f.step("exhausted"))
	require.Equal(t, CancellationReasonFeeLimit, f.get("exhausted").Msg)
	require.False(t, f.get("exhausted").BroadcastPending)
	attempts, err := f.db.SelectAttempts("exhausted")
	require.NoError(t, err)
	require.Len(t, attempts, maxFeeReplacements+1)
}

func TestPendingAgeIsNotDeferredForTheBumpInterval(t *testing.T) {
	t.Parallel()
	f, cl, _, _ := newExhaustedLane(t, exhaustedBuild{
		nonce:      4,
		suggested:  100,
		baseFee:    50,
		pendingAge: time.Millisecond,
		age:        time.Minute,
		bump:       time.Hour,
		balance:    new(big.Int).Mul(big.NewInt(100), big.NewInt(500_000)),
	})
	cl.On("SendTransaction", mock2.Anything, mock2.MatchedBy(func(tx *types.Transaction) bool {
		return tx != nil && tx.To() != nil && *tx.To() == f.auth.From && len(tx.Data()) == 0
	})).Return(nil).Once()

	require.NoError(t, f.step("exhausted"))
	attempts, err := f.db.SelectAttempts("exhausted")
	require.NoError(t, err)
	require.Len(t, attempts, maxFeeReplacements+1)
	require.Equal(t, uint64(params.TxGas), attempts[len(attempts)-1].Tx.Gas())
}

func TestLogicalSelfTransferIsNotCancelled(t *testing.T) {
	t.Parallel()
	f, cl, _, latest := newExhaustedLane(t, exhaustedBuild{
		nonce:      4,
		self:       true,
		suggested:  100,
		baseFee:    50,
		pendingAge: time.Millisecond,
		age:        time.Hour,
		balance:    big.NewInt(1 << 60),
	})
	calls := countSigner(f.auth)
	cl.On("SendTransaction", mock2.Anything, latest).Return(nil).Once()

	require.NoError(t, f.step("exhausted"))
	attempts, err := f.db.SelectAttempts("exhausted")
	require.NoError(t, err)
	require.Len(t, attempts, maxFeeReplacements)
	require.Equal(t, latest.Hash(), attempts[len(attempts)-1].Hash)
	require.Zero(t, *calls)
	require.NotEqual(t, txmngr_provider.SucceedTxStatus, f.get("exhausted").Status)
}

func TestCancelHashConflictResignsAboveTheFloor(t *testing.T) {
	t.Parallel()
	const nonce = uint64(12)
	f, cl, _, _ := newExhaustedLane(t, exhaustedBuild{
		nonce:      nonce,
		suggested:  100,
		baseFee:    50,
		pendingAge: time.Millisecond,
		age:        time.Hour,
		balance:    big.NewInt(1 << 60),
	})
	owned, err := f.auth.Signer(f.auth.From, types.NewTx(&types.LegacyTx{
		Nonce:    nonce,
		To:       &f.auth.From,
		Gas:      params.TxGas,
		GasPrice: big.NewInt(110),
		Value:    big.NewInt(0),
	}))
	require.NoError(t, err)
	require.NoError(t, f.db.Insert(txmngr_provider.Transaction{
		ID:            "prior-cancel",
		ChainID:       uint64(chainId),
		Sender:        f.auth.From,
		Address:       f.auth.From,
		Value:         big.NewInt(0),
		Status:        txmngr_provider.ErroredTxStatus,
		AssignedNonce: ptr(nonce),
		CreatedAt:     time.Now().Add(-time.Hour),
		UpdatedAt:     time.Now().Add(-time.Hour),
	}))
	f.seedAttempt("prior-cancel", owned, time.Now().Add(-time.Hour))
	cl.On("SendTransaction", mock2.Anything, mock2.MatchedBy(func(tx *types.Transaction) bool {
		return tx != nil && tx.GasPrice() != nil && tx.GasPrice().Cmp(big.NewInt(121)) >= 0
	})).Return(nil).Once()

	require.NoError(t, f.step("exhausted"))
	attempts, err := f.db.SelectAttempts("exhausted")
	require.NoError(t, err)
	require.Len(t, attempts, maxFeeReplacements+1)
	require.GreaterOrEqual(t, attempts[len(attempts)-1].Tx.GasPrice().Cmp(big.NewInt(121)), 0)
	require.NotEqual(t, owned.Hash(), attempts[len(attempts)-1].Hash)
}

func TestZeroTipCancellationIsStrictlyHigher(t *testing.T) {
	t.Parallel()
	to := common.HexToAddress("0x00000000000000000000000000000000000000aa")
	previous := types.NewTx(&types.DynamicFeeTx{
		ChainID:   big.NewInt(1),
		Nonce:     1,
		GasTipCap: big.NewInt(0),
		GasFeeCap: big.NewInt(0),
		Gas:       params.TxGas,
		To:        &to,
		Value:     big.NewInt(0),
	})
	pricing, err := priceCancellation(previous, cancelQuote{
		tip:     big.NewInt(0),
		baseFee: big.NewInt(0),
	}, nil)
	require.NoError(t, err)
	require.Positive(t, pricing.tip.Sign())
	require.Positive(t, pricing.price.Sign())
	require.Positive(t, pricing.tip.Cmp(previous.GasTipCap()))
	require.Positive(t, pricing.price.Cmp(previous.GasFeeCap()))
	require.GreaterOrEqual(t, pricing.price.Cmp(pricing.tip), 0)

	legacy := types.NewTx(&types.LegacyTx{
		Nonce:    1,
		GasPrice: big.NewInt(0),
		Gas:      params.TxGas,
		To:       &to,
		Value:    big.NewInt(0),
	})
	legacyPricing, err := priceCancellation(legacy, cancelQuote{suggested: big.NewInt(0)}, nil)
	require.NoError(t, err)
	require.Positive(t, legacyPricing.price.Cmp(big.NewInt(0)))
}

func TestZeroTipSetCodeCancellationReplaces(t *testing.T) {
	t.Parallel()
	f, cl, _, _ := newExhaustedLane(t, exhaustedBuild{
		nonce:      16,
		setCode:    true,
		zeroTip:    true,
		feeCap:     100,
		gas:        500_000,
		baseFee:    50,
		suggested:  100,
		pendingAge: time.Millisecond,
		age:        time.Hour,
		balance:    big.NewInt(1 << 60),
	})
	cl.On("SendTransaction", mock2.Anything, mock2.MatchedBy(func(tx *types.Transaction) bool {
		return tx != nil && tx.Type() == types.DynamicFeeTxType &&
			len(tx.SetCodeAuthorizations()) == 0 &&
			tx.GasTipCap() != nil && tx.GasTipCap().Sign() > 0 &&
			tx.GasFeeCap() != nil && tx.GasFeeCap().Cmp(big.NewInt(100)) > 0
	})).Return(nil).Once()

	require.NoError(t, f.step("exhausted"))
	attempts, err := f.db.SelectAttempts("exhausted")
	require.NoError(t, err)
	require.Len(t, attempts, maxFeeReplacements+1)
	signed := attempts[len(attempts)-1].Tx
	require.Equal(t, byte(types.DynamicFeeTxType), signed.Type())
	require.Positive(t, signed.GasTipCap().Sign())
	require.Positive(t, signed.GasFeeCap().Cmp(big.NewInt(100)))
}

func TestRepriceRefusalRepublishesTheSignedCancel(t *testing.T) {
	t.Parallel()
	const nonce = uint64(18)
	f, cl, _, _ := newExhaustedLane(t, exhaustedBuild{
		nonce:      nonce,
		suggested:  200,
		baseFee:    50,
		pendingAge: time.Millisecond,
		age:        time.Hour,
		// Covers the signed 110-wei cancel (21000*110) and not the 200-wei reprice.
		balance: big.NewInt(3_000_000),
	})
	cancel, err := f.auth.Signer(f.auth.From, types.NewTx(&types.LegacyTx{
		Nonce:    nonce,
		To:       &f.auth.From,
		Gas:      params.TxGas,
		GasPrice: big.NewInt(110),
		Value:    big.NewInt(0),
	}))
	require.NoError(t, err)
	// Newer than the payload signatures, and the parent hash already matches.
	f.seedAttempt("exhausted", cancel, time.Now().Add(-time.Minute))
	row := f.get("exhausted")
	row.Tx = cancel
	row.BroadcastPending = false
	require.NoError(t, f.db.Insert(*row))
	calls := countSigner(f.auth)
	cl.On("SendTransaction", mock2.Anything, cancel).Return(nil).Once()

	require.NoError(t, f.step("exhausted"))
	attempts, err := f.db.SelectAttempts("exhausted")
	require.NoError(t, err)
	require.Len(t, attempts, maxFeeReplacements+1)
	require.Equal(t, cancel.Hash(), attempts[len(attempts)-1].Hash)
	require.Zero(t, *calls)
	require.Contains(t, f.get("exhausted").Msg, CancellationReasonFunding)
	require.False(t, f.get("exhausted").BroadcastPending)
}

func TestFundingMessageClearsWhenTheTriggerPasses(t *testing.T) {
	t.Parallel()
	f, cl, client, latest := newExhaustedLane(t, exhaustedBuild{
		nonce:     19,
		suggested: 100,
		baseFee:   1,
		price:     100,
		bump:      time.Hour,
		age:       time.Minute,
		balance:   big.NewInt(0),
	})
	calls := countSigner(f.auth)

	require.NoError(t, f.step("exhausted"))
	require.Zero(t, *calls)
	require.Contains(t, f.get("exhausted").Msg, CancellationReasonFunding)
	require.False(t, f.get("exhausted").BroadcastPending)

	client.balance.Set(new(big.Int).Lsh(big.NewInt(1), 40))
	cl.On("SendTransaction", mock2.Anything, latest).Return(nil).Once()
	require.NoError(t, f.step("exhausted"))
	require.Zero(t, *calls)
	_, _, ok := FundingShortfall(f.get("exhausted").Msg)
	require.False(t, ok)
	require.NotEqual(t, CancellationReasonFeeLimit, f.get("exhausted").Msg)
}

func TestDelegationSelfCallIsReplacedByADynamicCancel(t *testing.T) {
	t.Parallel()
	f, cl, _, payload := newExhaustedLane(t, exhaustedBuild{
		nonce:      21,
		self:       true,
		setCode:    true,
		tip:        10,
		feeCap:     100,
		gas:        100_000,
		baseFee:    50,
		suggested:  100,
		pendingAge: time.Millisecond,
		age:        time.Hour,
		balance:    big.NewInt(1 << 60),
	})
	require.NotEmpty(t, payload.SetCodeAuthorizations())
	require.False(t, isCancellationTransaction(
		payload, f.auth.From, f.auth.From, nil, big.NewInt(0), true,
	))
	cl.On("SendTransaction", mock2.Anything, mock2.MatchedBy(func(tx *types.Transaction) bool {
		return tx != nil && tx.Type() == types.DynamicFeeTxType &&
			len(tx.SetCodeAuthorizations()) == 0 &&
			tx.To() != nil && *tx.To() == f.auth.From &&
			len(tx.Data()) == 0
	})).Return(nil).Once()

	require.NoError(t, f.step("exhausted"))
	attempts, err := f.db.SelectAttempts("exhausted")
	require.NoError(t, err)
	require.Len(t, attempts, maxFeeReplacements+1)
	signed := attempts[len(attempts)-1].Tx
	require.Equal(t, byte(types.DynamicFeeTxType), signed.Type())
	require.Empty(t, signed.SetCodeAuthorizations())
	require.True(t, isCancellationTransaction(
		signed, f.auth.From, f.auth.From, nil, big.NewInt(0), true,
	))
	require.Equal(t, CancellationReasonPending, f.get("exhausted").Msg)
}

func TestTransferValueCountsTowardTheSpendCeiling(t *testing.T) {
	t.Parallel()
	f, cl, _, _ := newExhaustedLane(t, exhaustedBuild{
		nonce:      22,
		gas:        params.TxGas,
		price:      100,
		value:      big.NewInt(10_000_000),
		suggested:  100,
		baseFee:    50,
		pendingAge: time.Millisecond,
		age:        time.Hour,
		balance:    big.NewInt(1 << 60),
	})
	cl.On("SendTransaction", mock2.Anything, mock2.MatchedBy(func(tx *types.Transaction) bool {
		return tx != nil && tx.To() != nil && *tx.To() == f.auth.From && len(tx.Data()) == 0
	})).Return(nil).Once()

	require.NoError(t, f.step("exhausted"))
	attempts, err := f.db.SelectAttempts("exhausted")
	require.NoError(t, err)
	require.Len(t, attempts, maxFeeReplacements+1)
	require.Equal(t, CancellationReasonPending, f.get("exhausted").Msg)
}

func TestSiblingNonceRaisesTheFundingGate(t *testing.T) {
	t.Parallel()
	const nonce = uint64(30)
	cancelCost := new(big.Int).Mul(big.NewInt(int64(params.TxGas)), big.NewInt(110))
	f, _, _, _ := newExhaustedLane(t, exhaustedBuild{
		nonce:      nonce,
		suggested:  100,
		baseFee:    50,
		pendingAge: time.Millisecond,
		age:        time.Hour,
		balance:    cancelCost,
	})
	follower, err := f.auth.Signer(f.auth.From, types.NewTx(&types.LegacyTx{
		Nonce:    nonce + 1,
		To:       ptr(common.HexToAddress("0x00000000000000000000000000000000000000bb")),
		Gas:      500_000,
		GasPrice: big.NewInt(100),
		Value:    big.NewInt(0),
		Data:     []byte{0x11},
	}))
	require.NoError(t, err)
	now := time.Now()
	require.NoError(t, f.db.Insert(txmngr_provider.Transaction{
		ID:            "follower",
		ChainID:       uint64(chainId),
		Sender:        f.auth.From,
		Address:       common.HexToAddress("0x00000000000000000000000000000000000000bb"),
		Data:          []byte{0x11},
		Value:         big.NewInt(0),
		GasLimit:      500_000,
		Status:        txmngr_provider.ConfirmingTxStatus,
		AssignedNonce: ptr(nonce + 1),
		NextActionAt:  now.Add(time.Hour),
		CreatedAt:     now,
		UpdatedAt:     now,
	}))
	f.seedAttempt("follower", follower, now)
	calls := countSigner(f.auth)

	require.NoError(t, f.step("exhausted"))
	require.Zero(t, *calls)
	attempts, err := f.db.SelectAttempts("exhausted")
	require.NoError(t, err)
	require.Len(t, attempts, maxFeeReplacements)
	required, available, ok := FundingShortfall(f.get("exhausted").Msg)
	require.True(t, ok)
	require.Equal(t, 0, available.Cmp(cancelCost))
	require.Positive(t, required.Cmp(available))
	require.NotNil(t, f.get("follower").AssignedNonce)
}

func TestUnderpricedRejectionRepublishesThePreviousAttempt(t *testing.T) {
	t.Parallel()
	f, cl, _, payload := newExhaustedLane(t, exhaustedBuild{
		nonce:      31,
		gas:        params.TxGas,
		price:      100,
		suggested:  100,
		baseFee:    50,
		pendingAge: time.Millisecond,
		age:        time.Hour,
		balance:    big.NewInt(1 << 60),
	})
	cancel, err := f.auth.Signer(f.auth.From, types.NewTx(&types.LegacyTx{
		Nonce:    31,
		To:       &f.auth.From,
		Gas:      params.TxGas,
		GasPrice: big.NewInt(110),
		Value:    big.NewInt(0),
	}))
	require.NoError(t, err)
	f.seedAttempt("exhausted", cancel, time.Now().Add(-time.Minute))
	row := f.get("exhausted")
	row.Tx = cancel
	row.BroadcastPending = false
	row.Msg = "broadcast attempt " + cancel.Hash().Hex() + ": replacement transaction underpriced"
	require.NoError(t, f.db.Insert(*row))
	calls := countSigner(f.auth)
	cl.On("SendTransaction", mock2.Anything, payload).Return(nil).Twice()

	require.NoError(t, f.step("exhausted"))
	require.Zero(t, *calls)
	attempts, err := f.db.SelectAttempts("exhausted")
	require.NoError(t, err)
	require.Len(t, attempts, maxFeeReplacements+1)
	require.Contains(t, f.get("exhausted").Msg, "replacement transaction underpriced")

	require.NoError(t, f.step("exhausted"))
	require.Zero(t, *calls)
	again, err := f.db.SelectAttempts("exhausted")
	require.NoError(t, err)
	require.Len(t, again, maxFeeReplacements+1)
	require.Contains(t, f.get("exhausted").Msg, "replacement transaction underpriced")
}

func TestSpendLimitRepublishesTheAffordableCancel(t *testing.T) {
	t.Parallel()
	f, cl, _, _ := newExhaustedLane(t, exhaustedBuild{
		nonce:      35,
		gas:        params.TxGas,
		price:      100,
		suggested:  200,
		baseFee:    150,
		pendingAge: time.Millisecond,
		age:        time.Hour,
		balance:    big.NewInt(1 << 60),
	})
	cancel, err := f.auth.Signer(f.auth.From, types.NewTx(&types.LegacyTx{
		Nonce:    35,
		To:       &f.auth.From,
		Gas:      params.TxGas,
		GasPrice: big.NewInt(110),
		Value:    big.NewInt(0),
	}))
	require.NoError(t, err)
	f.seedAttempt("exhausted", cancel, time.Now().Add(-time.Minute))
	row := f.get("exhausted")
	row.Tx = cancel
	row.BroadcastPending = false
	require.NoError(t, f.db.Insert(*row))
	calls := countSigner(f.auth)
	cl.On("SendTransaction", mock2.Anything, cancel).Return(nil).Once()

	require.NoError(t, f.step("exhausted"))
	require.Zero(t, *calls)
	attempts, err := f.db.SelectAttempts("exhausted")
	require.NoError(t, err)
	require.Len(t, attempts, maxFeeReplacements+1)
	require.Equal(t, CancellationReasonFeeLimit, f.get("exhausted").Msg)
}

func TestCancelInsufficientFundsReopensTheFundingGate(t *testing.T) {
	t.Parallel()
	f, cl, _, _ := newExhaustedLane(t, exhaustedBuild{
		nonce:     32,
		suggested: 100,
		baseFee:   50,
		balance:   big.NewInt(0),
	})
	cancel, err := f.auth.Signer(f.auth.From, types.NewTx(&types.LegacyTx{
		Nonce:    32,
		To:       &f.auth.From,
		Gas:      params.TxGas,
		GasPrice: big.NewInt(110),
		Value:    big.NewInt(0),
	}))
	require.NoError(t, err)
	f.seedAttempt("exhausted", cancel, time.Now().Add(-time.Minute))
	row := f.get("exhausted")
	row.Tx = cancel
	row.BroadcastPending = true
	row.Msg = "broadcast attempt " + cancel.Hash().Hex() + ": insufficient funds for gas * price + value"
	require.NoError(t, f.db.Insert(*row))
	calls := countSigner(f.auth)

	require.NoError(t, f.step("exhausted"))
	require.Zero(t, *calls)
	cl.AssertNotCalled(t, "SendTransaction", mock2.Anything, mock2.Anything)
	require.False(t, f.get("exhausted").BroadcastPending)
	_, _, ok := FundingShortfall(f.get("exhausted").Msg)
	require.True(t, ok)
}

func TestFundingShortfallIgnoresTheReconciliationSuffix(t *testing.T) {
	t.Parallel()
	message := fundingMessage(big.NewInt(10), big.NewInt(1)) + reconciliationSeparator + "retrieving receipt 0xabc: rpc transport error"
	required, available, ok := FundingShortfall(message)
	require.True(t, ok)
	require.Equal(t, int64(10), required.Int64())
	require.Equal(t, int64(1), available.Int64())
}

func TestQuoteExcludedCancelIsHeldWhenUnaffordable(t *testing.T) {
	t.Parallel()
	f, cl, _, _ := newExhaustedLane(t, exhaustedBuild{
		nonce:      33,
		suggested:  200,
		baseFee:    50,
		pendingAge: time.Millisecond,
		age:        time.Hour,
		balance:    big.NewInt(0),
	})
	cancel, err := f.auth.Signer(f.auth.From, types.NewTx(&types.LegacyTx{
		Nonce:    33,
		To:       &f.auth.From,
		Gas:      params.TxGas,
		GasPrice: big.NewInt(110),
		Value:    big.NewInt(0),
	}))
	require.NoError(t, err)
	f.seedAttempt("exhausted", cancel, time.Now().Add(-time.Minute))
	row := f.get("exhausted")
	row.Tx = cancel
	row.BroadcastPending = false
	row.Msg = ""
	require.NoError(t, f.db.Insert(*row))
	calls := countSigner(f.auth)

	require.NoError(t, f.step("exhausted"))
	require.Zero(t, *calls)
	cl.AssertNotCalled(t, "SendTransaction", mock2.Anything, mock2.Anything)
	require.False(t, f.get("exhausted").BroadcastPending)
	_, _, ok := FundingShortfall(f.get("exhausted").Msg)
	require.True(t, ok)
}

func TestSiblingReserveUsesTheDearestAttempt(t *testing.T) {
	t.Parallel()
	const nonce = uint64(34)
	cancelCost := new(big.Int).Mul(big.NewInt(int64(params.TxGas)), big.NewInt(110))
	cheap := new(big.Int).Mul(big.NewInt(int64(params.TxGas)), big.NewInt(50))
	balance := new(big.Int).Add(new(big.Int).Set(cancelCost), cheap)
	f, _, _, _ := newExhaustedLane(t, exhaustedBuild{
		nonce:      nonce,
		suggested:  100,
		baseFee:    50,
		pendingAge: time.Millisecond,
		age:        time.Hour,
		balance:    balance,
	})
	now := time.Now()
	expensive, err := f.auth.Signer(f.auth.From, types.NewTx(&types.LegacyTx{
		Nonce:    nonce + 1,
		To:       ptr(common.HexToAddress("0x00000000000000000000000000000000000000bb")),
		Gas:      500_000,
		GasPrice: big.NewInt(100),
		Value:    big.NewInt(0),
		Data:     []byte{0x11},
	}))
	require.NoError(t, err)
	later, err := f.auth.Signer(f.auth.From, types.NewTx(&types.LegacyTx{
		Nonce:    nonce + 1,
		To:       &f.auth.From,
		Gas:      params.TxGas,
		GasPrice: big.NewInt(50),
		Value:    big.NewInt(0),
	}))
	require.NoError(t, err)
	require.NoError(t, f.db.Insert(txmngr_provider.Transaction{
		ID:            "follower",
		ChainID:       uint64(chainId),
		Sender:        f.auth.From,
		Address:       common.HexToAddress("0x00000000000000000000000000000000000000bb"),
		Data:          []byte{0x11},
		Value:         big.NewInt(0),
		GasLimit:      500_000,
		Status:        txmngr_provider.ConfirmingTxStatus,
		AssignedNonce: ptr(nonce + 1),
		NextActionAt:  now.Add(time.Hour),
		CreatedAt:     now,
		UpdatedAt:     now,
	}))
	f.seedAttempt("follower", expensive, now.Add(-time.Minute))
	f.seedAttempt("follower", later, now)
	calls := countSigner(f.auth)

	require.NoError(t, f.step("exhausted"))
	require.Zero(t, *calls)
	required, available, ok := FundingShortfall(f.get("exhausted").Msg)
	require.True(t, ok)
	require.Equal(t, 0, available.Cmp(balance))
	require.Positive(t, required.Cmp(balance))
}

func TestOlderHashFeeRefusalKeepsTheUnderpricedMarker(t *testing.T) {
	t.Parallel()
	f, cl, _, payload := newExhaustedLane(t, exhaustedBuild{
		nonce:      38,
		gas:        params.TxGas,
		price:      100,
		suggested:  100,
		baseFee:    50,
		pendingAge: time.Millisecond,
		age:        time.Hour,
		balance:    big.NewInt(1 << 60),
	})
	cancel, err := f.auth.Signer(f.auth.From, types.NewTx(&types.LegacyTx{
		Nonce:    38,
		To:       &f.auth.From,
		Gas:      params.TxGas,
		GasPrice: big.NewInt(110),
		Value:    big.NewInt(0),
	}))
	require.NoError(t, err)
	f.seedAttempt("exhausted", cancel, time.Now().Add(-time.Minute))
	row := f.get("exhausted")
	row.Tx = cancel
	row.BroadcastPending = false
	row.Msg = "broadcast attempt " + cancel.Hash().Hex() + ": replacement transaction underpriced"
	require.NoError(t, f.db.Insert(*row))
	cl.On("SendTransaction", mock2.Anything, payload).
		Return(testRPCError{code: -32000, message: "max fee per gas less than block base fee"}).Once()

	require.NoError(t, f.step("exhausted"))
	require.False(t, f.get("exhausted").BroadcastPending)
	require.Contains(t, f.get("exhausted").Msg, "replacement transaction underpriced")

	cl.On("SendTransaction", mock2.Anything, payload).Return(nil).Once()
	require.NoError(t, f.step("exhausted"))
	require.Contains(t, f.get("exhausted").Msg, "replacement transaction underpriced")
	cl.AssertNotCalled(t, "SendTransaction", mock2.Anything, cancel)
}

func TestRescheduleKeepsTheFundingHold(t *testing.T) {
	t.Parallel()
	f, _, _, _ := newExhaustedLane(t, exhaustedBuild{
		nonce:   39,
		balance: big.NewInt(0),
	})
	row := f.get("exhausted")
	row.Msg = fundingMessage(big.NewInt(10), big.NewInt(0))
	require.NoError(t, f.db.Insert(*row))

	f.mngr.rescheduleTransaction(*row, errors.New("reading header: rpc transport error"))
	required, available, ok := FundingShortfall(f.get("exhausted").Msg)
	require.True(t, ok)
	require.Equal(t, int64(10), required.Int64())
	require.Zero(t, available.Sign())
}

func TestUnsetSenderSiblingStillRaisesTheFundingGate(t *testing.T) {
	t.Parallel()
	const nonce = uint64(40)
	cancelCost := new(big.Int).Mul(big.NewInt(int64(params.TxGas)), big.NewInt(110))
	f, _, _, _ := newExhaustedLane(t, exhaustedBuild{
		nonce:      nonce,
		suggested:  100,
		baseFee:    50,
		pendingAge: time.Millisecond,
		age:        time.Hour,
		balance:    cancelCost,
	})
	now := time.Now()
	follower, err := f.auth.Signer(f.auth.From, types.NewTx(&types.LegacyTx{
		Nonce:    nonce + 1,
		To:       ptr(common.HexToAddress("0x00000000000000000000000000000000000000bb")),
		Gas:      500_000,
		GasPrice: big.NewInt(100),
		Value:    big.NewInt(0),
		Data:     []byte{0x11},
	}))
	require.NoError(t, err)
	require.NoError(t, f.db.Insert(txmngr_provider.Transaction{
		ID:           "follower",
		ChainID:      uint64(chainId),
		Address:      common.HexToAddress("0x00000000000000000000000000000000000000bb"),
		Data:         []byte{0x11},
		Value:        big.NewInt(0),
		GasLimit:     500_000,
		Status:       txmngr_provider.ConfirmingTxStatus,
		NextActionAt: now.Add(time.Hour),
		CreatedAt:    now,
		UpdatedAt:    now,
	}))
	f.seedAttempt("follower", follower, now)
	calls := countSigner(f.auth)

	require.NoError(t, f.step("exhausted"))
	require.Zero(t, *calls)
	required, _, ok := FundingShortfall(f.get("exhausted").Msg)
	require.True(t, ok)
	require.Positive(t, required.Cmp(cancelCost))
}

func TestOlderHashRetryDoesNotResendTheRefusedCancel(t *testing.T) {
	t.Parallel()
	f, cl, _, payload := newExhaustedLane(t, exhaustedBuild{
		nonce:      36,
		gas:        params.TxGas,
		price:      100,
		suggested:  100,
		baseFee:    50,
		pendingAge: time.Millisecond,
		age:        time.Hour,
		balance:    big.NewInt(1 << 60),
	})
	cancel, err := f.auth.Signer(f.auth.From, types.NewTx(&types.LegacyTx{
		Nonce:    36,
		To:       &f.auth.From,
		Gas:      params.TxGas,
		GasPrice: big.NewInt(110),
		Value:    big.NewInt(0),
	}))
	require.NoError(t, err)
	f.seedAttempt("exhausted", cancel, time.Now().Add(-time.Minute))
	row := f.get("exhausted")
	row.Tx = cancel
	row.BroadcastPending = false
	row.Msg = "broadcast attempt " + cancel.Hash().Hex() + ": replacement transaction underpriced"
	require.NoError(t, f.db.Insert(*row))
	cl.On("SendTransaction", mock2.Anything, payload).
		Return(testRPCError{code: -38014, message: "insufficient funds for gas * price + value"}).Once()

	require.NoError(t, f.step("exhausted"))
	require.False(t, f.get("exhausted").BroadcastPending)
	require.Contains(t, f.get("exhausted").Msg, "replacement transaction underpriced")

	cl.On("SendTransaction", mock2.Anything, payload).Return(nil).Once()
	require.NoError(t, f.step("exhausted"))
	require.Contains(t, f.get("exhausted").Msg, "replacement transaction underpriced")
	cl.AssertNotCalled(t, "SendTransaction", mock2.Anything, cancel)
}

func TestOlderHashPermanentRejectionDoesNotAbandonTheLane(t *testing.T) {
	t.Parallel()
	const nonce = uint64(37)
	f, cl, _, payload := newExhaustedLane(t, exhaustedBuild{
		nonce:      nonce,
		gas:        params.TxGas,
		price:      100,
		suggested:  100,
		baseFee:    50,
		pendingAge: time.Millisecond,
		age:        time.Hour,
		balance:    big.NewInt(1 << 60),
	})
	cancel, err := f.auth.Signer(f.auth.From, types.NewTx(&types.LegacyTx{
		Nonce:    nonce,
		To:       &f.auth.From,
		Gas:      params.TxGas,
		GasPrice: big.NewInt(110),
		Value:    big.NewInt(0),
	}))
	require.NoError(t, err)
	f.seedAttempt("exhausted", cancel, time.Now().Add(-time.Minute))
	row := f.get("exhausted")
	row.Tx = cancel
	row.BroadcastPending = false
	row.Msg = "broadcast attempt " + cancel.Hash().Hex() + ": replacement transaction underpriced"
	require.NoError(t, f.db.Insert(*row))
	cl.On("SendTransaction", mock2.Anything, payload).
		Return(testRPCError{code: -32000, message: "gas limit too high"}).Once()

	require.NoError(t, f.step("exhausted"))
	stored := f.get("exhausted")
	require.NotNil(t, stored.AssignedNonce)
	require.Equal(t, nonce, *stored.AssignedNonce)
	require.NotEqual(t, txmngr_provider.ErroredTxStatus, stored.Status)
	require.Nil(t, stored.RejectedAttemptHash)
	require.Contains(t, stored.Msg, "replacement transaction underpriced")
}

func TestRescheduleKeepsAnInsufficientFundsCancel(t *testing.T) {
	t.Parallel()
	f, cl, _, _ := newExhaustedLane(t, exhaustedBuild{
		nonce:     41,
		suggested: 100,
		baseFee:   50,
		balance:   big.NewInt(0),
	})
	cancel, err := f.auth.Signer(f.auth.From, types.NewTx(&types.LegacyTx{
		Nonce:    41,
		To:       &f.auth.From,
		Gas:      params.TxGas,
		GasPrice: big.NewInt(110),
		Value:    big.NewInt(0),
	}))
	require.NoError(t, err)
	f.seedAttempt("exhausted", cancel, time.Now().Add(-time.Minute))
	row := f.get("exhausted")
	row.Tx = cancel
	row.BroadcastPending = true
	row.Msg = "broadcast attempt " + cancel.Hash().Hex() + ": insufficient funds for gas * price + value"
	require.NoError(t, f.db.Insert(*row))

	f.mngr.rescheduleTransaction(*row, errors.New("reading header: rpc transport error"))
	require.Contains(t, f.get("exhausted").Msg, "insufficient funds")
	require.True(t, f.get("exhausted").BroadcastPending)

	require.NoError(t, f.step("exhausted"))
	cl.AssertNotCalled(t, "SendTransaction", mock2.Anything, mock2.Anything)
	_, _, ok := FundingShortfall(f.get("exhausted").Msg)
	require.True(t, ok)
	require.False(t, f.get("exhausted").BroadcastPending)
}

func TestParentRawTransactionRaisesTheFundingGate(t *testing.T) {
	t.Parallel()
	const nonce = uint64(42)
	cancelCost := new(big.Int).Mul(big.NewInt(int64(params.TxGas)), big.NewInt(110))
	f, _, _, _ := newExhaustedLane(t, exhaustedBuild{
		nonce:      nonce,
		suggested:  100,
		baseFee:    50,
		pendingAge: time.Millisecond,
		age:        time.Hour,
		balance:    cancelCost,
	})
	now := time.Now()
	followerNonce := nonce + 1
	raw, err := f.auth.Signer(f.auth.From, types.NewTx(&types.LegacyTx{
		Nonce:    followerNonce,
		To:       ptr(common.HexToAddress("0x00000000000000000000000000000000000000cc")),
		Gas:      500_000,
		GasPrice: big.NewInt(100),
		Value:    big.NewInt(0),
		Data:     []byte{0x11},
	}))
	require.NoError(t, err)
	require.NoError(t, f.db.Insert(txmngr_provider.Transaction{
		ID:            "legacy",
		ChainID:       uint64(chainId),
		Address:       common.HexToAddress("0x00000000000000000000000000000000000000cc"),
		Data:          []byte{0x11},
		Value:         big.NewInt(0),
		GasLimit:      500_000,
		Sender:        f.auth.From,
		AssignedNonce: &followerNonce,
		Tx:            raw,
		Status:        txmngr_provider.ConfirmingTxStatus,
		NextActionAt:  now.Add(time.Hour),
		CreatedAt:     now,
		UpdatedAt:     now,
	}))
	calls := countSigner(f.auth)

	require.NoError(t, f.step("exhausted"))
	require.Zero(t, *calls)
	required, _, ok := FundingShortfall(f.get("exhausted").Msg)
	require.True(t, ok)
	require.Positive(t, required.Cmp(cancelCost))
}
