package pg

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
	q "github.com/superform-xyz/superform-go-utils/evm/txmngr/txmngr_provider"
)

func TestSenderScopeSurvivesNewAndLegacyFilter(t *testing.T) {
	sender := common.HexToAddress("0x1234")
	scoped := NewSenderTransactionsQ(nil, sender)
	filtered := scoped.New().FilterBySenderOrUnset(sender).FilterByIds("campaign")
	query, args, err := filtered.(transactionsQ).selector.ToSql()
	require.NoError(t, err)
	require.Contains(t, query, "WHERE sender = $1 AND (sender = $2 OR sender IS NULL)")
	require.Equal(t, []any{sender.Hex(), sender.Hex(), "campaign"}, args)
	require.Error(t, scoped.Insert(q.Transaction{Sender: common.Address{}}))
	_, err = scoped.InsertOrGet(q.Transaction{Sender: common.Address{}})
	require.Error(t, err)
}
