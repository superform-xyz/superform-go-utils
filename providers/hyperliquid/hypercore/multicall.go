package hypercore

import (
	"context"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/superform-xyz/superform-go-utils/evm/multicall3"
)

// Multicall3Address is the canonical Multicall3 deployment.
var Multicall3Address = common.HexToAddress(multicall3.CanonicalAddress)

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

// ReadSpotBalances reads one user's spot balances for several tokens in one EVM
// execution at latest, returned in token order. A failed or incomplete
// aggregate returns an error without falling back to separate reads.
func ReadSpotBalances(ctx context.Context, caller EthCaller, user common.Address, tokens ...uint64) ([]SpotBalance, error) {
	// Keep per-token failure diagnostics for the original spot-only reader.
	snapshot, err := readSpotBalanceSnapshot(ctx, caller, user, tokens, false, true)
	return snapshot.Balances, err
}

// SpotBalanceSnapshot holds spot balances in request order and, when requested,
// perp withdrawable in raw six-decimal USD units.
type SpotBalanceSnapshot struct {
	Balances     []SpotBalance
	Withdrawable uint64
}

// ReadSpotBalanceSnapshot reads one user's spot balances and optional perp
// withdrawable in a single latest EVM execution. A failed subcall reverts the
// aggregate. An incomplete or malformed result invalidates the entire snapshot.
func ReadSpotBalanceSnapshot(ctx context.Context, caller EthCaller, user common.Address, tokens []uint64, withWithdrawable bool) (SpotBalanceSnapshot, error) {
	return readSpotBalanceSnapshot(ctx, caller, user, tokens, withWithdrawable, false)
}

func readSpotBalanceSnapshot(ctx context.Context, caller EthCaller, user common.Address, tokens []uint64, withWithdrawable, allowFailure bool) (SpotBalanceSnapshot, error) {
	if len(tokens) == 0 {
		return SpotBalanceSnapshot{}, fmt.Errorf("hypercore: reading spot balances requires at least one token")
	}
	names := make([]string, len(tokens), len(tokens)+1)
	calls := make([]multicall3.Call, len(tokens), len(tokens)+1)
	for i, token := range tokens {
		input, err := spotBalanceInputs.Pack(user, token)
		if err != nil {
			return SpotBalanceSnapshot{}, fmt.Errorf("hypercore: packing spotBalance input: %w", err)
		}
		names[i] = fmt.Sprintf("spotBalance(token %d)", token)
		calls[i] = multicall3.Call{Target: SpotBalancePrecompile, AllowFailure: allowFailure, CallData: input}
	}
	if withWithdrawable {
		input, err := addressInputs.Pack(user)
		if err != nil {
			return SpotBalanceSnapshot{}, fmt.Errorf("hypercore: packing withdrawable input: %w", err)
		}
		names = append(names, "withdrawable")
		calls = append(calls, multicall3.Call{Target: WithdrawablePrecompile, CallData: input})
	}
	outputs, err := aggregate3(ctx, caller, names, calls)
	if err != nil {
		return SpotBalanceSnapshot{}, err
	}
	snapshot := SpotBalanceSnapshot{Balances: make([]SpotBalance, len(tokens))}
	for i := range tokens {
		if snapshot.Balances[i], err = decodeSpotBalance(outputs[i]); err != nil {
			return SpotBalanceSnapshot{}, err
		}
	}
	if withWithdrawable {
		if snapshot.Withdrawable, err = decodeWithdrawable(outputs[len(tokens)]); err != nil {
			return SpotBalanceSnapshot{}, err
		}
	}
	return snapshot, nil
}

// ReadTokenInfos reads several spot tokens' metadata in one EVM execution at
// latest, returned in token order. A failed or incomplete aggregate returns an
// error without falling back to separate reads.
func ReadTokenInfos(ctx context.Context, caller EthCaller, tokens []uint32) ([]TokenInfo, error) {
	if len(tokens) == 0 {
		return nil, fmt.Errorf("hypercore: reading token info requires at least one token")
	}
	names := make([]string, len(tokens))
	calls := make([]multicall3.Call, len(tokens))
	for i, token := range tokens {
		input, err := tokenInfoInputs.Pack(token)
		if err != nil {
			return nil, fmt.Errorf("hypercore: packing tokenInfo input: %w", err)
		}
		names[i] = fmt.Sprintf("tokenInfo(token %d)", token)
		calls[i] = multicall3.Call{Target: TokenInfoPrecompile, CallData: input}
	}
	outputs, err := aggregate3(ctx, caller, names, calls)
	if err != nil {
		return nil, err
	}
	infos := make([]TokenInfo, len(tokens))
	for i, token := range tokens {
		if infos[i], err = decodeTokenInfo(outputs[i]); err != nil {
			return nil, fmt.Errorf("hypercore: token %d: %w", token, err)
		}
	}
	return infos, nil
}

func readMarginPair(ctx context.Context, caller EthCaller, dex uint32, user common.Address, secondName string, secondTarget common.Address, secondInput []byte) (AccountMarginSummary, []byte, error) {
	marginInput, err := accountMarginInputs.Pack(dex, user)
	if err != nil {
		return AccountMarginSummary{}, nil, fmt.Errorf("hypercore: packing accountMarginSummary input: %w", err)
	}
	outputs, err := aggregate3(ctx, caller, []string{"accountMarginSummary", secondName}, []multicall3.Call{
		{Target: AccountMarginSummaryPrecompile, AllowFailure: true, CallData: marginInput},
		{Target: secondTarget, AllowFailure: true, CallData: secondInput},
	})
	if err != nil {
		return AccountMarginSummary{}, nil, err
	}
	margin, err := decodeAccountMarginSummary(outputs[0])
	if err != nil {
		return AccountMarginSummary{}, nil, err
	}
	return margin, outputs[1], nil
}

// aggregate3 runs calls in one EVM execution at latest and returns each
// subcall's return data in call order. A failed subcall, or a result count that
// differs from the calls, is an error rather than a partial result.
func aggregate3(ctx context.Context, caller EthCaller, names []string, calls []multicall3.Call) ([][]byte, error) {
	contractABI, err := multicall3.Multicall3MetaData.GetAbi()
	if err != nil {
		return nil, fmt.Errorf("hypercore: loading Multicall3 ABI: %w", err)
	}
	data, err := contractABI.Pack("aggregate3", calls)
	if err != nil {
		return nil, fmt.Errorf("hypercore: packing aggregate3 input: %w", err)
	}
	out, err := callLatest(ctx, caller, "aggregate3", Multicall3Address, data)
	if err != nil {
		return nil, err
	}
	method := contractABI.Methods["aggregate3"]
	values, err := unpackCanonical("aggregate3", method.Outputs, out, 0)
	if err != nil {
		return nil, err
	}
	var results []multicall3.Result
	if err := method.Outputs.Copy(&results, values); err != nil {
		return nil, fmt.Errorf("hypercore: decoding aggregate3 results: %w", err)
	}
	if len(results) != len(calls) {
		return nil, fmt.Errorf("hypercore: aggregate3 returned %d results, want %d", len(results), len(calls))
	}
	outputs := make([][]byte, len(calls))
	for i, result := range results {
		if !result.Success {
			return nil, fmt.Errorf("hypercore: %s subcall failed inside aggregate3", names[i])
		}
		outputs[i] = result.ReturnData
	}
	return outputs, nil
}
