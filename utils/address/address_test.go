package address

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNormalize(t *testing.T) {
	want := "0x833589fcd6edb6e08f4c7c32d4f71b54bda02913"
	assert.Equal(t, want, Normalize("0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913"))
	assert.Equal(t, want, Normalize("  833589FCD6EDB6E08F4C7C32D4F71B54BDA02913 "))
	assert.Equal(t, "", Normalize(""))
	assert.Equal(t, "", Normalize("0x1234"))
	assert.Equal(t, "", Normalize("0xzz3589fcd6edb6e08f4c7c32d4f71b54bda02913"))
}

func TestNormalizeAll(t *testing.T) {
	got := NormalizeAll([]string{
		"0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913",
		"bad",
		"0x833589fcd6edb6e08f4c7c32d4f71b54bda02913",
		"0x0000000000000000000000000000000000000001",
	})
	assert.Equal(t, []string{
		"0x833589fcd6edb6e08f4c7c32d4f71b54bda02913",
		"0x0000000000000000000000000000000000000001",
	}, got)
}

func TestEqual(t *testing.T) {
	assert.True(t, Equal("0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913", "833589fcd6edb6e08f4c7c32d4f71b54bda02913"))
	assert.False(t, Equal("", ""))
	assert.False(t, Equal("bad", "bad"))
}
