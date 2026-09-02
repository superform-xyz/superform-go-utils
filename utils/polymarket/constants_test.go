package polymarket

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
)

func TestCanonicalV2Deployments(t *testing.T) {
	require.Equal(t, uint64(137), PolygonChainID)
	require.Equal(t, common.HexToAddress("0x2791Bca1f2de4661ED88A30C99A7a9449Aa84174"), USDCEContract())
	require.Equal(t, common.HexToAddress("0xC011a7E12a19f7B1f670d46F03B03f3342E82DFB"), PUSDContract())
	require.Equal(t, common.HexToAddress("0x4D97DCd97eC945f40cF65F87097ACe5EA0476045"), ConditionalTokensContract())

	standard, err := MarketTypeStandard.Exchange()
	require.NoError(t, err)
	negRisk, err := MarketTypeNegRisk.Exchange()
	require.NoError(t, err)
	standardLiteral := common.HexToAddress("0xE111180000d2663C0091e4f400237545B87B996B")
	negRiskLiteral := common.HexToAddress("0xe2222d279d744050d28e00520010520000310F59")
	require.Equal(t, standardLiteral, StandardExchangeContract())
	require.Equal(t, negRiskLiteral, NegRiskExchangeContract())
	require.Equal(t, standardLiteral, standard)
	require.Equal(t, negRiskLiteral, negRisk)
	require.NotEqual(t, standard, negRisk)
}

func TestMarketTypeParsingIsExact(t *testing.T) {
	for _, marketType := range []MarketType{MarketTypeStandard, MarketTypeNegRisk} {
		parsed, err := ParseMarketType(string(marketType))
		require.NoError(t, err)
		require.Equal(t, marketType, parsed)
	}
	for _, value := range []string{"", "standard", " STANDARD", "STANDARD ", "OTHER"} {
		_, err := ParseMarketType(value)
		require.Error(t, err)
	}
}
