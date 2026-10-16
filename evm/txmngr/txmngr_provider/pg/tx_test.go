package pg

import (
	"context"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"

	"github.com/superform-xyz/superform-go-utils/evm/txmngr/txmngr_provider"
)

func TestFilterBySenderOrUnsetSQL(t *testing.T) {
	t.Parallel()

	sender := common.HexToAddress("0x0000000000000000000000000000000000000001")
	filtered := NewTransactionsQ(nil).FilterBySenderOrUnset(sender)
	query, args, err := filtered.(transactionsQ).selector.ToSql()
	require.NoError(t, err)
	require.Contains(t, query, "(sender = $1 OR sender IS NULL)")
	require.Equal(t, []interface{}{sender.Hex()}, args)
}

func TestCountByChain(t *testing.T) {
	t.Parallel()

	sqlDB, mockDB, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db := sqlx.NewDb(sqlDB, "sqlmock")

	mockDB.ExpectQuery(
		"SELECT chain_id, COUNT(*) AS count FROM transactions WHERE status IN ($1,$2,$3) GROUP BY chain_id",
	).
		WithArgs("pending", "processing", "confirming").
		WillReturnRows(sqlmock.NewRows([]string{"chain_id", "count"}).
			AddRow(1, 2).
			AddRow(8453, 11))

	counts, err := NewTransactionsQ(db).CountByChain(
		context.Background(), txmngr_provider.ActiveStatuses()...,
	)
	require.NoError(t, err)
	require.Equal(t, map[uint64]uint64{1: 2, 8453: 11}, counts)
	require.NoError(t, mockDB.ExpectationsWereMet())
}

func TestNormalizedHash(t *testing.T) {
	t.Parallel()

	canonicalAB := common.BytesToHash([]byte{0xab}).Hex()
	canonicalABC := common.BytesToHash([]byte{0x0a, 0xbc}).Hex()
	tests := []struct {
		name  string
		input *string
		want  *string
	}{
		{name: "nil", input: nil, want: nil},
		{name: "canonical", input: ptr(canonicalAB), want: ptr(canonicalAB)},
		{name: "uppercase", input: ptr("AB"), want: ptr(canonicalAB)},
		{name: "uppercase prefix and odd length", input: ptr("0XAbC"), want: ptr(canonicalABC)},
		{name: "omitted prefix and odd length", input: ptr("abc"), want: ptr(canonicalABC)},
		{name: "empty", input: ptr(""), want: ptr("")},
		{name: "prefix only", input: ptr("0x"), want: ptr("0x")},
		{name: "malformed", input: ptr("12zz"), want: ptr("12zz")},
		{name: "overlong", input: ptr(strings.Repeat("a", common.HashLength*2+1)), want: ptr(strings.Repeat("a", common.HashLength*2+1))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got := normalizedHash(test.input)
			require.Equal(t, test.want, got)
		})
	}
}

func TestAttemptModelsToAttemptsPreservesOrderAndRejectsPartialDecode(t *testing.T) {
	t.Parallel()

	to := common.HexToAddress("0x0000000000000000000000000000000000000001")
	raw, err := types.NewTx(&types.LegacyTx{
		Nonce:    1,
		GasPrice: big.NewInt(1),
		Gas:      21_000,
		To:       &to,
		Value:    big.NewInt(0),
	}).MarshalJSON()
	require.NoError(t, err)

	models := []attemptModel{
		{TransactionID: "first", TxHash: common.HexToHash("0x01").Hex(), Nonce: 1, RawTx: string(raw), CreatedAt: time.Now()},
		{TransactionID: "second", TxHash: common.HexToHash("0x02").Hex(), Nonce: 1, RawTx: string(raw), CreatedAt: time.Now().Add(time.Second)},
	}
	attempts, err := attemptModelsToAttempts(models)
	require.NoError(t, err)
	require.Equal(t, []string{"first", "second"}, []string{attempts[0].TransactionID, attempts[1].TransactionID})

	models[1].RawTx = "not-json"
	attempts, err = attemptModelsToAttempts(models)
	require.ErrorContains(t, err, models[1].TxHash)
	require.Nil(t, attempts, "a corrupt row must not return partial attempt history")
}

func ptr[T any](value T) *T {
	return &value
}
