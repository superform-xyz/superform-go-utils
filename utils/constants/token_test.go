package constants

import (
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

func TestIsNativeToken(t *testing.T) {
	native := GetNativeToken().Hex()
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"checksummed native", native, true},
		{"lowercase native", strings.ToLower(native), true},
		{"uppercase native", "0x" + strings.ToUpper(native[2:]), true},
		{"null", "0x0000000000000000000000000000000000000000", true},
		{"padded native", " " + strings.ToLower(native) + " ", true},
		{"other", "0x833589fcd6edb6e08f4c7c32d4f71b54bda02913", false},
		{"empty", "", false},
		{"malformed", "0xeeee", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsNativeToken(tt.in); got != tt.want {
				t.Fatalf("IsNativeToken(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}

	if !IsNativeToken(GetNativeToken()) || !IsNativeToken(GetNullAddress().Bytes()) {
		t.Fatal("address and bytes variants must match native and null")
	}
}

func TestIsNullAddress(t *testing.T) {
	if !IsNullAddress("0x0000000000000000000000000000000000000000") {
		t.Fatal("null string must match")
	}
	if IsNullAddress(strings.ToLower(GetNativeToken().Hex())) || IsNullAddress("") {
		t.Fatal("native and empty must not match null")
	}
	if !IsNullAddress(common.Address{}) {
		t.Fatal("zero address must match")
	}
}
