package constants

import (
	"strings"

	"github.com/ethereum/go-ethereum/common"
)

var (
	// NullAddress is the address of 0x000 format
	nullAddress = common.HexToAddress("0x0000000000000000000000000000000000000000")
	// NativeToken is the address of the native token in 0xeeee format
	nativeToken = common.HexToAddress("0xeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee")
)

// GetNullAddress returns the null address
func GetNullAddress() common.Address {
	return nullAddress
}

// GetNativeToken returns the native token address
func GetNativeToken() common.Address {
	return nativeToken
}

// IsNativeToken checks if the token is a native token by its address.
func IsNativeToken[T common.Address | string | []byte](token T) bool {
	switch v := any(token).(type) {
	case common.Address:
		return v.Cmp(nativeToken) == 0 || v.Cmp(nullAddress) == 0
	case string:
		addr, ok := parseHexAddress(v)
		return ok && (addr == nativeToken || addr == nullAddress)
	case []byte:
		return common.BytesToAddress(v).Cmp(nativeToken) == 0 || common.BytesToAddress(v).Cmp(nullAddress) == 0
	default:
		return false
	}
}

// IsNullAddress checks if the token is a null token by its address.
func IsNullAddress[T common.Address | string | []byte](token T) bool {
	switch v := any(token).(type) {
	case common.Address:
		return v.Cmp(nullAddress) == 0
	case string:
		addr, ok := parseHexAddress(v)
		return ok && addr == nullAddress
	case []byte:
		return common.BytesToAddress(v).Cmp(nullAddress) == 0
	default:
		return false
	}
}

// parseHexAddress parses a hex address case-insensitively, rejecting malformed input.
func parseHexAddress(s string) (common.Address, bool) {
	s = strings.TrimSpace(s)
	if !common.IsHexAddress(s) {
		return common.Address{}, false
	}
	return common.HexToAddress(s), true
}

// ChecksumAddress converts a raw address to a checksum address.
func ChecksumAddress(rawAddress string) string {
	return common.HexToAddress(rawAddress).Hex()
}
