package hypercore

import (
	"math"
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"
)

var measuredExtraDecimals = map[string]int8{
	"USDC":  -2,
	"USDT0": -2,
	"USDE":  10,
	"UETH":  9,
	"USOL":  1,
	"USDHL": -1,
}

func TestEvmToCoreWeiExactAcrossMeasuredTokens(t *testing.T) {
	cases := []struct {
		token   string
		evm     *big.Int
		coreWei uint64
	}{
		{"USDC", big.NewInt(1_500_000), 150_000_000},
		{"USDT0", big.NewInt(1_500_000), 150_000_000},
		{"USDE", new(big.Int).SetUint64(1_500_000_000_000_000_000), 150_000_000},
		{"UETH", new(big.Int).SetUint64(1_500_000_000_000_000_000), 1_500_000_000},
		{"USOL", big.NewInt(1_500_000_000), 150_000_000},
		{"USDHL", big.NewInt(1_500_000), 15_000_000},
	}

	for _, tc := range cases {
		extra := measuredExtraDecimals[tc.token]

		coreWei, err := EvmToCoreWei(tc.evm, extra)
		require.NoError(t, err, tc.token)
		require.Equal(t, tc.coreWei, coreWei, tc.token)

		evm, err := CoreWeiToEvm(tc.coreWei, extra)
		require.NoError(t, err, tc.token)
		require.Zero(t, evm.Cmp(tc.evm), tc.token)
	}
}

func TestEvmToCoreWeiRejectsSubCorePrecisionDust(t *testing.T) {
	amount := new(big.Int).SetUint64(1_500_000_000_000_000_001)

	_, err := EvmToCoreWei(amount, measuredExtraDecimals["USDE"])
	require.ErrorContains(t, err, "does not convert exactly")

	coreWei, remainder, err := EvmToCoreWeiFloor(amount, measuredExtraDecimals["USDE"])
	require.NoError(t, err)
	require.Equal(t, uint64(150_000_000), coreWei)
	require.Zero(t, remainder.Cmp(big.NewInt(1)))
}

func TestCoreWeiToEvmRejectsSubEvmPrecisionDust(t *testing.T) {
	_, err := CoreWeiToEvm(150_000_001, measuredExtraDecimals["USDC"])
	require.ErrorContains(t, err, "does not convert exactly")

	evm, remainder := CoreWeiToEvmFloor(150_000_001, measuredExtraDecimals["USDC"])
	require.Zero(t, evm.Cmp(big.NewInt(1_500_000)))
	require.Equal(t, uint64(1), remainder)
}

func TestEvmToCoreWeiEnforcesUint64Bound(t *testing.T) {
	over := new(big.Int).Lsh(big.NewInt(1), 64)

	_, err := EvmToCoreWei(over, -2)
	require.ErrorContains(t, err, "uint64 bound")

	max := new(big.Int).SetUint64(math.MaxUint64)
	coreWei, err := EvmToCoreWei(max, 0)
	require.NoError(t, err)
	require.Equal(t, uint64(math.MaxUint64), coreWei)
}

func TestEvmToCoreWeiRejectsInvalidAmounts(t *testing.T) {
	_, err := EvmToCoreWei(nil, 0)
	require.ErrorContains(t, err, "non-negative")

	_, err = EvmToCoreWei(big.NewInt(-1), 0)
	require.ErrorContains(t, err, "non-negative")
}

func TestZeroExtraDecimalsIsIdentity(t *testing.T) {
	coreWei, err := EvmToCoreWei(big.NewInt(42), 0)
	require.NoError(t, err)
	require.Equal(t, uint64(42), coreWei)

	evm, err := CoreWeiToEvm(42, 0)
	require.NoError(t, err)
	require.Zero(t, evm.Cmp(big.NewInt(42)))
}

func TestDecimalOffsetExtremesDoNotOverflowOrMutateInput(t *testing.T) {
	input := big.NewInt(1)
	_, err := EvmToCoreWei(input, math.MinInt8)
	require.ErrorContains(t, err, "uint64 bound")
	require.Equal(t, "1", input.String())

	amount, remainder, err := EvmToCoreWeiFloor(input, math.MaxInt8)
	require.NoError(t, err)
	require.Zero(t, amount)
	require.Equal(t, "1", remainder.String())
	require.Equal(t, "1", input.String())

	evm, dust := CoreWeiToEvmFloor(math.MaxUint64, math.MinInt8)
	require.Zero(t, evm.Sign())
	require.Equal(t, uint64(math.MaxUint64), dust)
	evm, err = CoreWeiToEvm(1, math.MaxInt8)
	require.NoError(t, err)
	require.Equal(t, 128, len(evm.String()))
}
