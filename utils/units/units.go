// Package units converts and scales token amounts held as base-unit integers.
package units

import (
	"fmt"
	"math/big"
	"strconv"
	"strings"
)

// BpsBase is the basis-point denominator (10000 = 100%).
const BpsBase = 10000

var ten = big.NewInt(10)

// Pow10 returns 10^decimals as a new *big.Int.
func Pow10(decimals uint) *big.Int {
	return new(big.Int).Exp(ten, new(big.Int).SetUint64(uint64(decimals)), nil)
}

// ToRat returns amount / 10^decimals exactly. A nil amount is zero.
func ToRat(amount *big.Int, decimals uint) *big.Rat {
	if amount == nil {
		return new(big.Rat)
	}
	return new(big.Rat).SetFrac(amount, Pow10(decimals))
}

// ToFloat returns amount / 10^decimals as the nearest float64.
func ToFloat(amount *big.Int, decimals uint) float64 {
	f, _ := ToRat(amount, decimals).Float64()
	return f
}

// FromFloat returns amount * 10^decimals, truncated toward zero.
// It returns nil for NaN or infinite input.
func FromFloat(amount float64, decimals uint) *big.Int {
	r := new(big.Rat)
	if _, ok := r.SetString(strconv.FormatFloat(amount, 'f', -1, 64)); !ok {
		return nil
	}
	r.Mul(r, new(big.Rat).SetInt(Pow10(decimals)))
	return new(big.Int).Quo(r.Num(), r.Denom())
}

// Format renders amount / 10^decimals as an exact decimal string without trailing zeros.
func Format(amount *big.Int, decimals uint) string {
	if amount == nil {
		return "0"
	}
	s := new(big.Int).Abs(amount).String()
	sign := ""
	if amount.Sign() < 0 {
		sign = "-"
	}
	if decimals == 0 {
		return sign + s
	}
	if pad := int(decimals) + 1 - len(s); pad > 0 {
		s = strings.Repeat("0", pad) + s
	}
	whole, frac := s[:len(s)-int(decimals)], strings.TrimRight(s[len(s)-int(decimals):], "0")
	if frac == "" {
		return sign + whole
	}
	return sign + whole + "." + frac
}

// Parse converts a decimal string into base units, rejecting more fractional
// digits than decimals allows rather than silently rounding.
func Parse(value string, decimals uint) (*big.Int, error) {
	value = strings.TrimSpace(value)
	sign := ""
	if strings.HasPrefix(value, "-") || strings.HasPrefix(value, "+") {
		sign, value = value[:1], value[1:]
	}
	whole, frac, _ := strings.Cut(value, ".")
	if whole == "" && frac == "" {
		return nil, fmt.Errorf("parsing amount %q: empty", value)
	}
	if uint(len(frac)) > decimals {
		return nil, fmt.Errorf("parsing amount %q: more than %d decimals", value, decimals)
	}
	digits := whole + frac + strings.Repeat("0", int(decimals)-len(frac))
	for _, c := range digits {
		if c < '0' || c > '9' {
			return nil, fmt.Errorf("parsing amount %q: invalid digit", value)
		}
	}
	out, _ := new(big.Int).SetString(digits, 10)
	if sign == "-" {
		out.Neg(out)
	}
	return out, nil
}

// Rescale converts amount between decimal precisions, truncating toward zero when reducing.
func Rescale(amount *big.Int, fromDecimals, toDecimals uint) *big.Int {
	if amount == nil {
		return new(big.Int)
	}
	switch {
	case toDecimals > fromDecimals:
		return new(big.Int).Mul(amount, Pow10(toDecimals-fromDecimals))
	case toDecimals < fromDecimals:
		return new(big.Int).Quo(amount, Pow10(fromDecimals-toDecimals))
	default:
		return new(big.Int).Set(amount)
	}
}

// ScaleByPercent returns amount * (1 + percent/100), truncated toward zero.
// A nil amount or non-finite percent yields zero.
func ScaleByPercent(amount *big.Int, percent float64) *big.Int {
	if amount == nil {
		return big.NewInt(0)
	}
	scale := new(big.Rat)
	if _, ok := scale.SetString(strconv.FormatFloat(percent, 'f', -1, 64)); !ok {
		return big.NewInt(0)
	}
	scale.Quo(scale, big.NewRat(100, 1))

	factor := new(big.Rat).Add(big.NewRat(1, 1), scale)
	scaled := new(big.Rat).Mul(new(big.Rat).SetInt(amount), factor)
	return new(big.Int).Quo(scaled.Num(), scaled.Denom())
}

// ApplySlippageBps returns amount * (10000 - slippageBps) / 10000, rounded down.
// It returns zero for a nil or zero amount, or slippage outside [0, 10000].
func ApplySlippageBps(amount *big.Int, slippageBps int) *big.Int {
	if amount == nil || amount.Sign() == 0 || slippageBps < 0 || slippageBps > BpsBase {
		return new(big.Int)
	}
	out := new(big.Int).Mul(amount, big.NewInt(int64(BpsBase-slippageBps)))
	return out.Quo(out, big.NewInt(BpsBase))
}

// MulBpsCeil returns ceil(amount * bps / 10000), for rounding in the protocol's favour.
// It returns zero for a non-positive amount or zero bps, and amount itself when bps exceeds 10000.
func MulBpsCeil(amount *big.Int, bps uint64) *big.Int {
	if amount == nil || amount.Sign() <= 0 || bps == 0 {
		return new(big.Int)
	}
	if bps > BpsBase {
		return new(big.Int).Set(amount)
	}
	base := big.NewInt(BpsBase)
	out := new(big.Int).Mul(amount, new(big.Int).SetUint64(bps))
	out.Add(out, big.NewInt(BpsBase-1))
	return out.Quo(out, base)
}
