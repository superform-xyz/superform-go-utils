package pg

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"fmt"
	"strings"
	"sync"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/jmoiron/sqlx"
	"github.com/pkg/errors"

	"github.com/superform-xyz/superform-go-utils/evm/txmngr/txmngr_provider"
)

const (
	txsTable          = "transactions"
	attemptsTable     = "transaction_attempts"
	laneUnlockTimeout = 5 * time.Second

	idTxsColumn      = "id"
	chainIDTxsColumn = "chain_id"
	senderTxsColumn  = "sender"
	statusTxsColumn  = "status"
	nextActionColumn = "next_action_at"

	// attemptsOwnerJoin resolves each attempt's owning transaction, so a
	// lane-wide nonce lookup can filter on the parent row's chain.
	attemptsOwnerJoin      = txsTable + " ON " + txsTable + "." + idTxsColumn + " = " + attemptsTable + ".transaction_id"
	txsChainIDColumn       = txsTable + "." + chainIDTxsColumn
	attemptsNonceColumn    = attemptsTable + ".nonce"
	attemptsCreatedAtOrder = attemptsTable + ".created_at ASC"
	attemptsIDOrder        = attemptsTable + ".id ASC"
)

// psql builds every statement with PostgreSQL's $N placeholders.
var psql = sq.StatementBuilder.PlaceholderFormat(sq.Dollar)

var (
	transactionColumns = []string{
		"id", "chain_id", "intent_id", "operation_key", "payload_hash", "sender", "address", "value", "data",
		"gas_limit", "raw_authorizations", "assigned_nonce", "raw_tx", "raw_receipt", "status", "msg",
		"next_action_at", "broadcast_pending", "rejected_attempt_hash", "nonce_consumed_at", "notified_at",
		"notification_attempts", "next_notification_at", "notification_error", "updated_at", "created_at",
	}
	attemptColumns = []string{"transaction_id", "tx_hash", "nonce", "raw_tx", "created_at"}

	// transactionColumnList is the RETURNING clause body, joined once.
	transactionColumnList = strings.Join(transactionColumns, ", ")

	// qualifiedAttemptColumns disambiguates the attempt projection in joined
	// queries; built once, like transactionColumnList.
	qualifiedAttemptColumns = qualifyColumns(attemptsTable, attemptColumns)
)

type attemptModel struct {
	TransactionID string    `db:"transaction_id"`
	TxHash        string    `db:"tx_hash"`
	Nonce         uint64    `db:"nonce"`
	RawTx         string    `db:"raw_tx"`
	CreatedAt     time.Time `db:"created_at"`
}

type txModel struct {
	ID                   string                   `db:"id"`
	ChainID              uint64                   `db:"chain_id"`
	IntentID             *string                  `db:"intent_id"`
	OperationKey         *string                  `db:"operation_key"`
	PayloadHash          *string                  `db:"payload_hash"`
	Sender               *string                  `db:"sender"`
	Address              string                   `db:"address"`
	Value                string                   `db:"value"`
	Data                 []byte                   `db:"data"`
	GasLimit             uint64                   `db:"gas_limit"`
	RawAuthorizations    *string                  `db:"raw_authorizations"`
	AssignedNonce        *uint64                  `db:"assigned_nonce"`
	RawTx                string                   `db:"raw_tx"`
	RawReceipt           string                   `db:"raw_receipt"`
	Status               txmngr_provider.TxStatus `db:"status"`
	Msg                  string                   `db:"msg"`
	NextActionAt         time.Time                `db:"next_action_at"`
	BroadcastPending     bool                     `db:"broadcast_pending"`
	RejectedAttemptHash  *string                  `db:"rejected_attempt_hash"`
	NonceConsumedAt      *time.Time               `db:"nonce_consumed_at"`
	NotifiedAt           *time.Time               `db:"notified_at"`
	NotificationAttempts uint32                   `db:"notification_attempts"`
	NextNotificationAt   time.Time                `db:"next_notification_at"`
	NotificationError    string                   `db:"notification_error"`
	UpdatedAt            time.Time                `db:"updated_at"`
	CreatedAt            time.Time                `db:"created_at"`
}

type chainTransactionCount struct {
	ChainID uint64 `db:"chain_id"`
	Count   uint64 `db:"count"`
}

type transactionsQ struct {
	db       *sqlx.DB
	sender   *common.Address
	selector sq.SelectBuilder
	inserter sq.InsertBuilder
}

var _ txmngr_provider.TransactionsQ = (*transactionsQ)(nil)

// NewTransactionsQ creates a new instance of pg transactionsQ
func NewTransactionsQ(db *sqlx.DB) txmngr_provider.TransactionsQ {
	return &transactionsQ{
		db:       db,
		selector: psql.Select(transactionColumns...).From(txsTable),
		inserter: psql.Insert(txsTable),
	}
}

// NewSenderTransactionsQ permanently scopes a queue to one signer, including
// New() and metrics. It cannot adopt historical rows with an unset sender.
func NewSenderTransactionsQ(db *sqlx.DB, sender common.Address) txmngr_provider.TransactionsQ {
	q := NewTransactionsQ(db).(*transactionsQ)
	q.sender = &sender
	q.selector = q.selector.Where(sq.Eq{senderTxsColumn: sender.Hex()})
	return q
}

func (q transactionsQ) New() txmngr_provider.TransactionsQ {
	if q.sender != nil {
		return NewSenderTransactionsQ(q.db, *q.sender)
	}
	return NewTransactionsQ(q.db)
}

func (q transactionsQ) Insert(tx txmngr_provider.Transaction) error {
	if q.sender != nil && tx.Sender != *q.sender {
		return errors.New("transaction sender differs from queue signer")
	}
	modelData, err := transactionToTxModel(tx)
	if err != nil {
		return fmt.Errorf("converting transaction %s to model data: %w", tx.ID, err)
	}
	query, args, err := q.inserter.SetMap(modelData).ToSql()
	if err != nil {
		return fmt.Errorf("building transaction insert: %w", err)
	}
	_, err = q.db.Exec(query, args...)
	return err
}

func (q transactionsQ) InsertOrGet(tx txmngr_provider.Transaction) (*txmngr_provider.Transaction, error) {
	if q.sender != nil && tx.Sender != *q.sender {
		return nil, errors.New("transaction sender differs from queue signer")
	}
	modelData, err := transactionToTxModel(tx)
	if err != nil {
		return nil, errors.Wrap(err, "failed to convert tx to model data")
	}

	// DO UPDATE rather than DO NOTHING: under READ COMMITTED, DO NOTHING returns
	// no row when a concurrent writer holds an uncommitted row for this key, and
	// the follow-up SELECT cannot see that row either — so a concurrent retry of
	// the same operation would fail instead of returning the existing
	// transaction. DO UPDATE blocks on the writer and returns the winning row.
	query, args, err := q.inserter.SetMap(modelData).
		Suffix(`ON CONFLICT (operation_key) WHERE operation_key IS NOT NULL
                DO UPDATE SET operation_key = EXCLUDED.operation_key
                RETURNING ` + transactionColumnList).
		ToSql()
	if err != nil {
		return nil, fmt.Errorf("building insert-or-get transaction query: %w", err)
	}

	var model txModel
	if err = q.db.Get(&model, query, args...); err != nil {
		return nil, fmt.Errorf("inserting transaction: %w", err)
	}

	if tx.OperationKey != nil && (model.PayloadHash == nil || tx.PayloadHash == nil || *model.PayloadHash != *tx.PayloadHash) {
		return nil, txmngr_provider.ErrIdempotencyConflict
	}
	return txModelToTransaction(model)
}

func (q transactionsQ) InsertAttempt(attempt txmngr_provider.TransactionAttempt) error {
	modelData, err := attemptToModel(attempt)
	if err != nil {
		return errors.Wrap(err, "failed to convert transaction attempt")
	}
	// DO UPDATE returns the owning row whether this call inserted it or lost the
	// race to a concurrent one, so a retry after a post-commit transport error
	// still reconciles ownership in a single round trip.
	query, args, err := psql.Insert(attemptsTable).SetMap(modelData).
		Suffix("ON CONFLICT (tx_hash) DO UPDATE SET tx_hash = EXCLUDED.tx_hash RETURNING transaction_id").ToSql()
	if err != nil {
		return fmt.Errorf("building transaction attempt insert: %w", err)
	}
	var transactionID string
	if err = q.db.Get(&transactionID, query, args...); err != nil {
		return fmt.Errorf("persisting transaction attempt %s: %w", attempt.Hash.Hex(), err)
	}
	if transactionID != attempt.TransactionID {
		return txmngr_provider.AttemptHashConflictError(attempt.Hash.Hex(), transactionID)
	}
	return nil
}

// Both the active and the terminal transition use the same guard; the caller's
// new status is what distinguishes them.
func (q transactionsQ) UpdateIfActive(tx txmngr_provider.Transaction) error {
	modelData, err := transactionToTxModel(tx)
	if err != nil {
		return fmt.Errorf("converting transaction %s to model data: %w", tx.ID, err)
	}
	query, args, err := psql.Update(txsTable).SetMap(modelData).
		Where(sq.Eq{idTxsColumn: tx.ID, statusTxsColumn: txmngr_provider.ActiveStatuses()}).ToSql()
	if err != nil {
		return fmt.Errorf("building conditional transaction update: %w", err)
	}
	result, err := q.db.Exec(query, args...)
	if err != nil {
		return fmt.Errorf("updating transaction %s: %w", tx.ID, err)
	}
	return expectSingleUpdate(result)
}

func (q transactionsQ) UpdateNotification(tx txmngr_provider.Transaction) error {
	query, args, err := psql.Update(txsTable).SetMap(map[string]interface{}{
		"notified_at":           tx.NotifiedAt,
		"notification_attempts": tx.NotificationAttempts,
		"next_notification_at":  tx.NextNotificationAt,
		"notification_error":    tx.NotificationError,
		"updated_at":            tx.UpdatedAt,
	}).Where(sq.Eq{
		idTxsColumn:     tx.ID,
		statusTxsColumn: txmngr_provider.TerminalStatuses(),
	}).Where("notified_at IS NULL").ToSql()
	if err != nil {
		return errors.Wrap(err, "failed to build notification update")
	}
	result, err := q.db.Exec(query, args...)
	if err != nil {
		return err
	}
	return expectSingleUpdate(result)
}

func expectSingleUpdate(result sql.Result) error {
	rows, err := result.RowsAffected()
	if err != nil {
		return errors.Wrap(err, "failed to inspect transaction update")
	}
	if rows != 1 {
		return txmngr_provider.ErrStaleTransaction
	}
	return nil
}

func (q transactionsQ) Select() ([]txmngr_provider.Transaction, error) {
	var (
		models []txModel
		result []txmngr_provider.Transaction
	)

	sql, args, err := q.selector.ToSql()
	if err != nil {
		return nil, err
	}

	if err := q.db.Select(&models, sql, args...); err != nil {
		return nil, errors.Wrap(err, "failed to select transactions")
	}

	for _, model := range models {
		tx, err := txModelToTransaction(model)
		if err != nil {
			return nil, errors.Wrap(err, "failed to convert model to transaction")
		}
		result = append(result, *tx)
	}

	return result, nil
}

func (q transactionsQ) Get() (*txmngr_provider.Transaction, error) {
	var model txModel

	sql, args, err := q.selector.ToSql()
	if err != nil {
		return nil, err
	}

	if err := q.db.Get(&model, sql, args...); err != nil {
		return nil, errors.Wrap(err, "failed to get transaction")
	}

	tx, err := txModelToTransaction(model)
	if err != nil {
		return nil, errors.Wrap(err, "failed to convert model to transaction")
	}

	return tx, nil
}

func (q transactionsQ) SelectAttempts(transactionID string) ([]txmngr_provider.TransactionAttempt, error) {
	var models []attemptModel
	query, args, err := psql.Select(attemptColumns...).From(attemptsTable).
		Where(sq.Eq{"transaction_id": transactionID}).OrderBy("created_at ASC", "id ASC").ToSql()
	if err != nil {
		return nil, fmt.Errorf("building attempt query for transaction %s: %w", transactionID, err)
	}
	if err = q.db.Select(&models, query, args...); err != nil {
		return nil, fmt.Errorf("selecting attempts for transaction %s: %w", transactionID, err)
	}
	attempts, err := attemptModelsToAttempts(models)
	if err != nil {
		return nil, fmt.Errorf("converting attempts for transaction %s: %w", transactionID, err)
	}
	return attempts, nil
}

func (q transactionsQ) SelectAttemptsByNonce(
	ctx context.Context,
	chainID, nonce uint64,
) ([]txmngr_provider.TransactionAttempt, error) {
	query, args, err := psql.Select(qualifiedAttemptColumns...).From(attemptsTable).
		Join(attemptsOwnerJoin).
		Where(sq.Eq{txsChainIDColumn: chainID, attemptsNonceColumn: nonce}).
		OrderBy(attemptsCreatedAtOrder, attemptsIDOrder).
		ToSql()
	if err != nil {
		return nil, fmt.Errorf("building nonce attempt query for chain %d nonce %d: %w", chainID, nonce, err)
	}

	var models []attemptModel
	if err = q.db.SelectContext(ctx, &models, query, args...); err != nil {
		return nil, fmt.Errorf("selecting attempts for chain %d nonce %d: %w", chainID, nonce, err)
	}
	attempts, err := attemptModelsToAttempts(models)
	if err != nil {
		return nil, fmt.Errorf("converting attempts for chain %d nonce %d: %w", chainID, nonce, err)
	}
	return attempts, nil
}

func (q transactionsQ) CountByChain(
	ctx context.Context,
	statuses ...txmngr_provider.TxStatus,
) (map[uint64]uint64, error) {
	counts := make(map[uint64]uint64)
	if len(statuses) == 0 {
		return counts, nil
	}

	builder := psql.
		Select(chainIDTxsColumn, "COUNT(*) AS count").
		From(txsTable).
		Where(sq.Eq{statusTxsColumn: statuses}).
		GroupBy(chainIDTxsColumn)
	if q.sender != nil {
		builder = builder.Where(sq.Eq{senderTxsColumn: q.sender.Hex()})
	}
	query, args, err := builder.ToSql()
	if err != nil {
		return nil, fmt.Errorf("building transaction count by chain query: %w", err)
	}

	var rows []chainTransactionCount
	if err = q.db.SelectContext(ctx, &rows, query, args...); err != nil {
		return nil, fmt.Errorf("counting transactions by chain: %w", err)
	}
	for _, row := range rows {
		counts[row.ChainID] = row.Count
	}
	return counts, nil
}

func (q transactionsQ) MaxAssignedNonce(
	ctx context.Context,
	sender common.Address,
	chainID uint64,
) (uint64, bool, error) {
	query, args, err := psql.Select("MAX(assigned_nonce)").
		From(txsTable).
		Where(sq.Eq{"sender": sender.Hex(), "chain_id": chainID}).
		Where("assigned_nonce IS NOT NULL").
		ToSql()
	if err != nil {
		return 0, false, fmt.Errorf("building lane assigned nonce query: %w", err)
	}
	var assignedNonce sql.NullInt64
	if err = q.db.GetContext(ctx, &assignedNonce, query, args...); err != nil {
		return 0, false, fmt.Errorf("selecting lane assigned nonce for chain %d: %w", chainID, err)
	}
	if !assignedNonce.Valid {
		return 0, false, nil
	}
	if assignedNonce.Int64 < 0 {
		return 0, false, fmt.Errorf("persisted assigned nonce %d for chain %d is negative", assignedNonce.Int64, chainID)
	}
	return uint64(assignedNonce.Int64), true, nil
}

type laneLock struct {
	conn *sqlx.Conn
	key  int64
	once sync.Once
	err  error
}

func (l *laneLock) Unlock() error {
	l.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), laneUnlockTimeout)
		defer cancel()
		var unlocked bool
		if err := l.conn.GetContext(ctx, &unlocked, "SELECT pg_advisory_unlock($1)", l.key); err != nil {
			discardConn(l.conn)
			l.err = fmt.Errorf("unlocking transaction lane: %w", err)
			return
		}
		if !unlocked {
			discardConn(l.conn)
			l.err = errors.New("transaction lane advisory lock was not held")
			return
		}
		l.err = l.conn.Close()
	})
	return l.err
}

func (q transactionsQ) TryLockLane(
	ctx context.Context,
	sender common.Address,
	chainID uint64,
) (txmngr_provider.LaneLock, bool, error) {
	conn, err := q.db.Connx(ctx)
	if err != nil {
		return nil, false, errors.Wrap(err, "acquiring transaction lane connection")
	}
	key := laneLockKey(sender, chainID)
	var locked bool
	if err = conn.GetContext(ctx, &locked, "SELECT pg_try_advisory_lock($1)", key); err != nil {
		discardConn(conn)
		return nil, false, errors.Wrap(err, "acquiring transaction lane advisory lock")
	}
	if !locked {
		if closeErr := conn.Close(); closeErr != nil {
			return nil, false, errors.Wrap(closeErr, "releasing unused transaction lane connection")
		}
		return nil, false, nil
	}
	return &laneLock{conn: conn, key: key}, true, nil
}

func laneLockKey(sender common.Address, chainID uint64) int64 {
	var encodedChainID [8]byte
	binary.BigEndian.PutUint64(encodedChainID[:], chainID)
	hash := crypto.Keccak256(sender.Bytes(), encodedChainID[:])
	// PostgreSQL advisory locks accept a signed 64-bit key; preserving all hash
	// bits through the conversion gives the intended deterministic bit pattern.
	//nolint:gosec // intentional uint64-to-int64 reinterpretation for advisory-lock identity
	return int64(binary.BigEndian.Uint64(hash[:8]))
}

func discardConn(conn *sqlx.Conn) {
	_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	_ = conn.Close()
}

func (q transactionsQ) FilterByIds(ids ...string) txmngr_provider.TransactionsQ {
	return q.withFilters(sq.Eq{idTxsColumn: ids})
}

func (q transactionsQ) FilterByChainIds(chainIDs ...uint64) txmngr_provider.TransactionsQ {
	return q.withFilters(sq.Eq{chainIDTxsColumn: chainIDs})
}

func (q transactionsQ) FilterBySenderOrUnset(sender common.Address) txmngr_provider.TransactionsQ {
	return q.withFilters(sq.Or{
		sq.Eq{senderTxsColumn: sender.Hex()},
		sq.Expr(senderTxsColumn + " IS NULL"),
	})
}

func (q transactionsQ) FilterByStatuses(statuses ...txmngr_provider.TxStatus) txmngr_provider.TransactionsQ {
	return q.withFilters(sq.Eq{statusTxsColumn: statuses})
}

func (q transactionsQ) FilterReadyBefore(before time.Time) txmngr_provider.TransactionsQ {
	return q.withFilters(sq.LtOrEq{nextActionColumn: before})
}

// FilterNotificationReadyBefore selects the undelivered terminal outbox. The
// predicate must stay in step with transactions_notification_work_idx.
func (q transactionsQ) FilterNotificationReadyBefore(before time.Time) txmngr_provider.TransactionsQ {
	q.selector = q.selector.
		Where(sq.Eq{statusTxsColumn: txmngr_provider.TerminalStatuses()}).
		Where("notified_at IS NULL").
		Where("intent_id IS NOT NULL AND intent_id <> ''").
		Where(sq.LtOrEq{"next_notification_at": before})
	return q
}

func (q transactionsQ) OrderByNextAction() txmngr_provider.TransactionsQ {
	q.selector = q.selector.OrderBy("next_action_at ASC", "created_at ASC", "id ASC")
	return q
}

func (q transactionsQ) OrderByLaneWork() txmngr_provider.TransactionsQ {
	q.selector = q.selector.OrderBy(
		"assigned_nonce ASC NULLS LAST",
		"next_action_at ASC",
		"created_at ASC",
		"id ASC",
	)
	return q
}

func (q transactionsQ) OrderByNextNotification() txmngr_provider.TransactionsQ {
	q.selector = q.selector.OrderBy("next_notification_at ASC", "created_at ASC", "id ASC")
	return q
}

func (q transactionsQ) Limit(limit uint64) txmngr_provider.TransactionsQ {
	q.selector = q.selector.Limit(limit)
	return q
}

func (q transactionsQ) withFilters(stmt interface{}) txmngr_provider.TransactionsQ {
	q.selector = q.selector.Where(stmt)
	return q
}
