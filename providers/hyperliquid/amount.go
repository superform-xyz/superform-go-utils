package hyperliquid

import (
	"fmt"
	"math/big"
)

// ParseAmount parses the venue's decimal text exactly, including exponent
// notation, without accepting fractions or non-decimal big.Rat syntax.
func ParseAmount(s string) (*big.Rat, error) {
	if !isDecimalNumber(s) {
		return nil, fmt.Errorf("invalid decimal amount %q", s)
	}
	amount, ok := new(big.Rat).SetString(s)
	if !ok {
		return nil, fmt.Errorf("invalid decimal amount %q", s)
	}
	return amount, nil
}

func isDecimalNumber(value string) bool {
	if value == "" {
		return false
	}
	i := 0
	if value[i] == '-' {
		i++
		if i == len(value) {
			return false
		}
	}
	integerStart := i
	for i < len(value) && value[i] >= '0' && value[i] <= '9' {
		i++
	}
	if i == integerStart {
		return false
	}
	if i < len(value) && value[i] == '.' {
		i++
		fractionStart := i
		for i < len(value) && value[i] >= '0' && value[i] <= '9' {
			i++
		}
		if i == fractionStart {
			return false
		}
	}
	if i < len(value) && (value[i] == 'e' || value[i] == 'E') {
		i++
		if i < len(value) && (value[i] == '+' || value[i] == '-') {
			i++
		}
		exponentStart := i
		for i < len(value) && value[i] >= '0' && value[i] <= '9' {
			i++
		}
		if i == exponentStart {
			return false
		}
	}
	return i == len(value)
}
