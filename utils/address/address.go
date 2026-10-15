// Package address normalizes EVM address strings for use as map keys and DB values.
package address

import (
	"strings"

	"github.com/ethereum/go-ethereum/common"
)

// Normalize trims and lowercases an EVM address, adding a missing 0x prefix.
// It returns "" when the input is not a 20-byte hex address.
func Normalize(raw string) string {
	addr := strings.ToLower(strings.TrimSpace(raw))
	if len(addr) == 40 && !strings.HasPrefix(addr, "0x") {
		addr = "0x" + addr
	}
	if !common.IsHexAddress(addr) {
		return ""
	}
	return addr
}

// NormalizeAll normalizes addresses, dropping invalid entries and duplicates while keeping order.
func NormalizeAll(raw []string) []string {
	out := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, r := range raw {
		addr := Normalize(r)
		if addr == "" {
			continue
		}
		if _, ok := seen[addr]; ok {
			continue
		}
		seen[addr] = struct{}{}
		out = append(out, addr)
	}
	return out
}

// Equal reports whether a and b are the same valid address, ignoring case and 0x prefix.
func Equal(a, b string) bool {
	na := Normalize(a)
	return na != "" && na == Normalize(b)
}
