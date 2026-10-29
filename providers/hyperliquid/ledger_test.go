package hyperliquid

import (
	"encoding/json"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
)

func TestDecodeLedgerDeltaCapturedSend(t *testing.T) {
	raw := json.RawMessage(`{"type":"send","user":"0x2321636903018992ae34f735b13054548b550bf6","destination":"0x2000000000000000000000000000000000000000","sourceDex":"spot","destinationDex":"spot","token":"USDC","amount":"25.643116","usdcValue":25.643116,"fee":"0.002387","nativeTokenFee":0.0,"nonce":2758256,"feeToken":"USDC"}`)
	delta, err := DecodeLedgerDelta(raw)
	require.NoError(t, err)
	send, ok := delta.(*SendLedgerDelta)
	require.True(t, ok)
	require.Equal(t, common.HexToAddress("0x2321636903018992ae34f735b13054548b550bf6"), send.User)
	require.Equal(t, common.HexToAddress("0x2000000000000000000000000000000000000000"), send.Destination)
	require.Equal(t, Decimal("25.643116"), send.Amount)
	require.Equal(t, Decimal("25.643116"), send.USDCValue)
	require.Equal(t, Decimal("0.0"), send.NativeTokenFee)
	require.Equal(t, uint64(2_758_256), send.Nonce)
	require.Equal(t, LedgerDeltaSend, LedgerDeltaKind(delta))
}

func TestDecodeLedgerDeltaVariants(t *testing.T) {
	user := `"0x0000000000000000000000000000000000000001"`
	destination := `"0x0000000000000000000000000000000000000002"`
	for _, test := range []struct {
		kind   LedgerDeltaType
		fields string
		want   NonFundingLedgerDelta
	}{
		{LedgerDeltaDeposit, `"usdc":1.0`, new(DepositLedgerDelta)},
		{LedgerDeltaWithdraw, `"usdc":"1.0","nonce":1,"fee":"0.1"`, new(WithdrawLedgerDelta)},
		{LedgerDeltaInternalTransfer, `"usdc":"1.0","user":` + user + `,"destination":` + destination + `,"fee":"0.0"`, new(InternalTransferLedgerDelta)},
		{LedgerDeltaSubAccountTransfer, `"usdc":"1.0","user":` + user + `,"destination":` + destination, new(SubAccountTransferLedgerDelta)},
		{LedgerDeltaLiquidation, `"accountValue":"-1.0","leverageType":"Cross","liquidatedPositions":[{"coin":"BTC","szi":"0.1"}]`, new(LiquidationLedgerDelta)},
		{LedgerDeltaVaultCreate, `"vault":` + user + `,"usdc":"1.0"`, new(VaultLedgerDelta)},
		{LedgerDeltaVaultDeposit, `"vault":` + user + `,"usdc":"1.0"`, new(VaultLedgerDelta)},
		{LedgerDeltaVaultDistribution, `"vault":` + user + `,"usdc":"1.0"`, new(VaultLedgerDelta)},
		{LedgerDeltaVaultWithdraw, `"vault":` + user + `,"user":` + destination + `,"requestedUsd":"1.0","commission":"0.1","closingCost":"0.1","basis":"1.0","netWithdrawnUsd":"0.8"`, new(VaultWithdrawalLedgerDelta)},
		{LedgerDeltaVaultLeaderCommission, `"user":` + user + `,"usdc":"1.0"`, new(VaultLeaderCommissionLedgerDelta)},
		{LedgerDeltaSpotTransfer, `"token":"USDC","amount":"1.0","usdcValue":"1.0","user":` + user + `,"destination":` + destination + `,"fee":"0.0"`, new(SpotTransferLedgerDelta)},
		{LedgerDeltaAccountClassTransfer, `"usdc":"1.0","toPerp":true`, new(AccountClassTransferLedgerDelta)},
		{LedgerDeltaSpotGenesis, `"token":"HYPE","amount":"1.0"`, new(SpotGenesisLedgerDelta)},
		{LedgerDeltaRewardsClaim, `"amount":"1.0"`, new(RewardsClaimLedgerDelta)},
	} {
		t.Run(string(test.kind), func(t *testing.T) {
			delta, err := DecodeLedgerDelta(json.RawMessage(`{"type":"` + string(test.kind) + `",` + test.fields + `}`))
			require.NoError(t, err)
			require.IsType(t, test.want, delta)
			require.Equal(t, test.kind, LedgerDeltaKind(delta))
		})
	}
	require.Empty(t, LedgerDeltaKind(nil))
}

func TestDecodeLedgerDeltaRequiresProtocolFields(t *testing.T) {
	for _, test := range []struct{ raw, want string }{
		{`{"type":"accountClassTransfer","usdc":"1.0"}`, `required field "toPerp" is missing`},
		{`{"type":"accountClassTransfer","usdc":"1.0","toPerp":null}`, `required field "toPerp" is null`},
		{`{"type":"send"}`, `required field "user" is missing`},
		{`{"type":"deposit","usdc":"invalid"}`, "invalid decimal"},
		{`{}`, "type is empty"},
		{`null`, "type is empty"},
		{`[]`, "decoding ledger delta type"},
		{`{"type":1}`, "decoding ledger delta type"},
	} {
		_, err := DecodeLedgerDelta(json.RawMessage(test.raw))
		require.ErrorContains(t, err, test.want)
	}
	// false is a supplied direction rather than a missing bool.
	_, err := DecodeLedgerDelta(json.RawMessage(`{"type":"accountClassTransfer","usdc":1.0,"toPerp":false}`))
	require.NoError(t, err)
}

func TestLedgerViewsPreserveUnknownPayloadAndExactDecimalText(t *testing.T) {
	raw := []byte(`{"time":1,"delta":{"type":"futureDelta","usdc":9007199254740993.000000001,"fee":"0.00000000001","newField":{"value":true}}}`)
	var update LedgerUpdate
	require.NoError(t, json.Unmarshal(raw, &update))
	require.Empty(t, update.Hash, "wire reads do not require an execution-evidence hash")
	require.Equal(t, "9007199254740993.000000001", update.Delta.Usdc)
	require.Equal(t, "0.00000000001", update.Delta.Fee)
	require.JSONEq(t, string(raw), string(update.Raw))
	decoded, err := DecodeLedgerDelta(update.Delta.Raw)
	require.NoError(t, err)
	unknown, ok := decoded.(UnknownLedgerDelta)
	require.True(t, ok)
	require.Equal(t, LedgerDeltaType("futureDelta"), unknown.Type)
	require.JSONEq(t, `{"type":"futureDelta","usdc":9007199254740993.000000001,"fee":"0.00000000001","newField":{"value":true}}`, string(unknown.Raw))
	raw[0] = '['
	require.Equal(t, byte('{'), update.Raw[0], "original input buffer is not retained")
	update.Delta.Raw[0] = '['
	require.Equal(t, byte('{'), unknown.Raw[0], "typed unknown payload is independently owned")
}

func TestDecimalAcceptsOnlyOneExactNumberOrQuotedDecimal(t *testing.T) {
	for _, raw := range []string{`9007199254740993.000000001`, `"9007199254740993.000000001"`, " \n 1.25e-2 \t"} {
		var value Decimal
		require.NoError(t, json.Unmarshal([]byte(raw), &value))
		rational, err := value.Rat()
		require.NoError(t, err)
		if value == "1.25e-2" {
			require.Equal(t, "1/80", rational.RatString())
		} else {
			require.Equal(t, "9007199254740993000000001/1000000000", rational.RatString())
		}
	}
	for _, raw := range []string{`null`, `true`, `{}`, `[]`, `"NaN"`, `"1/2"`, `"+1"`, `01`, `1 2`, `""`} {
		var value Decimal
		require.Error(t, value.UnmarshalJSON([]byte(raw)), raw)
	}
	var nilDecimal *Decimal
	require.Error(t, nilDecimal.UnmarshalJSON([]byte(`1`)))
}

func TestFlatLedgerDecimalTextPreservesCompatibility(t *testing.T) {
	var delta LedgerDelta
	require.NoError(t, json.Unmarshal([]byte(`{"type":"deposit","usdc":"","usdcValue":"not-a-number","fee":null,"amount":9007199254740993.000000001}`), &delta))
	require.Empty(t, delta.Usdc)
	require.Equal(t, "not-a-number", delta.UsdcValue)
	require.Empty(t, delta.Fee)
	require.Equal(t, "9007199254740993.000000001", delta.Amount)
	// The typed view continues to reject malformed amounts rather than assigning
	// an amount or policy outcome to invalid provider text.
	_, err := DecodeLedgerDelta(delta.Raw)
	require.ErrorContains(t, err, "invalid decimal")
}
