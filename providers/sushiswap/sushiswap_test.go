package sushiswap

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/superform-xyz/superform-go-utils/pkg/http_client"
	"github.com/superform-xyz/superform-go-utils/utils/constants"
)

var (
	testTokenOut = common.HexToAddress("0x6b3595068778dd592e39a122f4f5a5cf09c90fe2")
	testSender   = common.HexToAddress("0x1111111111111111111111111111111111111111")
)

func quoteRequest() QuoteRequest {
	return QuoteRequest{ChainID: 1, TokenOut: testTokenOut, Amount: big.NewInt(1_000_000_000_000_000), SlippageBps: 50}
}

func swapRequest() SwapRequest {
	return SwapRequest{ChainID: 1, TokenOut: testTokenOut, Amount: big.NewInt(1_000_000_000_000_000), SlippageBps: 50, Sender: testSender}
}

func newTestClient(t *testing.T, handler http.HandlerFunc, opts ...Option) Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	opts = append([]Option{WithBaseURL(server.URL)}, opts...)
	client, err := New(opts...)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	return client
}

func successBody(t *testing.T) map[string]any {
	t.Helper()
	body, err := os.ReadFile("testdata/success.json")
	require.NoError(t, err)
	var response map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	require.NoError(t, decoder.Decode(&response))
	return response
}

func writeJSON(t *testing.T, w http.ResponseWriter, response any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	assert.NoError(t, json.NewEncoder(w).Encode(response))
}

func TestGetQuote(t *testing.T) {
	for _, authenticated := range []bool{false, true} {
		t.Run(map[bool]string{false: "public", true: "api key"}[authenticated], func(t *testing.T) {
			body := successBody(t)
			delete(body, "tx") // quote responses do not contain a transaction
			apiKey := ""
			if authenticated {
				apiKey = "test-key &+?"
			}
			client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodGet, r.Method)
				assert.Equal(t, "/quote/v7/1", r.URL.Path)
				assert.Equal(t, "application/json", r.Header.Get("Accept"))
				params := r.URL.Query()
				assert.Equal(t, constants.GetNativeToken().Hex(), params.Get("tokenIn"))
				assert.Equal(t, testTokenOut.Hex(), params.Get("tokenOut"))
				assert.Equal(t, "1000000000000000", params.Get("amount"))
				assert.Equal(t, "0.005", params.Get("maxSlippage"))
				assert.Equal(t, "superform & utils", params.Get("referrer"))
				assert.Equal(t, apiKey, params.Get("apiKey"))
				assert.Equal(t, authenticated, params.Has("apiKey"))
				writeJSON(t, w, body)
			}, WithAPIKey("  "+apiKey+"  "))
			req := quoteRequest()
			req.Referrer = "superform & utils"
			quote, err := client.GetQuote(context.Background(), req)
			require.NoError(t, err)
			assert.Equal(t, StatusSuccess, quote.Status)
			assert.Equal(t, "11951002273851281408", quote.AssumedAmountOut.String())
			assert.Equal(t, "11891247262482025000", quote.EstimatedAmountOutMin.String())
			assert.Equal(t, req.Amount.String(), quote.AmountIn.String())
			assert.Equal(t, "SUSHI", quote.Tokens[quote.TokenTo].Symbol)
			assert.Equal(t, uint64(77000), quote.GasSpent)
			assert.Equal(t, "1000000000000000", req.Amount.String())
		})
	}
}

func TestGetSwap(t *testing.T) {
	for _, simulate := range []bool{false, true} {
		t.Run(map[bool]string{false: "without simulation", true: "with simulation"}[simulate], func(t *testing.T) {
			body := successBody(t)
			if simulate {
				body["tx"].(map[string]any)["gas"] = "123456"
			}
			recipient := common.HexToAddress("0x2222222222222222222222222222222222222222")
			client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/swap/v7/1", r.URL.Path)
				assert.Equal(t, testSender.Hex(), r.URL.Query().Get("sender"))
				assert.Equal(t, recipient.Hex(), r.URL.Query().Get("recipient"))
				assert.Equal(t, map[bool]string{false: "false", true: "true"}[simulate], r.URL.Query().Get("simulate"))
				assert.Equal(t, "test-key", r.URL.Query().Get("apiKey"))
				writeJSON(t, w, body)
			}, WithAPIKey("test-key"))
			req := swapRequest()
			req.Recipient, req.Simulate = recipient, simulate
			swap, err := client.GetSwap(context.Background(), req)
			require.NoError(t, err)
			assert.Equal(t, testSender, swap.Tx.From)
			assert.Equal(t, common.HexToAddress("0xac4c6e212a361c968f1725b4d055b47e63f80b75"), swap.Tx.To)
			assert.Equal(t, []byte{0x5f, 0x3b, 0xd1, 0xc8, 0, 0, 0, 0}, swap.Tx.Data)
			assert.Equal(t, "1000000000000000", swap.Tx.Value.String())
			assert.Equal(t, "47396891", swap.Tx.GasPrice.String())
			assert.Equal(t, "11891247262482025000", swap.EstimatedAmountOutMin.String())
			if simulate {
				assert.Equal(t, uint64(123456), swap.Tx.Gas)
			} else {
				assert.Zero(t, swap.Tx.Gas)
			}
		})
	}
}

func TestGetSwapOmittedValueAndRecipient(t *testing.T) {
	body := successBody(t)
	body["tokens"].([]any)[0].(map[string]any)["address"] = common.HexToAddress("0x3333333333333333333333333333333333333333").Hex()
	delete(body["tx"].(map[string]any), "value")
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.False(t, r.URL.Query().Has("recipient"))
		writeJSON(t, w, body)
	})
	req := swapRequest()
	req.TokenIn = common.HexToAddress("0x3333333333333333333333333333333333333333")
	swap, err := client.GetSwap(context.Background(), req)
	require.NoError(t, err)
	assert.Zero(t, swap.Tx.Value.Sign())
}

func TestInvalidRequests(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("invalid request reached the API")
	})
	for _, tc := range []struct {
		name   string
		modify func(*QuoteRequest)
	}{
		{"zero chain", func(r *QuoteRequest) { r.ChainID = 0 }},
		{"same token", func(r *QuoteRequest) { r.TokenIn = r.TokenOut }},
		{"native aliases", func(r *QuoteRequest) { r.TokenOut = constants.GetNativeToken() }},
		{"nil amount", func(r *QuoteRequest) { r.Amount = nil }},
		{"zero amount", func(r *QuoteRequest) { r.Amount = new(big.Int) }},
		{"negative amount", func(r *QuoteRequest) { r.Amount = big.NewInt(-1) }},
		{"overflow amount", func(r *QuoteRequest) { r.Amount = new(big.Int).Lsh(big.NewInt(1), 256) }},
		{"negative slippage", func(r *QuoteRequest) { r.SlippageBps = -1 }},
		{"100 percent slippage", func(r *QuoteRequest) { r.SlippageBps = 10000 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := quoteRequest()
			tc.modify(&r)
			quote, err := client.GetQuote(context.Background(), r)
			require.Error(t, err)
			assert.Nil(t, quote)
			swap, err := client.GetSwap(context.Background(), SwapRequest{
				ChainID: r.ChainID, TokenIn: r.TokenIn, TokenOut: r.TokenOut,
				Amount: r.Amount, SlippageBps: r.SlippageBps, Sender: testSender,
			})
			require.Error(t, err)
			assert.Nil(t, swap)
		})
	}
	req := swapRequest()
	req.Sender = common.Address{}
	swap, err := client.GetSwap(context.Background(), req)
	require.ErrorContains(t, err, "sender")
	assert.Nil(t, swap)
}

func TestSlippageBoundariesAndNativeAliases(t *testing.T) {
	for _, bps := range []int{0, 1, 9999} {
		t.Run(big.NewInt(int64(bps)).String(), func(t *testing.T) {
			req := quoteRequest()
			req.TokenIn = constants.GetNativeToken()
			req.SlippageBps = bps
			params, err := quoteParams(req)
			require.NoError(t, err)
			assert.Equal(t, map[int]string{0: "0", 1: "0.0001", 9999: "0.9999"}[bps], params.Get("maxSlippage"))
			assert.Equal(t, constants.GetNativeToken().Hex(), params.Get("tokenIn"))
		})
	}
}

func TestRejectedRoutes(t *testing.T) {
	for _, tc := range []struct {
		status RouteStatus
		err    error
	}{
		{StatusNoWay, ErrNoRoute},
		{StatusPartial, ErrPartialRoute},
	} {
		t.Run(string(tc.status), func(t *testing.T) {
			client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				writeJSON(t, w, map[string]any{"status": tc.status})
			})
			quote, err := client.GetQuote(context.Background(), quoteRequest())
			require.ErrorIs(t, err, tc.err)
			assert.Nil(t, quote)
			swap, err := client.GetSwap(context.Background(), swapRequest())
			require.ErrorIs(t, err, tc.err)
			assert.Nil(t, swap)
		})
	}
}

func TestMalformedResponses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		modify func(map[string]any)
	}{
		{"missing status", func(b map[string]any) { delete(b, "status") }},
		{"unknown status", func(b map[string]any) { b["status"] = "SomethingElse" }},
		{"missing amount", func(b map[string]any) { delete(b, "amountIn") }},
		{"mismatched input", func(b map[string]any) { b["amountIn"] = "1" }},
		{"zero output", func(b map[string]any) { b["assumedAmountOut"] = "0" }},
		{"negative output", func(b map[string]any) { b["assumedAmountOut"] = "-1" }},
		{"hex output", func(b map[string]any) { b["assumedAmountOut"] = "0xff" }},
		{"fractional output", func(b map[string]any) { b["assumedAmountOut"] = "1.5" }},
		{"overflow output", func(b map[string]any) { b["assumedAmountOut"] = new(big.Int).Lsh(big.NewInt(1), 256).String() }},
		{"missing token index", func(b map[string]any) { delete(b, "tokenFrom") }},
		{"negative token index", func(b map[string]any) { b["tokenFrom"] = -1 }},
		{"invalid token index", func(b map[string]any) { b["tokenTo"] = 10 }},
		{"mismatched token", func(b map[string]any) { b["tokens"].([]any)[1].(map[string]any)["address"] = testSender.Hex() }},
		{"missing tx", func(b map[string]any) { delete(b, "tx") }},
		{"zero router", func(b map[string]any) { b["tx"].(map[string]any)["to"] = (common.Address{}).Hex() }},
		{"bad router", func(b map[string]any) { b["tx"].(map[string]any)["to"] = "0x1234" }},
		{"bad hex", func(b map[string]any) { b["tx"].(map[string]any)["data"] = "0xzzzzzzzz" }},
		{"odd hex", func(b map[string]any) { b["tx"].(map[string]any)["data"] = "0x123456789" }},
		{"short calldata", func(b map[string]any) { b["tx"].(map[string]any)["data"] = "0x1234" }},
		{"missing hex prefix", func(b map[string]any) { b["tx"].(map[string]any)["data"] = "12345678" }},
		{"negative value", func(b map[string]any) { b["tx"].(map[string]any)["value"] = "-1" }},
		{"invalid gas", func(b map[string]any) { b["tx"].(map[string]any)["gas"] = "garbage" }},
		{"wrong sender", func(b map[string]any) { b["tx"].(map[string]any)["from"] = testTokenOut.Hex() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := successBody(t)
			tc.modify(body)
			client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(t, w, body) })
			swap, err := client.GetSwap(context.Background(), swapRequest())
			require.Error(t, err)
			assert.Nil(t, swap)
		})
	}
}

func TestHTTPFailures(t *testing.T) {
	for _, customClient := range []bool{false, true} {
		for _, status := range []int{401, 422, 429, 500, 503} {
			t.Run(big.NewInt(int64(status)).String()+map[bool]string{false: "/default", true: "/custom"}[customClient], func(t *testing.T) {
				var requests atomic.Int32
				var opts []Option
				if customClient {
					opts = append(opts, WithHTTPClient(&http.Client{Timeout: time.Second}))
				}
				client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					w.Header().Set("Retry-After", "2")
					w.WriteHeader(status)
					writeJSON(t, w, map[string]any{"title": "upstream error", "status": status})
				}, opts...)
				quote, err := client.GetQuote(context.Background(), quoteRequest())
				require.Error(t, err)
				assert.Nil(t, quote)
				code, body, ok := http_client.ResponseStatus(err)
				assert.True(t, ok)
				assert.Equal(t, status, code)
				assert.Contains(t, body, "upstream error")
				assert.Equal(t, int32(1), requests.Load(), "requests are not retried by default")
				if status == 429 {
					delay, ok := http_client.RetryAfter(err)
					assert.True(t, ok)
					assert.Equal(t, 2*time.Second, delay)
				}
			})
		}
	}
}

func TestInvalidJSON(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("<html>bad gateway</html>")) })
	quote, err := client.GetQuote(context.Background(), quoteRequest())
	require.Error(t, err)
	assert.Nil(t, quote)
}

func TestContextCancellationDoesNotExposeAPIKey(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("cancelled request reached API") }, WithAPIKey("secret-test-key"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := client.GetQuote(ctx, quoteRequest())
	require.ErrorIs(t, err, context.Canceled)
	assert.NotContains(t, err.Error(), "secret-test-key")
}

func TestTimeoutAndRetry(t *testing.T) {
	var requests atomic.Int32
	body := successBody(t)
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		writeJSON(t, w, body)
	}, WithTimeout(time.Second), WithRetry(1, time.Millisecond))
	_, err := client.GetQuote(context.Background(), quoteRequest())
	require.NoError(t, err)
	assert.Equal(t, int32(2), requests.Load())

	client = newTestClient(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }, WithTimeout(20*time.Millisecond))
	_, err = client.GetQuote(context.Background(), quoteRequest())
	require.True(t, errors.Is(err, context.DeadlineExceeded), "%v", err)
}
