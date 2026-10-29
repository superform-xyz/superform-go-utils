package hypercore

import (
	"context"
	"fmt"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// Multicall3Address is the canonical Multicall3 deployment.
var Multicall3Address = common.HexToAddress("0xcA11bde05977b3631167028862bE2a173976CA11")

type multicallCall struct {
	Target       common.Address
	AllowFailure bool
	CallData     []byte
}

type multicallResult struct {
	Success    bool
	ReturnData []byte
}

var (
	aggregate3Selector = crypto.Keccak256([]byte("aggregate3((address,bool,bytes)[])"))[:4]
	aggregate3Inputs   = abi.Arguments{{Type: mustABIType("tuple[]", []abi.ArgumentMarshaling{
		{Name: "target", Type: "address"}, {Name: "allowFailure", Type: "bool"}, {Name: "callData", Type: "bytes"},
	})}}
	aggregate3Outputs = abi.Arguments{{Type: mustABIType("tuple[]", []abi.ArgumentMarshaling{
		{Name: "success", Type: "bool"}, {Name: "returnData", Type: "bytes"},
	})}}
)

// MarginAndSpot contains two values observed in one EVM execution at latest.
// It carries no historical HyperCore block provenance.
type MarginAndSpot struct {
	Margin AccountMarginSummary
	Spot   SpotBalance
}

// PerpSnapshot contains two values observed in one EVM execution at latest.
type PerpSnapshot struct {
	Margin       AccountMarginSummary
	Withdrawable uint64
}

// ReadMarginAndSpot reads a strict atomic pair. A failed or incomplete
// aggregate returns an error without falling back to separate reads.
func ReadMarginAndSpot(ctx context.Context, caller EthCaller, dex uint32, user common.Address, token uint64) (MarginAndSpot, error) {
	secondInput, err := spotBalanceInputs.Pack(user, token)
	if err != nil {
		return MarginAndSpot{}, fmt.Errorf("hypercore: packing spotBalance input: %w", err)
	}
	margin, second, err := readMarginPair(ctx, caller, dex, user, "spotBalance", SpotBalancePrecompile, secondInput)
	if err != nil {
		return MarginAndSpot{}, err
	}
	spot, err := decodeSpotBalance(second)
	if err != nil {
		return MarginAndSpot{}, err
	}
	return MarginAndSpot{Margin: margin, Spot: spot}, nil
}

// ReadPerpSnapshot reads margin and withdrawable in one EVM execution at
// latest, rejecting partial responses and separate-read fallback.
func ReadPerpSnapshot(ctx context.Context, caller EthCaller, dex uint32, user common.Address) (PerpSnapshot, error) {
	secondInput, err := addressInputs.Pack(user)
	if err != nil {
		return PerpSnapshot{}, fmt.Errorf("hypercore: packing withdrawable input: %w", err)
	}
	margin, second, err := readMarginPair(ctx, caller, dex, user, "withdrawable", WithdrawablePrecompile, secondInput)
	if err != nil {
		return PerpSnapshot{}, err
	}
	withdrawable, err := decodeWithdrawable(second)
	if err != nil {
		return PerpSnapshot{}, err
	}
	return PerpSnapshot{Margin: margin, Withdrawable: withdrawable}, nil
}

func readMarginPair(ctx context.Context, caller EthCaller, dex uint32, user common.Address, secondName string, secondTarget common.Address, secondInput []byte) (AccountMarginSummary, []byte, error) {
	marginInput, err := accountMarginInputs.Pack(dex, user)
	if err != nil {
		return AccountMarginSummary{}, nil, fmt.Errorf("hypercore: packing accountMarginSummary input: %w", err)
	}
	input, err := aggregate3Inputs.Pack([]multicallCall{
		{Target: AccountMarginSummaryPrecompile, AllowFailure: true, CallData: marginInput},
		{Target: secondTarget, AllowFailure: true, CallData: secondInput},
	})
	if err != nil {
		return AccountMarginSummary{}, nil, fmt.Errorf("hypercore: packing aggregate3 input: %w", err)
	}
	data := append(append([]byte{}, aggregate3Selector...), input...)
	out, err := callLatest(ctx, caller, "aggregate3", Multicall3Address, data)
	if err != nil {
		return AccountMarginSummary{}, nil, err
	}
	values, err := unpackCanonical("aggregate3", aggregate3Outputs, out, 0)
	if err != nil {
		return AccountMarginSummary{}, nil, err
	}
	results, ok := abi.ConvertType(values[0], new([]multicallResult)).(*[]multicallResult)
	if !ok || results == nil {
		return AccountMarginSummary{}, nil, fmt.Errorf("hypercore: aggregate3 output is not a result array")
	}
	if len(*results) != 2 {
		return AccountMarginSummary{}, nil, fmt.Errorf("hypercore: aggregate3 returned %d results, want 2", len(*results))
	}
	for index, name := range []string{"accountMarginSummary", secondName} {
		if !(*results)[index].Success {
			return AccountMarginSummary{}, nil, fmt.Errorf("hypercore: %s subcall failed inside aggregate3", name)
		}
	}
	margin, err := decodeAccountMarginSummary((*results)[0].ReturnData)
	if err != nil {
		return AccountMarginSummary{}, nil, err
	}
	return margin, (*results)[1].ReturnData, nil
}
