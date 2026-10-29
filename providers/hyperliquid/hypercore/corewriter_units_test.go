package hypercore

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUsdFromScaledHandlesLosingAccounts(t *testing.T) {
	require.Equal(t, "-1250.500000", UsdFromScaled(-1_250_500_000).FloatString(6))
	require.Equal(t, "-0.000001", UsdFromScaled(-1).FloatString(6))
	require.Equal(t, "0.000000", UsdFromScaled(0).FloatString(6))

	require.Equal(t, -1, UsdFromScaled(-1_250_500_000).Sign())
	require.Equal(t, 1, UsdFromScaled(1_250_500_000).Sign())

	require.Equal(t, "-9223372036854.775808", UsdFromScaled(math.MinInt64).FloatString(6))
	require.Equal(t, "9223372036854.775807", UsdFromScaled(math.MaxInt64).FloatString(6))
}

func TestCoreWeiToAmountNormalisesAgainstPerpScale(t *testing.T) {
	spot := CoreWeiToAmount(487_710_000, 8)
	require.Equal(t, "4.877100", spot.FloatString(6))
	require.Equal(t, "4.877100", UsdFromScaled(4_877_100).FloatString(6))
}
