package coinbase

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestLiveCatalog makes five read-only requests and creates no session/payment.
func TestLiveCatalog(t *testing.T) {
	if os.Getenv("COINBASE_LIVE_TEST") != "1" {
		t.Skip("set COINBASE_LIVE_TEST=1, COINBASE_API_KEY_ID and COINBASE_API_KEY_SECRET for live discovery")
	}
	id, secret := os.Getenv("COINBASE_API_KEY_ID"), os.Getenv("COINBASE_API_KEY_SECRET")
	if id == "" || secret == "" {
		t.Fatal("COINBASE_API_KEY_ID and COINBASE_API_KEY_SECRET are required")
	}
	c, err := New(WithAPIKeyID(id), WithAPIKeySecret(secret), WithHTTPClient(&http.Client{
		Timeout:       5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}))
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	config, err := c.GetBuyConfig(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, config.Countries)
	t.Logf("GET buy/config: %d countries", len(config.Countries))
	for _, req := range []GetBuyOptionsRequest{
		{Country: "US", Subdivision: "CA"},
		{Country: "US", Subdivision: "NY"},
		{Country: "GB"},
		{Country: "DE"},
	} {
		options, err := c.GetBuyOptions(ctx, req)
		require.NoError(t, err, "%s/%s", req.Country, req.Subdivision)
		require.NotEmpty(t, options.PaymentCurrencies)
		hasBaseUSDC := false
		for _, asset := range options.PurchaseCurrencies {
			if asset.Symbol != "USDC" {
				continue
			}
			for _, network := range asset.Networks {
				if network.Name == "base" && network.ChainID == "8453" && strings.EqualFold(network.ContractAddress, "0x833589fcd6edb6e08f4c7c32d4f71b54bda02913") {
					hasBaseUSDC = true
				}
			}
		}
		require.True(t, hasBaseUSDC, "%s/%s missing native USDC on Base", req.Country, req.Subdivision)
		t.Logf("GET buy/options %s/%s: %d fiat currencies, %d purchase currencies; native USDC on Base verified", req.Country, req.Subdivision, len(options.PaymentCurrencies), len(options.PurchaseCurrencies))
	}
}
