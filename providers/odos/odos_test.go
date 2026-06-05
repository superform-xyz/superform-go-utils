package odos

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
)

func newTestClient(t *testing.T, handler http.HandlerFunc) *odos {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	client, ok := New(WithBaseURL(server.URL), WithRetry(0, time.Millisecond)).(*odos)
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
	client := New(WithBaseURL("https://example.com/"), WithTimeout(15*time.Second), WithRetry(2, time.Second))
	require.NotNil(t, client)

	concrete, ok := client.(*odos)
	require.True(t, ok)
	assert.Equal(t, "https://example.com", concrete.baseURL)
	assert.NotNil(t, concrete.client)
	require.NotNil(t, concrete.timeout)
	assert.Equal(t, 15*time.Second, *concrete.timeout)
	require.NotNil(t, concrete.maxRetries)
	assert.Equal(t, uint(2), *concrete.maxRetries)
	require.NotNil(t, concrete.retryDelay)
	assert.Equal(t, time.Second, *concrete.retryDelay)
}

func TestGetQuote(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/sor/quote/v2", r.URL.Path)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		var req QuoteRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		assert.Equal(t, uint64(1), req.ChainID)
		require.Len(t, req.InputTokens, 1)
		assert.Equal(t, "1000000", req.InputTokens[0].Amount)
		require.Len(t, req.OutputTokens, 1)
		assert.Equal(t, "0xdAC17F958D2ee523a2206206994597C13D831ec7", req.OutputTokens[0].TokenAddress)
		assert.Equal(t, []string{"Uniswap V2"}, req.SourceBlacklist)

		writeJSON(t, w, Quote{
			PathID:     "path-1",
			OutAmounts: []string{"999000"},
		})
	})

	quote, err := client.GetQuote(context.Background(), QuoteRequest{
		ChainID: 1,
		Compact: false,
		Simple:  true,
		InputTokens: []InputToken{{
			Amount:       "1000000",
			TokenAddress: "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48",
		}},
		OutputTokens: []OutputToken{{
			Proportion:   1,
			TokenAddress: "0xdAC17F958D2ee523a2206206994597C13D831ec7",
		}},
		SlippageLimitPercent: 1,
		UserAddr:             "0x1111111111111111111111111111111111111111",
		SourceBlacklist:      []string{"Uniswap V2"},
	})
	require.NoError(t, err)
	require.NotNil(t, quote)
	assert.Equal(t, "path-1", quote.PathID)
	assert.Equal(t, []string{"999000"}, quote.OutAmounts)
}

func TestGetQuoteHTTPError(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	})

	quote, err := client.GetQuote(context.Background(), QuoteRequest{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "odos quote API returned status 500")
	assert.Nil(t, quote)
}

func TestAssemble(t *testing.T) {
	outputToken := common.HexToAddress("0xdAC17F958D2ee523a2206206994597C13D831ec7")
	router := common.HexToAddress("0x3333333333333333333333333333333333333333")

	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/sor/assemble", r.URL.Path)

		var req AssembleRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		assert.Equal(t, "path-1", req.PathID)
		assert.False(t, req.Simulate)

		writeJSON(t, w, map[string]any{
			"traceId":          "trace-1",
			"blockNumber":      123,
			"gasEstimate":      21000,
			"gasEstimateValue": 0.12,
			"outputTokens": []map[string]any{
				{
					"tokenAddress": outputToken.Hex(),
					"amount":       "999000",
				},
			},
			"transaction": map[string]any{
				"data":     "0xabcd",
				"value":    "16",
				"to":       router.Hex(),
				"from":     "0x1111111111111111111111111111111111111111",
				"gas":      21000,
				"gasPrice": int64(100),
				"nonce":    7,
				"chainId":  1,
			},
		})
	})

	assembled, err := client.Assemble(context.Background(), AssembleRequest{PathID: "path-1"})
	require.NoError(t, err)
	require.NotNil(t, assembled)
	assert.Equal(t, "trace-1", assembled.TraceID)
	require.Len(t, assembled.OutputTokens, 1)
	assert.Equal(t, outputToken, assembled.OutputTokens[0].TokenAddress)
	assert.Equal(t, "999000", assembled.OutputTokens[0].Amount.String())
	assert.Equal(t, common.FromHex("0xabcd"), assembled.Transaction.Data)
	assert.Equal(t, big.NewInt(16), assembled.Transaction.Value)
	assert.Equal(t, router, assembled.Transaction.To)
}

func TestAssembleRejectsInvalidOutputAmount(t *testing.T) {
	err := json.Unmarshal([]byte(`{
		"outputTokens": [{"tokenAddress": "0x1", "amount": "bad"}],
		"transaction": {"value": "0"}
	}`), &Assemble{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse output amount")
}

func TestAssembleRejectsInvalidTransactionData(t *testing.T) {
	err := json.Unmarshal([]byte(`{
		"outputTokens": [{"tokenAddress": "0x1", "amount": "1"}],
		"transaction": {"data": "0xzz", "value": "0"}
	}`), &Assemble{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to decode transaction data")
}
