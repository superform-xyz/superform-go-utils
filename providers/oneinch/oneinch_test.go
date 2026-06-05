package oneinch

import (
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/superform-xyz/superform-go-utils/utils/constants"
)

var (
	testRouter      = common.HexToAddress("0x1111111254EEB25477B68fb85Ed929f73A960582")
	testUSDC        = common.HexToAddress("0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48")
	testUSDT        = common.HexToAddress("0xdAC17F958D2ee523a2206206994597C13D831ec7")
	testFromAddress = common.HexToAddress("0x1111111111111111111111111111111111111111")
	testToAddress   = common.HexToAddress("0x2222222222222222222222222222222222222222")
)

func newTestClient(t *testing.T, handler http.HandlerFunc) *oneInch {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	client, ok := New(
		"test-key",
		WithBaseURL(server.URL),
		WithRetry(0, time.Millisecond),
		WithSourceBlacklist([]string{"UNISWAP_V3", "CURVE"}),
		WithRouters(map[uint64]common.Address{
			constants.MainnetChainID: testRouter,
			constants.BaseChainID:    constants.GetNullAddress(),
		}),
	).(*oneInch)
	require.True(t, ok)
	return client
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	require.NoError(t, json.NewEncoder(w).Encode(v))
}

func TestNew(t *testing.T) {
	client := New(
		"test-key",
		WithBaseURL("https://example.com/"),
		WithRetry(2, time.Second),
		WithTimeout(15*time.Second),
		WithSourceBlacklist([]string{"UNISWAP_V3"}),
		WithRouters(map[uint64]common.Address{constants.MainnetChainID: testRouter}),
	)

	require.NotNil(t, client)
	concrete, ok := client.(*oneInch)
	require.True(t, ok)
	assert.Equal(t, "test-key", concrete.apiKey)
	assert.Equal(t, "https://example.com", concrete.baseURL)
	assert.Equal(t, []string{"UNISWAP_V3"}, concrete.sourceBlacklist)
	assert.Equal(t, testRouter, concrete.GetRouter(constants.MainnetChainID))
	assert.Equal(t, []uint64{constants.MainnetChainID}, concrete.SupportedChains())
	require.NotNil(t, concrete.timeout)
	assert.Equal(t, 15*time.Second, *concrete.timeout)
	require.NotNil(t, concrete.maxRetries)
	assert.Equal(t, uint(2), *concrete.maxRetries)
	require.NotNil(t, concrete.retryDelay)
	assert.Equal(t, time.Second, *concrete.retryDelay)
}

func TestGetQuote(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodGet, r.Method)
			assert.Equal(t, "Bearer test-key", r.Header.Get(authHeader))
			assert.Equal(t, "/swap/v6.0/1/quote", r.URL.Path)
			assert.Equal(t, constants.GetNativeToken().Hex(), r.URL.Query().Get("src"))
			assert.Equal(t, testUSDT.Hex(), r.URL.Query().Get("dst"))
			assert.Equal(t, "1000000", r.URL.Query().Get("amount"))
			assert.Equal(t, "UNISWAP_V3,CURVE", r.URL.Query().Get("excludedProtocols"))
			assert.Equal(t, "true", r.URL.Query().Get("includeTokensInfo"))
			assert.Equal(t, "true", r.URL.Query().Get("includeGas"))

			writeJSON(t, w, map[string]any{
				"dstAmount": "999000",
				"srcToken": map[string]any{
					"address":  constants.GetNativeToken().Hex(),
					"symbol":   "ETH",
					"name":     "Ether",
					"decimals": 18,
				},
				"dstToken": map[string]any{
					"address":  testUSDT.Hex(),
					"symbol":   "USDT",
					"name":     "Tether USD",
					"decimals": 6,
				},
				"gas": 150000,
			})
		})

		quote, err := client.GetQuote(context.Background(), QuoteRequest{
			ChainID:   constants.MainnetChainID,
			FromToken: constants.GetNullAddress(),
			ToToken:   testUSDT,
			Amount:    big.NewInt(1_000_000),
			Slippage:  1,
		})
		require.NoError(t, err)
		require.NotNil(t, quote)
		assert.Equal(t, "999000", quote.DstAmount.String())
		assert.Equal(t, "989010", quote.ToAmountMin.String())
		assert.Equal(t, "USDT", quote.DstToken.Symbol)
		assert.Equal(t, 150000, quote.Gas)
		assert.Equal(t, testRouter, quote.Router)
		assert.Equal(t, constants.MainnetChainID, quote.ChainID)
		assert.Equal(t, constants.GetNullAddress(), quote.FromToken)
		assert.Equal(t, testUSDT, quote.ToToken)
	})

	t.Run("same token rejected", func(t *testing.T) {
		client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("server should not be called")
		})

		quote, err := client.GetQuote(context.Background(), QuoteRequest{
			ChainID:   constants.MainnetChainID,
			FromToken: testUSDC,
			ToToken:   testUSDC,
			Amount:    big.NewInt(1),
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "from and to tokens are the same")
		assert.Nil(t, quote)
	})

	t.Run("http error", func(t *testing.T) {
		client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, `{"error":"upstream failed"}`, http.StatusInternalServerError)
		})

		quote, err := client.GetQuote(context.Background(), QuoteRequest{
			ChainID:   constants.MainnetChainID,
			FromToken: testUSDC,
			ToToken:   testUSDT,
			Amount:    big.NewInt(1),
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "1inch quote API returned status 500")
		assert.Nil(t, quote)
	})

	t.Run("invalid json", func(t *testing.T) {
		client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("bad json"))
		})

		quote, err := client.GetQuote(context.Background(), QuoteRequest{
			ChainID:   constants.MainnetChainID,
			FromToken: testUSDC,
			ToToken:   testUSDT,
			Amount:    big.NewInt(1),
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to decode 1inch quote response")
		assert.Nil(t, quote)
	})
}

func TestGetSwap(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodGet, r.Method)
			assert.Equal(t, "/swap/v6.0/1/swap", r.URL.Path)
			assert.Equal(t, testUSDC.Hex(), r.URL.Query().Get("src"))
			assert.Equal(t, testUSDT.Hex(), r.URL.Query().Get("dst"))
			assert.Equal(t, "1000000", r.URL.Query().Get("amount"))
			assert.Equal(t, testFromAddress.Hex(), r.URL.Query().Get("from"))
			assert.Equal(t, testFromAddress.Hex(), r.URL.Query().Get("origin"))
			assert.Equal(t, testToAddress.Hex(), r.URL.Query().Get("receiver"))
			assert.Equal(t, "1.000000", r.URL.Query().Get("slippage"))
			assert.Equal(t, "true", r.URL.Query().Get("disableEstimate"))
			assert.Equal(t, "true", r.URL.Query().Get("includeProtocols"))
			assert.Equal(t, "true", r.URL.Query().Get("usePatching"))

			writeJSON(t, w, map[string]any{
				"dstAmount": "998000",
				"tx": map[string]any{
					"from":     testFromAddress.Hex(),
					"to":       testRouter.Hex(),
					"data":     "0xabcdef",
					"value":    "0",
					"gas":      200000,
					"gasPrice": "20000000000",
				},
				"protocols": [][][]map[string]string{
					{{{"name": "UNISWAP_V3"}}},
				},
			})
		})

		swap, err := client.GetSwap(context.Background(), SwapRequest{
			ChainID:     constants.MainnetChainID,
			FromToken:   testUSDC,
			ToToken:     testUSDT,
			Amount:      big.NewInt(1_000_000),
			FromAddress: testFromAddress,
			ToAddress:   testToAddress,
			Slippage:    1,
		})
		require.NoError(t, err)
		require.NotNil(t, swap)
		assert.Equal(t, "998000", swap.DstAmount.String())
		assert.Equal(t, testRouter, swap.Tx.To)
		assert.Equal(t, common.FromHex("0xabcdef"), swap.Tx.Data)
		assert.Equal(t, "0", swap.Tx.Value.String())
		assert.Equal(t, []string{"UNISWAP_V3"}, swap.Protocols)
	})

	t.Run("rejects missing receiver", func(t *testing.T) {
		client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("server should not be called")
		})

		swap, err := client.GetSwap(context.Background(), SwapRequest{
			ChainID:     constants.MainnetChainID,
			FromToken:   testUSDC,
			ToToken:     testUSDT,
			Amount:      big.NewInt(1),
			FromAddress: testFromAddress,
			ToAddress:   constants.GetNullAddress(),
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "to address is not set")
		assert.Nil(t, swap)
	})
}

func TestSwapUnmarshalJSON(t *testing.T) {
	t.Run("invalid dstAmount is rejected", func(t *testing.T) {
		err := json.Unmarshal([]byte(`{"dstAmount":"bad","tx":{"value":"0"}}`), &Swap{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to parse dstAmount")
	})

	t.Run("hex value is supported", func(t *testing.T) {
		var swap Swap
		err := json.Unmarshal([]byte(`{
			"dstAmount": "10",
			"tx": {
				"from": "0x1111111111111111111111111111111111111111",
				"to": "0x2222222222222222222222222222222222222222",
				"data": "0xabcd",
				"value": "0x10",
				"gas": 1,
				"gasPrice": "1"
			}
		}`), &swap)
		require.NoError(t, err)
		assert.Equal(t, "16", swap.Tx.Value.String())
	})
}
