package units

import (
	"math"
	"math/big"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func bi(s string) *big.Int {
	v, ok := new(big.Int).SetString(s, 10)
	if !ok {
		panic(s)
	}
	return v
}

func TestPow10(t *testing.T) {
	assert.Equal(t, "1", Pow10(0).String())
	assert.Equal(t, "1000000000000000000", Pow10(18).String())
	// Callers may mutate the result, so each call must return a fresh value.
	Pow10(6).SetInt64(7)
	assert.Equal(t, "1000000", Pow10(6).String())
}

func TestToFloatAndFromFloat(t *testing.T) {
	assert.Equal(t, 1.5, ToFloat(bi("1500000"), 6))
	assert.Equal(t, 0.0, ToFloat(nil, 18))
	assert.Equal(t, "1500000", FromFloat(1.5, 6).String())
	assert.Equal(t, "100000000000000000", FromFloat(0.1, 18).String(), "uses the shortest decimal form, not binary float error")
	assert.Equal(t, "1", FromFloat(1.99, 0).String())
	assert.Nil(t, FromFloat(math.NaN(), 6))
}

func TestFormatAndParse(t *testing.T) {
	cases := []struct {
		raw      string
		decimals uint
		text     string
	}{
		{"0", 6, "0"},
		{"1", 6, "0.000001"},
		{"1500000", 6, "1.5"},
		{"-1500000", 6, "-1.5"},
		{"123", 0, "123"},
		{"1000000000000000000", 18, "1"},
	}
	for _, c := range cases {
		assert.Equal(t, c.text, Format(bi(c.raw), c.decimals))
		got, err := Parse(c.text, c.decimals)
		require.NoError(t, err)
		assert.Equal(t, c.raw, got.String())
	}

	got, err := Parse(".5", 2)
	require.NoError(t, err)
	assert.Equal(t, "50", got.String())

	for _, bad := range []string{"", "1.2345", "1e5", "abc", "1.2.3"} {
		_, err := Parse(bad, 3)
		assert.Error(t, err, bad)
	}
}

func TestRescale(t *testing.T) {
	assert.Equal(t, "1000000000000000000", Rescale(bi("1000000"), 6, 18).String())
	assert.Equal(t, "1", Rescale(bi("1999999999999"), 18, 6).String())
	assert.Equal(t, "5", Rescale(bi("5"), 6, 6).String())
	assert.Equal(t, "0", Rescale(nil, 6, 18).String())
}

func TestScaleByPercent(t *testing.T) {
	assert.Equal(t, "110", ScaleByPercent(big.NewInt(100), 10).String())
	assert.Equal(t, "90", ScaleByPercent(big.NewInt(100), -10).String())
	assert.Equal(t, "0", ScaleByPercent(nil, 10).String())
	assert.Equal(t, "0", ScaleByPercent(big.NewInt(100), math.Inf(1)).String())
}

func TestBps(t *testing.T) {
	assert.Equal(t, "995", ApplySlippageBps(big.NewInt(1000), 50).String())
	assert.Equal(t, "0", ApplySlippageBps(big.NewInt(1000), 10001).String())
	assert.Equal(t, "0", ApplySlippageBps(nil, 50).String())

	assert.Equal(t, "1", MulBpsCeil(big.NewInt(1), 1).String())
	assert.Equal(t, "5", MulBpsCeil(big.NewInt(1000), 50).String())
	assert.Equal(t, "1000", MulBpsCeil(big.NewInt(1000), 20000).String())
	assert.Equal(t, "0", MulBpsCeil(big.NewInt(-5), 50).String())
}
