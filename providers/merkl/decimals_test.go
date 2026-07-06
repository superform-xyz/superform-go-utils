package merkl

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTokenRewardDecimals(t *testing.T) {
	decimals, err := Token{Decimals: 18}.RewardDecimals()
	require.NoError(t, err)
	require.Equal(t, 18, decimals)

	_, err = Token{Decimals: 0}.RewardDecimals()
	require.ErrorIs(t, err, ErrMissingTokenDecimals)
}
