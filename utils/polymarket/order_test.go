package polymarket

import (
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
)

const (
	standardGoldenOrderHash = "0x83c5e044f3c8d7e77a151ed765dd2eaae73f3f2e429e6a19506f113e96dc1fda"
	negRiskGoldenOrderHash  = "0xd3ed7615cce52f272df3b7d20d83095ebe221310b261ada49d20f3ad8da34f37"
)

func goldenOrder() Order {
	return Order{
		Salt:          42,
		Maker:         common.HexToAddress("0x1111111111111111111111111111111111111111"),
		Signer:        common.HexToAddress("0x1111111111111111111111111111111111111111"),
		TokenID:       "123456",
		MakerAmount:   "500000",
		TakerAmount:   "1000000",
		Side:          OrderSideBuy,
		SignatureType: OrderSignatureTypePoly1271,
		Timestamp:     1_700_000_000_000,
	}
}

func TestOrderHashGoldenVectors(t *testing.T) {
	for _, test := range []struct {
		marketType MarketType
		want       string
	}{
		{marketType: MarketTypeStandard, want: standardGoldenOrderHash},
		{marketType: MarketTypeNegRisk, want: negRiskGoldenOrderHash},
	} {
		t.Run(string(test.marketType), func(t *testing.T) {
			digest, err := goldenOrder().Hash(test.marketType)
			require.NoError(t, err)
			require.Equal(t, test.want, digest.Hex())
		})
	}
}

func TestBuildOrderNormalizesBuyCeilingAndSellFloor(t *testing.T) {
	for _, test := range []struct {
		name        string
		side        OrderSide
		makerAmount string
		takerAmount string
	}{
		{name: "buy", side: OrderSideBuy, makerAmount: "336633", takerAmount: "1010000"},
		{name: "sell", side: OrderSideSell, makerAmount: "1010000", takerAmount: "336633"},
	} {
		t.Run(test.name, func(t *testing.T) {
			order, err := BuildOrder(BuildOrderInput{
				Salt: MaxSafeOrderSalt, Maker: common.HexToAddress("0x1111111111111111111111111111111111111111"),
				Signer: common.HexToAddress("0x1111111111111111111111111111111111111111"), TokenID: big.NewInt(7),
				PriceE6: 333300, SizeE6: big.NewInt(1010000), TickSizeE6: 100, Side: test.side,
				SignatureType: OrderSignatureTypePoly1271, Timestamp: 1_700_000_000_123,
			})
			require.NoError(t, err)
			require.Equal(t, test.makerAmount, order.MakerAmount)
			require.Equal(t, test.takerAmount, order.TakerAmount)
		})
	}
}

func TestEveryMutableSignedFieldChangesTheDigest(t *testing.T) {
	base := goldenOrder()
	baseHash, err := base.Hash(MarketTypeStandard)
	require.NoError(t, err)

	mutations := map[string]func(*Order){
		"salt":         func(order *Order) { order.Salt++ },
		"maker":        func(order *Order) { order.Maker = common.HexToAddress("0x2222222222222222222222222222222222222222") },
		"signer":       func(order *Order) { order.Signer = common.HexToAddress("0x3333333333333333333333333333333333333333") },
		"token id":     func(order *Order) { order.TokenID = "123457" },
		"maker amount": func(order *Order) { order.MakerAmount = "500001" },
		"taker amount": func(order *Order) { order.TakerAmount = "1000001" },
		"side":         func(order *Order) { order.Side = OrderSideSell },
		"timestamp":    func(order *Order) { order.Timestamp++ },
		"metadata":     func(order *Order) { order.Metadata = common.HexToHash("0x01") },
		"builder":      func(order *Order) { order.Builder = common.HexToHash("0x02") },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := base
			mutate(&changed)
			digest, err := changed.Hash(MarketTypeStandard)
			require.NoError(t, err)
			require.NotEqual(t, baseHash, digest)
		})
	}
}

func TestBuildOrderRejectsUnsupportedInputs(t *testing.T) {
	valid := BuildOrderInput{
		Salt: 42, Maker: common.HexToAddress("0x1111111111111111111111111111111111111111"),
		Signer: common.HexToAddress("0x1111111111111111111111111111111111111111"), TokenID: big.NewInt(7),
		PriceE6: 500000, SizeE6: big.NewInt(1000000), TickSizeE6: 100, Side: OrderSideBuy,
		SignatureType: OrderSignatureTypePoly1271, Timestamp: 1_700_000_000_000,
	}
	for _, test := range []struct {
		name   string
		mutate func(*BuildOrderInput)
		want   string
	}{
		{name: "zero maker", mutate: func(input *BuildOrderInput) { input.Maker = common.Address{} }, want: "maker and signer"},
		{name: "zero token", mutate: func(input *BuildOrderInput) { input.TokenID = new(big.Int) }, want: "token_id"},
		{name: "price one", mutate: func(input *BuildOrderInput) { input.PriceE6 = 1_000_000 }, want: "price_e6"},
		{name: "unsupported tick", mutate: func(input *BuildOrderInput) { input.TickSizeE6 = 3 }, want: "tick-size"},
		{name: "off grid", mutate: func(input *BuildOrderInput) { input.PriceE6 = 500050 }, want: "tick-size"},
		{name: "excess size precision", mutate: func(input *BuildOrderInput) { input.SizeE6 = big.NewInt(1000001) }, want: "two decimal"},
		{name: "wrong signature type", mutate: func(input *BuildOrderInput) { input.SignatureType = 2 }, want: "POLY_1271"},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := valid
			test.mutate(&input)
			_, err := BuildOrder(input)
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestIsCanonicalUint256(t *testing.T) {
	require.True(t, IsCanonicalUint256("1", false))
	require.True(t, IsCanonicalUint256("0", true))
	require.False(t, IsCanonicalUint256("0", false))
	require.False(t, IsCanonicalUint256("-1", true))
	require.False(t, IsCanonicalUint256("01", true))
	require.False(t, IsCanonicalUint256(new(big.Int).Lsh(big.NewInt(1), 256).String(), true))
}

func TestParseFixedPointRejectsUnboundedValues(t *testing.T) {
	maximum := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))
	value, err := ParseFixedPoint(maximum.String(), 0)
	require.NoError(t, err)
	require.Equal(t, maximum, value)

	_, err = ParseFixedPoint(new(big.Int).Add(maximum, big.NewInt(1)).String(), 0)
	require.ErrorContains(t, err, "uint256")
	_, err = ParseFixedPoint(strings.Repeat("9", 1_000_000), 6)
	require.Error(t, err)
}
