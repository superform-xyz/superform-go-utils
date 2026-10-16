package txmngr

import (
	"context"
	"errors"
	"time"

	"github.com/superform-xyz/superform-go-utils/evm/txmngr/txmngr_provider"
)

// ErrUnsignedTransactionExpired may be returned by an admission hook to cancel
// an obsolete payload. The manager honors it only with no durable signatures
// and no later nonce reservation; signed or ambiguous work remains recoverable.
var ErrUnsignedTransactionExpired = errors.New("unsigned transaction expired")

// WithBeforeAttempt bounds signing and broadcasting attempts without disabling
// receipt reconciliation. hasSignedAttempts includes crash-recovered envelopes.
// A future retry time defers work without making the transaction terminal.
func WithBeforeAttempt(gate func(context.Context, txmngr_provider.Transaction, bool) (time.Time, error)) Option {
	return func(m *txManager) { m.beforeAttempt = gate }
}

// Observer keeps application metrics registries out of the reusable manager.
type Observer interface {
	SetActiveTransactions(chainID, count uint64)
	ObserveLaneBarrier(chainID uint64, reason string)
	ObserveResult(chainID uint64, status string)
}

const (
	LaneBarrierRPCNonceBehind      = "rpc_nonce_behind"
	LaneBarrierRejectionNotTip     = "rejection_not_tip"
	LaneBarrierRejectionNodeBehind = "rejection_node_behind"
)

func WithObserver(observer Observer) Option {
	return func(m *txManager) {
		if observer != nil {
			m.observer = observer
		}
	}
}

type noopObserver struct{}

func (noopObserver) SetActiveTransactions(uint64, uint64) {}
func (noopObserver) ObserveLaneBarrier(uint64, string)    {}
func (noopObserver) ObserveResult(uint64, string)         {}
