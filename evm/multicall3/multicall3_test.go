package multicall3_test

import (
	"context"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/superform-xyz/superform-go-utils/evm/multicall3"
	multicall3mock "github.com/superform-xyz/superform-go-utils/evm/multicall3/mocks"
)

var _ multicall3.Caller = (*multicall3mock.MockCaller)(nil)

type ethCall func(context.Context, ethereum.CallMsg, *big.Int) ([]byte, error)

func (call ethCall) CallContract(ctx context.Context, msg ethereum.CallMsg, block *big.Int) ([]byte, error) {
	return call(ctx, msg, block)
}

func (ethCall) CodeAt(context.Context, common.Address, *big.Int) ([]byte, error) {
	return nil, errors.New("unexpected CodeAt")
}

func TestAggregateReturnsNilForEmptyCalls(t *testing.T) {
	results, err := multicall3.Aggregate(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Aggregate() error = %v", err)
	}
	if results != nil {
		t.Fatalf("Aggregate() results = %#v, want nil", results)
	}
}

func TestAggregateRejectsNilCaller(t *testing.T) {
	_, err := multicall3.Aggregate(context.Background(), nil, []multicall3.Call{
		{Target: common.HexToAddress("0x1111111111111111111111111111111111111111")},
	})
	if !errors.Is(err, multicall3.ErrUnavailable) {
		t.Fatalf("Aggregate() error = %v, want ErrUnavailable", err)
	}
}

func TestAggregateLeavesBlockUnset(t *testing.T) {
	ctx := t.Context()
	calls := []multicall3.Call{
		{
			Target:       common.HexToAddress("0x1111111111111111111111111111111111111111"),
			AllowFailure: true,
			CallData:     []byte{0x01, 0x02},
		},
		{
			Target:       common.HexToAddress("0x2222222222222222222222222222222222222222"),
			AllowFailure: false,
			CallData:     []byte{0x03, 0x04},
		},
	}
	want := []multicall3.Result{
		{Success: false, ReturnData: []byte{0xaa}},
		{Success: true, ReturnData: []byte{0xbb}},
	}
	contractABI, err := multicall3.Multicall3MetaData.GetAbi()
	require.NoError(t, err)
	input, err := contractABI.Pack("aggregate3", calls)
	require.NoError(t, err)
	output, err := contractABI.Methods["aggregate3"].Outputs.Pack(want)
	require.NoError(t, err)
	address := common.HexToAddress("0x1234")
	caller, err := multicall3.NewCaller(address, ethCall(func(gotCtx context.Context, msg ethereum.CallMsg, block *big.Int) ([]byte, error) {
		require.Equal(t, ctx, gotCtx)
		require.Nil(t, block)
		require.Equal(t, &address, msg.To)
		require.Equal(t, input, msg.Data)
		return output, nil
	}))
	require.NoError(t, err)

	results, err := multicall3.Aggregate(ctx, caller, calls)
	require.NoError(t, err)
	require.Equal(t, want, results)
}

func TestAggregateAtUsesPinnedBlock(t *testing.T) {
	ctx := t.Context()
	caller := multicall3mock.NewMockCaller(t)
	block := big.NewInt(42)
	calls := []multicall3.Call{{Target: common.HexToAddress("0x1")}}
	var received *big.Int
	caller.EXPECT().Aggregate3(&bind.CallOpts{Context: ctx, BlockNumber: big.NewInt(42)}, calls).
		Run(func(opts *bind.CallOpts, _ []multicall3.Call) { received = opts.BlockNumber }).
		Return([]multicall3.Result{{Success: true}}, nil)

	_, err := multicall3.AggregateAt(ctx, caller, block, calls)
	require.NoError(t, err)
	block.SetInt64(7)
	require.Equal(t, big.NewInt(42), received)
}

func TestAggregateWrapsCallerError(t *testing.T) {
	caller := multicall3mock.NewMockCaller(t)
	callerErr := errors.New("eth call failed")
	caller.EXPECT().
		Aggregate3(mock.Anything, mock.Anything).
		Return(nil, callerErr)

	_, err := multicall3.Aggregate(context.Background(), caller, []multicall3.Call{
		{Target: common.HexToAddress("0x1111111111111111111111111111111111111111")},
	})
	if !errors.Is(err, multicall3.ErrUnavailable) {
		t.Fatalf("Aggregate() error = %v, want ErrUnavailable", err)
	}
	if !errors.Is(err, callerErr) {
		t.Fatalf("Aggregate() error = %v, want caller error", err)
	}
}

func TestAggregateRejectsResultCountMismatch(t *testing.T) {
	caller := multicall3mock.NewMockCaller(t)
	caller.EXPECT().
		Aggregate3(mock.Anything, mock.Anything).
		Return([]multicall3.Result{
			{Success: true, ReturnData: []byte{0xaa}},
		}, nil)

	_, err := multicall3.Aggregate(context.Background(), caller, []multicall3.Call{
		{Target: common.HexToAddress("0x1111111111111111111111111111111111111111")},
		{Target: common.HexToAddress("0x2222222222222222222222222222222222222222")},
	})
	if !errors.Is(err, multicall3.ErrUnavailable) {
		t.Fatalf("Aggregate() error = %v, want ErrUnavailable", err)
	}
}
