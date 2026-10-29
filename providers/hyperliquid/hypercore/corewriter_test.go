package hypercore

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
)

func TestTokenSystemAddress(t *testing.T) {
	require.Equal(t,
		common.HexToAddress("0x2000000000000000000000000000000000000000"),
		TokenSystemAddress(0))

	require.Equal(t,
		common.HexToAddress("0x2000000000000000000000000000000000000001"),
		TokenSystemAddress(1))

	require.Equal(t,
		common.HexToAddress("0x200000000000000000000000000000000000010c"),
		TokenSystemAddress(268))
}

func TestUsdToNtl(t *testing.T) {
	ntl, err := UsdToNtl(big.NewRat(255, 10))
	require.NoError(t, err)
	require.Equal(t, uint64(25_500_000), ntl)

	ntl, err = UsdToNtl(big.NewRat(1, 1_000_000))
	require.NoError(t, err)
	require.Equal(t, uint64(1), ntl)

	_, err = UsdToNtl(big.NewRat(1, 10_000_000))
	require.ErrorContains(t, err, "does not convert exactly")

	_, err = UsdToNtl(nil)
	require.Error(t, err)
	_, err = UsdToNtl(big.NewRat(-1, 1))
	require.Error(t, err)
}

func TestNtlToUsdRoundTrip(t *testing.T) {
	usd := NtlToUsd(25_500_000)
	require.Zero(t, usd.Cmp(big.NewRat(255, 10)))

	ntl, err := UsdToNtl(usd)
	require.NoError(t, err)
	require.Equal(t, uint64(25_500_000), ntl)
}

func TestTokenSystemAddressHypeException(t *testing.T) {
	require.Equal(t, HypeSystemAddress, TokenSystemAddress(HypeTokenIndex))
	require.NotEqual(t, common.HexToAddress("0x2000000000000000000000000000000000000096"),
		TokenSystemAddress(HypeTokenIndex))

	require.Equal(t, common.HexToAddress("0x2000000000000000000000000000000000000000"), TokenSystemAddress(0))
	require.Equal(t, common.HexToAddress("0x200000000000000000000000000000000000010c"), TokenSystemAddress(268))
}
