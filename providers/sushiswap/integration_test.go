package sushiswap

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/superform-xyz/superform-go-utils/pkg/http_client"
)

// TestLive is opt-in and only fetches unsigned calldata; it never submits a transaction.
func TestLive(t *testing.T) {
	if os.Getenv("SUSHISWAP_LIVE") != "1" {
		t.Skip("set SUSHISWAP_LIVE=1 to check the public Sushi API")
	}
	client, err := New(WithTimeout(15 * time.Second))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	quote, err := client.GetQuote(context.Background(), quoteRequest())
	require.NoError(t, err)
	assert.Positive(t, quote.AssumedAmountOut.Sign())
	swap, err := client.GetSwap(context.Background(), swapRequest())
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(swap.Tx.Data), 4)
	assert.Equal(t, swapRequest().Amount, swap.Tx.Value)
}

func TestSimulation(t *testing.T) {
	url := os.Getenv("SUSHISWAP_SIMULATION_URL")
	if url == "" {
		t.Skip("set SUSHISWAP_SIMULATION_URL to the local Postman member URL")
	}
	client, err := New(WithBaseURL(url), WithTimeout(5*time.Second))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	quote, err := client.GetQuote(context.Background(), quoteRequest())
	require.NoError(t, err)
	assert.Equal(t, "11951002273851281408", quote.AssumedAmountOut.String())
	swap, err := client.GetSwap(context.Background(), swapRequest())
	require.NoError(t, err)
	assert.Equal(t, "1000000000000000", swap.Tx.Value.String())
	for _, tc := range []struct {
		fixture string
		err     error
	}{
		{"no-route", ErrNoRoute},
		{"partial", ErrPartialRoute},
		{"rate-limit", http_client.ErrRateLimited},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			req := swapRequest()
			req.Referrer = "fixture:" + tc.fixture
			result, err := client.GetSwap(context.Background(), req)
			require.ErrorIs(t, err, tc.err)
			assert.Nil(t, result)
		})
	}
}
