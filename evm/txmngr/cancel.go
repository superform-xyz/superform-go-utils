package txmngr

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	"github.com/pkg/errors"
	"github.com/rs/zerolog/log"

	"github.com/superform-xyz/superform-go-utils/evm/txmngr/txmngr_provider"
)

const (
	replacementPremiumPercent int64 = 10
	maxCancelSignatures             = 3
)

const (
	CancellationReasonMined        = "nonce consumed by same-nonce cancellation"
	CancellationReasonPending      = "same-nonce cancellation pending"
	CancellationReasonFunding      = "sender cannot fund same-nonce cancellation"
	CancellationReasonFeeLimit     = "cancellation price exceeds the spend limit"
	CancellationReasonSignatureCap = "same-nonce cancellation reached its signature limit"
	CancellationReasonUnsupported  = "transaction type does not support same-nonce cancellation"
)

// CancellationPolicy opts one chain into same-nonce cancellation.
// A zero PendingAge uses max(bump interval, exhausted poll), measured from the newest payload signature.
type CancellationPolicy struct {
	PendingAge time.Duration
}

type LaneRecoveryStatus string

const (
	LaneRecoveryBlocked          LaneRecoveryStatus = "blocked"
	LaneRecoveryCancelling       LaneRecoveryStatus = "cancelling"
	LaneRecoveryFunding          LaneRecoveryStatus = "funding"
	LaneRecoveryFeeLimited       LaneRecoveryStatus = "fee_limited"
	LaneRecoverySignatureLimited LaneRecoveryStatus = "signature_limited"
	LaneRecoveryUnsupported      LaneRecoveryStatus = "unsupported"
	LaneRecoveryCancelled        LaneRecoveryStatus = "cancelled"
	LaneRecoveryIncluded         LaneRecoveryStatus = "included"
	LaneRecoveryConsumed         LaneRecoveryStatus = "consumed"
)

// LaneRecoveryEvent amounts are wei. Nil means that figure was not read.
// Nonce is the assigned account nonce. A nil nonce is not a lane head.
type LaneRecoveryEvent struct {
	ChainID          uint64
	Nonce            *uint64
	Status           LaneRecoveryStatus
	BlockedAge       time.Duration
	PayloadAttempts  int
	FundingRequired  *big.Int
	FundingAvailable *big.Int
}

// WithCancellationPolicy enables cancellation for the listed chains.
// Chains absent from the map keep rebroadcasting the latest payload hash.
func WithCancellationPolicy(chains map[uint64]CancellationPolicy) Option {
	return func(m *txManager) {
		if len(chains) == 0 {
			return
		}
		copied := make(map[uint64]CancellationPolicy, len(chains))
		for chainID, policy := range chains {
			if policy.PendingAge < 0 {
				policy.PendingAge = 0
			}
			copied[chainID] = policy
		}
		m.cancellation = copied
	}
}

// WithLaneRecoveryObserver is called under the lane lock.
// It must not block or re-enter the manager.
func WithLaneRecoveryObserver(observe func(LaneRecoveryEvent)) Option {
	return func(m *txManager) {
		if observe != nil {
			m.laneRecovery = observe
		}
	}
}

func FundingShortfall(message string) (required, available *big.Int, ok bool) {
	message = recoveryPrimary(message)
	if !strings.HasPrefix(message, CancellationReasonFunding) {
		return nil, nil, false
	}
	required, available = parseWeiPair(message, "required_wei=", "available_wei=")
	if required == nil || available == nil {
		return nil, nil, false
	}
	return required, available, true
}

func (m *txManager) cancellationAge(chainID uint64) (time.Duration, bool) {
	if m.cancellation == nil {
		return 0, false
	}
	policy, ok := m.cancellation[chainID]
	if !ok {
		return 0, false
	}
	if policy.PendingAge > 0 {
		return policy.PendingAge, true
	}
	age := m.bumpInterval(chainID)
	if age < exhaustedPollInterval {
		age = exhaustedPollInterval
	}
	return age, true
}

// payloadNeedsCancellation reports an exhausted payload that still needs a cancel.
func (m *txManager) payloadNeedsCancellation(
	tx txmngr_provider.Transaction,
	attempts []txmngr_provider.TransactionAttempt,
) bool {
	if len(attempts) < maxFeeReplacements || plainSelfTransfer(tx, attempts) {
		return false
	}
	if _, enabled := m.cancellationAge(tx.ChainID); !enabled {
		return false
	}
	latest := attempts[len(attempts)-1].Tx
	return !isCancellationTransaction(
		latest, tx.Sender, tx.Address, tx.Data, tx.Value, logicalHasAuthorization(tx, attempts),
	)
}

func (m *txManager) recoverExhaustedNonce(
	ctx context.Context,
	tx txmngr_provider.Transaction,
	attempts []txmngr_provider.TransactionAttempt,
	lane *nonceLane,
) error {
	if plainSelfTransfer(tx, attempts) {
		return m.broadcastAndSchedule(ctx, tx, attempts, broadcastPolicy{
			nonceReleasable: lane.isTip(*tx.AssignedNonce),
		})
	}
	hasAuth := logicalHasAuthorization(tx, attempts)
	payload, cancels := splitAttempts(attempts, tx.Sender, tx.Address, tx.Data, tx.Value, hasAuth)
	if len(payload) == 0 || payload[len(payload)-1].Tx == nil {
		return m.broadcastAndSchedule(ctx, tx, attempts, broadcastPolicy{
			nonceReleasable: lane.isTip(*tx.AssignedNonce),
		})
	}
	if !supportsCancellation(payload[len(payload)-1].Tx.Type()) {
		tx.Msg = CancellationReasonUnsupported
		m.emitRecovery(tx, payload, LaneRecoveryUnsupported, nil, nil)
		return m.broadcastAndSchedule(ctx, tx, attempts, broadcastPolicy{
			nonceReleasable: lane.isTip(*tx.AssignedNonce),
		})
	}

	quote, err := m.readInclusionQuote(ctx, tx.ChainID, payload[len(payload)-1].Tx.Type())
	if err != nil {
		return err
	}
	latestCancel := (*types.Transaction)(nil)
	if len(cancels) > 0 {
		latestCancel = cancels[len(cancels)-1].Tx
	}
	reprice := latestCancel != nil && (cancelUnderpriced(latestCancel, quote) || feeRejectionMessage(tx.Msg))
	priorMsg := tx.Msg
	rejectedReplacement := replacementUnderpricedMessage(priorMsg)
	if latestCancel != nil && !reprice {
		if insufficientFundsMessage(tx.Msg) || strings.HasPrefix(recoveryPrimary(tx.Msg), CancellationReasonFunding) {
			if err = m.readCancelFunds(ctx, tx.ChainID, payload[len(payload)-1].Tx.Type(), &quote); err != nil {
				return err
			}
			if tx.AssignedNonce == nil {
				return errors.New("cannot price cancellation funding without an assigned nonce")
			}
			others, siblingErr := m.siblingNonceCost(tx.ChainID, *tx.AssignedNonce)
			if siblingErr != nil {
				return siblingErr
			}
			short := fundedShortfall(latestCancel.Cost(), others, quote.balance)
			if short != nil {
				tx.Msg = fundingMessage(short, quote.balance)
				m.emitRecovery(tx, payload, LaneRecoveryFunding, short, quote.balance)
				return m.holdRecovery(tx, attempts)
			}
			tx.Msg = ""
		}
		m.emitRecovery(tx, payload, LaneRecoveryCancelling, nil, nil)
		return m.broadcastAndSchedule(ctx, tx, attempts, broadcastPolicy{
			nonceReleasable: lane.isTip(*tx.AssignedNonce),
		})
	}
	if err = m.readCancelFunds(ctx, tx.ChainID, payload[len(payload)-1].Tx.Type(), &quote); err != nil {
		return err
	}
	age, _ := m.cancellationAge(tx.ChainID)
	blocked := payloadAge(payload, time.Now())
	payloadCost := upfrontWei(payload)
	triggered := cancellationTriggered(payload, quote, age, blocked, payloadCost)
	if latestCancel == nil && !triggered {
		if preservedRecoveryMessage(tx.Msg) != "" {
			tx.Msg = ""
		}
		m.emitRecovery(tx, payload, LaneRecoveryBlocked, nil, quote.balance)
		return m.broadcastAndSchedule(ctx, tx, attempts, broadcastPolicy{
			nonceReleasable: lane.isTip(*tx.AssignedNonce),
		})
	}
	if len(cancels) >= maxCancelSignatures {
		tx.Msg = CancellationReasonSignatureCap
		m.emitRecovery(tx, payload, LaneRecoverySignatureLimited, nil, quote.balance)
		return m.broadcastAndSchedule(ctx, tx, attempts, broadcastPolicy{
			nonceReleasable: lane.isTip(*tx.AssignedNonce),
		})
	}

	// Reprice against the cancel in the pool, not the original payload.
	previous := payload[len(payload)-1].Tx
	if latestCancel != nil {
		previous = latestCancel
	}
	if tx.AssignedNonce == nil {
		return errors.New("cannot price cancellation funding without an assigned nonce")
	}
	others, siblingErr := m.siblingNonceCost(tx.ChainID, *tx.AssignedNonce)
	if siblingErr != nil {
		return siblingErr
	}
	signed, pricing, err := m.persistCancellation(ctx, &tx, payload, previous, quote, payloadCost, others)
	if err != nil {
		return err
	}
	if signed == nil {
		if latestCancel != nil && tx.AssignedNonce != nil {
			if short := fundedShortfall(latestCancel.Cost(), others, quote.balance); short != nil {
				tx.Msg = fundingMessage(short, quote.balance)
				m.emitRecovery(tx, payload, LaneRecoveryFunding, short, quote.balance)
				return m.holdRecovery(tx, attempts)
			}
		} else if strings.HasPrefix(tx.Msg, CancellationReasonFunding) {
			return m.holdRecovery(tx, attempts)
		}
		if rejectedReplacement {
			tx.Msg = priorMsg
		}
		sent := attempts[len(attempts)-1].Tx
		policy := broadcastPolicy{nonceReleasable: lane.isTip(*tx.AssignedNonce)}
		if rejectedReplacement && len(attempts) > 1 {
			sent = attempts[len(attempts)-2].Tx
			policy.notDurableLatest = true
		}
		if err = m.clients[tx.ChainID].SendTransaction(ctx, sent); err != nil {
			return m.handleBroadcastFailure(ctx, tx, attempts, err, policy)
		}
		tx.Msg = preservedRecoveryMessage(tx.Msg)
		tx.BroadcastPending = false
		tx.RejectedAttemptHash = nil
		return m.scheduleNextCheck(tx, attempts)
	}
	attempt := txmngr_provider.TransactionAttempt{
		TransactionID: tx.ID,
		Hash:          signed.Hash(),
		Nonce:         signed.Nonce(),
		Tx:            signed,
		CreatedAt:     time.Now(),
	}
	stored, loadErr := m.transactionsQ.SelectAttempts(tx.ID)
	if loadErr != nil {
		return fmt.Errorf("reloading cancellation attempts: %w", loadErr)
	}
	attempts = stored
	tx.Tx = signed
	tx.Status = txmngr_provider.ConfirmingTxStatus
	tx.BroadcastPending = true
	tx.RejectedAttemptHash = nil
	tx.Msg = ""
	tx.UpdatedAt = attempt.CreatedAt
	if err = m.transactionsQ.UpdateIfActive(tx); err != nil {
		return fmt.Errorf("persisting cancellation candidate: %w", err)
	}
	cancelCost := new(big.Int).Mul(new(big.Int).SetUint64(pricing.gas), pricing.price)
	log.Warn().Str("txId", tx.ID).Uint64("chainId", tx.ChainID).
		Uint64("nonce", signed.Nonce()).Str("hash", signed.Hash().Hex()).
		Uint64("gas", pricing.gas).Str("fee_wei", pricing.price.String()).
		Msg("signed same-nonce cancellation")
	m.emitRecovery(tx, payload, LaneRecoveryCancelling, cancelCost, quote.balance)
	return m.broadcastAndSchedule(ctx, tx, attempts, broadcastPolicy{
		nonceReleasable: lane.isTip(*tx.AssignedNonce),
	})
}

// persistCancellation prices, gates, then signs. A nil transaction and nil error
// means a gate refused the signature.
func (m *txManager) persistCancellation(
	ctx context.Context,
	tx *txmngr_provider.Transaction,
	payload []txmngr_provider.TransactionAttempt,
	previous *types.Transaction,
	quote cancelQuote,
	payloadCost, others *big.Int,
) (*types.Transaction, cancelPricing, error) {
	var (
		floor        *GasFee
		previousHash common.Hash
		pricing      cancelPricing
	)
	if tx.AssignedNonce == nil {
		return nil, pricing, errors.New("cannot price cancellation funding without an assigned nonce")
	}
	for collision := range maxHashCollisionResigns {
		var priceErr error
		pricing, priceErr = priceCancellation(previous, quote, floor)
		if priceErr != nil {
			return nil, pricing, priceErr
		}
		cancelCost := new(big.Int).Mul(new(big.Int).SetUint64(pricing.gas), pricing.price)
		if payloadCost == nil || cancelCost.Cmp(payloadCost) > 0 {
			tx.Msg = CancellationReasonFeeLimit
			m.emitRecovery(*tx, payload, LaneRecoveryFeeLimited, cancelCost, quote.balance)
			return nil, pricing, nil
		}
		short := fundedShortfall(cancelCost, others, quote.balance)
		if short != nil {
			if !strings.HasPrefix(tx.Msg, CancellationReasonFunding) {
				log.Warn().Str("txId", tx.ID).Uint64("chainId", tx.ChainID).
					Str("required_wei", short.String()).
					Str("available_wei", intString(quote.balance)).
					Msg("sender cannot fund same-nonce cancellation")
			}
			tx.Msg = fundingMessage(short, quote.balance)
			m.emitRecovery(*tx, payload, LaneRecoveryFunding, short, quote.balance)
			return nil, pricing, nil
		}
		signed, signErr := m.signCancellation(ctx, *tx, pricing)
		if signErr != nil {
			return nil, pricing, signErr
		}
		if validateErr := validateSignedCancellation(*tx, signed); validateErr != nil {
			return nil, pricing, validateErr
		}
		attempt := txmngr_provider.TransactionAttempt{
			TransactionID: tx.ID,
			Hash:          signed.Hash(),
			Nonce:         signed.Nonce(),
			Tx:            signed,
			CreatedAt:     time.Now(),
		}
		insertErr := m.transactionsQ.InsertAttempt(attempt)
		// Same-owner insert of this hash is idempotent, so it is not a new replacement.
		if insertErr == nil && previous != nil && signed.Hash() == previous.Hash() {
			insertErr = txmngr_provider.ErrAttemptHashConflict
		}
		if insertErr == nil {
			return signed, pricing, nil
		}
		if !errors.Is(insertErr, txmngr_provider.ErrAttemptHashConflict) {
			return nil, pricing, fmt.Errorf("persisting same-nonce cancellation: %w", insertErr)
		}
		if collision > 0 && attempt.Hash == previousHash {
			return nil, pricing, fmt.Errorf(
				"re-signing cancellation did not change hash %s: %w",
				attempt.Hash.Hex(), insertErr)
		}
		previousHash = attempt.Hash
		laneAttempts, selectErr := m.transactionsQ.SelectAttemptsByNonce(ctx, tx.ChainID, signed.Nonce())
		if selectErr != nil {
			return nil, pricing, fmt.Errorf("loading durable nonce attempt history: %w", selectErr)
		}
		var floorErr error
		floor, floorErr = highestAttemptFee(laneAttempts, m.sender, tx.ChainID, signed.Type())
		if floorErr != nil {
			return nil, pricing, fmt.Errorf("deriving cancellation fee floor: %w", floorErr)
		}
		if floor == nil {
			return nil, pricing, fmt.Errorf("%w: conflicting hash %s is absent from durable lane history",
				txmngr_provider.ErrAttemptHashConflict, attempt.Hash.Hex())
		}
	}
	return nil, pricing, fmt.Errorf("persisting a unique cancellation after %d collisions: %w",
		maxHashCollisionResigns, txmngr_provider.ErrAttemptHashConflict)
}

func (m *txManager) holdRecovery(
	tx txmngr_provider.Transaction,
	attempts []txmngr_provider.TransactionAttempt,
) error {
	tx.BroadcastPending = false
	tx.RejectedAttemptHash = nil
	return m.scheduleNextCheck(tx, attempts)
}

func (m *txManager) finalizeCancellation(
	tx txmngr_provider.Transaction,
	winner *types.Transaction,
	receipt *types.Receipt,
	attempts []txmngr_provider.TransactionAttempt,
) error {
	log.Warn().Str("txId", tx.ID).Uint64("chainId", tx.ChainID).
		Str("hash", winner.Hash().Hex()).Uint64("nonce", winner.Nonce()).
		Msg("same-nonce cancellation mined")
	tx.Tx = winner
	tx.Receipt = receipt
	tx.RejectedAttemptHash = nil
	tx.Status = txmngr_provider.ErroredTxStatus
	tx.Msg = CancellationReasonMined
	if m.laneRecovery != nil {
		payload, _ := splitAttempts(attempts, tx.Sender, tx.Address, tx.Data, tx.Value, logicalHasAuthorization(tx, attempts))
		m.emitRecovery(tx, payload, LaneRecoveryCancelled, nil, nil)
	}
	return m.persistTerminal(tx)
}

func (m *txManager) observeIncludedPayload(
	tx txmngr_provider.Transaction,
	attempts []txmngr_provider.TransactionAttempt,
) {
	if _, enabled := m.cancellationAge(tx.ChainID); !enabled || m.laneRecovery == nil {
		return
	}
	payload, cancels := splitAttempts(attempts, tx.Sender, tx.Address, tx.Data, tx.Value, logicalHasAuthorization(tx, attempts))
	if len(cancels) == 0 && len(payload) < maxFeeReplacements {
		return
	}
	m.emitRecovery(tx, payload, LaneRecoveryIncluded, nil, nil)
}

// releaseConsumedNonce publishes the terminal status for an assigned nonce that
// left the active set without a receipt. Cancelled and included do not apply:
// neither our cancellation nor the payload was mined. Without this event the
// chain gauge keeps the last non-terminal head and drops every higher nonce.
func (m *txManager) releaseConsumedNonce(
	tx txmngr_provider.Transaction,
	attempts []txmngr_provider.TransactionAttempt,
) {
	if m.laneRecovery == nil || tx.AssignedNonce == nil {
		return
	}
	if _, enabled := m.cancellationAge(tx.ChainID); !enabled {
		return
	}
	hasAuth := logicalHasAuthorization(tx, attempts)
	payload, cancels := splitAttempts(attempts, tx.Sender, tx.Address, tx.Data, tx.Value, hasAuth)
	if len(cancels) == 0 && (len(payload) < maxFeeReplacements || plainSelfTransfer(tx, attempts)) {
		return
	}
	m.emitRecovery(tx, payload, LaneRecoveryConsumed, nil, nil)
}

type cancelQuote struct {
	suggested *big.Int
	tip       *big.Int
	baseFee   *big.Int
	balance   *big.Int
}

// readInclusionQuote loads the header. Legacy quotes also load eth_gasPrice.
func (m *txManager) readInclusionQuote(ctx context.Context, chainID uint64, txType byte) (cancelQuote, error) {
	var quote cancelQuote
	client, ok := m.clients[chainID]
	if !ok {
		return quote, fmt.Errorf("no client found for chainId: %d", chainID)
	}
	header, err := client.HeaderByNumber(ctx, nil)
	if err != nil {
		return quote, fmt.Errorf("pricing same-nonce cancellation: %w", redactedRPCError("reading header", err))
	}
	if header != nil {
		quote.baseFee = cloneBigInt(header.BaseFee)
	}
	if dynamicFeeType(txType) {
		if quote.baseFee == nil {
			return quote, errors.New("block header is missing a base fee")
		}
		return quote, nil
	}
	suggested, err := client.SuggestGasPrice(ctx)
	if err != nil {
		return quote, fmt.Errorf("pricing same-nonce cancellation: %w", redactedRPCError("suggesting gas price", err))
	}
	if suggested == nil || suggested.Sign() < 0 {
		return quote, errors.New("suggested gas price is missing")
	}
	quote.suggested = suggested
	return quote, nil
}

// readCancelFunds loads the tip and balance for a new signature.
func (m *txManager) readCancelFunds(ctx context.Context, chainID uint64, txType byte, quote *cancelQuote) error {
	if quote == nil {
		return errors.New("cancellation quote is missing")
	}
	client, ok := m.clients[chainID]
	if !ok {
		return fmt.Errorf("no client found for chainId: %d", chainID)
	}
	if dynamicFeeType(txType) {
		tip, tipErr := client.SuggestGasTipCap(ctx)
		if tipErr != nil {
			return fmt.Errorf("pricing same-nonce cancellation: %w", redactedRPCError("suggesting gas tip", tipErr))
		}
		if tip == nil || tip.Sign() < 0 {
			return errors.New("suggested gas tip is missing")
		}
		quote.tip = tip
	}
	balance, err := client.BalanceAt(ctx, m.sender, nil)
	if err != nil {
		return fmt.Errorf("pricing same-nonce cancellation: %w", redactedRPCError("reading sender balance", err))
	}
	if balance == nil || balance.Sign() < 0 {
		return errors.New("sender balance is missing")
	}
	quote.balance = balance
	return nil
}

type cancelPricing struct {
	gas     uint64
	price   *big.Int
	tip     *big.Int
	dynamic bool
}

func priceCancellation(previous *types.Transaction, quote cancelQuote, floor *GasFee) (cancelPricing, error) {
	if previous == nil {
		return cancelPricing{}, errors.New("cannot price a cancellation without a prior transaction")
	}
	switch previous.Type() {
	case types.DynamicFeeTxType, types.SetCodeTxType:
		tip, feeCap := dynamicCancelFee(previous, quote)
		if floor != nil {
			if floor.GasTipCap != nil && floor.GasTipCap.Cmp(tip) >= 0 {
				tip = bumpGasPrice(floor.GasTipCap, replacementPremiumPercent)
			}
			if floor.GasFeeCap != nil && floor.GasFeeCap.Cmp(feeCap) >= 0 {
				feeCap = bumpGasPrice(floor.GasFeeCap, replacementPremiumPercent)
			}
		}
		// Both tip and fee cap must be strictly greater than the pooled transaction.
		tip = strictlyAbove(previous.GasTipCap(), tip)
		feeCap = strictlyAbove(previous.GasFeeCap(), feeCap)
		if tip.Cmp(feeCap) > 0 {
			feeCap = new(big.Int).Set(tip)
		}
		// Type 2, no authorization list: a copied list would still install the delegation.
		return cancelPricing{gas: params.TxGas, price: feeCap, tip: tip, dynamic: true}, nil
	case types.LegacyTxType:
		price := legacyCancelPrice(previous, quote)
		if floor != nil && floor.GasPrice != nil && floor.GasPrice.Cmp(price) >= 0 {
			price = bumpGasPrice(floor.GasPrice, replacementPremiumPercent)
		}
		price = strictlyAbove(previous.GasPrice(), price)
		return cancelPricing{gas: params.TxGas, price: price}, nil
	default:
		return cancelPricing{}, fmt.Errorf("transaction type %d cannot be cancelled", previous.Type())
	}
}

func (m *txManager) signCancellation(
	ctx context.Context,
	tx txmngr_provider.Transaction,
	pricing cancelPricing,
) (*types.Transaction, error) {
	if tx.AssignedNonce == nil {
		return nil, errors.New("cannot cancel a transaction without an assigned nonce")
	}
	auth, err := m.getTransactOpts(tx.ChainID)
	if err != nil {
		return nil, fmt.Errorf("loading transact options: %w", err)
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if auth == nil || auth.Signer == nil {
		return nil, errors.New("transact options are nil")
	}
	if auth.From != m.sender {
		return nil, fmt.Errorf("transact signer %s differs from nonce-lane sender %s", auth.From.Hex(), m.sender.Hex())
	}
	if pricing.dynamic {
		signed, signErr := auth.Signer(auth.From, types.NewTx(&types.DynamicFeeTx{
			ChainID:   new(big.Int).SetUint64(tx.ChainID),
			Nonce:     *tx.AssignedNonce,
			GasTipCap: pricing.tip,
			GasFeeCap: pricing.price,
			Gas:       pricing.gas,
			To:        &m.sender,
			Value:     new(big.Int),
		}))
		if signErr != nil {
			return nil, fmt.Errorf("signing dynamic-fee cancellation: %w", signErr)
		}
		return signed, nil
	}
	signed, signErr := auth.Signer(auth.From, types.NewTx(&types.LegacyTx{
		Nonce:    *tx.AssignedNonce,
		GasPrice: pricing.price,
		Gas:      pricing.gas,
		To:       &m.sender,
		Value:    new(big.Int),
	}))
	if signErr != nil {
		return nil, fmt.Errorf("signing legacy cancellation: %w", signErr)
	}
	return signed, nil
}

// validateSignedCancellation rejects a signed body that is not the priced self-transfer.
func validateSignedCancellation(tx txmngr_provider.Transaction, signed *types.Transaction) error {
	if signed == nil {
		return errors.New("signed cancellation is missing")
	}
	if signed.Type() != types.LegacyTxType && signed.Type() != types.DynamicFeeTxType {
		return fmt.Errorf("signed cancellation has type %d", signed.Type())
	}
	if len(signed.SetCodeAuthorizations()) > 0 || len(signed.Data()) != 0 {
		return errors.New("signed cancellation carries a payload")
	}
	if signed.To() == nil || *signed.To() != tx.Sender {
		return errors.New("signed cancellation destination is not the sender")
	}
	if signed.Value() != nil && signed.Value().Sign() != 0 {
		return errors.New("signed cancellation transfers value")
	}
	if tx.AssignedNonce == nil || signed.Nonce() != *tx.AssignedNonce {
		return errors.New("signed cancellation nonce differs from the assigned nonce")
	}
	chainID := signed.ChainId()
	if chainID == nil || chainID.Uint64() != tx.ChainID {
		return errors.New("signed cancellation chain id differs")
	}
	from, err := types.Sender(types.LatestSignerForChainID(new(big.Int).SetUint64(tx.ChainID)), signed)
	if err != nil {
		return fmt.Errorf("recovering cancellation sender: %w", err)
	}
	if from != tx.Sender {
		return fmt.Errorf("signed cancellation sender %s differs from nonce-lane sender %s", from.Hex(), tx.Sender.Hex())
	}
	return nil
}

func (m *txManager) emitRecovery(
	tx txmngr_provider.Transaction,
	payload []txmngr_provider.TransactionAttempt,
	status LaneRecoveryStatus,
	required, available *big.Int,
) {
	age := laneBlockedAge(payload, time.Now())
	if age == 0 && !tx.CreatedAt.IsZero() && time.Now().After(tx.CreatedAt) {
		age = time.Since(tx.CreatedAt)
	}
	m.observeLaneRecovery(LaneRecoveryEvent{
		ChainID:          tx.ChainID,
		Nonce:            cloneNonce(tx.AssignedNonce),
		Status:           status,
		BlockedAge:       age,
		PayloadAttempts:  len(payload),
		FundingRequired:  required,
		FundingAvailable: available,
	})
}

func cloneNonce(nonce *uint64) *uint64 {
	if nonce == nil {
		return nil
	}
	copied := *nonce
	return &copied
}

func (m *txManager) observeLaneRecovery(event LaneRecoveryEvent) {
	if m.laneRecovery == nil {
		return
	}
	event.FundingRequired = cloneBigInt(event.FundingRequired)
	event.FundingAvailable = cloneBigInt(event.FundingAvailable)
	m.laneRecovery(event)
}

func cancellationTriggered(
	payload []txmngr_provider.TransactionAttempt,
	quote cancelQuote,
	pendingAge, blocked time.Duration,
	payloadCost *big.Int,
) bool {
	if pendingAge > 0 && blocked >= pendingAge {
		return true
	}
	latest := payload[len(payload)-1].Tx
	if marketExcludes(latest, quote) {
		return true
	}
	return payloadCost != nil && quote.balance != nil && quote.balance.Cmp(payloadCost) < 0
}

func marketExcludes(tx *types.Transaction, quote cancelQuote) bool {
	if tx == nil {
		return false
	}
	return feeExcluded(tx, quote)
}

func cancelUnderpriced(tx *types.Transaction, quote cancelQuote) bool {
	if tx == nil {
		return true
	}
	return feeExcluded(tx, quote)
}

// feeExcluded reports that the quote is above the transaction cap.
func feeExcluded(tx *types.Transaction, quote cancelQuote) bool {
	if dynamicFeeType(tx.Type()) {
		capFee := tx.GasFeeCap()
		return quote.baseFee != nil && capFee != nil && quote.baseFee.Cmp(capFee) > 0
	}
	price := tx.GasPrice()
	if price == nil {
		return true
	}
	if quote.baseFee != nil && quote.baseFee.Cmp(price) > 0 {
		return true
	}
	return quote.suggested != nil && quote.suggested.Cmp(price) > 0
}

// strictlyAbove returns candidate, or at least one wei above previous.
func strictlyAbove(previous, candidate *big.Int) *big.Int {
	if candidate == nil || candidate.Sign() < 0 {
		candidate = big.NewInt(0)
	} else {
		candidate = new(big.Int).Set(candidate)
	}
	if previous == nil || previous.Sign() < 0 {
		previous = big.NewInt(0)
	}
	if candidate.Cmp(previous) > 0 {
		return candidate
	}
	if previous.Sign() == 0 {
		return big.NewInt(1)
	}
	raised := bumpGasPrice(previous, replacementPremiumPercent)
	if raised.Cmp(previous) <= 0 {
		return new(big.Int).Add(previous, big.NewInt(1))
	}
	return raised
}

func legacyCancelPrice(previous *types.Transaction, quote cancelQuote) *big.Int {
	price := bumpGasPrice(previous.GasPrice(), replacementPremiumPercent)
	if quote.suggested != nil && quote.suggested.Cmp(price) > 0 {
		price = new(big.Int).Set(quote.suggested)
	}
	if quote.baseFee != nil && quote.baseFee.Cmp(price) > 0 {
		price = new(big.Int).Set(quote.baseFee)
	}
	return price
}

func dynamicCancelFee(previous *types.Transaction, quote cancelQuote) (*big.Int, *big.Int) {
	tip := bumpGasPrice(previous.GasTipCap(), replacementPremiumPercent)
	if quote.tip != nil && quote.tip.Cmp(tip) > 0 {
		tip = new(big.Int).Set(quote.tip)
	}
	feeCap := bumpGasPrice(previous.GasFeeCap(), replacementPremiumPercent)
	if quote.baseFee != nil {
		inclusion := new(big.Int).Add(new(big.Int).Set(quote.baseFee), tip)
		if inclusion.Cmp(feeCap) > 0 {
			feeCap = inclusion
		}
	}
	if tip.Cmp(feeCap) > 0 {
		feeCap = new(big.Int).Set(tip)
	}
	return tip, feeCap
}

func supportsCancellation(txType byte) bool {
	switch txType {
	case types.LegacyTxType, types.DynamicFeeTxType, types.SetCodeTxType:
		return true
	default:
		return false
	}
}

func dynamicFeeType(txType byte) bool {
	return txType == types.DynamicFeeTxType || txType == types.SetCodeTxType
}

func splitAttempts(
	attempts []txmngr_provider.TransactionAttempt,
	sender, logicalTo common.Address,
	logicalData []byte,
	logicalValue *big.Int,
	logicalHasAuth bool,
) (payload, cancels []txmngr_provider.TransactionAttempt) {
	for _, attempt := range attempts {
		if isCancellationTransaction(attempt.Tx, sender, logicalTo, logicalData, logicalValue, logicalHasAuth) {
			cancels = append(cancels, attempt)
			continue
		}
		payload = append(payload, attempt)
	}
	return payload, cancels
}

func logicalTransactionIsCancel(tx txmngr_provider.Transaction) bool {
	if len(tx.RawAuthorizations) > 0 {
		return false
	}
	if tx.Address != tx.Sender || len(tx.Data) != 0 {
		return false
	}
	return tx.Value == nil || tx.Value.Sign() == 0
}

// plainSelfTransfer is a zero-value empty self-call with no authorization list.
func plainSelfTransfer(tx txmngr_provider.Transaction, attempts []txmngr_provider.TransactionAttempt) bool {
	return logicalTransactionIsCancel(tx) && !attemptsCarryAuthorization(attempts)
}

func logicalHasAuthorization(tx txmngr_provider.Transaction, attempts []txmngr_provider.TransactionAttempt) bool {
	return len(tx.RawAuthorizations) > 0 || attemptsCarryAuthorization(attempts)
}

func attemptsCarryAuthorization(attempts []txmngr_provider.TransactionAttempt) bool {
	for _, attempt := range attempts {
		if attempt.Tx != nil && len(attempt.Tx.SetCodeAuthorizations()) > 0 {
			return true
		}
	}
	return false
}

func isCancellationTransaction(
	tx *types.Transaction,
	sender, logicalTo common.Address,
	logicalData []byte,
	logicalValue *big.Int,
	logicalHasAuth bool,
) bool {
	if tx == nil || tx.To() == nil || *tx.To() != sender {
		return false
	}
	if len(tx.Data()) != 0 || len(tx.SetCodeAuthorizations()) > 0 {
		return false
	}
	if tx.Value() != nil && tx.Value().Sign() != 0 {
		return false
	}
	logicalCancel := !logicalHasAuth && logicalTo == sender && len(logicalData) == 0 &&
		(logicalValue == nil || logicalValue.Sign() == 0)
	return !logicalCancel
}

func hasCancellationAttempt(attempts []txmngr_provider.TransactionAttempt, sender common.Address) bool {
	seenPayload := false
	seenCancel := false
	for _, attempt := range attempts {
		if attempt.Tx == nil || attempt.Tx.To() == nil {
			continue
		}
		self := *attempt.Tx.To() == sender && len(attempt.Tx.Data()) == 0 &&
			(attempt.Tx.Value() == nil || attempt.Tx.Value().Sign() == 0) &&
			len(attempt.Tx.SetCodeAuthorizations()) == 0
		if self {
			seenCancel = true
			continue
		}
		seenPayload = true
	}
	return seenPayload && seenCancel
}

// payloadAge is the newest payload signature. A fee bump restarts the pending-age wait.
func payloadAge(attempts []txmngr_provider.TransactionAttempt, now time.Time) time.Duration {
	if len(attempts) == 0 {
		return 0
	}
	return attemptAge(attempts[len(attempts)-1], now)
}

// laneBlockedAge is the oldest payload signature.
func laneBlockedAge(attempts []txmngr_provider.TransactionAttempt, now time.Time) time.Duration {
	if len(attempts) == 0 {
		return 0
	}
	return attemptAge(attempts[0], now)
}

func attemptAge(attempt txmngr_provider.TransactionAttempt, now time.Time) time.Duration {
	if attempt.CreatedAt.IsZero() || !now.After(attempt.CreatedAt) {
		return 0
	}
	return now.Sub(attempt.CreatedAt)
}

func upfrontWei(attempts []txmngr_provider.TransactionAttempt) *big.Int {
	var highest *big.Int
	for _, attempt := range attempts {
		if attempt.Tx == nil {
			continue
		}
		cost := attempt.Tx.Cost()
		if highest == nil || cost.Cmp(highest) > 0 {
			highest = cost
		}
	}
	return highest
}

// fundedShortfall returns the wei still required, or nil when the balance covers it.
func fundedShortfall(cancelCost, others, balance *big.Int) *big.Int {
	required := cancelCost
	if required == nil {
		required = big.NewInt(0)
	}
	if others != nil && others.Sign() > 0 {
		required = new(big.Int).Add(required, others)
	}
	if balance != nil && balance.Cmp(required) >= 0 {
		return nil
	}
	return required
}

func (m *txManager) siblingNonceCost(chainID, nonce uint64) (*big.Int, error) {
	rows, err := m.transactionsQ.New().
		FilterByChainIds(chainID).
		FilterBySenderOrUnset(m.sender).
		FilterByStatuses(txmngr_provider.ActiveStatuses()...).
		Select()
	if err != nil {
		return nil, fmt.Errorf("loading sibling nonce costs: %w", err)
	}
	total := big.NewInt(0)
	for _, row := range rows {
		attempts, attemptErr := m.transactionsQ.SelectAttempts(row.ID)
		if attemptErr != nil {
			return nil, fmt.Errorf("loading sibling nonce attempts %s: %w", row.ID, attemptErr)
		}
		priced := make([]*types.Transaction, 0, len(attempts)+1)
		for _, attempt := range attempts {
			if attempt.Tx != nil {
				priced = append(priced, attempt.Tx)
			}
		}
		if len(priced) == 0 {
			if row.Tx == nil {
				continue
			}
			priced = append(priced, row.Tx)
		}
		siblingNonce := priced[len(priced)-1].Nonce()
		if row.AssignedNonce != nil && *row.AssignedNonce != siblingNonce {
			return nil, fmt.Errorf("sibling %s assigned nonce %d differs from attempt nonce %d", row.ID, *row.AssignedNonce, siblingNonce)
		}
		if siblingNonce == nonce {
			continue
		}
		if row.Sender != m.sender {
			var (
				from      common.Address
				senderErr error
			)
			if len(attempts) > 0 && attempts[0].Tx != nil {
				from, _, senderErr = validatePersistedAttempts(attempts)
			} else {
				from, senderErr = types.Sender(types.LatestSignerForChainID(priced[0].ChainId()), priced[0])
			}
			if senderErr != nil {
				return nil, fmt.Errorf("reading sibling %s signer: %w", row.ID, senderErr)
			}
			if from != m.sender {
				continue
			}
		}
		var highest *big.Int
		for _, body := range priced {
			cost := body.Cost()
			if highest == nil || cost.Cmp(highest) > 0 {
				highest = cost
			}
		}
		if highest != nil {
			total.Add(total, highest)
		}
	}
	return total, nil
}

func recoveryPrimary(message string) string {
	if split := strings.Index(message, reconciliationSeparator); split >= 0 {
		return message[:split]
	}
	return message
}

func preservedRecoveryMessage(message string) string {
	primary := recoveryPrimary(message)
	switch {
	case primary == CancellationReasonFeeLimit,
		primary == CancellationReasonSignatureCap,
		primary == CancellationReasonUnsupported,
		replacementUnderpricedMessage(primary):
		return message
	case strings.HasPrefix(primary, CancellationReasonFunding):
		return message
	default:
		return ""
	}
}

func feeRejectionMessage(message string) bool {
	lower := strings.ToLower(recoveryPrimary(message))
	for _, fragment := range feePolicyRejectionMessages {
		if strings.Contains(lower, fragment) {
			return true
		}
	}
	return false
}

func replacementUnderpricedMessage(message string) bool {
	lower := strings.ToLower(recoveryPrimary(message))
	for _, fragment := range []string{
		"replacement transaction underpriced",
		"replacement underpriced",
		"transaction underpriced",
		"fee too low to replace",
	} {
		if strings.Contains(lower, fragment) {
			return true
		}
	}
	return false
}

func insufficientFundsMessage(message string) bool {
	return strings.Contains(strings.ToLower(recoveryPrimary(message)), "insufficient funds")
}

func fundingMessage(required, available *big.Int) string {
	return fmt.Sprintf("%s: required_wei=%s available_wei=%s",
		CancellationReasonFunding, intString(required), intString(available))
}

func intString(value *big.Int) string {
	if value == nil {
		return "0"
	}
	return value.String()
}

func parseWeiPair(message, requiredKey, availableKey string) (*big.Int, *big.Int) {
	required := weiAfter(message, requiredKey, " ")
	available := weiAfter(message, availableKey, ";")
	return required, available
}

func weiAfter(message, key, terminator string) *big.Int {
	index := strings.Index(message, key)
	if index < 0 {
		return nil
	}
	raw := message[index+len(key):]
	if terminator != "" {
		if end := strings.Index(raw, terminator); end >= 0 {
			raw = raw[:end]
		}
	}
	raw = strings.TrimSpace(raw)
	parsed, ok := new(big.Int).SetString(raw, 10)
	if !ok {
		return nil
	}
	return parsed
}
