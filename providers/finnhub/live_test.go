//go:build finnhub_live

package finnhub

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLiveFinnhubClient(t *testing.T) {
	apiKey := os.Getenv("FINNHUB_API_KEY")
	if apiKey == "" {
		t.Skip("FINNHUB_API_KEY is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	c, err := New(apiKey)
	require.NoError(t, err)
	defer func() { require.NoError(t, c.Close()) }()

	profile, err := c.GetCompanyProfile(ctx, "AAPL")
	require.NoError(t, err)
	require.Equal(t, "AAPL", profile.Ticker)
	require.NotEmpty(t, profile.Name)
	require.NotNil(t, profile.MarketCapitalization)

	financials, err := c.GetCompanyBasicFinancials(ctx, BasicFinancialsRequest{
		Symbol: "AAPL",
		Metric: "all",
	})
	require.NoError(t, err)
	require.Equal(t, "AAPL", financials.Symbol)
	_, ok := financials.MetricValue("marketCapitalization")
	require.True(t, ok)

	fundamentals, err := c.GetStockFundamentals(ctx, "AAPL")
	require.NoError(t, err)
	require.Equal(t, "AAPL", fundamentals.Symbol)
	require.NotNil(t, fundamentals.MarketCapitalization)
	require.NotNil(t, fundamentals.PERatio)
}
