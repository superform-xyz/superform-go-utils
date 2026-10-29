package hypercore

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
)

type checkedCaller func(context.Context, ethereum.CallMsg, *big.Int) ([]byte, error)

func (f checkedCaller) CallContract(ctx context.Context, call ethereum.CallMsg, block *big.Int) ([]byte, error) {
	return f(ctx, call, block)
}

// These fixtures encode the ABI layout directly rather than using the SDK's
// argument definitions, so target order, selector and tuple offsets are checked
// independently of the implementation.
func word(value string) string { return strings.Repeat("0", 64-len(value)) + value }

func aggregateResponse(firstSuccess, secondSuccess bool, first, second string) string {
	firstFlag, secondFlag := "0", "0"
	if firstSuccess {
		firstFlag = "1"
	}
	if secondSuccess {
		secondFlag = "1"
	}
	firstTuple := word(firstFlag) + word("40") + word(fmt.Sprintf("%x", len(first)/2)) + first
	secondTuple := word(secondFlag) + word("40") + word(fmt.Sprintf("%x", len(second)/2)) + second
	return word("20") + word("2") + word("40") + word(fmt.Sprintf("%x", 64+len(firstTuple)/2)) + firstTuple + secondTuple
}

func TestAtomicBundlesUseOneLatestExecution(t *testing.T) {
	user := common.HexToAddress("0x1111111111111111111111111111111111111111")
	marginInput := word("7") + word(strings.TrimPrefix(user.Hex(), "0x"))
	marginCall := word("80f") + word("1") + word("60") + word("40") + marginInput
	maxUint := word("ffffffffffffffff")
	for _, test := range []struct {
		name   string
		target string
		input  string
		output string
		read   func(context.Context, EthCaller) error
	}{
		{
			name: "margin and spot", target: "801",
			input: word(strings.TrimPrefix(user.Hex(), "0x")) + word("10000002a"), output: mainnetSpotBalance,
			read: func(ctx context.Context, caller EthCaller) error {
				pair, err := ReadMarginAndSpot(ctx, caller, 7, user, uint64(1)<<32|42)
				if err == nil {
					require.Equal(t, int64(-9_277_363_620), pair.Margin.RawUsd)
					require.Equal(t, uint64(0x1d10f2d0), pair.Spot.Total)
				}
				return err
			},
		},
		{
			name: "margin and withdrawable", target: "803",
			input: word(strings.TrimPrefix(user.Hex(), "0x")), output: maxUint,
			read: func(ctx context.Context, caller EthCaller) error {
				pair, err := ReadPerpSnapshot(ctx, caller, 7, user)
				if err == nil {
					require.Equal(t, int64(-9_277_363_620), pair.Margin.RawUsd)
					require.Equal(t, uint64(math.MaxUint64), pair.Withdrawable)
				}
				return err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			secondCall := word(test.target) + word("1") + word("60") + word(fmt.Sprintf("%x", len(test.input)/2)) + test.input
			expectedInput := "82ad56cb" + word("20") + word("2") + word("40") + word("100") + marginCall + secondCall
			calls := 0
			caller := checkedCaller(func(_ context.Context, call ethereum.CallMsg, block *big.Int) ([]byte, error) {
				calls++
				require.Equal(t, common.HexToAddress("0xcA11bde05977b3631167028862bE2a173976CA11"), *call.To)
				require.Nil(t, block)
				require.Equal(t, mustHex(t, expectedInput), call.Data)
				return mustHex(t, aggregateResponse(true, true, mainnetAccountMarginSummaryNegative, test.output)), nil
			})
			require.NoError(t, test.read(context.Background(), caller))
			require.Equal(t, 1, calls)
		})
	}
}

func TestAtomicBundlesRejectIncompleteOrMalformedPairsWithoutFallback(t *testing.T) {
	for _, bundle := range []struct {
		name       string
		second     string
		secondName string
		read       func(EthCaller) error
	}{
		{"spot", mainnetSpotBalance, "spotBalance", func(c EthCaller) error {
			_, err := ReadMarginAndSpot(context.Background(), c, 0, common.Address{}, 0)
			return err
		}},
		{"withdrawable", word("2a"), "withdrawable", func(c EthCaller) error {
			_, err := ReadPerpSnapshot(context.Background(), c, 0, common.Address{})
			return err
		}},
	} {
		t.Run(bundle.name, func(t *testing.T) {
			for _, test := range []struct{ name, out, want string }{
				{"margin subcall", aggregateResponse(false, true, "", bundle.second), "accountMarginSummary"},
				{"second subcall", aggregateResponse(true, false, mainnetAccountMarginSummary, ""), bundle.secondName},
				{"short margin", aggregateResponse(true, true, word("1"), bundle.second), "accountMarginSummary"},
				{"short second", aggregateResponse(true, true, mainnetAccountMarginSummary, ""), bundle.secondName},
				{"trailing margin", aggregateResponse(true, true, mainnetAccountMarginSummary+word("0"), bundle.second), "accountMarginSummary"},
				{"trailing aggregate", aggregateResponse(true, true, mainnetAccountMarginSummary, bundle.second) + word("0"), "aggregate3"},
				{"empty aggregate", "", "aggregate3"},
				{"zero results", word("20") + word("0"), "returned 0 results, want 2"},
				{"malformed offset", word("ffff"), "aggregate3"},
			} {
				t.Run(test.name, func(t *testing.T) {
					calls := 0
					caller := checkedCaller(func(context.Context, ethereum.CallMsg, *big.Int) ([]byte, error) {
						calls++
						return mustHex(t, test.out), nil
					})
					require.ErrorContains(t, bundle.read(caller), test.want)
					require.Equal(t, 1, calls, "failed pairs must not trigger separate fallback reads")
				})
			}
			cause := errors.New("RPC unavailable")
			caller := checkedCaller(func(context.Context, ethereum.CallMsg, *big.Int) ([]byte, error) { return nil, cause })
			require.ErrorIs(t, bundle.read(caller), cause)
		})
	}
}

func TestReadCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	caller := checkedCaller(func(context.Context, ethereum.CallMsg, *big.Int) ([]byte, error) {
		t.Fatal("a cancelled read must not call the RPC")
		return nil, nil
	})
	_, err := ReadPerpSnapshot(ctx, caller, 0, common.Address{})
	require.ErrorIs(t, err, context.Canceled)
	_, err = ReadSpotBalance(ctx, caller, common.Address{}, 0)
	require.ErrorIs(t, err, context.Canceled)

	ctx, cancel = context.WithCancel(context.Background())
	caller = checkedCaller(func(received context.Context, _ ethereum.CallMsg, _ *big.Int) ([]byte, error) {
		require.Equal(t, ctx, received)
		cancel()
		return nil, received.Err()
	})
	_, err = ReadPerpSnapshot(ctx, caller, 0, common.Address{})
	require.ErrorIs(t, err, context.Canceled)
}
