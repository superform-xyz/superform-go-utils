// Package hypercore reads HyperCore precompiles through an injected HyperEVM
// caller. Values remain in the protocol's raw units; application policy,
// configuration, caching and decimal presentation belong to callers.
package hypercore

import (
	"bytes"
	"context"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
)

const (
	USDCTokenIndex                        = 0
	DefaultPerpDexIndex                   = uint32(0)
	NtlDecimals                           = 6
	UsdDecimals                           = NtlDecimals
	SpotBalancePrecompileAddress          = "0x0000000000000000000000000000000000000801"
	WithdrawablePrecompileAddress         = "0x0000000000000000000000000000000000000803"
	TokenInfoPrecompileAddress            = "0x000000000000000000000000000000000000080C"
	AccountMarginSummaryPrecompileAddress = "0x000000000000000000000000000000000000080F"
	CoreUserExistsPrecompileAddress       = "0x0000000000000000000000000000000000000810"
)

var (
	SpotBalancePrecompile          = common.HexToAddress(SpotBalancePrecompileAddress)
	WithdrawablePrecompile         = common.HexToAddress(WithdrawablePrecompileAddress)
	TokenInfoPrecompile            = common.HexToAddress(TokenInfoPrecompileAddress)
	AccountMarginSummaryPrecompile = common.HexToAddress(AccountMarginSummaryPrecompileAddress)
	CoreUserExistsPrecompile       = common.HexToAddress(CoreUserExistsPrecompileAddress)
)

// EthCaller is the EVM contract-call boundary needed by the reader.
type EthCaller interface {
	CallContract(context.Context, ethereum.CallMsg, *big.Int) ([]byte, error)
}

// SpotBalance contains amounts in the spot token's own wei units.
type SpotBalance struct {
	Total    uint64
	Hold     uint64
	EntryNtl uint64
}

// AccountMarginSummary contains signed account equity/raw USD and unsigned
// margin/notional values in protocol USD units (six decimal places).
type AccountMarginSummary struct {
	AccountValue int64
	MarginUsed   uint64
	NtlPos       uint64
	RawUsd       int64
}

// TokenInfo is the protocol metadata for a spot token.
type TokenInfo struct {
	Name                    string
	Spots                   []uint64
	DeployerTradingFeeShare uint64
	Deployer                common.Address
	// EvmContract may identify a gateway (as in the captured USDC metadata),
	// so callers must not assume it is the ERC-20 address.
	EvmContract         common.Address
	SzDecimals          uint8
	WeiDecimals         uint8
	EvmExtraWeiDecimals int8
}

var (
	spotBalanceInputs    = abi.Arguments{{Type: mustABIType("address", nil)}, {Type: mustABIType("uint64", nil)}}
	accountMarginInputs  = abi.Arguments{{Type: mustABIType("uint32", nil)}, {Type: mustABIType("address", nil)}}
	accountMarginOutputs = abi.Arguments{
		{Type: mustABIType("int64", nil)}, {Type: mustABIType("uint64", nil)},
		{Type: mustABIType("uint64", nil)}, {Type: mustABIType("int64", nil)},
	}
	addressInputs      = abi.Arguments{{Type: mustABIType("address", nil)}}
	uint64Outputs      = abi.Arguments{{Type: mustABIType("uint64", nil)}}
	boolOutputs        = abi.Arguments{{Type: mustABIType("bool", nil)}}
	spotBalanceOutputs = abi.Arguments{
		{Type: mustABIType("uint64", nil)}, {Type: mustABIType("uint64", nil)}, {Type: mustABIType("uint64", nil)},
	}
	tokenInfoInputs  = abi.Arguments{{Type: mustABIType("uint32", nil)}}
	tokenInfoOutputs = abi.Arguments{{Type: mustABIType("tuple", []abi.ArgumentMarshaling{
		{Name: "name", Type: "string"}, {Name: "spots", Type: "uint64[]"},
		{Name: "deployerTradingFeeShare", Type: "uint64"}, {Name: "deployer", Type: "address"},
		{Name: "evmContract", Type: "address"}, {Name: "szDecimals", Type: "uint8"},
		{Name: "weiDecimals", Type: "uint8"}, {Name: "evmExtraWeiDecimals", Type: "int8"},
	})}}
)

func mustABIType(name string, fields []abi.ArgumentMarshaling) abi.Type {
	typ, err := abi.NewType(name, "", fields)
	if err != nil {
		panic(fmt.Sprintf("hypercore: invalid ABI type %s: %v", name, err))
	}
	return typ
}

// ReadSpotBalance reads a spot balance at latest.
func ReadSpotBalance(ctx context.Context, caller EthCaller, user common.Address, token uint64) (SpotBalance, error) {
	out, err := readPrecompile(ctx, caller, "spotBalance", SpotBalancePrecompile, spotBalanceInputs, user, token)
	if err != nil {
		return SpotBalance{}, err
	}
	return decodeSpotBalance(out)
}

func decodeSpotBalance(out []byte) (SpotBalance, error) {
	values, err := unpackCanonical("spotBalance", spotBalanceOutputs, out, 3*32)
	if err != nil {
		return SpotBalance{}, err
	}
	return SpotBalance{Total: values[0].(uint64), Hold: values[1].(uint64), EntryNtl: values[2].(uint64)}, nil
}

// ReadAccountMarginSummary reads a margin summary at latest.
func ReadAccountMarginSummary(ctx context.Context, caller EthCaller, dex uint32, user common.Address) (AccountMarginSummary, error) {
	out, err := readPrecompile(ctx, caller, "accountMarginSummary", AccountMarginSummaryPrecompile, accountMarginInputs, dex, user)
	if err != nil {
		return AccountMarginSummary{}, err
	}
	return decodeAccountMarginSummary(out)
}

func decodeAccountMarginSummary(out []byte) (AccountMarginSummary, error) {
	values, err := unpackCanonical("accountMarginSummary", accountMarginOutputs, out, 4*32)
	if err != nil {
		return AccountMarginSummary{}, err
	}
	return AccountMarginSummary{
		AccountValue: values[0].(int64), MarginUsed: values[1].(uint64),
		NtlPos: values[2].(uint64), RawUsd: values[3].(int64),
	}, nil
}

// ReadWithdrawable reads withdrawable protocol USD units at latest.
func ReadWithdrawable(ctx context.Context, caller EthCaller, user common.Address) (uint64, error) {
	out, err := readPrecompile(ctx, caller, "withdrawable", WithdrawablePrecompile, addressInputs, user)
	if err != nil {
		return 0, err
	}
	return decodeWithdrawable(out)
}

func decodeWithdrawable(out []byte) (uint64, error) {
	values, err := unpackCanonical("withdrawable", uint64Outputs, out, 32)
	if err != nil {
		return 0, err
	}
	return values[0].(uint64), nil
}

// ReadCoreUserExists reports whether an account exists at latest.
func ReadCoreUserExists(ctx context.Context, caller EthCaller, user common.Address) (bool, error) {
	out, err := readPrecompile(ctx, caller, "coreUserExists", CoreUserExistsPrecompile, addressInputs, user)
	if err != nil {
		return false, err
	}
	values, err := unpackCanonical("coreUserExists", boolOutputs, out, 32)
	if err != nil {
		return false, err
	}
	return values[0].(bool), nil
}

// ReadTokenInfo reads one spot token's metadata at latest.
func ReadTokenInfo(ctx context.Context, caller EthCaller, token uint32) (TokenInfo, error) {
	out, err := readPrecompile(ctx, caller, "tokenInfo", TokenInfoPrecompile, tokenInfoInputs, token)
	if err != nil {
		return TokenInfo{}, err
	}
	values, err := unpackCanonical("tokenInfo", tokenInfoOutputs, out, 0)
	if err != nil {
		return TokenInfo{}, err
	}
	info, ok := abi.ConvertType(values[0], new(TokenInfo)).(*TokenInfo)
	if !ok || info == nil {
		return TokenInfo{}, fmt.Errorf("hypercore: tokenInfo output did not convert to the expected struct")
	}
	return *info, nil
}

func readPrecompile(ctx context.Context, caller EthCaller, name string, target common.Address, args abi.Arguments, values ...any) ([]byte, error) {
	input, err := args.Pack(values...)
	if err != nil {
		return nil, fmt.Errorf("hypercore: packing %s input: %w", name, err)
	}
	return callLatest(ctx, caller, name, target, input)
}

func callLatest(ctx context.Context, caller EthCaller, name string, target common.Address, input []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("hypercore: calling %s: %w", name, err)
	}
	if caller == nil {
		return nil, fmt.Errorf("hypercore: calling %s: EVM caller is required", name)
	}
	// A block tag selects EVM state; it does not prove historical HyperCore state.
	out, err := caller.CallContract(ctx, ethereum.CallMsg{To: &target, Data: input}, nil)
	if err != nil {
		return nil, fmt.Errorf("hypercore: calling %s: %w", name, err)
	}
	return out, nil
}

// Re-encoding rejects values geth can decode by truncating noncanonical high
// bits, incorrect sign extension, dynamic offsets and trailing response bytes.
func unpackCanonical(name string, args abi.Arguments, out []byte, size int) ([]any, error) {
	if size > 0 && len(out) != size {
		return nil, fmt.Errorf("hypercore: decoding %s output: got %d bytes, want %d", name, len(out), size)
	}
	values, err := args.Unpack(out)
	if err != nil {
		return nil, fmt.Errorf("hypercore: decoding %s output: %w", name, err)
	}
	canonical, err := args.Pack(values...)
	if err != nil {
		return nil, fmt.Errorf("hypercore: validating %s output: %w", name, err)
	}
	if !bytes.Equal(canonical, out) {
		return nil, fmt.Errorf("hypercore: decoding %s output: noncanonical ABI encoding", name)
	}
	return values, nil
}
