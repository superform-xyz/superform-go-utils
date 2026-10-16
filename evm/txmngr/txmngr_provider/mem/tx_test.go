package mem

import (
	"context"
	"fmt"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/stretchr/testify/require"

	"github.com/superform-xyz/superform-go-utils/evm/txmngr/txmngr_provider"
)

func TestQueryClonesShareSynchronization(t *testing.T) {
	t.Parallel()

	db := NewTransactionsQ(make(map[string]txmngr_provider.Transaction))
	const count = 100
	var wg sync.WaitGroup
	for i := range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := fmt.Sprintf("tx-%03d", i)
			tx := txmngr_provider.Transaction{
				ID:           id,
				Status:       txmngr_provider.PendingTxStatus,
				NextActionAt: time.Now(),
				CreatedAt:    time.Now(),
			}
			require.NoError(t, db.Insert(tx))
			tx.Status = txmngr_provider.ConfirmingTxStatus
			require.NoError(t, db.Insert(tx))
			_, err := db.New().FilterByStatuses(txmngr_provider.PendingTxStatus, txmngr_provider.ConfirmingTxStatus).Select()
			require.NoError(t, err)
		}()
	}
	wg.Wait()

	transactions, err := db.New().FilterByStatuses(txmngr_provider.ConfirmingTxStatus).Select()
	require.NoError(t, err)
	require.Len(t, transactions, count)
}

func TestAttemptHashCannotBelongToDifferentTransactions(t *testing.T) {
	t.Parallel()

	db := NewTransactionsQ(make(map[string]txmngr_provider.Transaction))
	tx := legacyTx()
	first := txmngr_provider.TransactionAttempt{TransactionID: "first", Hash: tx.Hash(), Tx: tx}
	require.NoError(t, db.InsertAttempt(first))
	require.NoError(t, db.InsertAttempt(first), "same parent retry must be idempotent")

	second := first
	second.TransactionID = "second"
	require.ErrorIs(t, db.InsertAttempt(second), txmngr_provider.ErrAttemptHashConflict)
}

func TestFilterByChainOrdersBeforeLimit(t *testing.T) {
	t.Parallel()

	db := NewTransactionsQ(make(map[string]txmngr_provider.Transaction))
	now := time.Now()
	for i := range 5 {
		require.NoError(t, db.Insert(txmngr_provider.Transaction{
			ID:           fmt.Sprintf("chain-one-%d", i),
			ChainID:      1,
			Status:       txmngr_provider.PendingTxStatus,
			NextActionAt: now.Add(time.Duration(i) * time.Millisecond),
		}))
		require.NoError(t, db.Insert(txmngr_provider.Transaction{
			ID:           fmt.Sprintf("chain-two-%d", i),
			ChainID:      2,
			Status:       txmngr_provider.PendingTxStatus,
			NextActionAt: now.Add(time.Second + time.Duration(i)*time.Millisecond),
		}))
	}
	selected, err := db.New().
		FilterByChainIds(2).
		FilterByStatuses(txmngr_provider.PendingTxStatus).
		OrderByNextAction().
		Limit(4).
		Select()
	require.NoError(t, err)
	require.Len(t, selected, 4)
	for i, tx := range selected {
		require.Equal(t, uint64(2), tx.ChainID)
		require.Equal(t, fmt.Sprintf("chain-two-%d", i), tx.ID)
	}
}

func TestCountByChain(t *testing.T) {
	t.Parallel()

	db := NewTransactionsQ(map[string]txmngr_provider.Transaction{
		"pending-one":    {ChainID: 1, Status: txmngr_provider.PendingTxStatus},
		"confirming-one": {ChainID: 1, Status: txmngr_provider.ConfirmingTxStatus},
		"processing-two": {ChainID: 2, Status: txmngr_provider.ProcessingTxStatus},
		"succeeded-two":  {ChainID: 2, Status: txmngr_provider.SucceedTxStatus},
	})

	counts, err := db.CountByChain(context.Background(), txmngr_provider.ActiveStatuses()...)
	require.NoError(t, err)
	require.Equal(t, map[uint64]uint64{1: 2, 2: 1}, counts)
}

func TestOrderByLaneWorkPrioritizesAssignedRowsBeforeLimit(t *testing.T) {
	t.Parallel()

	db := NewTransactionsQ(make(map[string]txmngr_provider.Transaction))
	now := time.Now()
	for i := range 30 {
		nextActionAt := now.Add(-time.Hour)
		createdAt := now
		switch i {
		case 0:
			nextActionAt = now.Add(-3 * time.Hour)
		case 1:
			nextActionAt = now.Add(-2 * time.Hour)
			createdAt = now.Add(-2 * time.Hour)
		case 2, 3:
			nextActionAt = now.Add(-2 * time.Hour)
			createdAt = now.Add(-time.Hour)
		}
		require.NoError(t, db.Insert(txmngr_provider.Transaction{
			ID:           fmt.Sprintf("queued-%02d", i),
			Status:       txmngr_provider.PendingTxStatus,
			NextActionAt: nextActionAt,
			CreatedAt:    createdAt,
		}))
	}
	for _, nonce := range []uint64{12, 10, 11} {
		require.NoError(t, db.Insert(txmngr_provider.Transaction{
			ID:            fmt.Sprintf("assigned-%d", nonce),
			Status:        txmngr_provider.ConfirmingTxStatus,
			AssignedNonce: &nonce,
			NextActionAt:  now,
			CreatedAt:     now,
		}))
	}

	selected, err := db.New().
		FilterByStatuses(txmngr_provider.ActiveStatuses()...).
		OrderByLaneWork().
		Limit(7).
		Select()
	require.NoError(t, err)
	require.Len(t, selected, 7)
	require.Equal(t, []string{
		"assigned-10", "assigned-11", "assigned-12",
		"queued-00", "queued-01", "queued-02", "queued-03",
	}, []string{
		selected[0].ID,
		selected[1].ID,
		selected[2].ID,
		selected[3].ID,
		selected[4].ID,
		selected[5].ID,
		selected[6].ID,
	})
}

func TestFilterBySenderOrUnset(t *testing.T) {
	t.Parallel()

	current := common.HexToAddress("0x0000000000000000000000000000000000000001")
	other := common.HexToAddress("0x0000000000000000000000000000000000000002")
	db := NewTransactionsQ(map[string]txmngr_provider.Transaction{
		"current": {ID: "current", Sender: current},
		"legacy":  {ID: "legacy"},
		"other":   {ID: "other", Sender: other},
	})

	selected, err := db.New().FilterBySenderOrUnset(current).OrderByNextAction().Select()
	require.NoError(t, err)
	require.Len(t, selected, 2)
	require.ElementsMatch(t, []string{"current", "legacy"}, []string{selected[0].ID, selected[1].ID})
}

func TestCompareLaneWorkMatchesPostgresOrdering(t *testing.T) {
	t.Parallel()

	now := time.Now()
	nonce := uint64(7)
	tests := []struct {
		name string
		a    txmngr_provider.Transaction
		b    txmngr_provider.Transaction
		want int
	}{
		{
			name: "assigned before unset",
			a:    txmngr_provider.Transaction{AssignedNonce: &nonce},
			b:    txmngr_provider.Transaction{},
			want: -1,
		},
		{
			name: "equal nonce uses schedule",
			a:    txmngr_provider.Transaction{AssignedNonce: &nonce, NextActionAt: now.Add(-time.Second)},
			b:    txmngr_provider.Transaction{AssignedNonce: &nonce, NextActionAt: now},
			want: -1,
		},
		{
			name: "equal nonce and schedule use id",
			a:    txmngr_provider.Transaction{ID: "a", AssignedNonce: &nonce, NextActionAt: now, CreatedAt: now},
			b:    txmngr_provider.Transaction{ID: "b", AssignedNonce: &nonce, NextActionAt: now, CreatedAt: now},
			want: -1,
		},
		{
			name: "equal unset rows",
			a:    txmngr_provider.Transaction{ID: "same", NextActionAt: now, CreatedAt: now},
			b:    txmngr_provider.Transaction{ID: "same", NextActionAt: now, CreatedAt: now},
			want: 0,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, test.want, compareLaneWork(test.a, test.b))
			require.Equal(t, -test.want, compareLaneWork(test.b, test.a), "ordering must be antisymmetric")
		})
	}
}

func TestConditionalUpdatesPreserveTerminalState(t *testing.T) {
	t.Parallel()

	db := NewTransactionsQ(make(map[string]txmngr_provider.Transaction))
	now := time.Now()
	active := txmngr_provider.Transaction{
		ID:        "monotone",
		Status:    txmngr_provider.ConfirmingTxStatus,
		CreatedAt: now,
		UpdatedAt: now,
	}
	require.NoError(t, db.Insert(active))

	terminal := active
	terminal.Status = txmngr_provider.SucceedTxStatus
	require.NoError(t, db.UpdateIfActive(terminal))

	stale := active
	stale.Msg = "late active update"
	require.ErrorIs(t, db.UpdateIfActive(stale), txmngr_provider.ErrStaleTransaction)

	stored, err := db.New().FilterByIds(active.ID).Get()
	require.NoError(t, err)
	require.Equal(t, txmngr_provider.SucceedTxStatus, stored.Status)
	require.Empty(t, stored.Msg)
}

func TestNotificationUpdateCannotClearAcknowledgement(t *testing.T) {
	t.Parallel()

	db := NewTransactionsQ(make(map[string]txmngr_provider.Transaction))
	now := time.Now()
	terminal := txmngr_provider.Transaction{
		ID:        "notification-race",
		Status:    txmngr_provider.SucceedTxStatus,
		CreatedAt: now,
		UpdatedAt: now,
	}
	require.NoError(t, db.Insert(terminal))

	failed := terminal
	failed.NotificationAttempts = 1
	failed.NotificationError = "temporary failure"
	require.NoError(t, db.UpdateNotification(failed))

	acknowledged := terminal
	acknowledged.NotifiedAt = &now
	require.NoError(t, db.UpdateNotification(acknowledged))

	failed.NotificationAttempts = 2
	require.ErrorIs(t, db.UpdateNotification(failed), txmngr_provider.ErrStaleTransaction)
	stored, err := db.New().FilterByIds(terminal.ID).Get()
	require.NoError(t, err)
	require.NotNil(t, stored.NotifiedAt)
	require.Equal(t, now, *stored.NotifiedAt)
}

func TestLaneLockIsSharedByQueryClones(t *testing.T) {
	t.Parallel()

	db := NewTransactionsQ(make(map[string]txmngr_provider.Transaction))
	sender := common.HexToAddress("0x0000000000000000000000000000000000000001")
	lock, locked, err := db.TryLockLane(context.Background(), sender, 1)
	require.NoError(t, err)
	require.True(t, locked)

	_, locked, err = db.New().TryLockLane(context.Background(), sender, 1)
	require.NoError(t, err)
	require.False(t, locked)

	other, locked, err := db.New().TryLockLane(context.Background(), sender, 2)
	require.NoError(t, err)
	require.True(t, locked)
	require.NoError(t, other.Unlock())
	require.NoError(t, lock.Unlock())

	reacquired, locked, err := db.New().TryLockLane(context.Background(), sender, 1)
	require.NoError(t, err)
	require.True(t, locked)
	require.NoError(t, reacquired.Unlock())
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

func ptr[T any](value T) *T {
	return &value
}
