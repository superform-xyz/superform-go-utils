package txmngr_provider

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// ErrIdempotencyConflict is returned when an operation key already exists for
// a different transaction payload.
var ErrIdempotencyConflict = errors.New("transaction operation key already exists with a different payload")

// ErrAttemptHashConflict is returned when an immutable signed transaction is
// already owned by another logical transaction. One on-chain hash must have a
// single owner so receipt and notification attribution cannot become ambiguous.
var ErrAttemptHashConflict = errors.New("signed transaction hash already belongs to another transaction")

// AttemptHashConflictError reports that hash is durably owned by another
// logical transaction, so every provider describes the conflict identically.
func AttemptHashConflictError(hash, ownerTransactionID string) error {
	return fmt.Errorf("%w: hash %s belongs to transaction %s",
		ErrAttemptHashConflict, hash, ownerTransactionID)
}

// ErrStaleTransaction is returned when a conditional transaction update lost
// a race with a terminal transition or notification acknowledgement.
var ErrStaleTransaction = errors.New("transaction state changed before update")

// LaneLock serializes work for one sender and chain across manager instances.
type LaneLock interface {
	Unlock() error
}

// SendTxOptions contains optional metadata for one logical transaction.
type SendTxOptions struct {
	OperationKey *string
}

// SendTxOption configures one logical transaction before it is queued.
type SendTxOption func(*SendTxOptions)

// TxStatus represents the status of a transaction.
type TxStatus string

// Transaction statuses.
const (
	SucceedTxStatus    TxStatus = "succeed"
	ErroredTxStatus    TxStatus = "errored"
	ProcessingTxStatus TxStatus = "processing"
	ConfirmingTxStatus TxStatus = "confirming"
	PendingTxStatus    TxStatus = "pending"
	FailedTxStatus     TxStatus = "failed"
)

// ActiveStatuses are the statuses of a transaction still being worked on. The
// partial indexes in the migrations repeat this set in SQL; keep them in step.
func ActiveStatuses() []TxStatus {
	return []TxStatus{PendingTxStatus, ProcessingTxStatus, ConfirmingTxStatus}
}

// TerminalStatuses are the statuses a transaction never leaves.
func TerminalStatuses() []TxStatus {
	return []TxStatus{SucceedTxStatus, FailedTxStatus, ErroredTxStatus}
}

// TransactionsQ represents the baviour of a transaction queue.
type TransactionsQ interface {
	New() TransactionsQ

	// Insert stores tx, replacing any row that already has its ID. Production
	// writes go through InsertOrGet and the conditional updates below.
	Insert(tx Transaction) error
	InsertOrGet(tx Transaction) (*Transaction, error)
	InsertAttempt(attempt TransactionAttempt) error
	// UpdateIfActive writes tx only while the stored row is still active, so a
	// transition that lost a race with a terminal one reports ErrStaleTransaction.
	UpdateIfActive(tx Transaction) error
	UpdateNotification(tx Transaction) error

	Select() ([]Transaction, error)
	Get() (*Transaction, error)
	SelectAttempts(transactionID string) ([]TransactionAttempt, error)
	// SelectAttemptsByNonce returns immutable candidates for nonce on chain,
	// across logical transaction owners. It is used only to advance past a
	// globally owned hash when a safely released nonce is reused.
	//
	// It is deliberately not scoped by sender: the persisted sender column may
	// be stale or NULL, so callers filter on the sender recovered from each
	// candidate's signature. Providers must not add a sender predicate, or the
	// conflicting attempt would drop out of the result and a recoverable hash
	// collision would become a hard error.
	SelectAttemptsByNonce(ctx context.Context, chainID, nonce uint64) ([]TransactionAttempt, error)
	// CountByChain returns the number of transactions in the requested statuses,
	// grouped by chain. It supports low-cost queue-depth metrics without loading
	// transaction payloads into memory.
	CountByChain(ctx context.Context, statuses ...TxStatus) (map[uint64]uint64, error)
	// MaxAssignedNonce reports the highest nonce still reserved or consumed on
	// this lane. Terminal mined/consumed rows count; a transaction proven never
	// admitted clears its assignment before reaching a terminal state.
	MaxAssignedNonce(ctx context.Context, sender common.Address, chainID uint64) (uint64, bool, error)
	TryLockLane(ctx context.Context, sender common.Address, chainID uint64) (LaneLock, bool, error)

	FilterByIds(ids ...string) TransactionsQ
	FilterByChainIds(chainIDs ...uint64) TransactionsQ
	// FilterBySenderOrUnset scopes lane work to its current signer while retaining
	// legacy rows whose sender has not yet been recovered from a signed attempt.
	FilterBySenderOrUnset(sender common.Address) TransactionsQ
	FilterByStatuses(statuses ...TxStatus) TransactionsQ
	FilterReadyBefore(before time.Time) TransactionsQ
	FilterNotificationReadyBefore(before time.Time) TransactionsQ
	OrderByNextAction() TransactionsQ
	// OrderByLaneWork prioritizes durable nonce owners before unassigned work.
	// Assigned rows are ordered by nonce; unassigned rows retain FIFO schedule
	// order. Providers must apply this ordering before Limit.
	OrderByLaneWork() TransactionsQ
	OrderByNextNotification() TransactionsQ
	Limit(limit uint64) TransactionsQ
}

// TransactionAttempt is an immutable signed transaction candidate. Every
// candidate for one logical transaction uses the same nonce; only fees differ.
type TransactionAttempt struct {
	TransactionID string
	Hash          common.Hash
	Nonce         uint64
	Tx            *types.Transaction
	CreatedAt     time.Time
}

// Transaction is the high level abstraction model of a transaction.
type Transaction struct {
	ID                   string
	IntentID             *string
	OperationKey         *string
	PayloadHash          *string
	ChainID              uint64
	Sender               common.Address
	Address              common.Address
	Data                 []byte
	Value                *big.Int
	GasLimit             uint64
	RawAuthorizations    []types.SetCodeAuthorization
	AssignedNonce        *uint64
	Tx                   *types.Transaction
	Receipt              *types.Receipt
	Status               TxStatus
	Msg                  string
	NextActionAt         time.Time
	BroadcastPending     bool
	RejectedAttemptHash  *string
	NonceConsumedAt      *time.Time
	NotifiedAt           *time.Time
	NotificationAttempts uint32
	NextNotificationAt   time.Time
	NotificationError    string
	UpdatedAt            time.Time
	CreatedAt            time.Time
}

// IsCompleted returns true if the transaction is completed.
func (tx *Transaction) IsCompleted() bool {
	return tx.Status == ErroredTxStatus ||
		tx.Status == FailedTxStatus ||
		tx.Status == SucceedTxStatus
}
