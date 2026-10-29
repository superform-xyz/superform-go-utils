package hyperliquid

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
)

// These captures exercise wire text and open-position fields rather than
// deriving a response from the SDK's own data structures.
func TestInfoReadsPreserveWireValues(t *testing.T) {
	user := common.HexToAddress("0xABCDEF0000000000000000000000000000000123")
	responses := map[string]string{
		"clearinghouseState":          `{"marginSummary":{"accountValue":"332.396495","totalNtlPos":"3323.160170","totalRawUsd":"-2990.763675","totalMarginUsed":"332.316017"},"withdrawable":"0","assetPositions":[{"type":"oneWay","position":{"coin":"ETH","szi":"1.23","unrealizedPnl":"-5.11","entryPx":null}}],"time":1755640000000}`,
		"spotClearinghouseState":      `{"balances":[{"coin":"USDC","token":0,"total":"4.8771","hold":"0.0","entryNtl":"4.8"}]}`,
		"extraAgents":                 `[{"address":"0xa54bae3cc20f1c9dda3a8d1b35f6614023461f02","name":"mobile","validUntil":1863632559923}]`,
		"userNonFundingLedgerUpdates": `[{"time":1755640000000,"hash":"0x1234","delta":{"type":"send","usdcValue":"1.00000001","user":"0xabc","destination":"0xdef","token":"USDC","fee":"0.01"}}]`,
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "application/json", r.Header.Get("Content-Type"))
		var request struct {
			Type      string `json:"type"`
			User      string `json:"user"`
			StartTime int64  `json:"startTime"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		require.Equal(t, strings.ToLower(user.Hex()), request.User)
		if request.Type == "userNonFundingLedgerUpdates" {
			require.Equal(t, int64(123), request.StartTime)
		}
		response, ok := responses[request.Type]
		require.True(t, ok)
		_, err := io.WriteString(w, response)
		require.NoError(t, err)
	}))
	defer server.Close()
	c := NewInfoClient(server.URL, server.Client())
	perp, err := c.ClearinghouseState(context.Background(), user)
	require.NoError(t, err)
	require.Equal(t, "-2990.763675", perp.MarginSummary.TotalRawUsd)
	require.Equal(t, "0", perp.Withdrawable)
	require.Len(t, perp.AssetPositions, 1)
	require.Equal(t, "-5.11", perp.AssetPositions[0].Position.UnrealizedPnl)
	require.Nil(t, perp.AssetPositions[0].Position.EntryPx)
	spot, err := c.SpotClearinghouseState(context.Background(), user)
	require.NoError(t, err)
	require.Equal(t, []SpotBalance{{Coin: "USDC", Token: 0, Total: "4.8771", Hold: "0.0", EntryNtl: "4.8"}}, spot.Balances)
	agents, err := c.ExtraAgents(context.Background(), user)
	require.NoError(t, err)
	require.Equal(t, common.HexToAddress("0xa54bae3cc20f1c9dda3a8d1b35f6614023461f02"), agents[0].Address)
	ledger, err := c.NonFundingLedgerUpdates(context.Background(), user, 123)
	require.NoError(t, err)
	require.Equal(t, "send", ledger[0].Delta.Type)
	require.Equal(t, "1.00000001", ledger[0].Delta.UsdcValue)
}

func TestSpotMetadataAndContexts(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		wantErr bool
	}{
		{"valid", `[{"tokens":[{"name":"HYPE","index":150,"weiDecimals":8,"evmContract":{"address":"0x2222222222222222222222222222222222222222","evm_extra_wei_decimals":10}}],"universe":[{"name":"HYPE/USDC","tokens":[150,0],"isCanonical":true,"index":1}]},[{"midPx":null,"markPx":"24.123456789"}]]`, false},
		{"missing contexts", `[{}]`, true},
		{"invalid meta", `[1,[]]`, true},
		{"invalid contexts", `[{},1]`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request map[string]string
				require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
				require.Equal(t, map[string]string{"type": "spotMetaAndAssetCtxs"}, request)
				_, err := io.WriteString(w, tc.body)
				require.NoError(t, err)
			}))
			defer server.Close()
			meta, contexts, err := NewInfoClient(server.URL, server.Client()).SpotMetaAndAssetCtxs(context.Background())
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, int64(150), meta.Tokens[0].Index)
			require.Equal(t, int32(10), meta.Tokens[0].EvmContract.EvmExtraWeiDecimals)
			require.Nil(t, contexts[0].MidPx)
			require.Equal(t, "24.123456789", *contexts[0].MarkPx)
		})
	}
}

func TestInfoFailureMetadataAndNoRetries(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		limit  int64
		kind   ErrorKind
	}{
		{"rate limited", 429, `{}`, 1024, ""},
		{"server error", 503, strings.Repeat("e", 600), 1024, ""},
		{"oversized", 200, `{"value":"123456"}`, 8, ErrorDecode},
		{"invalid JSON", 200, `{`, 1024, ErrorDecode},
		{"trailing JSON", 200, `{} {}`, 1024, ErrorDecode},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Date", "Thu, 29 Oct 2026 09:00:00 GMT")
				w.WriteHeader(tc.status)
				_, err := io.WriteString(w, tc.body)
				require.NoError(t, err)
			}))
			defer server.Close()
			var out map[string]any
			meta, err := NewInfoClient(server.URL, server.Client(), WithResponseLimit(tc.limit)).Post(context.Background(), map[string]string{"type": "test"}, &out)
			require.Error(t, err)
			require.True(t, meta.Dispatched)
			require.Equal(t, tc.status, meta.StatusCode)
			require.Equal(t, "Thu, 29 Oct 2026 09:00:00 GMT", meta.Date)
			require.Equal(t, int64(1), calls.Load())
			if tc.status == 429 {
				require.ErrorIs(t, err, ErrRateLimited)
			} else if tc.status == 503 {
				var httpErr *HTTPError
				require.ErrorAs(t, err, &httpErr)
				require.Len(t, httpErr.Body, 512)
			} else {
				var requestErr *RequestError
				require.ErrorAs(t, err, &requestErr)
				require.Equal(t, tc.kind, requestErr.Kind)
			}
		})
	}
}

type doerFunc func(*http.Request) (*http.Response, error)

func (f doerFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

func TestInfoDispatchBoundaryAndCancellation(t *testing.T) {
	var calls int
	c := NewInfoClient("http://example.invalid", doerFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return nil, r.Context().Err()
	}))
	var out any
	meta, err := c.Post(context.Background(), make(chan int), &out)
	require.Error(t, err)
	require.False(t, meta.Dispatched)
	var requestErr *RequestError
	require.ErrorAs(t, err, &requestErr)
	require.Equal(t, ErrorEncode, requestErr.Kind)
	meta, err = NewInfoClient("://invalid", c.httpClient).Post(context.Background(), struct{}{}, &out)
	require.Error(t, err)
	require.False(t, meta.Dispatched)
	require.ErrorAs(t, err, &requestErr)
	require.Equal(t, ErrorRequest, requestErr.Kind)
	require.Zero(t, calls)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	meta, err = c.Post(ctx, struct{}{}, &out)
	require.ErrorIs(t, err, context.Canceled)
	require.True(t, meta.Dispatched)
	require.Equal(t, 1, calls)
	require.ErrorAs(t, err, &requestErr)
	require.Equal(t, ErrorTransport, requestErr.Kind)
}

func TestNormalizeAddress(t *testing.T) {
	got, err := NormalizeAddress(" 0xABCDEF0000000000000000000000000000000123 ")
	require.NoError(t, err)
	require.Equal(t, "0xabcdef0000000000000000000000000000000123", got)
	for _, address := range []string{"", "0x123", "0xgggggg0000000000000000000000000000000123"} {
		_, err := NormalizeAddress(address)
		require.True(t, errors.Is(err, ErrInvalidAddress))
	}
}

func TestParseAmount(t *testing.T) {
	for text, want := range map[string]*big.Rat{
		"4.8771":       big.NewRat(48771, 10000),
		"0.0":          big.NewRat(0, 1),
		"1.25e-2":      big.NewRat(1, 80),
		"-2990.763675": big.NewRat(-2990763675, 1000000),
	} {
		got, err := ParseAmount(text)
		require.NoError(t, err)
		require.Zero(t, got.Cmp(want))
	}
	for _, text := range []string{"", "not-a-number", "1/2", "+1", "1.", " 1", "1e", "1e+", "-"} {
		_, err := ParseAmount(text)
		require.Error(t, err, text)
	}
}
