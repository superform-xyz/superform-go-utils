package txmngr

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"maps"
	"math/big"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/google/uuid"
	"github.com/holiman/uint256"
	"github.com/pkg/errors"
	"github.com/rs/zerolog/log"
	"golang.org/x/sync/errgroup"

	"github.com/superform-xyz/superform-go-utils/evm/txmngr/txmngr_provider"
)

const (
	// priorityCoefficient is the 15% priority coefficient for the gas price calculation
	priorityCoefficient = int64(15)

	// pendingTxProcessingInterval is the interval for processing pending transactions from the DB
	pendingTxProcessingInterval = time.Second / 2
	// transactionMetricsInterval limits queue-depth reads against the durable store.
	transactionMetricsInterval = 15 * time.Second

	// txCompletionWaitInterval is the interval for waiting for the tx to be broadcasted/mined
	txCompletionWaitInterval = time.Second / 2

	// maxFeeReplacements caps how many times one logical transaction is re-signed
	// at a higher fee. It is not a lifetime bound: once reached, the newest
	// durable candidate keeps being rebroadcast at exhaustedPollInterval.
	maxFeeReplacements            = 5
	notificationWorkBatch         = 50
	maxChainWorkers               = 4
	maxTransactionsPerLane        = 25
	maxHashCollisionResigns       = 8
	maxNotificationWorkers        = 4
	maxTransactionMessageBytes    = 512
	maxReconciliationPrimaryBytes = maxTransactionMessageBytes / 2
	reconciliationSeparator       = "; reconciliation: "
	messageOmissionMarker         = "..."
	rpcStepTimeout                = 15 * time.Second
	notificationTimeout           = 10 * time.Second
	notificationMinBackoff        = time.Second
	notificationMaxBackoff        = time.Minute
	exhaustedPollInterval         = 30 * time.Second

	// consumedNonceGrace is how long a transaction whose nonce was reported as
	// already used keeps polling for a lagging receipt before being abandoned.
	consumedNonceGrace = 2 * time.Minute
	// recoveredAttemptRejectionGrace is how long a durable attempt that may have
	// crossed a crash boundary remains ambiguous before a later explicit
	// rejection may be reconciled as non-admission.
	recoveredAttemptRejectionGrace = 2 * time.Minute
)

var (
	rpcURLPattern = regexp.MustCompile(`(?i)(?:https?|wss?)://[^\s"'<>]+`)
	bearerPattern = regexp.MustCompile(`(?i)\bbearer\s+[a-z0-9._~+/=-]+`)
	jwtPattern    = regexp.MustCompile(`\beyJ[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+\b`)

	// ErrIdempotencyConflict is returned when an operation key is reused for a
	// different transaction payload.
	ErrIdempotencyConflict = txmngr_provider.ErrIdempotencyConflict

	// errTransactionUnprocessable marks a permanent, row-local defect: the row
	// can never advance no matter how often it is retried. It is parked rather
	// than aborting its lane, because an ambiguous infrastructure error must
	// still stop the lane while a bad row must not starve the rows behind it.
	errTransactionUnprocessable = errors.New("transaction cannot be advanced")

	// errLaneAwaitingNodeNonce is an expected lane barrier. A durable nonce
	// assignment exists that the selected RPC backend has not made contiguous
	// yet, so assigning a higher nonce could create a permanent gap.
	errLaneAwaitingNodeNonce = errors.New("transaction lane is awaiting node nonce")

	// errLaneStateChanged tells the batch to reload its high-water mark after a
	// terminal transition released an assigned nonce.
	errLaneStateChanged = errors.New("transaction lane state changed")
)

// nonceLane tracks the next free nonce for one (sender, chain) while its lane
// lock is held. The lock is what serializes assignment across processes, so this
// counter never outlives the batch and needs no synchronization of its own.
type nonceLane struct {
	next  uint64
	known bool
}

// observe records that nonce has been assigned.
func (l *nonceLane) observe(nonce uint64) {
	if next := nonce + 1; !l.known || next > l.next {
		l.next, l.known = next, true
	}
}

// reserve returns the next nonce only when the node's pending nonce has caught
// up with every durable assignment. The durable high-water mark prevents nonce
// reuse across replicas; the RPC check prevents an unadmitted nonce at N from
// cascading into signed transactions at N+1, N+2, and beyond.
//
// It does not advance the lane: the caller calls observe only once the
// assignment is durable, so a failed write reuses the nonce instead of leaving
// a gap.
func (l *nonceLane) reserve(rpcNonce uint64) (uint64, bool) {
	if l.known && rpcNonce < l.next {
		return 0, false
	}
	return rpcNonce, true
}

// isTip reports whether nonce is the lane's highest durable assignment. A
// rejected nonce can only be released automatically at the tip; otherwise a
// previously assigned follower would be left behind a database nonce gap.
func (l *nonceLane) isTip(nonce uint64) bool {
	return l.known && l.next == nonce+1
}

// EthClient is the interface for the Ethereum client
type EthClient interface {
	bind.ContractTransactor
	bind.ContractBackend
	bind.DeployBackend
	ethereum.TransactionReader
}

// TxManager is the interface for the tx manager
type TxManager interface {
	// Start starts the tx manager
	Start(ctx context.Context)

	// Stop stops the tx manager
	Stop()

	// SendTxAsync sends a tx asynchronously
	SendTxAsync(
		ctx context.Context,
		chainId uint64,
		addr common.Address,
		data []byte,
		gasLimit uint64,
		value *big.Int,
		authorizations []types.SetCodeAuthorization,
		intentID *string,
		opts ...txmngr_provider.SendTxOption,
	) (string, error)

	// WaitTxCompleted waits for the tx completion
	WaitTxCompleted(ctx context.Context, id string) (*txmngr_provider.Transaction, error)
}

// txManager is the implementation of the TxManager interface
type txManager struct {
	observer        Observer
	beforeAttempt   func(context.Context, txmngr_provider.Transaction, bool) (time.Time, error)
	transactionsQ   txmngr_provider.TransactionsQ
	clients         map[uint64]EthClient
	sender          common.Address
	bumpIntervalMap map[uint64]time.Duration
	blockTimeMap    map[uint64]time.Duration
	getTransactOpts func(chainId uint64) (*bind.TransactOpts, error)
	onStatusChange  TxStatusCallback
	wg              sync.WaitGroup
	stop            chan struct{}
	startOnce       sync.Once
	stopOnce        sync.Once
	lifecycleMu     sync.Mutex
	cancel          context.CancelFunc
	stopped         bool
	workMu          sync.Mutex
	nextChainIndex  int
}

var _ TxManager = (*txManager)(nil)

// TxStatusCallback is called after a transaction reaches a terminal status and
// the status update has been persisted.
type TxStatusCallback func(context.Context, txmngr_provider.Transaction) error

// SendTxOption configures one queued transaction. It remains exported from
// this package as an alias so existing callers do not need to import the
// provider package.
type SendTxOption = txmngr_provider.SendTxOption

// WithOperationKey makes SendTxAsync idempotent for a deterministic logical
// operation. Reusing the key with different payload fields returns
// ErrIdempotencyConflict.
func WithOperationKey(operationKey string) SendTxOption {
	return func(options *txmngr_provider.SendTxOptions) {
		if operationKey != "" {
			options.OperationKey = &operationKey
		}
	}
}

// Option configures a tx manager.
type Option func(*txManager)

// WithOnStatusChange registers an optional terminal status callback.
func WithOnStatusChange(callback TxStatusCallback) Option {
	return func(m *txManager) {
		m.onStatusChange = callback
	}
}

// New creates a new tx manager
func New(
	transactionsQ txmngr_provider.TransactionsQ,
	clients map[uint64]EthClient,
	sender common.Address,
	bumpIntervalMap map[uint64]time.Duration,
	blockTimeMap map[uint64]time.Duration,
	getTransactOpts func(chainId uint64) (*bind.TransactOpts, error),
	opts ...Option,
) TxManager {
	m := &txManager{
		observer:        noopObserver{},
		transactionsQ:   transactionsQ,
		clients:         clients,
		sender:          sender,
		bumpIntervalMap: bumpIntervalMap,
		blockTimeMap:    blockTimeMap,
		getTransactOpts: getTransactOpts,
		stop:            make(chan struct{}),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(m)
		}
	}
	return m
}

// Start starts the tx manager
func (m *txManager) Start(ctx context.Context) {
	m.startOnce.Do(func() {
		runCtx, cancel := context.WithCancel(ctx)
		m.lifecycleMu.Lock()
		if m.stopped {
			m.lifecycleMu.Unlock()
			cancel()
			return
		}
		m.cancel = cancel
		// Without a callback there is nothing to deliver, so the notification
		// processor would tick forever into an immediate return.
		notify := m.onStatusChange != nil
		m.wg.Add(2)
		if notify {
			m.wg.Add(1)
		}
		m.lifecycleMu.Unlock()
		go m.runLoop(runCtx, "tx processor", pendingTxProcessingInterval, m.processTxs)
		go m.runLoop(runCtx, "transaction metrics", transactionMetricsInterval, m.observeTransactionMetrics)
		if notify {
			go m.runLoop(runCtx, "notification processor", pendingTxProcessingInterval, m.deliverStatusNotifications)
		}
	})
}

// Stop stops the tx manager
func (m *txManager) Stop() {
	m.stopOnce.Do(func() {
		close(m.stop)
		m.lifecycleMu.Lock()
		m.stopped = true
		if m.cancel != nil {
			m.cancel()
		}
		m.lifecycleMu.Unlock()
	})
	m.wg.Wait()
}

// SendTxAsync stores TX in the transactionsQ and returns its ID
func (m *txManager) SendTxAsync(
	ctx context.Context,
	chainId uint64,
	addr common.Address,
	txData []byte,
	gasLimit uint64,
	value *big.Int,
	authorizations []types.SetCodeAuthorization,
	intentID *string,
	opts ...txmngr_provider.SendTxOption,
) (string, error) {
	// Make sure the given chain ID is supported
	if _, ok := m.clients[chainId]; !ok {
		return "", errors.Errorf("unsupported chain ID: %d", chainId)
	}

	options := new(txmngr_provider.SendTxOptions)
	for _, option := range opts {
		if option != nil {
			option(options)
		}
	}
	if value == nil {
		value = new(big.Int)
	}
	payloadHash, err := transactionPayloadHash(chainId, addr, txData, gasLimit, value, authorizations)
	if err != nil {
		return "", fmt.Errorf("hashing transaction payload: %w", err)
	}

	id := uuid.New().String()
	now := time.Now()

	stored, err := m.transactionsQ.InsertOrGet(txmngr_provider.Transaction{
		ID:                 id,
		ChainID:            chainId,
		Sender:             m.sender,
		Address:            addr,
		Data:               append([]byte(nil), txData...),
		GasLimit:           gasLimit,
		Value:              new(big.Int).Set(value),
		RawAuthorizations:  append([]types.SetCodeAuthorization(nil), authorizations...),
		Status:             txmngr_provider.PendingTxStatus,
		OperationKey:       options.OperationKey,
		PayloadHash:        &payloadHash,
		NextActionAt:       now,
		NextNotificationAt: now,
		CreatedAt:          now,
		UpdatedAt:          now,
		IntentID:           intentID,
	})
	if err != nil {
		return "", errors.Wrap(err, "failed to store tx in the transactionsQ")
	}

	return stored.ID, nil
}

// WaitTxCompleted waits for the tx completion
func (m *txManager) WaitTxCompleted(ctx context.Context, id string) (*txmngr_provider.Transaction, error) {
	ticker := time.NewTicker(txCompletionWaitInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			tx, err := m.transactionsQ.New().FilterByIds(id).Get()
			if err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					continue
				}
				return nil, err
			}

			if tx.IsCompleted() {
				return tx, nil
			}
		}
	}
}

// runLoop ticks tick until the manager is stopped or ctx is cancelled. It owns
// the wait-group slot the caller added before starting it.
func (m *txManager) runLoop(
	ctx context.Context,
	name string,
	interval time.Duration,
	tick func(context.Context),
) {
	defer m.wg.Done()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Info().Str("loop", name).Msg("tx manager loop stopped due to cancelled context")
			return
		case <-m.stop:
			log.Info().Str("loop", name).Msg("tx manager loop stopped")
			return
		case <-ticker.C:
			tick(ctx)
		}
	}
}

func (m *txManager) observeTransactionMetrics(ctx context.Context) {
	counts, err := m.transactionsQ.CountByChain(ctx, txmngr_provider.ActiveStatuses()...)
	if err != nil {
		if ctx.Err() == nil {
			log.Error().Err(err).Msg("failed to observe transaction manager backlog")
		}
		return
	}

	for chainID := range m.clients {
		m.observer.SetActiveTransactions(chainID, counts[chainID])
		delete(counts, chainID)
	}
	// Rows for a removed chain are still an actionable backlog even though this
	// manager no longer has a client capable of advancing them.
	for chainID, count := range counts {
		m.observer.SetActiveTransactions(chainID, count)
	}
}

// processTxs advances a bounded batch of due transactions. Nonce assignment is
// serialized per chain by the lane lock; chains advance concurrently.
func (m *txManager) processTxs(ctx context.Context) {
	var workers errgroup.Group
	workers.SetLimit(maxChainWorkers)
	for _, chainID := range m.nextWorkChainIDs(maxChainWorkers) {
		workers.Go(func() error {
			m.processLaneBatch(ctx, chainID)
			return nil
		})
	}
	_ = workers.Wait()
}

// logLane reports a lane-level failure, unless the context is already cancelled
// and the error is only fallout from shutting down.
func (m *txManager) logLane(ctx context.Context, chainID uint64, err error, msg string) {
	if ctx.Err() != nil {
		return
	}
	log.Error().Err(err).Str("sender", m.sender.Hex()).Uint64("chainId", chainID).Msg(msg)
}

func (m *txManager) nextWorkChainIDs(limit int) []uint64 {
	chainIDs := slices.Sorted(maps.Keys(m.clients))
	if len(chainIDs) == 0 || limit <= 0 {
		return nil
	}
	limit = min(limit, len(chainIDs))

	m.workMu.Lock()
	defer m.workMu.Unlock()
	start := m.nextChainIndex % len(chainIDs)
	selected := make([]uint64, 0, limit)
	for offset := range limit {
		selected = append(selected, chainIDs[(start+offset)%len(chainIDs)])
	}
	m.nextChainIndex = (start + limit) % len(chainIDs)
	return selected
}

// processLaneBatch advances one chain's due transactions while holding that
// chain's lane lock, so nonce assignment is serialized across manager instances.
func (m *txManager) processLaneBatch(ctx context.Context, chainID uint64) {
	if ctx.Err() != nil {
		return
	}
	laneLock, locked, err := m.transactionsQ.TryLockLane(ctx, m.sender, chainID)
	if err != nil {
		m.logLane(ctx, chainID, err, "failed to acquire transaction lane")
		return
	}
	if !locked {
		return
	}
	defer func() {
		if unlockErr := laneLock.Unlock(); unlockErr != nil {
			log.Error().Err(unlockErr).Str("sender", m.sender.Hex()).Uint64("chainId", chainID).
				Msg("failed to release transaction lane")
		}
	}()

	// Read the durable high-water mark only now that the lock excludes every
	// other writer, so the lane counter cannot start from a stale view.
	maxNonce, found, err := m.transactionsQ.MaxAssignedNonce(ctx, m.sender, chainID)
	if err != nil {
		m.logLane(ctx, chainID, err, "failed to read transaction lane nonce")
		return
	}
	lane := &nonceLane{known: found}
	if found {
		lane.next = maxNonce + 1
	}

	transactions, err := m.transactionsQ.New().
		FilterByChainIds(chainID).
		FilterBySenderOrUnset(m.sender).
		FilterByStatuses(txmngr_provider.ActiveStatuses()...).
		FilterReadyBefore(time.Now()).
		OrderByLaneWork().
		Limit(maxTransactionsPerLane).
		Select()
	if err != nil {
		m.logLane(ctx, chainID, err, "failed to list ready transactions")
		return
	}

	for _, tx := range transactions {
		if ctx.Err() != nil {
			return
		}
		stepCtx, cancel := context.WithTimeout(ctx, rpcStepTimeout)
		err = m.processTransactionStep(stepCtx, tx, lane)
		cancel()
		if err == nil || errors.Is(err, txmngr_provider.ErrStaleTransaction) {
			continue
		}
		if errors.Is(err, errLaneAwaitingNodeNonce) || errors.Is(err, errLaneStateChanged) {
			// These are deliberate control-flow barriers, not failed work. Reload
			// the durable lane state and the node's pending nonce on the next pass.
			return
		}
		if ctx.Err() != nil {
			return
		}
		log.Error().Err(err).Str("txId", tx.ID).Uint64("chainId", tx.ChainID).
			Msg("failed to advance transaction")
		m.rescheduleTransaction(tx, err)
		if errors.Is(err, errTransactionUnprocessable) {
			// Defer this permanent row-local defect and continue through the
			// already selected sender-scoped batch instead of starving its peers.
			continue
		}
		// Any other failure may be an ambiguous write that already committed.
		// Stop the lane so later rows cannot allocate from a stale view; the
		// next pass reloads durable state under the lane lock.
		return
	}
}

func (m *txManager) processTransactionStep(
	ctx context.Context,
	tx txmngr_provider.Transaction,
	lane *nonceLane,
) error {
	if _, ok := m.clients[tx.ChainID]; !ok {
		return fmt.Errorf("no client configured for chain %d: %w", tx.ChainID, errTransactionUnprocessable)
	}
	attempts, err := m.transactionsQ.SelectAttempts(tx.ID)
	if err != nil {
		return fmt.Errorf("loading signed attempts: %w", err)
	}

	// Backward-compatible recovery for rows written before transaction_attempts
	// existed. The persisted raw transaction is the only safe candidate to
	// resume; its nonce must never be replaced with the current account nonce.
	if len(attempts) == 0 && tx.Tx != nil {
		legacyAttempt := txmngr_provider.TransactionAttempt{
			TransactionID: tx.ID,
			Hash:          tx.Tx.Hash(),
			Nonce:         tx.Tx.Nonce(),
			Tx:            tx.Tx,
			CreatedAt:     tx.UpdatedAt,
		}
		if legacyAttempt.CreatedAt.IsZero() {
			legacyAttempt.CreatedAt = time.Now()
		}
		if _, _, err = validatePersistedAttempts([]txmngr_provider.TransactionAttempt{legacyAttempt}); err != nil {
			return fmt.Errorf("validating legacy signed attempt: %w: %w", err, errTransactionUnprocessable)
		}
		if err = m.transactionsQ.InsertAttempt(legacyAttempt); err != nil {
			return fmt.Errorf("recovering legacy signed attempt: %w", err)
		}
		attempts = append(attempts, legacyAttempt)
		// The old schema could not distinguish a durable raw transaction from a
		// completed broadcast call. Replaying this exact hash is the only safe
		// recovery; fee replacement must wait until that ambiguity is resolved.
		tx.BroadcastPending = true
	}

	storedSender := tx.Sender
	storedNonce := tx.AssignedNonce
	receiptReadFailed := false
	if len(attempts) > 0 {
		attemptSender, nonce, validationErr := validatePersistedAttempts(attempts)
		if validationErr != nil {
			return fmt.Errorf("%w: %w", validationErr, errTransactionUnprocessable)
		}
		tx.Sender = attemptSender
		tx.AssignedNonce = &nonce
		tx.Status = txmngr_provider.ConfirmingTxStatus
		if attemptSender == m.sender {
			// A durable signed attempt reserves its nonce even when the legacy
			// parent row predates assigned_nonce. Keep later rows in this same
			// lane batch from reusing it while an RPC backend is lagging.
			lane.observe(nonce)
		}
		latest := attempts[len(attempts)-1].Tx
		if tx.Tx == nil || tx.Tx.Hash() != latest.Hash() {
			tx.Tx = latest
			tx.BroadcastPending = true
		}

		// Reconcile structurally valid durable attempts before inspecting mutable
		// parent metadata or the currently configured signer. This lets a mined
		// old-signer attempt reach a terminal state after key rotation and repairs
		// stale parent sender/nonce fields from the immutable winning attempt.
		if receipt, winner, receiptErr := m.findKnownReceipt(ctx, tx.ChainID, attempts); receipt != nil {
			return m.finalizeTransaction(tx, winner, receipt)
		} else if receiptErr != nil {
			tx.Msg = appendReconciliationError(tx.Msg, receiptErr)
			receiptReadFailed = true
		}
		if storedNonce != nil && *storedNonce != nonce {
			return fmt.Errorf("assigned nonce %d differs from signed attempt nonce %d: %w", *storedNonce, nonce, errTransactionUnprocessable)
		}
		if storedSender != (common.Address{}) && storedSender != attemptSender {
			return fmt.Errorf("persisted sender %s differs from signed attempt sender %s: %w", storedSender.Hex(), attemptSender.Hex(), errTransactionUnprocessable)
		}
	} else if tx.Sender == (common.Address{}) {
		tx.Sender = m.sender
	}

	// Key rotation must not cause this manager to sign or broadcast from a
	// different account. Receipt reconciliation above remains safe and lets an
	// already-mined transaction complete while the old signer is unavailable.
	if tx.Sender != m.sender {
		return fmt.Errorf("persisted sender %s differs from manager sender %s: %w", tx.Sender.Hex(), m.sender.Hex(), errTransactionUnprocessable)
	}
	if tx.RejectedAttemptHash != nil {
		if len(attempts) == 0 || tx.AssignedNonce == nil ||
			*tx.RejectedAttemptHash != attempts[len(attempts)-1].Hash.Hex() {
			return fmt.Errorf("persisted rejection does not match the latest assigned attempt: %w", errTransactionUnprocessable)
		}
		if receiptReadFailed {
			return m.scheduleRetryAfterBlock(tx, attempts)
		}
		return m.reconcileRejectedSubmission(
			ctx, tx, attempts, time.Now(), lane.isTip(*tx.AssignedNonce),
		)
	}
	resumeBroadcast := len(attempts) > 0 && tx.BroadcastPending
	if tx.NonceConsumedAt != nil && !resumeBroadcast {
		return m.pollConsumedNonce(tx, attempts)
	}
	if len(attempts) > 0 && !tx.BroadcastPending && time.Now().Before(m.nextBumpAt(tx.ChainID, attempts[len(attempts)-1])) {
		return m.scheduleNextCheck(tx, attempts)
	}
	if m.beforeAttempt != nil {
		retryAt, admissionErr := m.beforeAttempt(ctx, tx, len(attempts) > 0)
		if errors.Is(admissionErr, ErrUnsignedTransactionExpired) && len(attempts) == 0 &&
			(tx.AssignedNonce == nil || lane.isTip(*tx.AssignedNonce)) {
			tx.AssignedNonce = nil
			if err := m.abandonTransaction(tx, "unsigned transaction expired before signing"); err != nil {
				return err
			}
			return errLaneStateChanged
		}
		if admissionErr != nil {
			return fmt.Errorf("attempt admission: %w", admissionErr)
		}
		if retryAt.After(time.Now()) {
			tx.Msg = "attempt deferred by application admission"
			return m.scheduleNextCheckNotBefore(tx, attempts, retryAt)
		}
	}
	if resumeBroadcast {
		return m.broadcastAndSchedule(ctx, tx, attempts, broadcastPolicy{
			nonceReleasable: lane.isTip(*tx.AssignedNonce),
		})
	}

	if tx.AssignedNonce == nil {
		rpcNonce, nonceErr := m.clients[tx.ChainID].PendingNonceAt(ctx, m.sender)
		if nonceErr != nil {
			return redactedRPCError("getting pending nonce", nonceErr)
		}
		nonce, ready := lane.reserve(rpcNonce)
		if !ready {
			m.observer.ObserveLaneBarrier(
				tx.ChainID, LaneBarrierRPCNonceBehind,
			)
			return fmt.Errorf(
				"%w: node pending nonce %d is behind durable next nonce %d",
				errLaneAwaitingNodeNonce, rpcNonce, lane.next,
			)
		}
		tx.AssignedNonce = &nonce
		tx.Status = txmngr_provider.ProcessingTxStatus
		tx.UpdatedAt = time.Now()
		if err = m.transactionsQ.UpdateIfActive(tx); err != nil {
			return fmt.Errorf("persisting assigned nonce: %w", err)
		}
		lane.observe(nonce)
	}

	if len(attempts) == 0 {
		return m.createAndBroadcastAttempt(ctx, tx, attempts, lane.isTip(*tx.AssignedNonce))
	}

	if len(attempts) < maxFeeReplacements {
		return m.createAndBroadcastAttempt(ctx, tx, attempts, false)
	}
	// The cap limits newly signed fee replacements, not propagation of the
	// durable candidate. A previously accepted transaction may be evicted from
	// a node's mempool; periodically resending the exact hash is idempotent and
	// cannot migrate the logical payload to another nonce.
	return m.broadcastAndSchedule(ctx, tx, attempts, broadcastPolicy{
		nonceReleasable: lane.isTip(*tx.AssignedNonce),
	})
}

// pollConsumedNonce handles a transaction whose nonce a node reported as already
// used. Receipts can lag a node or two, so they get a grace window; after that
// the row is abandoned, because polling it forever would hang WaitTxCompleted
// and hold its notification slot indefinitely.
func (m *txManager) pollConsumedNonce(
	tx txmngr_provider.Transaction,
	attempts []txmngr_provider.TransactionAttempt,
) error {
	if time.Since(*tx.NonceConsumedAt) > consumedNonceGrace {
		nonce := uint64(0)
		if tx.AssignedNonce != nil {
			nonce = *tx.AssignedNonce
		}
		return m.abandonTransaction(tx, fmt.Sprintf(
			"nonce %d was consumed by another transaction and no attempt was mined", nonce))
	}
	return m.scheduleNextCheck(tx, attempts)
}

func (m *txManager) createAndBroadcastAttempt(
	ctx context.Context,
	tx txmngr_provider.Transaction,
	attempts []txmngr_provider.TransactionAttempt,
	canReleaseNonce bool,
) error {
	if tx.AssignedNonce == nil {
		return errors.New("cannot sign transaction without an assigned nonce")
	}
	// Captured before persistUniqueAttempt, because attempts is appended to
	// below and len(attempts) then no longer answers "was this the first
	// attempt of this logical transaction?".
	firstAttempt := len(attempts) == 0
	attempt, err := m.persistUniqueAttempt(ctx, tx, attempts)
	if err != nil {
		return err
	}
	now := attempt.CreatedAt
	attempts = append(attempts, attempt)
	tx.Tx = attempt.Tx
	tx.Status = txmngr_provider.ConfirmingTxStatus
	tx.BroadcastPending = true
	tx.RejectedAttemptHash = nil
	tx.NextActionAt = now.Add(m.blockTime(tx.ChainID))
	tx.Msg = ""
	tx.UpdatedAt = now
	if err = m.transactionsQ.UpdateIfActive(tx); err != nil {
		return fmt.Errorf("persisting canonical transaction attempt: %w", err)
	}
	return m.broadcastAndSchedule(ctx, tx, attempts, broadcastPolicy{
		firstAttemptThisPass: firstAttempt,
		nonceReleasable:      canReleaseNonce,
	})
}

// persistUniqueAttempt signs and durably records one attempt whose hash no
// other logical transaction owns. Keep one on-chain hash owned by one logical
// transaction: if a terminal, proven-unadmitted row previously signed the exact
// same envelope, load the durable global history for this lane and re-sign
// above its highest fee. Reading immutable attempts makes progress survive
// process restarts.
func (m *txManager) persistUniqueAttempt(
	ctx context.Context,
	tx txmngr_provider.Transaction,
	attempts []txmngr_provider.TransactionAttempt,
) (txmngr_provider.TransactionAttempt, error) {
	var (
		collisionFeeFloor *GasFee
		previousHash      common.Hash
		attempt           txmngr_provider.TransactionAttempt
	)
	for collision := range maxHashCollisionResigns {
		signedTx, err := m.signAttempt(ctx, tx, attempts, collisionFeeFloor)
		if err != nil {
			return attempt, fmt.Errorf("signing transaction attempt: %w", err)
		}
		attempt = txmngr_provider.TransactionAttempt{
			TransactionID: tx.ID,
			Hash:          signedTx.Hash(),
			Nonce:         signedTx.Nonce(),
			Tx:            signedTx,
			CreatedAt:     time.Now(),
		}
		// This commit is deliberately before SendTransaction. A restart must
		// always assume that a network call may have occurred and resume the
		// exact hash.
		insertErr := m.transactionsQ.InsertAttempt(attempt)
		if insertErr == nil {
			return attempt, nil
		}
		if !errors.Is(insertErr, txmngr_provider.ErrAttemptHashConflict) {
			return attempt, fmt.Errorf("persisting signed attempt: %w", insertErr)
		}
		// Re-signing above the fee floor produced a byte-identical envelope, so
		// no number of further attempts can separate the hashes. Chains whose
		// suggested gas price is zero land here, because bumpGasPrice cannot
		// raise zero. Stop instead of burning the whole collision budget.
		if collision > 0 && attempt.Hash == previousHash {
			return attempt, fmt.Errorf(
				"re-signing above the durable fee floor did not change hash %s: %w",
				attempt.Hash.Hex(), insertErr)
		}
		previousHash = attempt.Hash

		laneAttempts, selectErr := m.transactionsQ.SelectAttemptsByNonce(ctx, tx.ChainID, attempt.Nonce)
		if selectErr != nil {
			return attempt, fmt.Errorf("loading durable nonce attempt history: %w", selectErr)
		}
		collisionFeeFloor, err = highestAttemptFee(laneAttempts, m.sender, tx.ChainID, signedTx.Type())
		if err != nil {
			return attempt, fmt.Errorf("deriving collision fee floor: %w", err)
		}
		if collisionFeeFloor == nil {
			return attempt, fmt.Errorf("%w: conflicting hash %s is absent from durable lane history",
				txmngr_provider.ErrAttemptHashConflict, attempt.Hash.Hex())
		}
	}
	return attempt, fmt.Errorf("persisting a unique signed attempt after %d collisions: %w",
		maxHashCollisionResigns, txmngr_provider.ErrAttemptHashConflict)
}

// broadcastPolicy carries what the caller has proven about this send. The two
// facts are independent, but only their conjunction skips the recovery grace
// window: a rejection may be acted on immediately only when this pass both
// signed the attempt and owns the nonce it would release.
type broadcastPolicy struct {
	// firstAttemptThisPass is set only for the first attempt of a logical
	// transaction signed during this same pass. A rejection of it cannot be a
	// replay of a pre-crash broadcast.
	firstAttemptThisPass bool
	// nonceReleasable is set when this row owns the lane's highest durable
	// assignment, so releasing its nonce cannot orphan an assigned follower.
	nonceReleasable bool
}

// broadcastAndSchedule sends the newest durable candidate and records what the
// node said about it. Resending an already-broadcast hash is idempotent, so the
// same path serves a freshly signed attempt and the resumption of an ambiguous
// one; only the durable state written beforehand differs.
func (m *txManager) broadcastAndSchedule(
	ctx context.Context,
	tx txmngr_provider.Transaction,
	attempts []txmngr_provider.TransactionAttempt,
	policy broadcastPolicy,
) error {
	if err := m.clients[tx.ChainID].SendTransaction(ctx, attempts[len(attempts)-1].Tx); err != nil {
		return m.handleBroadcastFailure(ctx, tx, attempts, err, policy)
	}
	// A hash accepted microseconds ago cannot have been mined yet, so there is
	// nothing to reconcile on the success path.
	tx.Msg = ""
	tx.BroadcastPending = false
	tx.RejectedAttemptHash = nil
	return m.scheduleNextCheck(tx, attempts)
}

// handleBroadcastFailure records what the node said about the newest durable
// candidate and decides whether an explicit rejection can be reconciled now,
// later, or not at all.
func (m *txManager) handleBroadcastFailure(
	ctx context.Context,
	tx txmngr_provider.Transaction,
	attempts []txmngr_provider.TransactionAttempt,
	sendErr error,
	policy broadcastPolicy,
) error {
	latest := attempts[len(attempts)-1]
	now := time.Now()
	reconcileAfter := latest.CreatedAt.Add(recoveredAttemptRejectionGrace)
	// An attempt signed during this pass, at a nonce this row owns, cannot be a
	// replay of a pre-crash broadcast, so it skips the recovery grace window.
	mayReconcile := (policy.firstAttemptThisPass && policy.nonceReleasable) ||
		!now.Before(reconcileAfter)
	safeErr := rpcErrorSummary(sendErr)
	reason := broadcastAttemptReason(latest.Hash, safeErr)
	disposition := classifyBroadcastError(sendErr)
	rejected := disposition == broadcastTransactionRejectedPermanent

	// Exactly one condition below arms durable rejection evidence, so clearing
	// it up front makes that the enforced default. A new disposition arm can
	// then never leave a stale hash behind, which processTransactionStep would
	// otherwise park as unprocessable.
	tx.RejectedAttemptHash = nil
	switch disposition {
	case broadcastAlreadyKnown:
		tx.Msg = ""
		tx.BroadcastPending = false
	case broadcastNonceConsumed:
		tx.Msg = reason
		tx.NonceConsumedAt = &now
		tx.BroadcastPending = false
	case broadcastFeeRejected:
		tx.Msg = reason
		tx.BroadcastPending = false
	case broadcastTransactionRejectedPermanent:
		tx.Msg = reason
		// Before the durable grace boundary, a rejection observed while replaying
		// a recovered hash cannot prove the original call was never admitted.
		// Keep the exact candidate and do not churn RPCs before it becomes
		// eligible for evidence-based reconciliation.
		tx.BroadcastPending = !mayReconcile
		if mayReconcile {
			rejectedHash := latest.Hash.Hex()
			tx.RejectedAttemptHash = &rejectedHash
		}
	case broadcastTransactionRejectedRetryable:
		tx.Msg = reason
		// State or service health may make this exact envelope viable later.
		// Preserve its nonce and retry it without manufacturing followers.
		tx.BroadcastPending = true
	default:
		tx.Msg = "ambiguous " + reason
		tx.BroadcastPending = true
	}

	broadcastLog := log.Warn().Str("error", safeErr).Str("txId", tx.ID).Uint64("nonce", latest.Nonce).
		Str("hash", latest.Hash.Hex()).Uint64("chainId", tx.ChainID).
		Int("rpcErrorCode", rpcErrorCode(sendErr))
	if rejected {
		broadcastLog.Msg("transaction broadcast was rejected by the node")
	} else {
		broadcastLog.Msg("transaction broadcast remains unconfirmed")
	}

	// Persist fresh, explicit rejection evidence before any additional RPC
	// reconciliation. If those reads fail or the process restarts, the next pass
	// resumes reconciliation instead of forgetting that evidence. A crash before
	// this write remains conservatively ambiguous.
	if tx.RejectedAttemptHash != nil {
		if scheduleErr := m.scheduleNextCheck(tx, attempts); scheduleErr != nil {
			return scheduleErr
		}
	}

	// A receipt always wins over the send response: an earlier candidate may
	// have mined while a replacement or replay was being submitted.
	if receipt, winner, receiptErr := m.findKnownReceipt(ctx, tx.ChainID, attempts); receipt != nil {
		return m.finalizeTransaction(tx, winner, receipt)
	} else if receiptErr != nil {
		tx.Msg = appendReconciliationError(tx.Msg, receiptErr)
		notBefore := time.Time{}
		if rejected {
			notBefore = reconcileAfter
			if mayReconcile {
				notBefore = now.Add(m.blockTime(tx.ChainID))
			}
		}
		return m.scheduleNextCheckNotBefore(tx, attempts, notBefore)
	}

	if rejected {
		if mayReconcile {
			return m.reconcileRejectedSubmission(ctx, tx, attempts, now, policy.nonceReleasable)
		}
		return m.scheduleNextCheckNotBefore(tx, attempts, reconcileAfter)
	}
	return m.scheduleNextCheck(tx, attempts)
}

// reconcileRejectedSubmission resolves a durable explicit rejection. Positive
// receipt/hash evidence always wins. A clean rejection with the node still
// waiting at this nonce can release the row only at the durable lane tip; an
// advanced node nonce is treated as consumed instead. A non-tip rejection is a
// soft barrier: assigned successors may still reconcile, while the normal
// high-water check prevents any unassigned follower from taking another nonce.
func (m *txManager) reconcileRejectedSubmission(
	ctx context.Context,
	tx txmngr_provider.Transaction,
	attempts []txmngr_provider.TransactionAttempt,
	now time.Time,
	canReleaseNonce bool,
) error {
	latest := attempts[len(attempts)-1]
	// tx.Msg already holds the rejection reason recorded when the evidence was
	// armed; it is what abandonTransaction persists if this row is released.
	// Taking it from the struct keeps the two from drifting apart.
	reason := tx.Msg
	known, err := m.findKnownTransaction(ctx, tx.ChainID, attempts)
	if err != nil {
		tx.Msg = appendReconciliationError(tx.Msg, err)
		return m.scheduleRetryAfterBlock(tx, attempts)
	}
	if known != nil {
		tx.Tx = known
		tx.Msg = ""
		tx.BroadcastPending = false
		tx.RejectedAttemptHash = nil
		return m.scheduleNextCheck(tx, attempts)
	}

	pendingNonce, err := m.clients[tx.ChainID].PendingNonceAt(ctx, m.sender)
	if err != nil {
		tx.Msg = appendReconciliationError(
			tx.Msg, fmt.Errorf("pending nonce: %s", rpcErrorSummary(err)),
		)
		return m.scheduleRetryAfterBlock(tx, attempts)
	}
	switch {
	case pendingNonce == latest.Nonce:
		if !canReleaseNonce {
			m.observer.ObserveLaneBarrier(
				tx.ChainID, LaneBarrierRejectionNotTip,
			)
			return m.scheduleRetryAfterBlock(tx, attempts)
		}
		tx.AssignedNonce = nil
		tx.BroadcastPending = false
		if err = m.abandonTransaction(tx, reason); err != nil {
			return err
		}
		return errLaneStateChanged
	case pendingNonce > latest.Nonce:
		// The exact hash is absent but the node has moved beyond its nonce,
		// so this slot is consumed. Retain the assignment as a high-water mark
		// and give a lagging receipt the normal grace period.
		tx.NonceConsumedAt = &now
		tx.BroadcastPending = false
		tx.RejectedAttemptHash = nil
		return m.scheduleNextCheck(tx, attempts)
	default:
		// A node behind the assigned nonce cannot corroborate release, whether or
		// not this row is the lane tip. Preserve the exact candidate, but let
		// other assigned rows reconcile before an unassigned row reaches the
		// lane's high-water barrier.
		m.observer.ObserveLaneBarrier(
			tx.ChainID, LaneBarrierRejectionNodeBehind,
		)
		return m.scheduleRetryAfterBlock(tx, attempts)
	}
}

func (m *txManager) signAttempt(
	ctx context.Context,
	tx txmngr_provider.Transaction,
	attempts []txmngr_provider.TransactionAttempt,
	feeFloor *GasFee,
) (*types.Transaction, error) {
	if m.getTransactOpts == nil {
		return nil, errors.New("transact options provider is nil")
	}
	auth, err := m.getTransactOpts(tx.ChainID)
	if err != nil {
		return nil, fmt.Errorf("loading transact options: %w", err)
	}
	if auth == nil {
		return nil, errors.New("transact options are nil")
	}
	if auth.From != m.sender {
		return nil, fmt.Errorf("transact signer %s differs from nonce-lane sender %s", auth.From.Hex(), m.sender.Hex())
	}
	value := tx.Value
	if value == nil {
		value = new(big.Int)
	}
	attemptNumber := int64(len(attempts) + 1)
	var currentFee *GasFee
	if len(attempts) > 0 {
		previous := attempts[len(attempts)-1].Tx
		currentFee = &GasFee{
			GasPrice:  previous.GasPrice(),
			GasTipCap: previous.GasTipCap(),
			GasFeeCap: previous.GasFeeCap(),
		}
	}
	currentFee = highestGasFee(currentFee, feeFloor)
	if len(tx.RawAuthorizations) > 0 {
		currentFee, err = m.suggestEIP1559GasFee(ctx, tx.ChainID, currentFee, priorityCoefficient*attemptNumber)
		if err != nil {
			return nil, fmt.Errorf("suggesting EIP-1559 gas fee: %w", err)
		}
		return auth.Signer(auth.From, types.NewTx(&types.SetCodeTx{
			To:        tx.Address,
			Nonce:     *tx.AssignedNonce,
			GasTipCap: uint256.MustFromBig(currentFee.GasTipCap),
			GasFeeCap: uint256.MustFromBig(currentFee.GasFeeCap),
			Gas:       tx.GasLimit,
			Data:      tx.Data,
			Value:     uint256.MustFromBig(value),
			AuthList:  tx.RawAuthorizations,
		}))
	}
	currentFee, err = m.suggestGasPrice(ctx, tx.ChainID, currentFee, priorityCoefficient*attemptNumber)
	if err != nil {
		return nil, fmt.Errorf("suggesting gas price: %w", err)
	}
	return auth.Signer(auth.From, types.NewTx(&types.LegacyTx{
		To:       &tx.Address,
		Nonce:    *tx.AssignedNonce,
		GasPrice: currentFee.GasPrice,
		Gas:      tx.GasLimit,
		Data:     tx.Data,
		Value:    value,
	}))
}

// highestAttemptFee derives a component-wise fee floor from immutable attempts
// for the configured sender, chain, and transaction type. Other senders can
// legitimately use the same nonce in the same service database and are ignored.
func highestAttemptFee(
	attempts []txmngr_provider.TransactionAttempt,
	sender common.Address,
	chainID uint64,
	txType uint8,
) (*GasFee, error) {
	var highest *GasFee
	for _, attempt := range attempts {
		if attempt.Tx == nil {
			return nil, fmt.Errorf("attempt %s has no signed transaction", attempt.Hash.Hex())
		}
		rawChainID := attempt.Tx.ChainId()
		if rawChainID == nil || !rawChainID.IsUint64() || rawChainID.Uint64() != chainID || attempt.Tx.Type() != txType {
			continue
		}
		attemptSender, err := types.Sender(types.LatestSignerForChainID(rawChainID), attempt.Tx)
		if err != nil {
			return nil, fmt.Errorf("recovering signer for attempt %s: %w", attempt.Hash.Hex(), err)
		}
		if attemptSender != sender {
			continue
		}
		// Only this sender's own rows must satisfy the durable invariants. These
		// checks fail the whole lane, and a row owned by another sender is one
		// this manager can neither repair nor answer for, so validating it would
		// let a single foreign corrupt row halt every transaction on the chain.
		// The filters above are field reads, so the reordering costs nothing.
		if attempt.Tx.Hash() != attempt.Hash {
			return nil, fmt.Errorf("attempt hash %s differs from raw transaction hash %s",
				attempt.Hash.Hex(), attempt.Tx.Hash().Hex())
		}
		if attempt.Tx.Nonce() != attempt.Nonce {
			return nil, fmt.Errorf("attempt %s nonce %d differs from raw transaction nonce %d",
				attempt.Hash.Hex(), attempt.Nonce, attempt.Tx.Nonce())
		}
		highest = highestGasFee(highest, &GasFee{
			GasPrice:  attempt.Tx.GasPrice(),
			GasTipCap: attempt.Tx.GasTipCap(),
			GasFeeCap: attempt.Tx.GasFeeCap(),
		})
	}
	return highest, nil
}

func highestGasFee(left, right *GasFee) *GasFee {
	switch {
	case left == nil:
		return cloneGasFee(right)
	case right == nil:
		return cloneGasFee(left)
	}
	return &GasFee{
		GasPrice:  maxBigInt(left.GasPrice, right.GasPrice),
		GasTipCap: maxBigInt(left.GasTipCap, right.GasTipCap),
		GasFeeCap: maxBigInt(left.GasFeeCap, right.GasFeeCap),
	}
}

// cloneBigInt deep copies value, so a returned GasFee never aliases a caller's
// big.Int and cannot be mutated underneath it.
func cloneBigInt(value *big.Int) *big.Int {
	if value == nil {
		return nil
	}
	return new(big.Int).Set(value)
}

func cloneGasFee(fee *GasFee) *GasFee {
	if fee == nil {
		return nil
	}
	return &GasFee{
		GasPrice:  cloneBigInt(fee.GasPrice),
		GasTipCap: cloneBigInt(fee.GasTipCap),
		GasFeeCap: cloneBigInt(fee.GasFeeCap),
	}
}

// maxBigInt returns a copy of the larger operand, treating nil as absent.
// cloneBigInt(nil) is nil, which covers the both-absent case.
func maxBigInt(left, right *big.Int) *big.Int {
	switch {
	case left == nil:
		return cloneBigInt(right)
	case right == nil || left.Cmp(right) >= 0:
		return cloneBigInt(left)
	default:
		return cloneBigInt(right)
	}
}

func validatePersistedAttempts(
	attempts []txmngr_provider.TransactionAttempt,
) (common.Address, uint64, error) {
	if len(attempts) == 0 {
		return common.Address{}, 0, errors.New("no persisted attempts")
	}
	var sender common.Address
	nonce := attempts[0].Nonce
	for _, attempt := range attempts {
		if attempt.Tx == nil {
			return common.Address{}, 0, fmt.Errorf("persisted attempt %s has no signed transaction", attempt.Hash.Hex())
		}
		if attempt.Tx.Hash() != attempt.Hash {
			return common.Address{}, 0, fmt.Errorf("persisted attempt hash %s differs from raw transaction hash %s", attempt.Hash.Hex(), attempt.Tx.Hash().Hex())
		}
		if attempt.Tx.Nonce() != attempt.Nonce {
			return common.Address{}, 0, fmt.Errorf("persisted attempt %s nonce %d differs from raw transaction nonce %d", attempt.Hash.Hex(), attempt.Nonce, attempt.Tx.Nonce())
		}
		if attempt.Nonce != nonce {
			return common.Address{}, 0, fmt.Errorf("signed attempts contain different nonces: %d and %d", nonce, attempt.Nonce)
		}
		derived, err := types.Sender(types.LatestSignerForChainID(attempt.Tx.ChainId()), attempt.Tx)
		if err != nil {
			return common.Address{}, 0, fmt.Errorf("recovering signer for persisted attempt %s: %w", attempt.Hash.Hex(), err)
		}
		if sender == (common.Address{}) {
			sender = derived
		} else if derived != sender {
			return common.Address{}, 0, fmt.Errorf("persisted attempt %s signer %s differs from attempt sender %s", attempt.Hash.Hex(), derived.Hex(), sender.Hex())
		}
	}
	return sender, nonce, nil
}

func (m *txManager) findKnownReceipt(
	ctx context.Context,
	chainID uint64,
	attempts []txmngr_provider.TransactionAttempt,
) (*types.Receipt, *types.Transaction, error) {
	var lastErr error
	for _, attempt := range attempts {
		receipt, err := m.clients[chainID].TransactionReceipt(ctx, attempt.Hash)
		if err == nil && receipt != nil {
			if receipt.TxHash != attempt.Hash {
				lastErr = fmt.Errorf("receipt hash %s does not match requested attempt %s", receipt.TxHash.Hex(), attempt.Hash.Hex())
				continue
			}
			if receipt.Status != types.ReceiptStatusFailed && receipt.Status != types.ReceiptStatusSuccessful {
				lastErr = fmt.Errorf("receipt %s has unsupported status %d", attempt.Hash.Hex(), receipt.Status)
				continue
			}
			return receipt, attempt.Tx, nil
		}
		if err != nil && !errors.Is(err, ethereum.NotFound) {
			lastErr = redactedRPCError(fmt.Sprintf("retrieving receipt %s", attempt.Hash.Hex()), err)
		}
	}
	return nil, nil, lastErr
}

// findKnownTransaction checks both the canonical chain and the node's pending
// pool for every durable candidate. A matching hash is positive admission
// evidence even when its receipt is not available yet.
func (m *txManager) findKnownTransaction(
	ctx context.Context,
	chainID uint64,
	attempts []txmngr_provider.TransactionAttempt,
) (*types.Transaction, error) {
	var lastErr error
	for _, attempt := range attempts {
		known, _, err := m.clients[chainID].TransactionByHash(ctx, attempt.Hash)
		if err == nil && known != nil {
			if known.Hash() != attempt.Hash {
				lastErr = fmt.Errorf(
					"transaction hash %s does not match requested attempt %s",
					known.Hash().Hex(), attempt.Hash.Hex(),
				)
				continue
			}
			return attempt.Tx, nil
		}
		if err != nil && !errors.Is(err, ethereum.NotFound) {
			lastErr = redactedRPCError(fmt.Sprintf("retrieving transaction %s", attempt.Hash.Hex()), err)
		}
	}
	return nil, lastErr
}

func (m *txManager) finalizeTransaction(
	tx txmngr_provider.Transaction,
	winner *types.Transaction,
	receipt *types.Receipt,
) error {
	tx.Tx = winner
	tx.Receipt = receipt
	tx.Msg = ""
	tx.RejectedAttemptHash = nil
	if receipt.Status == types.ReceiptStatusFailed {
		tx.Status = txmngr_provider.FailedTxStatus
	} else {
		tx.Status = txmngr_provider.SucceedTxStatus
	}
	return m.persistTerminal(tx)
}

// abandonTransaction records a transaction that can never be mined. Reaching a
// terminal status is what releases WaitTxCompleted and notifies the caller.
func (m *txManager) abandonTransaction(tx txmngr_provider.Transaction, reason string) error {
	log.Warn().Str("txId", tx.ID).Uint64("chainId", tx.ChainID).Str("reason", reason).
		Msg("abandoning transaction that can no longer be mined")
	tx.Status = txmngr_provider.ErroredTxStatus
	tx.Msg = reason
	tx.RejectedAttemptHash = nil
	return m.persistTerminal(tx)
}

func (m *txManager) persistTerminal(tx txmngr_provider.Transaction) error {
	now := time.Now()
	tx.Msg = transactionMessageTail(tx.Msg)
	tx.UpdatedAt = now
	tx.NextActionAt = now
	// Intent-backed terminal rows are the durable notification outbox. Keep
	// them pending even when this process has no callback configured so a later
	// restart with a publisher can deliver the result. Rows without an intent
	// have no consumer and can be acknowledged immediately.
	if tx.IntentID == nil || *tx.IntentID == "" {
		tx.NotifiedAt = &now
	} else {
		tx.NotifiedAt = nil
		tx.NotificationAttempts = 0
		tx.NextNotificationAt = now
		tx.NotificationError = ""
	}
	if err := m.transactionsQ.UpdateIfActive(tx); err != nil {
		return fmt.Errorf("persisting terminal transaction %s: %w", tx.ID, err)
	}
	m.observer.ObserveResult(tx.ChainID, string(tx.Status))
	return nil
}

func (m *txManager) scheduleNextCheck(
	tx txmngr_provider.Transaction,
	attempts []txmngr_provider.TransactionAttempt,
) error {
	return m.scheduleNextCheckNotBefore(tx, attempts, time.Time{})
}

// scheduleRetryAfterBlock defers the next pass by at least one block time. It
// floors the schedule so a pending fee bump cannot pull reconciliation forward:
// re-reading the same chain state inside one block only repeats the same RPC
// calls and returns the same inconclusive answer.
func (m *txManager) scheduleRetryAfterBlock(
	tx txmngr_provider.Transaction,
	attempts []txmngr_provider.TransactionAttempt,
) error {
	return m.scheduleNextCheckNotBefore(tx, attempts, time.Now().Add(m.blockTime(tx.ChainID)))
}

func (m *txManager) scheduleNextCheckNotBefore(
	tx txmngr_provider.Transaction,
	attempts []txmngr_provider.TransactionAttempt,
	notBefore time.Time,
) error {
	now := time.Now()
	next := now.Add(m.blockTime(tx.ChainID))
	if tx.NonceConsumedAt != nil || len(attempts) >= maxFeeReplacements {
		next = now.Add(exhaustedPollInterval)
	} else if len(attempts) > 0 {
		if bumpAt := m.nextBumpAt(tx.ChainID, attempts[len(attempts)-1]); bumpAt.Before(next) {
			next = bumpAt
		}
	}
	if next.Before(notBefore) {
		next = notBefore
	}
	if len(attempts) > 0 {
		tx.Status = txmngr_provider.ConfirmingTxStatus
	}
	tx.Msg = transactionMessageTail(tx.Msg)
	tx.NextActionAt = next
	tx.UpdatedAt = now
	if err := m.transactionsQ.UpdateIfActive(tx); err != nil {
		return fmt.Errorf("scheduling transaction %s confirmation: %w", tx.ID, err)
	}
	return nil
}

func appendReconciliationError(message string, err error) string {
	message = strings.ToValidUTF8(message, "")
	if err == nil {
		return transactionMessageTail(message)
	}
	detail := strings.ToValidUTF8(err.Error(), "")
	if message == "" {
		return transactionMessageTail(detail)
	}
	combined := message + reconciliationSeparator + detail
	if len(combined) <= maxTransactionMessageBytes {
		return combined
	}

	primaryEnd := strings.Index(message, reconciliationSeparator)
	if primaryEnd < 0 {
		primaryEnd = len(message)
	}
	primary := messageHeadAndTail(message[:primaryEnd], maxReconciliationPrimaryBytes)
	tailBytes := maxTransactionMessageBytes - len(primary) - len(reconciliationSeparator)
	if tailBytes <= 0 {
		return transactionMessageTail(combined)
	}
	history := combined[primaryEnd+len(reconciliationSeparator):]
	return primary + reconciliationSeparator + messageTail(history, tailBytes)
}

func transactionMessageTail(message string) string {
	return messageTail(message, maxTransactionMessageBytes)
}

// broadcastAttemptReason preserves the attempt identity and both ends of a
// provider rejection while reserving half the durable message for later
// reconciliation evidence.
func broadcastAttemptReason(hash common.Hash, detail string) string {
	prefix := fmt.Sprintf("broadcast attempt %s: ", hash.Hex())
	detailBytes := maxReconciliationPrimaryBytes - len(prefix)
	if detailBytes <= 0 {
		return messageHeadAndTail(prefix, maxReconciliationPrimaryBytes)
	}
	return prefix + messageHeadAndTail(detail, detailBytes)
}

func messageHeadAndTail(message string, maxBytes int) string {
	message = strings.ToValidUTF8(message, "")
	if maxBytes <= 0 {
		return ""
	}
	if len(message) <= maxBytes {
		return message
	}
	if maxBytes <= len(messageOmissionMarker) {
		return messageTail(message, maxBytes)
	}

	contentBytes := maxBytes - len(messageOmissionMarker)
	tailBytes := contentBytes / 2
	headEnd := contentBytes - tailBytes
	for headEnd > 0 && !utf8.RuneStart(message[headEnd]) {
		headEnd--
	}
	return message[:headEnd] + messageOmissionMarker + messageTail(message, tailBytes)
}

func messageTail(message string, maxBytes int) string {
	message = strings.ToValidUTF8(message, "")
	if len(message) <= maxBytes {
		return message
	}
	start := len(message) - maxBytes
	for start < len(message) && !utf8.RuneStart(message[start]) {
		start++
	}
	return message[start:]
}

// containsAny reports whether err's message contains any of needles, compared
// case-insensitively. A nil error contains nothing.
func containsAny(err error, needles ...string) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return slices.ContainsFunc(needles, func(needle string) bool {
		return strings.Contains(message, needle)
	})
}

type broadcastErrorDisposition uint8

const (
	broadcastAmbiguous broadcastErrorDisposition = iota
	broadcastAlreadyKnown
	broadcastNonceConsumed
	broadcastFeeRejected
	broadcastTransactionRejectedRetryable
	broadcastTransactionRejectedPermanent
)

// classifyBroadcastError separates admission evidence from retry policy. There
// are intentionally no chain IDs, protocol gas caps, provider names, or
// chain-specific validation messages here.
func classifyBroadcastError(err error) broadcastErrorDisposition {
	if err == nil {
		return broadcastAmbiguous
	}
	switch {
	case isAlreadyKnownError(err):
		return broadcastAlreadyKnown
	case isNonceTooLowError(err):
		return broadcastNonceConsumed
	case isFeePolicyRejectionError(err):
		return broadcastFeeRejected
	case isRetryableTransactionRejectionError(err):
		return broadcastTransactionRejectedRetryable
	case isPermanentTransactionRejectionError(err):
		return broadcastTransactionRejectedPermanent
	default:
		return broadcastAmbiguous
	}
}

// rpcErrorCode returns zero for transport/HTTP errors that did not carry a
// JSON-RPC response. Zero is not a JSON-RPC server error code.
func rpcErrorCode(err error) int {
	var responseErr rpc.Error
	if errors.As(err, &responseErr) {
		return responseErr.ErrorCode()
	}
	return 0
}

// rpcErrorSummary preserves structured node validation details while ensuring
// transport endpoints, credentials, response bodies, and bearer tokens never
// reach durable transaction messages or logs.
// redactedRPCError wraps err in an operation description with a message that is
// safe to persist and log. Cancellation and deadline errors are re-wrapped with
// %w because they carry no endpoint or credential and callers legitimately
// branch on them; every other RPC error is flattened to a redacted summary so a
// URL or bearer token can never reach tx.Msg.
func redactedRPCError(operation string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: %w", operation, err)
	}
	return fmt.Errorf("%s: %s", operation, rpcErrorSummary(err))
}

func rpcErrorSummary(err error) string {
	if err == nil {
		return ""
	}
	var responseErr rpc.Error
	if errors.As(err, &responseErr) {
		return fmt.Sprintf("rpc error %d: %s", responseErr.ErrorCode(), redactRPCText(responseErr.Error()))
	}
	var httpErr rpc.HTTPError
	if errors.As(err, &httpErr) {
		return fmt.Sprintf("rpc HTTP status %d", httpErr.StatusCode)
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "rpc transport timeout"
	case errors.Is(err, context.Canceled):
		return "rpc transport cancelled"
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return fmt.Sprintf("rpc transport %s failed", urlErr.Op)
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "rpc transport timeout"
	}
	return fmt.Sprintf("rpc transport error (%T)", err)
}

func redactRPCText(message string) string {
	message = rpcURLPattern.ReplaceAllString(message, "<redacted-rpc-url>")
	message = bearerPattern.ReplaceAllString(message, "Bearer <redacted>")
	return jwtPattern.ReplaceAllString(message, "<redacted-jwt>")
}

// feePolicyRejectionMessages are node messages that reject an envelope purely
// on fee grounds, so a fee replacement can still succeed at the same nonce.
var feePolicyRejectionMessages = []string{
	"replacement transaction underpriced", "replacement underpriced", "transaction underpriced",
	"fee too low to replace", "max fee per gas less than block base fee",
	"max fee per gas below block base fee", "fee cap less than block base fee",
	"fee cap below base fee",
}

// permanentRejectionMessages are node validation messages that mean the exact
// submitted envelope can never be admitted. Grouped by what they reject.
var permanentRejectionMessages = []string{
	// gas limit and intrinsic gas
	"gas limit too high",
	"exceeds block gas limit",
	"tx gas limit exceeds",
	"intrinsic gas too low",
	"intrinsic gas exceeds gas limit",
	"gas uint64 overflow",
	// sender and signature
	"invalid sender",
	"sender not an eoa",
	"invalid transaction signature",
	"invalid signature",
	"invalid chain id",
	"only replay-protected",
	// envelope shape
	"unsupported transaction type",
	"transaction type not supported",
	"max initcode size exceeded",
	"initcode size exceeded",
	// fee relationships
	"max priority fee per gas higher than max fee per gas",
	"max priority fee per gas exceeds max fee per gas",
	"tip above fee cap",
}

func isNonceTooLowError(err error) bool {
	code := rpcErrorCode(err)
	return code == 1 || code == -38010 || containsAny(err, "nonce too low")
}

func isAlreadyKnownError(err error) bool {
	return rpcErrorCode(err) == 1000 ||
		(!containsAny(err, "unknown transaction") && containsAny(err, "already known", "known transaction"))
}

func isFeePolicyRejectionError(err error) bool {
	code := rpcErrorCode(err)
	return code == 802 || code == 806 || containsAny(err, feePolicyRejectionMessages...)
}

// isRetryableTransactionRejectionError identifies explicit non-admission that
// may become viable after nonce, balance, or service state changes. These
// attempts retain their nonce and exact hash rather than being terminalized.
func isRetryableTransactionRejectionError(err error) bool {
	code := rpcErrorCode(err)
	return code == 2 || code == 809 || code == -38011 || code == -38014 || code == -38026 ||
		code == -32700 || code == -32600 || code == -32601 || code == -32004 || code == -32006 ||
		containsAny(err, "nonce too high", "insufficient funds", "transaction pool is full", "txpool is full")
}

// isPermanentTransactionRejectionError identifies a synchronous JSON-RPC
// response whose code or semantic message says the submitted envelope cannot
// be admitted unchanged. In particular, -32000 is deliberately not sufficient:
// Geth and hosted providers overload it for unrelated backend failures.
// Transport/HTTP failures and timeout, internal, response-size, and capacity
// errors remain ambiguous.
// The leading guard repeats the dispositions classifyBroadcastError has already
// ruled out. That is redundant on the production path but keeps this predicate
// correct for direct and test callers, so the taxonomy's mutual exclusion does
// not depend on call order.
func isPermanentTransactionRejectionError(err error) bool {
	if isAlreadyKnownError(err) || isNonceTooLowError(err) || isFeePolicyRejectionError(err) ||
		isRetryableTransactionRejectionError(err) {
		return false
	}
	var responseErr rpc.Error
	if !errors.As(err, &responseErr) {
		return false
	}
	switch responseErr.ErrorCode() {
	case 3, 4, // execution rejection
		800, 801, 803, 804, 805, 807, 808, // gas/envelope validation
		1001,                           // invalid sender/signature
		-38013, -38015, -38024, -38025, // go-ethereum validation errors
		-32602: // invalid transaction/request parameters
		return true
	}
	// Every other code, including the overloaded -32603/-32000/-32002/-32003/
	// -32005 family that Geth and hosted providers reuse for unrelated backend
	// failures, is decided by the semantic taxonomy below. An unknown message
	// stays ambiguous. There is deliberately no allowlist gating that check: a
	// nonstandard provider code with an unmistakably permanent message must not
	// be forced back into an ambiguous retry loop.
	return containsAny(err, permanentRejectionMessages...)
}

func (m *txManager) rescheduleTransaction(tx txmngr_provider.Transaction, cause error) {
	current, err := m.transactionsQ.New().FilterByIds(tx.ID).Get()
	if err != nil {
		log.Error().Err(err).Str("txId", tx.ID).Msg("failed to reload transaction before reschedule")
		return
	}
	if current.IsCompleted() {
		return
	}
	tx = *current
	tx.Msg = transactionMessageTail(cause.Error())
	tx.NextActionAt = time.Now().Add(m.blockTime(tx.ChainID))
	tx.UpdatedAt = time.Now()
	if tx.Tx != nil || tx.AssignedNonce != nil {
		tx.Status = txmngr_provider.ConfirmingTxStatus
	}
	if err := m.transactionsQ.UpdateIfActive(tx); err != nil && !errors.Is(err, txmngr_provider.ErrStaleTransaction) {
		log.Error().Err(err).Str("txId", tx.ID).Msg("failed to reschedule transaction")
	}
}

// nextBumpAt schedules the next fee replacement relative to when the newest
// candidate was signed. Rebroadcasting that same candidate does not defer it:
// an unconfirmed transaction is a reason to bump sooner, not later.
func (m *txManager) nextBumpAt(chainID uint64, latest txmngr_provider.TransactionAttempt) time.Time {
	return latest.CreatedAt.Add(m.bumpInterval(chainID))
}

func (m *txManager) blockTime(chainID uint64) time.Duration {
	duration := m.blockTimeMap[chainID]
	if duration <= 0 {
		return time.Second
	}
	return duration
}

func (m *txManager) bumpInterval(chainID uint64) time.Duration {
	duration := m.bumpIntervalMap[chainID]
	if duration <= 0 {
		return 3 * m.blockTime(chainID)
	}
	return duration
}

func (m *txManager) deliverStatusNotifications(ctx context.Context) {
	txs, err := m.transactionsQ.New().
		FilterNotificationReadyBefore(time.Now()).
		Limit(notificationWorkBatch).
		OrderByNextNotification().
		Select()
	if err != nil {
		log.Error().Err(err).Msg("failed to load transaction notifications")
		return
	}

	var workers errgroup.Group
	workers.SetLimit(maxNotificationWorkers)
	for _, tx := range txs {
		if ctx.Err() != nil {
			break
		}
		workers.Go(func() error {
			m.deliverStatusNotification(ctx, tx)
			return nil
		})
	}
	_ = workers.Wait()
}

func (m *txManager) deliverStatusNotification(ctx context.Context, tx txmngr_provider.Transaction) {
	notifyCtx, cancel := context.WithTimeout(ctx, notificationTimeout)
	err := m.callStatusCallback(notifyCtx, tx)
	cancel()
	if ctx.Err() != nil {
		return
	}
	now := time.Now()
	if err == nil {
		tx.NotifiedAt = &now
		tx.NotificationError = ""
	} else {
		tx.NotificationAttempts++
		tx.NotificationError = err.Error()
		tx.NextNotificationAt = now.Add(notificationBackoff(tx.NotificationAttempts))
	}
	tx.UpdatedAt = now
	if updateErr := m.transactionsQ.UpdateNotification(tx); updateErr != nil &&
		!errors.Is(updateErr, txmngr_provider.ErrStaleTransaction) {
		log.Error().Err(updateErr).Str("txId", tx.ID).Msg("failed to persist notification result")
	}
}

func (m *txManager) callStatusCallback(ctx context.Context, tx txmngr_provider.Transaction) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("transaction status callback panicked: %v", recovered)
		}
	}()
	return m.onStatusChange(ctx, tx)
}

func notificationBackoff(attempt uint32) time.Duration {
	return min(notificationMinBackoff*time.Duration(1<<min(attempt, 6)), notificationMaxBackoff)
}

func transactionPayloadHash(
	chainID uint64,
	address common.Address,
	data []byte,
	gasLimit uint64,
	value *big.Int,
	authorizations []types.SetCodeAuthorization,
) (string, error) {
	type payload struct {
		ChainID        uint64                       `json:"chain_id"`
		Address        string                       `json:"address"`
		Data           []byte                       `json:"data"`
		GasLimit       uint64                       `json:"gas_limit"`
		Value          string                       `json:"value"`
		Authorizations []types.SetCodeAuthorization `json:"authorizations,omitempty"`
	}
	encoded, err := json.Marshal(payload{
		ChainID:        chainID,
		Address:        address.Hex(),
		Data:           data,
		GasLimit:       gasLimit,
		Value:          value.String(),
		Authorizations: authorizations,
	})
	if err != nil {
		return "", fmt.Errorf("marshalling canonical payload: %w", err)
	}
	return crypto.Keccak256Hash(encoded).Hex(), nil
}

/* -------------------------------------------------------------------------- */
/*                          Helpers to get gas price                          */
/* -------------------------------------------------------------------------- */

// GasFee holds the suggested fee parameters for a transaction.
type GasFee struct {
	// Legacy
	GasPrice *big.Int

	// EIP-1559
	GasTipCap *big.Int // Corresponds to maxPriorityFeePerGas
	GasFeeCap *big.Int // Corresponds to maxFeePerGas
}

// suggestGasPrice calculates the gas price for the tx based on the current gas price and the suggested gas price
func (m *txManager) suggestGasPrice(ctx context.Context, chainId uint64, currentGasPrice *GasFee, coef int64) (*GasFee, error) {
	client, clientOk := m.clients[chainId]
	if !clientOk {
		return nil, fmt.Errorf("no client found for chainId: %d", chainId)
	}
	suggestedGasPrice, err := client.SuggestGasPrice(ctx)
	if err != nil {
		return nil, redactedRPCError(fmt.Sprintf("suggesting gas price for chain %d", chainId), err)
	}
	gasPriceBase := suggestedGasPrice
	if currentGasPrice != nil && currentGasPrice.GasPrice != nil && currentGasPrice.GasPrice.Cmp(gasPriceBase) > 0 {
		gasPriceBase = currentGasPrice.GasPrice
	}

	return &GasFee{
		GasPrice: bumpGasPrice(gasPriceBase, coef),
	}, nil
}

// suggestEIP1559GasFee suggests the gas fee parameters for an EIP-1559 transaction.
// It fetches the suggested gas tip cap and the latest block's base fee to calculate
// the appropriate GasTipCap and GasFeeCap.
func (m *txManager) suggestEIP1559GasFee(ctx context.Context, chainId uint64, currentGasPrice *GasFee, coef int64) (*GasFee, error) {
	client, clientOk := m.clients[chainId]
	if !clientOk {
		return nil, fmt.Errorf("no client found for chainId: %d", chainId)
	}

	header, err := client.HeaderByNumber(ctx, nil) // nil for the latest block
	if err != nil {
		return nil, redactedRPCError(fmt.Sprintf("getting latest block header for chain %d", chainId), err)
	}
	if header == nil || header.BaseFee == nil {
		return nil, fmt.Errorf("client returned a block header without a base fee, EIP-1559 may not be supported")
	}
	baseFee := header.BaseFee

	suggestedGasTipCap, err := client.SuggestGasTipCap(ctx)
	if err != nil {
		return nil, redactedRPCError(fmt.Sprintf("suggesting gas tip cap for chain %d", chainId), err)
	}
	gasTipCapBase := suggestedGasTipCap
	if currentGasPrice != nil && currentGasPrice.GasTipCap != nil && currentGasPrice.GasTipCap.Cmp(gasTipCapBase) > 0 {
		gasTipCapBase = currentGasPrice.GasTipCap
	}
	gasTipCapToUse := bumpGasPrice(gasTipCapBase, coef)

	// GasFeeCap = 2*BaseFee + GasTipCap, so the tx survives several base-fee rises.
	baseFeeBuffer := new(big.Int).Mul(baseFee, big.NewInt(2))
	gasFeeCap := new(big.Int).Add(baseFeeBuffer, gasTipCapToUse)
	if currentGasPrice != nil && currentGasPrice.GasFeeCap != nil {
		bumpedPreviousFeeCap := bumpGasPrice(currentGasPrice.GasFeeCap, coef)
		if bumpedPreviousFeeCap.Cmp(gasFeeCap) > 0 {
			gasFeeCap = bumpedPreviousFeeCap
		}
	}

	return &GasFee{
		GasTipCap: gasTipCapToUse,
		GasFeeCap: gasFeeCap,
	}, nil
}

// bumpGasPrice raises gasPrice by coef percent, and always by at least one wei
// so a replacement is never signed at the fee it is meant to outbid.
func bumpGasPrice(gasPrice *big.Int, coef int64) *big.Int {
	if gasPrice == nil {
		return big.NewInt(0)
	}
	hundred := big.NewInt(100) //nolint:mnd
	newGasPrice := new(big.Int).Mul(gasPrice, big.NewInt(100+coef))
	newGasPrice.Div(newGasPrice, hundred)
	if gasPrice.Sign() > 0 && newGasPrice.Cmp(gasPrice) <= 0 {
		newGasPrice.Add(gasPrice, big.NewInt(1))
	}

	return newGasPrice
}
