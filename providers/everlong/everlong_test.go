package everlong

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/superform-xyz/superform-go-utils/pkg/http_client"
	"github.com/superform-xyz/superform-go-utils/utils/constants"
)

func swapRequest() SwapRequest {
	return SwapRequest{
		QuoteRequest: QuoteRequest{
			ChainID:  constants.KatanaChainID,
			TokenIn:  common.HexToAddress("0x203A662b0BD271A6ed5a60EdFbd04bFce608FD36"),
			TokenOut: common.HexToAddress("0xEE7D8BCFb72bC1880D0Cf19822eB0A2e6577aB62"),
			Amount:   big.NewInt(1000000),
		},
		Recipient:   common.HexToAddress("0x1111111111111111111111111111111111111111"),
		SlippageBps: 50,
	}
}

func fixture(t *testing.T) map[string]any {
	t.Helper()
	data, err := os.ReadFile("testdata/success.json")
	require.NoError(t, err)
	var result map[string]any
	require.NoError(t, json.Unmarshal(data, &result))
	return result
}

func testClient(t *testing.T, handler http.HandlerFunc, opts ...Option) Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := New(append([]Option{WithBaseURL(server.URL), WithAPIKey("test-key")}, opts...)...)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	return client
}

func writeResponse(t *testing.T, w http.ResponseWriter, body any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	assert.NoError(t, json.NewEncoder(w).Encode(body))
}

func TestGetSwap(t *testing.T) {
	req := swapRequest()
	req.ExcludePools = []common.Address{common.HexToAddress("0x2222222222222222222222222222222222222222"), common.HexToAddress("0x3333333333333333333333333333333333333333")}
	body := fixture(t)
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/v1/swap", r.URL.Path)
		assert.Equal(t, "test-key", r.Header.Get("x-api-key"))
		assert.Equal(t, "application/json", r.Header.Get("Accept"))
		assert.Equal(t, req.TokenIn.Hex(), r.URL.Query().Get("tokenIn"))
		assert.Equal(t, req.TokenOut.Hex(), r.URL.Query().Get("tokenOut"))
		assert.Equal(t, "1000000", r.URL.Query().Get("amountIn"))
		assert.Equal(t, "50", r.URL.Query().Get("slippage"))
		assert.Equal(t, req.Recipient.Hex(), r.URL.Query().Get("recipient"))
		assert.Equal(t, req.ExcludePools[0].Hex()+","+req.ExcludePools[1].Hex(), r.URL.Query().Get("excludePools"))
		assert.Len(t, r.URL.Query(), 6, "only documented parameters are sent")
		writeResponse(t, w, body)
	})
	swap, err := client.GetSwap(t.Context(), req)
	require.NoError(t, err)
	assert.Equal(t, req.Amount, swap.AmountIn)
	assert.Equal(t, "488353227241368", swap.AssumedAmountOut.String(), "the API already deducted its fee")
	assert.Equal(t, "485911461105161", swap.EstimatedAmountOutMin.String())
	assert.Equal(t, big.NewInt(195419458680), swap.FeeAmountOut)
	assert.Equal(t, common.HexToAddress("0x42eF97FaE7AD46d98bAf99A0e505b43B7e1C00e9"), swap.Tx.To)
	assert.Equal(t, common.FromHex(body["tx"].(map[string]any)["data"].(string)), swap.Tx.Data)
	assert.Zero(t, swap.Tx.Value.Sign())
}

func TestGetQuote(t *testing.T) {
	body := fixture(t)
	delete(body, "tx")
	body["status"] = "Ok" // The guide documents Ok; Swagger and live responses use Success.
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/quote", r.URL.Path)
		assert.Len(t, r.URL.Query(), 3)
		writeResponse(t, w, body)
	})
	quote, err := client.GetQuote(t.Context(), swapRequest().QuoteRequest)
	require.NoError(t, err)
	assert.Equal(t, StatusOK, quote.Status)
	assert.Equal(t, quote.AssumedAmountOut, quote.EstimatedAmountOutMin)
}

func TestGetSwapNativeValueAndMissingERC20Value(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(fmt.Sprintf("native=%t", native), func(t *testing.T) {
			req := swapRequest()
			body := fixture(t)
			tx := body["tx"].(map[string]any)
			delete(tx, "value") // The guide omits zero ERC20 transaction values.
			if native {
				req.TokenIn = common.Address{}
				body["tokens"].([]any)[0].(map[string]any)["address"] = constants.GetNativeToken().Hex()
				tx["value"] = req.Amount.String()
			}
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if native {
					assert.Equal(t, constants.GetNativeToken().Hex(), r.URL.Query().Get("tokenIn"))
				}
				writeResponse(t, w, body)
			})
			swap, err := client.GetSwap(t.Context(), req)
			require.NoError(t, err)
			if native {
				assert.Equal(t, req.Amount, swap.Tx.Value)
			} else {
				assert.Zero(t, swap.Tx.Value.Sign())
			}
		})
	}
}

func TestInvalidRequestsDoNotCallAPI(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*SwapRequest)
	}{
		{"unsupported chain", func(r *SwapRequest) { r.ChainID = 1 }},
		{"missing chain", func(r *SwapRequest) { r.ChainID = 0 }},
		{"same tokens", func(r *SwapRequest) { r.TokenOut = r.TokenIn }},
		{"same native tokens", func(r *SwapRequest) { r.TokenIn = common.Address{}; r.TokenOut = constants.GetNativeToken() }},
		{"missing amount", func(r *SwapRequest) { r.Amount = nil }},
		{"zero amount", func(r *SwapRequest) { r.Amount = big.NewInt(0) }},
		{"negative amount", func(r *SwapRequest) { r.Amount = big.NewInt(-1) }},
		{"overflow amount", func(r *SwapRequest) { r.Amount = new(big.Int).Lsh(big.NewInt(1), 256) }},
		{"missing recipient", func(r *SwapRequest) { r.Recipient = common.Address{} }},
		{"zero slippage", func(r *SwapRequest) { r.SlippageBps = 0 }},
		{"negative slippage", func(r *SwapRequest) { r.SlippageBps = -1 }},
		{"excessive slippage", func(r *SwapRequest) { r.SlippageBps = 1001 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected API request"); w.WriteHeader(500) })
			req := swapRequest()
			tc.edit(&req)
			swap, err := client.GetSwap(t.Context(), req)
			require.Error(t, err)
			assert.Nil(t, swap)
		})
	}
}

func TestRejectsInvalidResponses(t *testing.T) {
	for _, tc := range []struct {
		name  string
		edit  func(map[string]any)
		cause error
	}{
		{"no route", func(r map[string]any) { r["status"] = "NoWay" }, ErrNoRoute},
		{"partial route", func(r map[string]any) { r["status"] = "Partial" }, ErrPartialRoute},
		{"unknown status", func(r map[string]any) { r["status"] = "unknown" }, nil},
		{"mismatched input", func(r map[string]any) { r["amountIn"] = "2" }, nil},
		{"negative output", func(r map[string]any) { r["assumedAmountOut"] = "-1" }, nil},
		{"zero output", func(r map[string]any) { r["assumedAmountOut"] = "0" }, nil},
		{"overflow output", func(r map[string]any) { r["assumedAmountOut"] = new(big.Int).Lsh(big.NewInt(1), 256).String() }, nil},
		{"missing output", func(r map[string]any) { delete(r, "assumedAmountOut") }, nil},
		{"missing token index", func(r map[string]any) { delete(r, "tokenTo") }, nil},
		{"invalid token index", func(r map[string]any) { r["tokenTo"] = -1 }, nil},
		{"out of range token", func(r map[string]any) { r["tokenTo"] = 99 }, nil},
		{"mismatched token", func(r map[string]any) {
			r["tokens"].([]any)[0].(map[string]any)["address"] = swapRequest().TokenOut.Hex()
		}, nil},
		{"missing tx", func(r map[string]any) { delete(r, "tx") }, nil},
		{"missing router", func(r map[string]any) { delete(r["tx"].(map[string]any), "to") }, nil},
		{"malformed calldata", func(r map[string]any) { r["tx"].(map[string]any)["data"] = "0xzz" }, nil},
		{"short calldata", func(r map[string]any) { r["tx"].(map[string]any)["data"] = "0x1234" }, nil},
		{"missing hex prefix", func(r map[string]any) { r["tx"].(map[string]any)["data"] = "12345678" }, nil},
		{"invalid tx value", func(r map[string]any) { r["tx"].(map[string]any)["value"] = "-1" }, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := fixture(t)
			tc.edit(body)
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) { writeResponse(t, w, body) })
			swap, err := client.GetSwap(t.Context(), swapRequest())
			require.Error(t, err)
			assert.Nil(t, swap)
			if tc.cause != nil {
				assert.ErrorIs(t, err, tc.cause)
			}
		})
	}
}

func TestHTTPFailures(t *testing.T) {
	for _, status := range []int{401, 429, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) { calls++; http.Error(w, "upstream failed", status) })
			_, err := client.GetSwap(t.Context(), swapRequest())
			require.Error(t, err)
			got, _, ok := http_client.ResponseStatus(err)
			require.True(t, ok)
			assert.Equal(t, status, got)
			assert.Equal(t, 1, calls)
			assert.NotContains(t, err.Error(), "test-key")
		})
	}
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, err := fmt.Fprint(w, "<html>gateway error</html>")
		assert.NoError(t, err)
	})
	_, err := client.GetSwap(t.Context(), swapRequest())
	require.ErrorContains(t, err, "decoding JSON")
}

func TestCancellationAndTimeout(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }, WithTimeout(20*time.Millisecond))
	_, err := client.GetSwap(t.Context(), swapRequest())
	require.ErrorIs(t, err, context.DeadlineExceeded)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = client.GetSwap(ctx, swapRequest())
	require.ErrorIs(t, err, context.Canceled)
}

func TestNew(t *testing.T) {
	_, err := New()
	require.ErrorContains(t, err, "API key")
	_, err = New(WithAPIKey("  "))
	require.ErrorContains(t, err, "API key")
	_, err = New(WithAPIKey("test-key"), WithBaseURL("://invalid"))
	require.ErrorContains(t, err, "base URL")
	client, err := New(nil, WithAPIKey(" test-key "), WithBaseURL(" https://example.com/ "), WithHTTPClient(&http.Client{}))
	require.NoError(t, err)
	defer func() { require.NoError(t, client.Close()) }()
	assert.Equal(t, "https://example.com", client.(*everlong).baseURL)
}

func TestLiveSwap(t *testing.T) {
	if testing.Short() {
		t.Skip("live Everlong API check")
	}
	key := strings.TrimSpace(os.Getenv("EVERLONG_API_KEY"))
	if key == "" {
		t.Skip("EVERLONG_API_KEY is not set")
	}
	client, err := New(WithAPIKey(key))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	swap, err := client.GetSwap(t.Context(), swapRequest())
	require.NoError(t, err)
	require.Positive(t, swap.AssumedAmountOut.Sign())
	require.GreaterOrEqual(t, len(swap.Tx.Data), 4)
}
