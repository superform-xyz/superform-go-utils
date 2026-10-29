package hyperliquid

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
)

func TestAdditionalInfoReadsReturnFactsWithoutEligibilityPolicy(t *testing.T) {
	user := common.HexToAddress("0xABCDEF0000000000000000000000000000000123")
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var request map[string]json.RawMessage
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		var kind string
		require.NoError(t, json.Unmarshal(request["type"], &kind))
		if kind != "meta" {
			require.JSONEq(t, `"`+strings.ToLower(user.Hex())+`"`, string(request["user"]))
		}
		var response string
		switch kind {
		case "meta":
			response = `{"universe":[{"name":"OLD","szDecimals":9,"isDelisted":true},{"name":"ETH","szDecimals":4,"maxLeverage":50}]}`
		case "userRateLimit":
			response = `{"nRequestsCap":10,"nRequestsUsed":12,"cumVlm":9007199254740993.000000001}`
		case "extraAgents":
			response = `[{"address":"malformed-provider-address","name":"unknown","validUntil":-1},{"address":"0xABCDEF0000000000000000000000000000000123","name":"active","validUntil":100}]`
		case "orderStatus":
			require.Equal(t, `"0xclient-id"`, string(request["oid"]))
			response = `{"status":"order","order":{"status":"futureVenueStatus","order":{"oid":18446744073709551615,"cloid":"0xclient-id","futureField":true}}}`
		default:
			t.Errorf("unexpected request %q", kind)
		}
		_, err := io.WriteString(w, response)
		require.NoError(t, err)
	}))
	defer server.Close()
	client := NewInfoClient(server.URL, server.Client())
	meta, err := client.PerpMeta(context.Background())
	require.NoError(t, err)
	require.Len(t, meta.Universe, 2)
	require.True(t, meta.Universe[0].Delisted)
	require.Equal(t, 9, meta.Universe[0].SizeDecimals, "SDK does not apply app market eligibility")
	budget, err := client.UserRateLimit(context.Background(), user)
	require.NoError(t, err)
	require.Equal(t, int64(10), *budget.Cap)
	require.Equal(t, int64(12), *budget.Used, "SDK does not reserve or reinterpret allowance")
	require.Equal(t, "9007199254740993.000000001", budget.CumVlm)
	agents, err := client.RawExtraAgents(context.Background(), user)
	require.NoError(t, err)
	require.Len(t, agents, 2)
	require.Equal(t, "malformed-provider-address", agents[0].Address)
	require.Equal(t, int64(-1), agents[0].ValidUntil)
	require.Equal(t, "0xABCDEF0000000000000000000000000000000123", agents[1].Address, "raw facts retain original case")
	status, err := client.OrderStatus(context.Background(), user, "0xclient-id")
	require.NoError(t, err)
	require.Equal(t, "futureVenueStatus", status.Order.Status)
	require.Equal(t, uint64(18446744073709551615), status.Order.Order.OrderID)
	require.Contains(t, string(status.Raw), `"futureField":true`)
	marshaled, err := json.Marshal(status)
	require.NoError(t, err)
	require.NotContains(t, string(marshaled), "futureField", "retained raw provider prose is not included in typed evidence by default")
	require.Equal(t, int32(4), calls.Load())
}

func TestDecodeSpotMetaAndAssetCtxsWithoutTransport(t *testing.T) {
	meta, contexts, err := DecodeSpotMetaAndAssetCtxs([]json.RawMessage{
		json.RawMessage(`{"tokens":[{"name":"HYPE","index":150,"weiDecimals":8}]}`),
		json.RawMessage(`[{"midPx":null,"markPx":"24.123456789"}]`),
	})
	require.NoError(t, err)
	require.Equal(t, int64(150), meta.Tokens[0].Index)
	require.Nil(t, contexts[0].MidPx)
	require.Equal(t, "24.123456789", *contexts[0].MarkPx)
	_, _, err = DecodeSpotMetaAndAssetCtxs([]json.RawMessage{json.RawMessage(`{}`)})
	require.ErrorContains(t, err, "returned 1 elements, want 2")
}

func TestUserRateLimitRetainsMissingCounters(t *testing.T) {
	var limit UserRateLimit
	require.NoError(t, json.Unmarshal([]byte(`{"cumVlm":""}`), &limit))
	require.Nil(t, limit.Cap)
	require.Nil(t, limit.Used)
	require.Empty(t, limit.CumVlm)
}

func TestLedgerPagePreservesHashlessRepeatedAndUnorderedRows(t *testing.T) {
	for _, withEnd := range []bool{false, true} {
		t.Run(map[bool]string{false: "without end", true: "with end"}[withEnd], func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var request map[string]json.RawMessage
				require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
				require.Equal(t, `"userNonFundingLedgerUpdates"`, string(request["type"]))
				require.Equal(t, `123`, string(request["startTime"]))
				end, exists := request["endTime"]
				require.Equal(t, withEnd, exists)
				if withEnd {
					require.Equal(t, `200`, string(end))
				}
				// A raw page imposes no fixed 500-row contract or global dedup.
				row := `{"time":125,"delta":{"type":"deposit","usdc":1.000000001}}`
				_, err := io.WriteString(w, `[`+strings.Repeat(row+`,`, 500)+`{"time":124,"delta":{"type":"future","newField":1}}]`)
				require.NoError(t, err)
			}))
			defer server.Close()
			var end *int64
			if withEnd {
				value := int64(200)
				end = &value
			}
			page, err := NewInfoClient(server.URL, server.Client()).NonFundingLedgerUpdatesPage(context.Background(), common.Address{}, 123, end)
			require.NoError(t, err)
			require.Len(t, page, 501)
			require.Empty(t, page[0].Hash)
			require.Equal(t, "1.000000001", page[0].Delta.Usdc)
			require.Equal(t, page[0], page[1])
			require.Equal(t, int64(124), page[500].Time)
			require.Contains(t, string(page[500].Delta.Raw), `"newField":1`)
			require.Equal(t, int32(1), calls.Load())
		})
	}
}

func TestOrderStatusSendsNumericOrderIDWithoutFloatConversion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]json.RawMessage
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		require.Equal(t, `18446744073709551615`, string(request["oid"]))
		_, err := io.WriteString(w, `{"status":"unknownOid"}`)
		require.NoError(t, err)
	}))
	defer server.Close()
	status, err := NewInfoClient(server.URL, server.Client()).OrderStatus(context.Background(), common.Address{}, uint64(18446744073709551615))
	require.NoError(t, err)
	require.Equal(t, "unknownOid", status.Status)
	require.Nil(t, status.Order)
}
