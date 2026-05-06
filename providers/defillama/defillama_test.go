package defillama

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/superform-xyz/superform-go-utils/utils/constants"
)

// newTestClient creates an httptest server and returns a defiLlama client wired to it.
func newTestClient(t *testing.T, handler http.HandlerFunc) *defiLlama {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &defiLlama{coinsBaseUrl: server.URL, client: server.Client()}
}

func jsonOK(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	require.NoError(t, json.NewEncoder(w).Encode(v))
}

var (
	usdcAddr   = common.HexToAddress("0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48")
	nativeAddr = constants.GetNativeToken()
	nullAddr   = constants.GetNullAddress()
)

// ── New ─────────────────────────────────────────────────────────────────

func TestNew(t *testing.T) {
	dl := New()
	require.NotNil(t, dl)

	concrete, ok := dl.(*defiLlama)
	require.True(t, ok)
	assert.Equal(t, coinsBaseUrl, concrete.coinsBaseUrl)
	assert.NotNil(t, concrete.client)
}

// ── HealthCheck ─────────────────────────────────────────────────────────

func TestHealthCheck(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		dl := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			jsonOK(t, w, CoinsResponse{
				Coins: map[string]Coin{"ethereum:0xa0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48": {Price: 1.0}},
			})
		})
		assert.NoError(t, dl.HealthCheck())
	})

	t.Run("http error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		server.Close()
		dl := &defiLlama{coinsBaseUrl: server.URL, client: server.Client()}

		assert.Error(t, dl.HealthCheck())
	})

	t.Run("invalid json", func(t *testing.T) {
		dl := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("not json"))
		})
		assert.Error(t, dl.HealthCheck())
	})
}

// ── GetCoin ─────────────────────────────────────────────────────────────

func TestGetCoin(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		dl := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			assert.Contains(t, r.URL.Path, "/prices/current/ethereum:")
			jsonOK(t, w, CoinsResponse{
				Coins: map[string]Coin{
					"ethereum:" + usdcAddr.Hex(): {Price: 1.0, Decimals: 6, Symbol: "USDC"},
				},
			})
		})

		coin, err := dl.GetCoin(constants.MainnetChainID, usdcAddr)
		require.NoError(t, err)
		require.NotNil(t, coin)
		assert.Equal(t, 1.0, coin.Price)
		assert.Equal(t, int32(6), coin.Decimals)
		assert.Equal(t, "USDC", coin.Symbol)
		assert.Equal(t, constants.MainnetChainID, coin.ChainId)
		assert.Equal(t, usdcAddr, coin.Address)
	})

	t.Run("native token converted to null address", func(t *testing.T) {
		dl := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			// API should receive null address, not 0xeeee...
			assert.Contains(t, r.URL.Path, nullAddr.Hex())
			jsonOK(t, w, CoinsResponse{
				Coins: map[string]Coin{
					"ethereum:" + nullAddr.Hex(): {Price: 3500.0, Symbol: "ETH"},
				},
			})
		})

		coin, err := dl.GetCoin(constants.MainnetChainID, nativeAddr)
		require.NoError(t, err)
		require.NotNil(t, coin)
		assert.Equal(t, 3500.0, coin.Price)
		// Address in result should be the original native address
		assert.Equal(t, nativeAddr, coin.Address)
	})

	t.Run("token not found", func(t *testing.T) {
		dl := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			jsonOK(t, w, CoinsResponse{Coins: map[string]Coin{}})
		})

		coin, err := dl.GetCoin(constants.MainnetChainID, usdcAddr)
		assert.ErrorIs(t, err, ErrTokenNotFound)
		assert.Nil(t, coin)
	})

	t.Run("http error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		server.Close()
		dl := &defiLlama{coinsBaseUrl: server.URL, client: server.Client()}

		coin, err := dl.GetCoin(constants.MainnetChainID, usdcAddr)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to get prices")
		assert.Nil(t, coin)
	})

	t.Run("json decode error", func(t *testing.T) {
		dl := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("bad json"))
		})

		coin, err := dl.GetCoin(constants.MainnetChainID, usdcAddr)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to decode prices")
		assert.Nil(t, coin)
	})
}

// ── GetHistoricalCoin ───────────────────────────────────────────────────

func TestGetHistoricalCoin(t *testing.T) {
	ts := time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC)

	t.Run("success", func(t *testing.T) {
		dl := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			assert.Contains(t, r.URL.Path, "/prices/historical/")
			assert.Contains(t, r.URL.Path, "ethereum:")
			jsonOK(t, w, CoinsResponse{
				Coins: map[string]Coin{
					"ethereum:" + usdcAddr.Hex(): {Price: 0.9999, Decimals: 6, Symbol: "USDC"},
				},
			})
		})

		coin, err := dl.GetHistoricalCoin(constants.MainnetChainID, usdcAddr, ts)
		require.NoError(t, err)
		require.NotNil(t, coin)
		assert.Equal(t, 0.9999, coin.Price)
		assert.Equal(t, constants.MainnetChainID, coin.ChainId)
		assert.Equal(t, usdcAddr, coin.Address)
	})

	t.Run("native token converted to null address", func(t *testing.T) {
		dl := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			assert.Contains(t, r.URL.Path, nullAddr.Hex())
			jsonOK(t, w, CoinsResponse{
				Coins: map[string]Coin{
					"ethereum:" + nullAddr.Hex(): {Price: 3000.0, Symbol: "ETH"},
				},
			})
		})

		coin, err := dl.GetHistoricalCoin(constants.MainnetChainID, nativeAddr, ts)
		require.NoError(t, err)
		require.NotNil(t, coin)
		assert.Equal(t, nativeAddr, coin.Address)
	})

	t.Run("token not found", func(t *testing.T) {
		dl := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			jsonOK(t, w, CoinsResponse{Coins: map[string]Coin{}})
		})

		coin, err := dl.GetHistoricalCoin(constants.MainnetChainID, usdcAddr, ts)
		assert.ErrorIs(t, err, ErrTokenNotFound)
		assert.Nil(t, coin)
	})

	t.Run("http error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		server.Close()
		dl := &defiLlama{coinsBaseUrl: server.URL, client: server.Client()}

		coin, err := dl.GetHistoricalCoin(constants.MainnetChainID, usdcAddr, ts)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to get prices")
		assert.Nil(t, coin)
	})

	t.Run("json decode error", func(t *testing.T) {
		dl := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("bad json"))
		})

		coin, err := dl.GetHistoricalCoin(constants.MainnetChainID, usdcAddr, ts)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to decode prices")
		assert.Nil(t, coin)
	})
}

// ── GetMultipleCoins ────────────────────────────────────────────────────

func TestGetMultipleCoins(t *testing.T) {
	wethAddr := common.HexToAddress("0xC02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2")

	t.Run("success with multiple tokens", func(t *testing.T) {
		dl := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			assert.Contains(t, r.URL.Path, "/prices/current/")
			jsonOK(t, w, CoinsResponse{
				Coins: map[string]Coin{
					"ethereum:" + usdcAddr.Hex(): {Price: 1.0, Decimals: 6, Symbol: "USDC"},
					"ethereum:" + wethAddr.Hex(): {Price: 3500.0, Decimals: 18, Symbol: "WETH"},
				},
			})
		})

		coins, err := dl.GetMultipleCoins([]QueryTokenPrice{
			{ChainId: constants.MainnetChainID, TokenAddress: usdcAddr},
			{ChainId: constants.MainnetChainID, TokenAddress: wethAddr},
		})
		require.NoError(t, err)
		require.Len(t, coins, 2)

		assert.Equal(t, usdcAddr, coins[0].Address)
		assert.Equal(t, constants.MainnetChainID, coins[0].ChainId)
		assert.Equal(t, 1.0, coins[0].Price)

		assert.Equal(t, wethAddr, coins[1].Address)
		assert.Equal(t, 3500.0, coins[1].Price)
	})

	t.Run("native token converted to null address", func(t *testing.T) {
		dl := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			// Request should use null address
			assert.Contains(t, r.URL.Path, nullAddr.Hex())
			jsonOK(t, w, CoinsResponse{
				Coins: map[string]Coin{
					"ethereum:" + nullAddr.Hex(): {Price: 3500.0, Symbol: "ETH"},
				},
			})
		})

		coins, err := dl.GetMultipleCoins([]QueryTokenPrice{
			{ChainId: constants.MainnetChainID, TokenAddress: nativeAddr},
		})
		require.NoError(t, err)
		require.Len(t, coins, 1)
		// Result should have original native address
		assert.Equal(t, nativeAddr, coins[0].Address)
		assert.Equal(t, 3500.0, coins[0].Price)
	})

	t.Run("unknown chain ID skipped", func(t *testing.T) {
		dl := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			// Only the known chain token should appear in the request
			assert.Contains(t, r.URL.Path, "ethereum:")
			assert.NotContains(t, r.URL.Path, ":"+usdcAddr.Hex()+",") // unknown chain token should not be first
			jsonOK(t, w, CoinsResponse{
				Coins: map[string]Coin{
					"ethereum:" + wethAddr.Hex(): {Price: 3500.0, Symbol: "WETH"},
				},
			})
		})

		coins, err := dl.GetMultipleCoins([]QueryTokenPrice{
			{ChainId: 999999, TokenAddress: usdcAddr},                   // unknown chain
			{ChainId: constants.MainnetChainID, TokenAddress: wethAddr}, // known chain
		})
		require.NoError(t, err)
		// Only the known-chain token should be in results
		require.Len(t, coins, 1)
		assert.Equal(t, wethAddr, coins[0].Address)
	})

	t.Run("token not found in response skipped", func(t *testing.T) {
		dl := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			// Return only one of the two requested tokens
			jsonOK(t, w, CoinsResponse{
				Coins: map[string]Coin{
					"ethereum:" + usdcAddr.Hex(): {Price: 1.0, Symbol: "USDC"},
				},
			})
		})

		coins, err := dl.GetMultipleCoins([]QueryTokenPrice{
			{ChainId: constants.MainnetChainID, TokenAddress: usdcAddr},
			{ChainId: constants.MainnetChainID, TokenAddress: wethAddr}, // not in response
		})
		require.NoError(t, err)
		require.Len(t, coins, 1)
		assert.Equal(t, usdcAddr, coins[0].Address)
	})

	t.Run("http error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		server.Close()
		dl := &defiLlama{coinsBaseUrl: server.URL, client: server.Client()}

		coins, err := dl.GetMultipleCoins([]QueryTokenPrice{
			{ChainId: constants.MainnetChainID, TokenAddress: usdcAddr},
		})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to get prices")
		assert.Nil(t, coins)
	})

	t.Run("json decode error", func(t *testing.T) {
		dl := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("bad json"))
		})

		coins, err := dl.GetMultipleCoins([]QueryTokenPrice{
			{ChainId: constants.MainnetChainID, TokenAddress: usdcAddr},
		})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to decode prices")
		assert.Nil(t, coins)
	})
}

// ── Integration ─────────────────────────────────────────────────────────

func TestDefiLlama_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	dl := New()

	t.Run("health check", func(t *testing.T) {
		require.NoError(t, dl.HealthCheck())
	})

	t.Run("get coin", func(t *testing.T) {
		coin, err := dl.GetCoin(constants.MainnetChainID, usdcAddr)
		require.NoError(t, err)
		require.NotNil(t, coin)
		assert.Equal(t, constants.MainnetChainID, coin.ChainId)
		assert.Equal(t, usdcAddr, coin.Address)
		assert.NotZero(t, coin.Price)
		assert.Equal(t, "USDC", coin.Symbol)
	})

	t.Run("get coin native token", func(t *testing.T) {
		coin, err := dl.GetCoin(constants.MainnetChainID, nativeAddr)
		require.NoError(t, err)
		require.NotNil(t, coin)
		assert.Equal(t, nativeAddr, coin.Address)
		assert.NotZero(t, coin.Price)
	})

	t.Run("get historical coin", func(t *testing.T) {
		ts := time.Now().Add(-time.Hour).UTC()
		coin, err := dl.GetHistoricalCoin(constants.MainnetChainID, usdcAddr, ts)
		require.NoError(t, err)
		require.NotNil(t, coin)
		assert.Equal(t, constants.MainnetChainID, coin.ChainId)
		assert.Equal(t, usdcAddr, coin.Address)
		assert.NotZero(t, coin.Price)
	})

	t.Run("get multiple coins", func(t *testing.T) {
		wethAddr := common.HexToAddress("0xC02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2")
		coins, err := dl.GetMultipleCoins([]QueryTokenPrice{
			{ChainId: constants.MainnetChainID, TokenAddress: usdcAddr},
			{ChainId: constants.MainnetChainID, TokenAddress: wethAddr},
			{ChainId: constants.MainnetChainID, TokenAddress: nativeAddr},
		})
		require.NoError(t, err)
		require.NotEmpty(t, coins)

		for _, coin := range coins {
			assert.Equal(t, constants.MainnetChainID, coin.ChainId)
			assert.NotZero(t, coin.Price)
		}
	})

	t.Run("get coin not found", func(t *testing.T) {
		coin, err := dl.GetCoin(constants.MainnetChainID, common.HexToAddress("0x1111111111111111111111111111111111111111"))
		assert.ErrorIs(t, err, ErrTokenNotFound)
		assert.Nil(t, coin)
	})
}
