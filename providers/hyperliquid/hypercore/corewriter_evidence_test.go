package hypercore

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/stretchr/testify/require"
)

func TestDecodeCoreWriterRawActionLiveFixtures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		user       string
		data       string
		actionID   uint32
		assertions func(*testing.T, CoreWriterAction)
	}{
		{
			name:     "usd class transfer",
			user:     "0x2321636903018992ae34f735b13054548b550bf6",
			actionID: ActionUsdClassTransfer,
			data:     "0x000000000000000000000000000000000000000000000000000000000000002000000000000000000000000000000000000000000000000000000000000000440100000700000000000000000000000000000000000000000000000000000000018765fe000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000",
			assertions: func(t *testing.T, action CoreWriterAction) {
				decoded, ok := action.(USDClassTransferAction)
				require.True(t, ok)
				require.Equal(t, uint64(25_650_686), decoded.Ntl)
				require.False(t, decoded.ToPerp)
			},
		},
		{
			name:     "add API wallet",
			user:     "0xdcbbfc41c414375d730208e4c1cba6081564bf8b",
			actionID: ActionAddApiWallet,
			data:     "0x00000000000000000000000000000000000000000000000000000000000000200000000000000000000000000000000000000000000000000000000000000084010000090000000000000000000000008ab6e71cd969177f8ccfdba2829c3785d13263ea0000000000000000000000000000000000000000000000000000000000000040000000000000000000000000000000000000000000000000000000000000000d74726164696e672d6167656e740000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000",
			assertions: func(t *testing.T, action CoreWriterAction) {
				decoded, ok := action.(AddAPIWalletAction)
				require.True(t, ok)
				require.Equal(t, common.HexToAddress("0x8ab6e71cd969177f8ccfdba2829c3785d13263ea"), decoded.Agent)
				require.Equal(t, "trading-agent", decoded.Name)
			},
		},
		{
			name:     "approve builder fee",
			user:     "0x9421c6a9c4394bed7cd1c7c1a6754523c51be791",
			actionID: ActionApproveBuilderFee,
			data:     "0x000000000000000000000000000000000000000000000000000000000000002000000000000000000000000000000000000000000000000000000000000000440100000c00000000000000000000000000000000000000000000000000000000000000640000000000000000000000002d105e9c16edd512c608432ddc20337f32a2c72c00000000000000000000000000000000000000000000000000000000",
			assertions: func(t *testing.T, action CoreWriterAction) {
				decoded, ok := action.(ApproveBuilderFeeAction)
				require.True(t, ok)
				require.Equal(t, uint64(100), decoded.MaxFeeRate)
				require.Equal(t, common.HexToAddress("0x2d105e9c16edd512c608432ddc20337f32a2c72c"), decoded.Builder)
			},
		},
		{
			name:     "send asset",
			user:     "0x6b9e773128f453f5c2c60935ee2de2cbc5390a24",
			actionID: ActionSendAsset,
			data:     "0x000000000000000000000000000000000000000000000000000000000000002000000000000000000000000000000000000000000000000000000000000000c40100000d000000000000000000000000ac21a983ff4e65613187432272d374adfded1b64000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000ffffffff0000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000005bb58de8000000000000000000000000000000000000000000000000000000000",
			assertions: func(t *testing.T, action CoreWriterAction) {
				decoded, ok := action.(SendAssetAction)
				require.True(t, ok)
				require.Equal(t, common.HexToAddress("0xac21a983ff4e65613187432272d374adfded1b64"), decoded.Destination)
				require.Zero(t, decoded.Subaccount)
				require.Equal(t, DexSpotSentinel, decoded.SourceDex)
				require.Zero(t, decoded.DestinationDex)
				require.Zero(t, decoded.Token)
				require.Equal(t, uint64(24_618_000_000), decoded.Wei)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log := rawActionFixture(tt.user, tt.data)
			decoded, err := DecodeCoreWriterRawActionLog(log)
			require.NoError(t, err)
			require.Equal(t, common.HexToAddress(tt.user), decoded.User)
			require.Equal(t, CoreWriterEncodingVersion, decoded.Version)
			require.Equal(t, tt.actionID, decoded.ActionID)
			require.Equal(t, log.TxHash, decoded.TransactionHash)
			require.Equal(t, log.Index, decoded.LogIndex)
			tt.assertions(t, decoded.Action)
		})
	}
}

func TestDecodeCoreWriterActionPreservesUnknownVersionOneAction(t *testing.T) {
	t.Parallel()

	raw := []byte{CoreWriterEncodingVersion, 0x01, 0x02, 0x03, 0xde, 0xad}
	action, err := DecodeCoreWriterAction(raw)
	require.NoError(t, err)
	unknown, ok := action.(UnknownCoreWriterAction)
	require.True(t, ok)
	require.Equal(t, uint32(0x010203), unknown.ID)
	require.Equal(t, []byte{0xde, 0xad}, unknown.Payload)

	raw[4] = 0
	require.Equal(t, []byte{0xde, 0xad}, unknown.Payload, "the decoder must own its evidence bytes")
}

func TestDecodeCoreWriterActionRejectsMalformedPayloads(t *testing.T) {
	t.Parallel()

	valid, err := usdClassTransferData.Pack(uint64(1), true)
	require.NoError(t, err)

	tests := []struct {
		name string
		raw  []byte
		want string
	}{
		{name: "missing header", raw: []byte{1, 0, 0}, want: "4-byte header"},
		{name: "unknown version", raw: []byte{2, 0, 0, 7}, want: "unsupported"},
		{name: "short supported payload", raw: append([]byte{1, 0, 0, 7}, valid[:31]...), want: "decoding CoreWriter action 7"},
		{name: "noncanonical supported payload", raw: append(append([]byte{1, 0, 0, 7}, valid...), make([]byte, 32)...), want: "not canonical"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeCoreWriterAction(tt.raw)
			require.ErrorContains(t, err, tt.want)
		})
	}
}

func TestDecodeCoreWriterRawActionLogValidatesEventEvidence(t *testing.T) {
	t.Parallel()

	valid := rawActionFixture(
		"0x2321636903018992ae34f735b13054548b550bf6",
		"0x000000000000000000000000000000000000000000000000000000000000002000000000000000000000000000000000000000000000000000000000000000440100000700000000000000000000000000000000000000000000000000000000018765fe000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000",
	)

	tests := []struct {
		name   string
		mutate func(*types.Log)
		want   string
	}{
		{name: "wrong address", mutate: func(log *types.Log) { log.Address = common.Address{1} }, want: "is not CoreWriter"},
		{name: "removed", mutate: func(log *types.Log) { log.Removed = true }, want: "was removed"},
		{name: "wrong topic count", mutate: func(log *types.Log) { log.Topics = log.Topics[:1] }, want: "want 2"},
		{name: "wrong signature", mutate: func(log *types.Log) { log.Topics[0] = common.Hash{1} }, want: "is not RawAction"},
		{name: "noncanonical user", mutate: func(log *types.Log) { log.Topics[1][0] = 1 }, want: "not a canonical address"},
		{name: "malformed event data", mutate: func(log *types.Log) { log.Data = log.Data[:31] }, want: "decoding CoreWriter RawAction"},
		{name: "noncanonical event data", mutate: func(log *types.Log) { log.Data = append(log.Data, make([]byte, 32)...) }, want: "not canonical"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			copyLog := *valid
			copyLog.Topics = append([]common.Hash(nil), valid.Topics...)
			copyLog.Data = append([]byte(nil), valid.Data...)
			tt.mutate(&copyLog)
			_, err := DecodeCoreWriterRawActionLog(&copyLog)
			require.ErrorContains(t, err, tt.want)
		})
	}

	_, err := DecodeCoreWriterRawActionLog(nil)
	require.ErrorContains(t, err, "nil")
}

func TestDecodeCoreWriterRawActionsReturnsValidEvidenceWithMalformedLogs(t *testing.T) {
	t.Parallel()

	txHash := common.HexToHash("0x1234")
	valid := rawActionFixture(
		"0x2321636903018992ae34f735b13054548b550bf6",
		"0x000000000000000000000000000000000000000000000000000000000000002000000000000000000000000000000000000000000000000000000000000000440100000700000000000000000000000000000000000000000000000000000000018765fe000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000",
	)
	valid.TxHash = common.Hash{}
	valid.BlockNumber = 10
	malformed := &types.Log{Address: CoreWriterAddress, Index: 8, Topics: []common.Hash{CoreWriterRawActionTopic}}
	unrelated := &types.Log{Address: common.Address{1}}

	actions, err := DecodeCoreWriterRawActions(&types.Receipt{
		Status:      types.ReceiptStatusSuccessful,
		TxHash:      txHash,
		BlockNumber: big.NewInt(10),
		Logs:        []*types.Log{valid, malformed, unrelated, nil},
	})
	require.Error(t, err)
	require.ErrorContains(t, err, "decoding CoreWriter log 8")
	require.Len(t, actions, 1)
	require.Equal(t, txHash, actions[0].TransactionHash)

	actions, err = DecodeCoreWriterRawActions(&types.Receipt{Status: types.ReceiptStatusFailed, TxHash: txHash})
	require.Nil(t, actions)
	require.ErrorContains(t, err, "did not succeed")
	actions, err = DecodeCoreWriterRawActions(&types.Receipt{
		Status:      types.ReceiptStatusSuccessful,
		BlockNumber: big.NewInt(10),
		Logs:        []*types.Log{valid},
	})
	require.Nil(t, actions)
	require.ErrorContains(t, err, "no transaction hash")
	valid.TxHash = txHash
	actions, err = DecodeCoreWriterRawActions(&types.Receipt{
		Status: types.ReceiptStatusSuccessful,
		TxHash: txHash,
		Logs:   []*types.Log{valid},
	})
	require.Nil(t, actions)
	require.ErrorContains(t, err, "no block number")
	actions, err = DecodeCoreWriterRawActions(&types.Receipt{
		Status:      types.ReceiptStatusSuccessful,
		TxHash:      txHash,
		BlockNumber: new(big.Int).Lsh(big.NewInt(1), 65),
		Logs:        []*types.Log{valid},
	})
	require.Nil(t, actions)
	require.ErrorContains(t, err, "invalid block number")
	actions, err = DecodeCoreWriterRawActions(&types.Receipt{Status: types.ReceiptStatusSuccessful})
	require.NoError(t, err)
	require.Empty(t, actions)

	actions, err = DecodeCoreWriterRawActions(nil)
	require.Nil(t, actions)
	require.ErrorContains(t, err, "nil")
}

func rawActionFixture(user string, data string) *types.Log {
	return &types.Log{
		Address: CoreWriterAddress,
		Topics: []common.Hash{
			CoreWriterRawActionTopic,
			common.BytesToHash(common.HexToAddress(user).Bytes()),
		},
		Data:        common.FromHex(data),
		BlockNumber: 43_690_777,
		TxHash:      common.HexToHash("0x9ac61f2836e5b7d2f41bafb62948cb97c53a7c450da7889786f284bfd8a81ff4"),
		Index:       1,
	}
}
