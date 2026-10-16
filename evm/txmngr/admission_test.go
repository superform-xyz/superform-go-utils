package txmngr

import (
	"context"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/core/types"
	mock "github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	q "github.com/superform-xyz/superform-go-utils/evm/txmngr/txmngr_provider"
	"testing"
	"time"
)

func TestAdmissionDefersWithoutSigningOrCompleting(t *testing.T) {
	retry := time.Now().Add(time.Hour)
	f := newFixture(t, fixtureOpts{seed: map[string]q.Transaction{"pending": {ID: "pending", ChainID: uint64(chainId), Status: q.PendingTxStatus}},
		managerOpts: []Option{WithBeforeAttempt(func(_ context.Context, _ q.Transaction, signed bool) (time.Time, error) {
			require.False(t, signed)
			return retry, nil
		})}})
	require.NoError(t, f.step("pending"))
	got := f.get("pending")
	require.False(t, got.IsCompleted())
	require.Nil(t, got.AssignedNonce)
	require.Equal(t, retry, got.NextActionAt)
	f.cl.AssertNotCalled(t, "PendingNonceAt", mock.Anything, mock.Anything)
	f.cl.AssertNotCalled(t, "SendTransaction", mock.Anything, mock.Anything)
}

func TestReceiptReconciliationPrecedesAdmission(t *testing.T) {
	auth := newSigner(t)
	signed := signLegacy(t, auth, 7)
	f := newFixture(t, fixtureOpts{auth: auth, seed: map[string]q.Transaction{"signed": {ID: "signed", ChainID: uint64(chainId), Status: q.ConfirmingTxStatus, Tx: signed}},
		managerOpts: []Option{WithBeforeAttempt(func(context.Context, q.Transaction, bool) (time.Time, error) {
			t.Fatal("mined receipt must bypass exhausted budget")
			return time.Time{}, nil
		})}})
	f.seedAttempt("signed", signed, time.Now())
	f.cl.On("TransactionReceipt", mock.Anything, signed.Hash()).Return(&types.Receipt{TxHash: signed.Hash(), Status: types.ReceiptStatusSuccessful}, nil)
	require.NoError(t, f.step("signed"))
	require.Equal(t, q.SucceedTxStatus, f.get("signed").Status)
}

func TestExpiredUnsignedCancellationCannotDiscardSignedEvidence(t *testing.T) {
	for _, signed := range []bool{false, true} {
		t.Run(map[bool]string{false: "unsigned", true: "signed"}[signed], func(t *testing.T) {
			auth := newSigner(t)
			f := newFixture(t, fixtureOpts{auth: auth, seed: map[string]q.Transaction{"expired": {ID: "expired", ChainID: uint64(chainId), Status: q.PendingTxStatus}},
				managerOpts: []Option{WithBeforeAttempt(func(context.Context, q.Transaction, bool) (time.Time, error) {
					return time.Time{}, ErrUnsignedTransactionExpired
				})}})
			if signed {
				tx := signLegacy(t, auth, 7)
				f.seedAttempt("expired", tx, time.Now())
				f.cl.On("TransactionReceipt", mock.Anything, tx.Hash()).Return(nil, ethereum.NotFound)
			}
			err := f.step("expired")
			require.Error(t, err)
			require.Equal(t, !signed, f.get("expired").IsCompleted())
			if !signed {
				require.Nil(t, f.get("expired").AssignedNonce)
			}
			f.cl.AssertNotCalled(t, "SendTransaction", mock.Anything, mock.Anything)
		})
	}
}
