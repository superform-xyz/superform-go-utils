package utils

import (
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUint64ArrayType(t *testing.T) {
	abiType := Uint64ArrayType()
	assert.Equal(t, "uint64[]", abiType.String())
}

/* -------------------------------------------------------------------------- */
/*                                Encoder Tests                               */
/* -------------------------------------------------------------------------- */

func TestEncodeABI(t *testing.T) {
	tests := []struct {
		name     string
		types    []abi.Type
		values   []interface{}
		wantErr  bool
		expected []byte
	}{
		{
			name:     "empty input",
			types:    []abi.Type{},
			values:   []interface{}{},
			wantErr:  false,
			expected: []byte{},
		},
		{
			name: "single uint256",
			types: []abi.Type{
				Uint256Type(),
			},
			values: []interface{}{
				big.NewInt(42),
			},
			wantErr: false,
			expected: common.Hex2Bytes(
				"000000000000000000000000000000000000000000000000000000000000002a",
			),
		},
		{
			name: "address and uint256",
			types: []abi.Type{
				AddressType(),
				Uint256Type(),
			},
			values: []interface{}{
				common.HexToAddress("0x1234567890123456789012345678901234567890"),
				big.NewInt(100),
			},
			wantErr: false,
			expected: common.Hex2Bytes(
				"0000000000000000000000001234567890123456789012345678901234567890" +
					"0000000000000000000000000000000000000000000000000000000000000064",
			),
		},
		{
			name: "string and bool",
			types: []abi.Type{
				StringType(),
				BoolType(),
			},
			values: []interface{}{
				"test",
				true,
			},
			wantErr: false,
			expected: common.Hex2Bytes(
				"0000000000000000000000000000000000000000000000000000000000000040" +
					"0000000000000000000000000000000000000000000000000000000000000001" +
					"0000000000000000000000000000000000000000000000000000000000000004" +
					"7465737400000000000000000000000000000000000000000000000000000000",
			),
		},
		{
			name: "bytes32 and uint48",
			types: []abi.Type{
				Bytes32Type(),
				Uint48Type(),
			},
			values: []interface{}{
				[32]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32},
				big.NewInt(12345),
			},
			wantErr: false,
			expected: common.Hex2Bytes(
				"0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20" +
					"0000000000000000000000000000000000000000000000000000000000003039",
			),
		},
		{
			name: "mismatched types and values length",
			types: []abi.Type{
				Uint256Type(),
				AddressType(),
			},
			values: []interface{}{
				big.NewInt(1),
				// Missing second value
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := EncodeABI(tt.types, tt.values)

			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestABIMethodByName(t *testing.T) {
	parsed, err := abi.JSON(strings.NewReader(`[
		{
			"type": "function",
			"name": "approve",
			"inputs": [
				{"name": "spender", "type": "address"},
				{"name": "amount", "type": "uint256"}
			],
			"outputs": [{"name": "", "type": "bool"}],
			"stateMutability": "nonpayable"
		}
	]`))
	require.NoError(t, err)

	method, err := ABIMethodByName(&parsed, "approve")
	require.NoError(t, err)
	assert.Equal(t, "approve", method.Name)

	_, err = ABIMethodByName(&parsed, "transfer")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ABI method transfer not found")
}

func TestEncodeABIMethodCall(t *testing.T) {
	parsed, err := abi.JSON(strings.NewReader(`[
		{
			"type": "function",
			"name": "approve",
			"inputs": [
				{"name": "spender", "type": "address"},
				{"name": "amount", "type": "uint256"}
			],
			"outputs": [{"name": "", "type": "bool"}],
			"stateMutability": "nonpayable"
		}
	]`))
	require.NoError(t, err)

	method, err := ABIMethodByName(&parsed, "approve")
	require.NoError(t, err)

	result, err := EncodeABIMethodCall(
		method,
		common.HexToAddress("0x1234567890123456789012345678901234567890"),
		big.NewInt(100),
	)
	require.NoError(t, err)

	assert.Equal(t, common.Hex2Bytes(
		"095ea7b3"+
			"0000000000000000000000001234567890123456789012345678901234567890"+
			"0000000000000000000000000000000000000000000000000000000000000064",
	), result)

	_, err = EncodeABIMethodCall(method, common.HexToAddress("0x1234567890123456789012345678901234567890"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "encoding ABI method approve inputs")
}

func TestDecodeABIMethodCall(t *testing.T) {
	parsed, err := abi.JSON(strings.NewReader(`[
		{
			"type": "function",
			"name": "approve",
			"inputs": [
				{"name": "spender", "type": "address"},
				{"name": "amount", "type": "uint256"}
			],
			"outputs": [{"name": "", "type": "bool"}],
			"stateMutability": "nonpayable"
		}
	]`))
	require.NoError(t, err)

	method, err := ABIMethodByName(&parsed, "approve")
	require.NoError(t, err)

	spender := common.HexToAddress("0x1234567890123456789012345678901234567890")
	amount := big.NewInt(100)
	data, err := EncodeABIMethodCall(method, spender, amount)
	require.NoError(t, err)

	inputs, err := DecodeABIMethodCall(method, data)
	require.NoError(t, err)
	require.Len(t, inputs, 2)
	assert.Equal(t, spender, inputs[0])
	assert.Equal(t, 0, amount.Cmp(inputs[1].(*big.Int)))

	_, err = DecodeABIMethodCall(method, append([]byte{0, 0, 0, 0}, data[len(method.ID):]...))
	require.ErrorContains(t, err, "want approve selector")

	_, err = DecodeABIMethodCall(method, data[:len(data)-1])
	require.ErrorContains(t, err, "unpacking approve inputs")
}

func TestDecodeABIOutputBigInt(t *testing.T) {
	parsed, err := abi.JSON(strings.NewReader(`[
		{
			"type": "function",
			"name": "quote",
			"inputs": [],
			"outputs": [
				{"name": "amountOut", "type": "uint256"},
				{"name": "gasEstimate", "type": "uint256"}
			],
			"stateMutability": "view"
		}
	]`))
	require.NoError(t, err)

	method, err := ABIMethodByName(&parsed, "quote")
	require.NoError(t, err)

	data, err := method.Outputs.Pack(big.NewInt(42), big.NewInt(99))
	require.NoError(t, err)

	result, err := DecodeABIOutput[*big.Int](method, data, 0)
	require.NoError(t, err)
	assert.Equal(t, 0, result.Cmp(big.NewInt(42)))

	result, err = DecodeABIOutput[*big.Int](method, data, 1)
	require.NoError(t, err)
	assert.Equal(t, 0, result.Cmp(big.NewInt(99)))

	_, err = DecodeABIOutput[*big.Int](method, data, 2)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "output index 2 out of range")
}

func TestDecodeABIOutputAddress(t *testing.T) {
	parsed, err := abi.JSON(strings.NewReader(`[
		{
			"type": "function",
			"name": "getPool",
			"inputs": [],
			"outputs": [{"name": "pool", "type": "address"}],
			"stateMutability": "view"
		}
	]`))
	require.NoError(t, err)

	method, err := ABIMethodByName(&parsed, "getPool")
	require.NoError(t, err)

	expected := common.HexToAddress("0x1234567890123456789012345678901234567890")
	data, err := method.Outputs.Pack(expected)
	require.NoError(t, err)

	result, err := DecodeABIOutput[common.Address](method, data, 0)
	require.NoError(t, err)
	assert.Equal(t, expected, result)
}

func TestTypeHelpers(t *testing.T) {
	tests := []struct {
		name     string
		actual   abi.Type
		expected abi.Type
	}{
		{
			name:     "Uint8Type",
			actual:   Uint8Type(),
			expected: mustNewType(t, "uint8"),
		},
		{
			name:     "Uint256Type",
			actual:   Uint256Type(),
			expected: mustNewType(t, "uint256"),
		},
		{
			name:     "Uint256ArrayType",
			actual:   Uint256ArrayType(),
			expected: mustNewType(t, "uint256[]"),
		},
		{
			name:     "AddressType",
			actual:   AddressType(),
			expected: mustNewType(t, "address"),
		},
		{
			name:     "AddressArrayType",
			actual:   AddressArrayType(),
			expected: mustNewType(t, "address[]"),
		},
		{
			name:     "StringType",
			actual:   StringType(),
			expected: mustNewType(t, "string"),
		},
		{
			name:     "Bytes32Type",
			actual:   Bytes32Type(),
			expected: mustNewType(t, "bytes32"),
		},
		{
			name:     "Bytes32ArrayType",
			actual:   Bytes32ArrayType(),
			expected: mustNewType(t, "bytes32[]"),
		},
		{
			name:     "Uint48Type",
			actual:   Uint48Type(),
			expected: mustNewType(t, "uint48"),
		},
		{
			name:     "BytesType",
			actual:   BytesType(),
			expected: mustNewType(t, "bytes"),
		},
		{
			name:     "BoolType",
			actual:   BoolType(),
			expected: mustNewType(t, "bool"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected.String(), tt.actual.String())
			assert.Equal(t, tt.expected.T, tt.actual.T)
			assert.Equal(t, tt.expected.Elem != nil, tt.actual.Elem != nil)
			if tt.expected.Elem != nil {
				assert.Equal(t, tt.expected.Elem.String(), tt.actual.Elem.String())
			}
			assert.Equal(t, tt.expected.Size, tt.actual.Size)
			assert.Equal(t, tt.expected.T == abi.ArrayTy, tt.actual.T == abi.ArrayTy)
		})
	}
}

func TestMustABIType(t *testing.T) {
	assert.Equal(t, "uint8", MustABIType("uint8").String())
	require.Panics(t, func() {
		MustABIType("not-an-abi-type")
	})
}

// mustNewType is a helper function to create a new type or fail the test
func mustNewType(t *testing.T, typeStr string) abi.Type {
	typ, err := abi.NewType(typeStr, "", nil)
	if err != nil {
		t.Fatalf("Failed to create type %s: %v", typeStr, err)
	}
	return typ
}
