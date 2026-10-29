package hypercore

import (
	"context"
	"encoding/hex"
	"errors"
	"math"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
)

const (
	mainnetTokenInfoUSDC = "000000000000000000000000000000000000000000000000000000000000002000000000000000000000000000000000000000000000000000000000000001000000000000000000000000000000000000000000000000000000000000000140000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000006b9e773128f453f5c2c60935ee2de2cbc5390a2400000000000000000000000000000000000000000000000000000000000000080000000000000000000000000000000000000000000000000000000000000008fffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffe000000000000000000000000000000000000000000000000000000000000000455534443000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000"

	mainnetSpotBalance = "000000000000000000000000000000000000000000000000000000001d10f2d000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000"
)

type fakeCaller struct {
	responses map[common.Address][]byte
	err       error
	calls     int
	lastCall  ethereum.CallMsg
}

func (f *fakeCaller) CallContract(_ context.Context, msg ethereum.CallMsg, _ *big.Int) ([]byte, error) {
	f.calls++
	f.lastCall = msg
	if f.err != nil {
		return nil, f.err
	}
	return f.responses[*msg.To], nil
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	require.NoError(t, err)
	return b
}

func TestReadTokenInfoDecodesMainnetUSDC(t *testing.T) {
	caller := &fakeCaller{responses: map[common.Address][]byte{
		TokenInfoPrecompile: mustHex(t, mainnetTokenInfoUSDC),
	}}

	info, err := ReadTokenInfo(context.Background(), caller, USDCTokenIndex)
	require.NoError(t, err)

	require.Equal(t, "USDC", info.Name)
	require.Empty(t, info.Spots)
	require.Equal(t, uint64(0), info.DeployerTradingFeeShare)
	require.Equal(t, common.Address{}, info.Deployer)
	require.Equal(t, common.HexToAddress("0x6B9E773128f453f5c2C60935Ee2DE2CBc5390A24"), info.EvmContract)
	require.Equal(t, uint8(8), info.SzDecimals)
	require.Equal(t, uint8(8), info.WeiDecimals)
	require.Equal(t, int8(-2), info.EvmExtraWeiDecimals)

	require.Equal(t, TokenInfoPrecompile, *caller.lastCall.To)
	require.Len(t, caller.lastCall.Data, 32)
}

func TestReadTokenInfoRoundTripsPositiveExtraDecimals(t *testing.T) {
	fixture := TokenInfo{
		Name:                    "USDE",
		Spots:                   []uint64{166},
		DeployerTradingFeeShare: 1,
		Deployer:                common.HexToAddress("0x000000000000000000000000000000000000dEaD"),
		EvmContract:             common.HexToAddress("0x5d3a1Ff2b6BAb83b63cd9AD0787074081a52ef34"),
		SzDecimals:              2,
		WeiDecimals:             8,
		EvmExtraWeiDecimals:     10,
	}
	packed, err := tokenInfoOutputs.Pack(fixture)
	require.NoError(t, err)

	caller := &fakeCaller{responses: map[common.Address][]byte{TokenInfoPrecompile: packed}}
	info, err := ReadTokenInfo(context.Background(), caller, 200)
	require.NoError(t, err)
	require.Equal(t, fixture, info)
}

func TestReadSpotBalanceDecodesMainnetShape(t *testing.T) {
	caller := &fakeCaller{responses: map[common.Address][]byte{
		SpotBalancePrecompile: mustHex(t, mainnetSpotBalance),
	}}

	user := common.HexToAddress("0x1111111111111111111111111111111111111111")
	balance, err := ReadSpotBalance(context.Background(), caller, user, USDCTokenIndex)
	require.NoError(t, err)

	require.Equal(t, uint64(0x1d10f2d0), balance.Total)
	require.Equal(t, uint64(0), balance.Hold)
	require.Equal(t, uint64(0), balance.EntryNtl)

	require.Equal(t, SpotBalancePrecompile, *caller.lastCall.To)
	require.Len(t, caller.lastCall.Data, 64)
	require.Equal(t, user.Bytes(), caller.lastCall.Data[12:32])
}

func TestReadSpotBalancePropagatesCallErrors(t *testing.T) {
	caller := &fakeCaller{err: errors.New("rpc unreachable")}

	_, err := ReadSpotBalance(context.Background(), caller, common.Address{}, USDCTokenIndex)
	require.ErrorContains(t, err, "rpc unreachable")

	_, err = ReadTokenInfo(context.Background(), caller, USDCTokenIndex)
	require.ErrorContains(t, err, "rpc unreachable")
}

const mainnetAccountMarginSummary = "00000000000000000000000000000000000000000000000000002ac14b4d8193" +
	"000000000000000000000000000000000000000000000000000000001c7bad85" +
	"0000000000000000000000000000000000000000000000000000000000000000" +
	"00000000000000000000000000000000000000000000000000002ac14b4d8193"

const mainnetAccountMarginSummaryNegative = "000000000000000000000000000000000000000000000000000000003deb7a70" +
	"000000000000000000000000000000000000000000000000000000003d7d4802" +
	"0000000000000000000000000000000000000000000000000000000266e4d014" +
	"fffffffffffffffffffffffffffffffffffffffffffffffffffffffdd706aa5c"

func TestReadAccountMarginSummarySignExtendsNegativeValues(t *testing.T) {
	caller := &fakeCaller{responses: map[common.Address][]byte{
		AccountMarginSummaryPrecompile: mustHex(t, mainnetAccountMarginSummaryNegative),
	}}

	user := common.HexToAddress("0x4b07a01ef76a91929c12e510d641e1c267c5f5a9")
	margin, err := ReadAccountMarginSummary(context.Background(), caller, DefaultPerpDexIndex, user)
	require.NoError(t, err)

	require.Equal(t, int64(1_038_842_480), margin.AccountValue)
	require.Equal(t, uint64(1_031_620_610), margin.MarginUsed)
	require.Equal(t, uint64(10_316_206_100), margin.NtlPos)

	require.Equal(t, int64(-9_277_363_620), margin.RawUsd)

	require.Greater(t, margin.NtlPos, uint64(margin.AccountValue))

	require.Equal(t, margin.AccountValue, margin.RawUsd+int64(margin.NtlPos))
}

func TestReadAccountMarginSummaryDecodesMainnetShape(t *testing.T) {
	caller := &fakeCaller{responses: map[common.Address][]byte{
		AccountMarginSummaryPrecompile: mustHex(t, mainnetAccountMarginSummary),
	}}

	user := common.HexToAddress("0xdfc24b077bc1425ad1dea75bcb6f8158e10df303")
	margin, err := ReadAccountMarginSummary(context.Background(), caller, DefaultPerpDexIndex, user)
	require.NoError(t, err)

	require.Equal(t, int64(47_009_680_425_363), margin.AccountValue)
	require.Equal(t, uint64(477_867_397), margin.MarginUsed)
	require.Equal(t, uint64(0), margin.NtlPos)
	require.Equal(t, int64(47_009_680_425_363), margin.RawUsd)

	require.Equal(t, AccountMarginSummaryPrecompile, *caller.lastCall.To)
	require.Len(t, caller.lastCall.Data, 64)
	require.Equal(t, user.Bytes(), caller.lastCall.Data[44:64])
}

func TestReadCoreUserExists(t *testing.T) {
	yes := &fakeCaller{responses: map[common.Address][]byte{
		CoreUserExistsPrecompile: mustHex(t, "0000000000000000000000000000000000000000000000000000000000000001"),
	}}
	exists, err := ReadCoreUserExists(context.Background(), yes, common.Address{1})
	require.NoError(t, err)
	require.True(t, exists)

	no := &fakeCaller{responses: map[common.Address][]byte{
		CoreUserExistsPrecompile: mustHex(t, "0000000000000000000000000000000000000000000000000000000000000000"),
	}}
	exists, err = ReadCoreUserExists(context.Background(), no, common.Address{1})
	require.NoError(t, err)
	require.False(t, exists)
}

func TestReadWithdrawable(t *testing.T) {
	caller := &fakeCaller{responses: map[common.Address][]byte{
		WithdrawablePrecompile: mustHex(t, "00000000000000000000000000000000000000000000000000002ac12ed1d55e"),
	}}
	withdrawable, err := ReadWithdrawable(context.Background(), caller, common.Address{1})
	require.NoError(t, err)
	require.Equal(t, uint64(47_009_202_558_302), withdrawable)
}

func TestReadRawInputsAndUnsignedBoundaries(t *testing.T) {
	user := common.HexToAddress("0x1234567890123456789012345678901234567890")
	for _, test := range []struct {
		name          string
		target        common.Address
		input, output string
		read          func(EthCaller) error
	}{
		{"spotBalance", SpotBalancePrecompile,
			word("1234567890123456789012345678901234567890") + word("ffffffffffffffff"),
			word("ffffffffffffffff") + word("8000000000000000") + word("ffffffffffffffff"),
			func(c EthCaller) error {
				balance, err := ReadSpotBalance(context.Background(), c, user, math.MaxUint64)
				if err == nil {
					require.Equal(t, uint64(math.MaxUint64), balance.Total)
					require.Equal(t, uint64(1)<<63, balance.Hold)
				}
				return err
			}},
		{"accountMarginSummary", AccountMarginSummaryPrecompile,
			word("ffffffff") + word("1234567890123456789012345678901234567890"),
			"ffffffffffffffffffffffffffffffffffffffffffffffff8000000000000000" + word("ffffffffffffffff") + word("8000000000000000") + word("7fffffffffffffff"),
			func(c EthCaller) error {
				margin, err := ReadAccountMarginSummary(context.Background(), c, math.MaxUint32, user)
				if err == nil {
					require.Equal(t, int64(math.MinInt64), margin.AccountValue)
					require.Equal(t, uint64(math.MaxUint64), margin.MarginUsed)
					require.Equal(t, uint64(1)<<63, margin.NtlPos)
					require.Equal(t, int64(math.MaxInt64), margin.RawUsd)
				}
				return err
			}},
		{"withdrawable", WithdrawablePrecompile, word("1234567890123456789012345678901234567890"), word("ffffffffffffffff"),
			func(c EthCaller) error {
				amount, err := ReadWithdrawable(context.Background(), c, user)
				if err == nil {
					require.Equal(t, uint64(math.MaxUint64), amount)
				}
				return err
			}},
		{"coreUserExists", CoreUserExistsPrecompile, word("1234567890123456789012345678901234567890"), word("1"),
			func(c EthCaller) error { _, err := ReadCoreUserExists(context.Background(), c, user); return err }},
		{"tokenInfo", TokenInfoPrecompile, word("ffffffff"), mainnetTokenInfoUSDC,
			func(c EthCaller) error { _, err := ReadTokenInfo(context.Background(), c, math.MaxUint32); return err }},
	} {
		t.Run(test.name, func(t *testing.T) {
			caller := checkedCaller(func(_ context.Context, call ethereum.CallMsg, block *big.Int) ([]byte, error) {
				require.Equal(t, test.target, *call.To)
				require.Equal(t, mustHex(t, test.input), call.Data)
				require.Nil(t, block, "all reads use latest without inventing historical HyperCore provenance")
				return mustHex(t, test.output), nil
			})
			require.NoError(t, test.read(caller))
		})
	}
}

func TestMalformedPrecompileResponses(t *testing.T) {
	for _, test := range []struct {
		name  string
		valid string
		read  func(EthCaller) error
	}{
		{"spotBalance", mainnetSpotBalance, func(c EthCaller) error {
			_, err := ReadSpotBalance(context.Background(), c, common.Address{}, 0)
			return err
		}},
		{"accountMarginSummary", mainnetAccountMarginSummary, func(c EthCaller) error {
			_, err := ReadAccountMarginSummary(context.Background(), c, 0, common.Address{})
			return err
		}},
		{"withdrawable", word("1"), func(c EthCaller) error {
			_, err := ReadWithdrawable(context.Background(), c, common.Address{})
			return err
		}},
		{"coreUserExists", word("1"), func(c EthCaller) error {
			_, err := ReadCoreUserExists(context.Background(), c, common.Address{})
			return err
		}},
		{"tokenInfo", mainnetTokenInfoUSDC, func(c EthCaller) error { _, err := ReadTokenInfo(context.Background(), c, 0); return err }},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, out := range []string{"", test.valid[:len(test.valid)-2], test.valid + word("0"), "01" + test.valid[2:]} {
				caller := checkedCaller(func(context.Context, ethereum.CallMsg, *big.Int) ([]byte, error) { return mustHex(t, out), nil })
				require.ErrorContains(t, test.read(caller), test.name)
			}
			require.ErrorContains(t, test.read(nil), "caller is required")
		})
	}
	for _, invalid := range []string{word("2"), word("100")} {
		caller := &fakeCaller{responses: map[common.Address][]byte{CoreUserExistsPrecompile: mustHex(t, invalid)}}
		_, err := ReadCoreUserExists(context.Background(), caller, common.Address{})
		require.ErrorContains(t, err, "coreUserExists")
	}
	// An int64 negative value requires full 256-bit sign extension, and uint64
	// values must not carry bits outside their declared width.
	for _, invalid := range []string{
		word("ffffffffffffffff") + word("0") + word("0") + word("0"),
		word("0") + word("10000000000000000") + word("0") + word("0"),
	} {
		caller := &fakeCaller{responses: map[common.Address][]byte{AccountMarginSummaryPrecompile: mustHex(t, invalid)}}
		_, err := ReadAccountMarginSummary(context.Background(), caller, 0, common.Address{})
		require.ErrorContains(t, err, "accountMarginSummary")
	}
}
