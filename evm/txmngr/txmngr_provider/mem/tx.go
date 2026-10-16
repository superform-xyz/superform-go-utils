package mem

import (
	"cmp"
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/superform-xyz/superform-go-utils/evm/txmngr/txmngr_provider"
)

type txsState struct {
	*sync.RWMutex
	txs       map[string]txmngr_provider.Transaction
	attempts  map[string][]txmngr_provider.TransactionAttempt
	hashes    map[string]string
	laneLocks map[string]struct{}
}

type txsQ struct {
	state   *txsState
	filters []filterer[txmngr_provider.Transaction]
	orderBy txOrder
	limit   uint64
}

type txOrder uint8

const (
	orderNone txOrder = iota
	orderNextAction
	orderLaneWork
	orderNextNotification
)

var _ txmngr_provider.TransactionsQ = (*txsQ)(nil)

// NewTransactionsQ creates a new instance of inmem txsQ
func NewTransactionsQ(txs map[string]txmngr_provider.Transaction) txmngr_provider.TransactionsQ {
	if txs == nil {
		txs = make(map[string]txmngr_provider.Transaction)
	}
	return &txsQ{
		state: &txsState{
			RWMutex:   new(sync.RWMutex),
			txs:       txs,
			attempts:  make(map[string][]txmngr_provider.TransactionAttempt),
			hashes:    make(map[string]string),
			laneLocks: make(map[string]struct{}),
		},
	}
}

func (t txsQ) New() txmngr_provider.TransactionsQ {
	return &txsQ{state: t.state}
}

func (t txsQ) Insert(tx txmngr_provider.Transaction) error {
	t.state.Lock()
	defer t.state.Unlock()

	t.state.txs[tx.ID] = tx
	return nil
}

func (t txsQ) InsertOrGet(tx txmngr_provider.Transaction) (*txmngr_provider.Transaction, error) {
	t.state.Lock()
	defer t.state.Unlock()

	if tx.OperationKey != nil {
		for _, existing := range t.state.txs {
			if existing.OperationKey == nil || *existing.OperationKey != *tx.OperationKey {
				continue
			}
			if existing.PayloadHash == nil || tx.PayloadHash == nil || *existing.PayloadHash != *tx.PayloadHash {
				return nil, txmngr_provider.ErrIdempotencyConflict
			}
			return &existing, nil
		}
	}

	t.state.txs[tx.ID] = tx
	return &tx, nil
}

func (t txsQ) InsertAttempt(attempt txmngr_provider.TransactionAttempt) error {
	t.state.Lock()
	defer t.state.Unlock()

	hash := attempt.Hash.Hex()
	if transactionID, exists := t.state.hashes[hash]; exists {
		if transactionID != attempt.TransactionID {
			return txmngr_provider.AttemptHashConflictError(hash, transactionID)
		}
		return nil
	}
	t.state.hashes[hash] = attempt.TransactionID
	t.state.attempts[attempt.TransactionID] = append(t.state.attempts[attempt.TransactionID], attempt)
	return nil
}

func (t txsQ) UpdateIfActive(tx txmngr_provider.Transaction) error {
	t.state.Lock()
	defer t.state.Unlock()

	current, ok := t.state.txs[tx.ID]
	if !ok || current.IsCompleted() {
		return txmngr_provider.ErrStaleTransaction
	}
	t.state.txs[tx.ID] = tx
	return nil
}

func (t txsQ) UpdateNotification(tx txmngr_provider.Transaction) error {
	t.state.Lock()
	defer t.state.Unlock()

	current, ok := t.state.txs[tx.ID]
	if !ok || !current.IsCompleted() || current.NotifiedAt != nil {
		return txmngr_provider.ErrStaleTransaction
	}
	current.NotifiedAt = tx.NotifiedAt
	current.NotificationAttempts = tx.NotificationAttempts
	current.NextNotificationAt = tx.NextNotificationAt
	current.NotificationError = tx.NotificationError
	current.UpdatedAt = tx.UpdatedAt
	t.state.txs[tx.ID] = current
	return nil
}

func (t txsQ) Select() ([]txmngr_provider.Transaction, error) {
	t.state.RLock()
	defer t.state.RUnlock()

	result := make([]txmngr_provider.Transaction, 0, len(t.state.txs))

	for _, value := range t.state.txs {
		if filter(t.filters, value) {
			result = append(result, value)
		}
	}
	switch t.orderBy {
	case orderNextNotification:
		slices.SortStableFunc(result, func(a, b txmngr_provider.Transaction) int {
			return cmp.Or(a.NextNotificationAt.Compare(b.NextNotificationAt), strings.Compare(a.ID, b.ID))
		})
	case orderNextAction:
		slices.SortStableFunc(result, compareScheduleOrder)
	case orderLaneWork:
		slices.SortStableFunc(result, compareLaneWork)
	}
	if t.limit > 0 && uint64(len(result)) > t.limit {
		result = result[:t.limit]
	}

	return result, nil
}

func (t txsQ) Get() (*txmngr_provider.Transaction, error) {
	t.state.RLock()
	defer t.state.RUnlock()

	for _, value := range t.state.txs {
		if filter(t.filters, value) {
			return &value, nil
		}
	}

	return nil, sql.ErrNoRows
}

func (t txsQ) SelectAttempts(transactionID string) ([]txmngr_provider.TransactionAttempt, error) {
	t.state.RLock()
	defer t.state.RUnlock()

	attempts := append([]txmngr_provider.TransactionAttempt(nil), t.state.attempts[transactionID]...)
	slices.SortStableFunc(attempts, func(a, b txmngr_provider.TransactionAttempt) int {
		return a.CreatedAt.Compare(b.CreatedAt)
	})
	return attempts, nil
}

func (t txsQ) SelectAttemptsByNonce(
	_ context.Context,
	chainID, nonce uint64,
) ([]txmngr_provider.TransactionAttempt, error) {
	t.state.RLock()
	defer t.state.RUnlock()

	var attempts []txmngr_provider.TransactionAttempt
	for transactionID, candidates := range t.state.attempts {
		owner, ok := t.state.txs[transactionID]
		if !ok || owner.ChainID != chainID {
			continue
		}
		for _, candidate := range candidates {
			if candidate.Nonce == nonce {
				attempts = append(attempts, candidate)
			}
		}
	}
	// pg tiebreaks on the attempt's serial id; the fake has no insertion counter
	// and map iteration is random, so the hash supplies the deterministic total
	// order. The only caller takes a maximum, so the two orders are equivalent.
	slices.SortStableFunc(attempts, func(a, b txmngr_provider.TransactionAttempt) int {
		return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), strings.Compare(a.Hash.Hex(), b.Hash.Hex()))
	})
	return attempts, nil
}

func (t txsQ) CountByChain(
	ctx context.Context,
	statuses ...txmngr_provider.TxStatus,
) (map[uint64]uint64, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("counting transactions by chain: %w", err)
	}

	t.state.RLock()
	defer t.state.RUnlock()

	counts := make(map[uint64]uint64)
	for _, tx := range t.state.txs {
		if slices.Contains(statuses, tx.Status) {
			counts[tx.ChainID]++
		}
	}
	return counts, nil
}

func (t txsQ) MaxAssignedNonce(
	_ context.Context,
	sender common.Address,
	chainID uint64,
) (uint64, bool, error) {
	t.state.RLock()
	defer t.state.RUnlock()

	var maxNonce uint64
	found := false
	for _, tx := range t.state.txs {
		if tx.Sender != sender || tx.ChainID != chainID || tx.AssignedNonce == nil {
			continue
		}
		if !found || *tx.AssignedNonce > maxNonce {
			maxNonce = *tx.AssignedNonce
			found = true
		}
	}
	return maxNonce, found, nil
}

type laneLock struct {
	state *txsState
	key   string
	once  sync.Once
}

func (l *laneLock) Unlock() error {
	l.once.Do(func() {
		l.state.Lock()
		delete(l.state.laneLocks, l.key)
		l.state.Unlock()
	})
	return nil
}

func (t txsQ) TryLockLane(
	ctx context.Context,
	sender common.Address,
	chainID uint64,
) (txmngr_provider.LaneLock, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	key := sender.Hex() + ":" + strconv.FormatUint(chainID, 10)
	t.state.Lock()
	defer t.state.Unlock()
	if _, locked := t.state.laneLocks[key]; locked {
		return nil, false, nil
	}
	t.state.laneLocks[key] = struct{}{}
	return &laneLock{state: t.state, key: key}, true, nil
}

// withFilter returns a branch of this query carrying one more filter. Clipping
// forces the append to copy, so two branches of the same base query cannot
// share a backing array and overwrite each other's last filter.
func (t txsQ) withFilter(f func(txmngr_provider.Transaction) bool) txmngr_provider.TransactionsQ {
	t.filters = append(slices.Clip(t.filters), f)
	return t
}

func (t txsQ) FilterByIds(ids ...string) txmngr_provider.TransactionsQ {
	return t.withFilter(func(value txmngr_provider.Transaction) bool {
		return slices.Contains(ids, value.ID)
	})
}

func (t txsQ) FilterByChainIds(chainIDs ...uint64) txmngr_provider.TransactionsQ {
	return t.withFilter(func(value txmngr_provider.Transaction) bool {
		return slices.Contains(chainIDs, value.ChainID)
	})
}

func (t txsQ) FilterBySenderOrUnset(sender common.Address) txmngr_provider.TransactionsQ {
	return t.withFilter(func(value txmngr_provider.Transaction) bool {
		return value.Sender == sender || value.Sender == (common.Address{})
	})
}

func (t txsQ) FilterByStatuses(statuses ...txmngr_provider.TxStatus) txmngr_provider.TransactionsQ {
	return t.withFilter(func(value txmngr_provider.Transaction) bool {
		return slices.Contains(statuses, value.Status)
	})
}

func (t txsQ) FilterReadyBefore(before time.Time) txmngr_provider.TransactionsQ {
	return t.withFilter(func(value txmngr_provider.Transaction) bool {
		return !value.NextActionAt.After(before)
	})
}

func (t txsQ) FilterNotificationReadyBefore(before time.Time) txmngr_provider.TransactionsQ {
	return t.withFilter(func(value txmngr_provider.Transaction) bool {
		return value.IsCompleted() && value.IntentID != nil && *value.IntentID != "" && value.NotifiedAt == nil &&
			!value.NextNotificationAt.After(before)
	})
}

func (t txsQ) OrderByNextAction() txmngr_provider.TransactionsQ {
	t.orderBy = orderNextAction
	return t
}

func (t txsQ) OrderByLaneWork() txmngr_provider.TransactionsQ {
	t.orderBy = orderLaneWork
	return t
}

func (t txsQ) OrderByNextNotification() txmngr_provider.TransactionsQ {
	t.orderBy = orderNextNotification
	return t
}

func (t txsQ) Limit(limit uint64) txmngr_provider.TransactionsQ {
	t.limit = limit
	return t
}

// compareScheduleOrder is the FIFO tail shared by both work orderings. It
// mirrors "next_action_at ASC, created_at ASC, id ASC" in the pg provider and,
// because ids are unique, makes every ordering that ends in it a total order.
func compareScheduleOrder(a, b txmngr_provider.Transaction) int {
	return cmp.Or(a.NextActionAt.Compare(b.NextActionAt),
		a.CreatedAt.Compare(b.CreatedAt), strings.Compare(a.ID, b.ID))
}

// compareLaneWork mirrors the pg provider's
// "assigned_nonce ASC NULLS LAST, next_action_at ASC, created_at ASC, id ASC".
func compareLaneWork(a, b txmngr_provider.Transaction) int {
	return cmp.Or(
		compareAssignedNonce(a.AssignedNonce, b.AssignedNonce),
		compareScheduleOrder(a, b),
	)
}

// compareAssignedNonce orders assigned rows by nonce ahead of every unassigned
// row, which is what PostgreSQL's "ASC NULLS LAST" does. Two unassigned rows
// compare equal, so the caller's tiebreaker decides them.
//
// Do not "simplify" this by treating a nil nonce as 0, which would sort
// unassigned rows first and invert the lane's priority, or by comparing the
// pointers themselves.
func compareAssignedNonce(a, b *uint64) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return 1
	case b == nil:
		return -1
	default:
		return cmp.Compare(*a, *b)
	}
}
