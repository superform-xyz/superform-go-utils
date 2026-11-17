package confighooks

import (
	"crypto/ecdsa"
	"reflect"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
)

func TestPrivateKeyHook(t *testing.T) {
	validPrivateKey := "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"

	tests := []struct {
		name        string
		fromType    reflect.Type
		toType      reflect.Type
		input       interface{}
		wantErr     bool
		errContains string
	}{
		{
			name:     "non-private key type - should pass through",
			fromType: reflect.TypeOf(""),
			toType:   reflect.TypeOf(1),
			input:    "test",
			wantErr:  false,
		},
		{
			name:     "non-string input - should error",
			fromType: reflect.TypeOf(123),
			toType:   reflect.TypeOf(ecdsa.PrivateKey{}),
			input:    123,
			wantErr:  true,
		},
		{
			name:     "empty string - should error",
			fromType: reflect.TypeOf(""),
			toType:   reflect.TypeOf(ecdsa.PrivateKey{}),
			input:    "",
			wantErr:  true,
		},
		{
			name:     "invalid private key - should error",
			fromType: reflect.TypeOf(""),
			toType:   reflect.TypeOf(ecdsa.PrivateKey{}),
			input:    "invalid",
			wantErr:  true,
		},
		{
			name:     "valid private key - should succeed",
			fromType: reflect.TypeOf(""),
			toType:   reflect.TypeOf(ecdsa.PrivateKey{}),
			input:    validPrivateKey,
			wantErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := PrivateKeyHook(tt.fromType, tt.toType, tt.input)
			if tt.wantErr {
				assert.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				assert.NoError(t, err)
				if tt.toType != reflect.TypeOf(ecdsa.PrivateKey{}) {
					assert.Equal(t, tt.input, result)
				}
			}
		})
	}
}

func TestAddressHooks(t *testing.T) {
	t.Run("AddressHook", func(t *testing.T) {
		tests := []struct {
			name        string
			fromType    reflect.Type
			toType      reflect.Type
			input       interface{}
			expected    common.Address
			wantErr     bool
			errContains string
		}{
			{
				name:     "non-address type",
				fromType: reflect.TypeOf(""),
				toType:   reflect.TypeOf(1),
				input:    "0x123",
				expected: common.Address{},
				wantErr:  false,
			},
			{
				name:        "invalid address",
				fromType:    reflect.TypeOf(""),
				toType:      reflect.TypeOf(common.Address{}),
				input:       "0x123",
				expected:    common.Address{},
				wantErr:     true,
				errContains: "invalid address string '0x123' for target common.Address",
			},
			{
				name:     "valid address",
				fromType: reflect.TypeOf(""),
				toType:   reflect.TypeOf(common.Address{}),
				input:    "0x1234567890123456789012345678901234567890",
				expected: common.HexToAddress("0x1234567890123456789012345678901234567890"),
				wantErr:  false,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				result, err := AddressHook(tt.fromType, tt.toType, tt.input)
				if tt.wantErr {
					assert.Error(t, err)
					if tt.errContains != "" {
						assert.Contains(t, err.Error(), tt.errContains)
					}
				} else {
					assert.NoError(t, err)
					if tt.toType == reflect.TypeOf(common.Address{}) {
						assert.Equal(t, tt.expected, result)
					} else {
						assert.Equal(t, tt.input, result)
					}
				}
			})
		}
	})

	t.Run("AddressSliceHook", func(t *testing.T) {
		tests := []struct {
			name        string
			input       interface{}
			expected    []common.Address
			wantErr     bool
			errContains string
		}{
			{
				name:     "non-slice input",
				input:    "not a slice",
				expected: nil,
				wantErr:  true,
			},
			{
				name: "invalid element type",
				input: []int{
					123,
				},
				expected:    nil,
				wantErr:     true,
				errContains: "element 0 in slice is not a string or interface{}",
			},
			{
				name: "invalid address in slice",
				input: []string{
					"0x123",
				},
				expected:    nil,
				wantErr:     true,
				errContains: "element 0 ('0x123') is not a valid hex address",
			},
			{
				name: "valid addresses",
				input: []string{
					"0x1234567890123456789012345678901234567890",
					"0x0000000000000000000000000000000000000001",
				},
				expected: []common.Address{
					common.HexToAddress("0x1234567890123456789012345678901234567890"),
					common.HexToAddress("0x0000000000000000000000000000000000000001"),
				},
				wantErr: false,
			},
			{
				name: "interface element not string - should error",
				input: []interface{}{
					123, // Interface{} containing int, not string
				},
				expected:    nil,
				wantErr:     true,
				errContains: "element 0 in slice is an interface but not a string (kind: int)",
			},
			{
				name: "interface element with string - should succeed",
				input: []interface{}{
					"0x1234567890123456789012345678901234567890", // Interface{} containing string
				},
				expected: []common.Address{
					common.HexToAddress("0x1234567890123456789012345678901234567890"),
				},
				wantErr: false,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				result, err := AddressSliceHook(reflect.TypeOf(tt.input), reflect.TypeOf([]common.Address{}), tt.input)
				if tt.wantErr {
					assert.Error(t, err)
					if tt.errContains != "" {
						assert.Contains(t, err.Error(), tt.errContains)
					}
				} else {
					assert.NoError(t, err)
					assert.Equal(t, tt.expected, result)
				}
			})
		}
	})

	t.Run("AddressMapHook", func(t *testing.T) {
		tests := []struct {
			name        string
			input       interface{}
			expected    map[string]common.Address
			wantErr     bool
			errContains string
		}{
			{
				name:        "invalid map type",
				input:       map[int]string{1: "0x123"},
				expected:    nil,
				wantErr:     true,
				errContains: "map keys must be strings",
			},
			{
				name: "invalid address in map",
				input: map[string]string{
					"key1": "0x123",
				},
				expected:    nil,
				wantErr:     true,
				errContains: "invalid address for key key1: 0x123",
			},
			{
				name: "valid addresses",
				input: map[string]string{
					"addr1": "0x1234567890123456789012345678901234567890",
					"addr2": "0x0000000000000000000000000000000000000001",
				},
				expected: map[string]common.Address{
					"addr1": common.HexToAddress("0x1234567890123456789012345678901234567890"),
					"addr2": common.HexToAddress("0x0000000000000000000000000000000000000001"),
				},
				wantErr: false,
			},
			{
				name: "non-string value in map - should error",
				input: map[string]interface{}{
					"addr1": 123, // Not a string
				},
				expected:    nil,
				wantErr:     true,
				errContains: "value for key addr1 is not a string (type: int)",
			},
			{
				name:        "non-map input - should error",
				input:       "not a map",
				expected:    nil,
				wantErr:     true,
				errContains: "expected map input, got string",
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				result, err := AddressMapHook(
					reflect.TypeOf(tt.input),
					reflect.TypeOf(map[string]common.Address{}),
					tt.input,
				)
				if tt.wantErr {
					assert.Error(t, err)
					if tt.errContains != "" {
						assert.Contains(t, err.Error(), tt.errContains)
					}
				} else {
					assert.NoError(t, err)
					assert.Equal(t, tt.expected, result)
				}
			})
		}
	})
}
