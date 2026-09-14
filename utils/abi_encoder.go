package utils

import (
	"bytes"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
)

// EncodeABI encodes the given values into ABI format
func EncodeABI(types []abi.Type, values []any) ([]byte, error) {
	if len(types) == 0 && len(values) == 0 {
		return []byte{}, nil
	}

	arguments := abi.Arguments{}
	for _, t := range types {
		arguments = append(arguments, abi.Argument{Type: t})
	}

	encodedData, err := arguments.Pack(values...)
	if err != nil {
		return nil, fmt.Errorf("error encoding data: %w", err)
	}

	return encodedData, nil
}

// EncodeABIMethodCall encodes calldata for an ABI method, including the 4-byte selector.
func EncodeABIMethodCall(method abi.Method, values ...any) ([]byte, error) {
	types := make([]abi.Type, 0, len(method.Inputs))
	for _, input := range method.Inputs {
		types = append(types, input.Type)
	}

	encoded, err := EncodeABI(types, values)
	if err != nil {
		return nil, fmt.Errorf("encoding ABI method %s inputs: %w", method.Name, err)
	}

	data := make([]byte, 0, len(method.ID)+len(encoded))
	data = append(data, method.ID...)
	data = append(data, encoded...)
	return data, nil
}

// DecodeABIMethodCall decodes calldata for an ABI method after validating its selector.
func DecodeABIMethodCall(method abi.Method, data []byte) ([]any, error) {
	if !bytes.HasPrefix(data, method.ID) {
		return nil, fmt.Errorf("calldata = %x, want %s selector %x", data, method.Name, method.ID)
	}

	inputs, err := method.Inputs.Unpack(data[len(method.ID):])
	if err != nil {
		return nil, fmt.Errorf("unpacking %s inputs: %w", method.Name, err)
	}
	return inputs, nil
}

// ABIMethodByName returns the ABI method with the given name.
func ABIMethodByName(parsed *abi.ABI, name string) (abi.Method, error) {
	method, ok := parsed.Methods[name]
	if !ok {
		return abi.Method{}, fmt.Errorf("ABI method %s not found", name)
	}
	return method, nil
}

// DecodeABIOutput decodes the return value at outputIndex from an ABI method.
func DecodeABIOutput[T comparable](method abi.Method, data []byte, outputIndex int) (T, error) {
	var zero T

	values, err := method.Outputs.Unpack(data)
	if err != nil {
		return zero, fmt.Errorf("unpacking %s: %w", method.Name, err)
	}
	if outputIndex < 0 || outputIndex >= len(values) {
		return zero, fmt.Errorf("%s returned %d values, output index %d out of range", method.Name, len(values), outputIndex)
	}

	value := values[outputIndex]
	switch any(zero).(type) {
	case *big.Int:
		decoded, ok := abi.ConvertType(value, new(*big.Int)).(**big.Int)
		if !ok || decoded == nil || *decoded == nil {
			return zero, fmt.Errorf("%s returned unexpected type %T at output index %d", method.Name, value, outputIndex)
		}
		return any(new(big.Int).Set(*decoded)).(T), nil
	case common.Address:
		decoded, ok := abi.ConvertType(value, new(common.Address)).(*common.Address)
		if !ok {
			return zero, fmt.Errorf("%s returned unexpected type %T at output index %d", method.Name, value, outputIndex)
		}
		return any(*decoded).(T), nil
	default:
		return zero, fmt.Errorf("%s output index %d does not support decode type %T", method.Name, outputIndex, zero)
	}
}

// * --------------------------------------- * //
// * Helper functions for getting abi types * //
// * --------------------------------------- * //

// MustABIType returns the requested ABI type or panics if the type is invalid.
func MustABIType(name string) abi.Type {
	abiType, err := abi.NewType(name, "", nil)
	if err != nil {
		panic(fmt.Sprintf("creating ABI type %s: %v", name, err))
	}
	return abiType
}

// Uint8Type is a helper function to get uint8 type.
// Note: go-ethereum packs uint8 values as a native Go uint8.
func Uint8Type() abi.Type {
	return MustABIType("uint8")
}

// Uint256Type is a helperfunction to get uint256 type
func Uint256Type() abi.Type {
	return MustABIType("uint256")
}

// Uint256ArrayType is a helper function to get uint256[] type
func Uint256ArrayType() abi.Type {
	return MustABIType("uint256[]")
}

// Uint48Type is a helper function to get uint48 type
func Uint48Type() abi.Type {
	return MustABIType("uint48")
}

// Uint24Type is a helper function to get uint24 type.
// Note: go-ethereum packs uint24 values as *big.Int (size not in {8,16,32,64}).
func Uint24Type() abi.Type {
	return MustABIType("uint24")
}

// Uint32Type is a helper function to get uint32 type.
// Note: go-ethereum packs uint32 values as a native Go uint32.
func Uint32Type() abi.Type {
	return MustABIType("uint32")
}

// Uint64Type returns the uint64 ABI type, packed as a native Go uint64.
func Uint64Type() abi.Type {
	return MustABIType("uint64")
}

// Int64Type returns the int64 ABI type, packed as a native Go int64.
func Int64Type() abi.Type {
	return MustABIType("int64")
}

// Uint160Type is a helper function to get uint160 type.
// Note: go-ethereum packs uint160 values as *big.Int (size not in {8,16,32,64}).
func Uint160Type() abi.Type {
	return MustABIType("uint160")
}

// Uint64ArrayType is a helper function to get uint64[] type
func Uint64ArrayType() abi.Type {
	return MustABIType("uint64[]")
}

// AddressType is a helper function to get address type
func AddressType() abi.Type {
	return MustABIType("address")
}

// AddressArrayType is a helper function to get address[] type
func AddressArrayType() abi.Type {
	return MustABIType("address[]")
}

// StringType is a helper function to get string type
func StringType() abi.Type {
	return MustABIType("string")
}

// Bytes32Type is a helper function to get bytes32 type
func Bytes32Type() abi.Type {
	return MustABIType("bytes32")
}

// BytesType is a helper function to get bytes type
func BytesType() abi.Type {
	return MustABIType("bytes")
}

// Bytes32ArrayType is a helper function to get bytes32[] type
func Bytes32ArrayType() abi.Type {
	return MustABIType("bytes32[]")
}

// BoolType is a helper function to get bool type
func BoolType() abi.Type {
	return MustABIType("bool")
}
