// Package multicall3 batches EVM reads through the Multicall3 contract.
package multicall3

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
)

// CanonicalAddress is the common Multicall3 deployment address.
const CanonicalAddress = "0xcA11bde05977b3631167028862bE2a173976CA11"

// ErrUnavailable identifies failures to execute or decode the aggregate.
var ErrUnavailable = errors.New("multicall3 unavailable")

// Call describes one target and its calldata within an aggregate3 request.
type Call = Multicall3Call3

// Result contains the success flag and return data for one call.
type Result = Multicall3Result

// Caller executes aggregate3 as an eth_call.
type Caller interface {
	Aggregate3(opts *bind.CallOpts, calls []Call) ([]Result, error)
}

type caller struct {
	raw Multicall3CallerRaw
}

// NewCaller binds a read-only Multicall3 caller at address.
func NewCaller(address common.Address, backend bind.ContractCaller) (Caller, error) {
	contract, err := NewMulticall3Caller(address, backend)
	if err != nil {
		return nil, fmt.Errorf("binding Multicall3: %w", err)
	}
	return &caller{raw: Multicall3CallerRaw{Contract: contract}}, nil
}

func (c *caller) Aggregate3(opts *bind.CallOpts, calls []Call) ([]Result, error) {
	var results []Result
	out := []any{&results}
	if err := c.raw.Call(opts, &out, "aggregate3", calls); err != nil {
		return nil, fmt.Errorf("calling aggregate3: %w", err)
	}
	return results, nil
}

// Aggregate executes calls at the latest block and returns their results in order.
func Aggregate(ctx context.Context, caller Caller, calls []Call) ([]Result, error) {
	return AggregateAt(ctx, caller, nil, calls)
}

// AggregateAt executes calls at a copy of block. A nil block reads the latest head.
func AggregateAt(ctx context.Context, caller Caller, block *big.Int, calls []Call) ([]Result, error) {
	if len(calls) == 0 {
		return nil, nil
	}
	if caller == nil {
		return nil, fmt.Errorf("%w: nil caller", ErrUnavailable)
	}

	opts := &bind.CallOpts{Context: ctx}
	if block != nil {
		opts.BlockNumber = new(big.Int).Set(block)
	}
	results, err := caller.Aggregate3(opts, calls)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if len(results) != len(calls) {
		return nil, fmt.Errorf("%w: aggregate3 returned %d results for %d calls", ErrUnavailable, len(results), len(calls))
	}
	return results, nil
}
