package kyberswap

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

	"github.com/superform-xyz/superform-go-utils/pkg/http_client"
	"github.com/superform-xyz/superform-go-utils/utils/constants"
)

const testRouterAddress = "0x6131B5fae19EA4f9D964eAc0408E4408b66337b5"

func newTestClient(t *testing.T, handler http.HandlerFunc) *kyberswap {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	return &kyberswap{
		clientID: "superform",
		baseURL:  server.URL,
		client: http_client.NewClientBuilder().
			SetAuth("x-client-id", "superform").
			SetRetry(0, time.Millisecond).
			BuildClient(),
	}
}

func TestNew(t *testing.T) {
	client := New("client-id")

	require.NotNil(t, client)

	concrete, ok := client.(*kyberswap)
	require.True(t, ok)
	assert.Equal(t, "client-id", concrete.clientID)
	assert.Equal(t, kyberswapBaseURL, concrete.baseURL)
	assert.NotNil(t, concrete.client)
}

func TestGetRoute(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "superform", r.Header.Get("x-client-id"))
			assert.Equal(t, http.MethodGet, r.Method)
			assert.Equal(t, "/ethereum/api/v1/routes", r.URL.Path)
			assert.Equal(t, "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48", r.URL.Query().Get("tokenIn"))
			assert.Equal(t, "0xC02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2", r.URL.Query().Get("tokenOut"))
			assert.Equal(t, "1000000000", r.URL.Query().Get("amountIn"))

			writeJSON(t, w, routeResponse{
				Code: 0,
				Data: routeData{
					RouteSummary:  json.RawMessage(`{"amountOut":"500000000000000000"}`),
					RouterAddress: testRouterAddress,
				},
			})
		})

		route, err := client.GetRoute(context.Background(), RouteRequest{
			ChainID:  constants.MainnetChainID,
			TokenIn:  "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48",
			TokenOut: "0xC02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2",
			AmountIn: "1000000000",
		})
		require.NoError(t, err)
		assert.JSONEq(t, `{"amountOut":"500000000000000000"}`, string(route.RouteSummary))
		assert.Equal(t, testRouterAddress, route.RouterAddress)
	})

	t.Run("only scalable sources", func(t *testing.T) {
		client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "true", r.URL.Query().Get("onlyScalableSources"))
			writeJSON(t, w, routeResponse{
				Code: 0,
				Data: routeData{
					RouteSummary:  json.RawMessage(`{"amountOut":"1"}`),
					RouterAddress: testRouterAddress,
				},
			})
		})

		_, err := client.GetRoute(context.Background(), RouteRequest{
			ChainID:             constants.MainnetChainID,
			TokenIn:             "0x1",
			TokenOut:            "0x2",
			AmountIn:            "1",
			OnlyScalableSources: true,
		})
		require.NoError(t, err)
	})

	t.Run("unsupported chain", func(t *testing.T) {
		client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("server should not be called")
		})

		route, err := client.GetRoute(context.Background(), RouteRequest{ChainID: 99999})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unsupported chain ID")
		assert.Nil(t, route)
	})

	t.Run("api error", func(t *testing.T) {
		client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			writeJSON(t, w, routeResponse{Code: 4008, Message: "route not found"})
		})

		route, err := client.GetRoute(context.Background(), RouteRequest{
			ChainID:  constants.MainnetChainID,
			TokenIn:  "0x1",
			TokenOut: "0x2",
			AmountIn: "1",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "route API error")
		assert.Nil(t, route)
	})

	t.Run("http error", func(t *testing.T) {
		client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "internal server error", http.StatusInternalServerError)
		})

		route, err := client.GetRoute(context.Background(), RouteRequest{
			ChainID:  constants.MainnetChainID,
			TokenIn:  "0x1",
			TokenOut: "0x2",
			AmountIn: "1",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "status 500")
		assert.Nil(t, route)
	})

	t.Run("null route summary", func(t *testing.T) {
		client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			writeJSON(t, w, routeResponse{
				Code: 0,
				Data: routeData{
					RouteSummary:  json.RawMessage(`null`),
					RouterAddress: testRouterAddress,
				},
			})
		})

		route, err := client.GetRoute(context.Background(), RouteRequest{
			ChainID:  constants.MainnetChainID,
			TokenIn:  "0x1",
			TokenOut: "0x2",
			AmountIn: "1",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no route")
		assert.Nil(t, route)
	})
}

func TestBuildRoute(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "superform", r.Header.Get("x-client-id"))
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, "/ethereum/api/v1/route/build", r.URL.Path)

			var req buildRequestJSON
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			assert.JSONEq(t, `{"amountOut":"500000000000000000"}`, string(req.RouteSummary))
			assert.Equal(t, "0x1111111111111111111111111111111111111111", req.Sender)
			assert.Equal(t, "0x2222222222222222222222222222222222222222", req.Recipient)
			assert.Equal(t, 50, req.SlippageTolerance)

			writeJSON(t, w, buildResponseFixture{
				Code: 0,
				Data: &buildDataFixture{
					AmountOut:        "500000000000000000",
					Data:             "0xabcdef0123456789",
					RouterAddress:    testRouterAddress,
					TransactionValue: "100",
				},
			})
		})

		build, err := client.BuildRoute(context.Background(), BuildRequest{
			ChainID:           constants.MainnetChainID,
			RouteSummary:      json.RawMessage(`{"amountOut":"500000000000000000"}`),
			Sender:            "0x1111111111111111111111111111111111111111",
			Recipient:         "0x2222222222222222222222222222222222222222",
			SlippageTolerance: 50,
		})
		require.NoError(t, err)
		assert.Equal(t, "500000000000000000", build.AmountOut.String())
		assert.Equal(t, common.FromHex("0xabcdef0123456789"), build.TxData)
		assert.Equal(t, common.HexToAddress(testRouterAddress), build.RouterAddress)
		assert.Equal(t, "100", build.TransactionValue.String())
	})

	t.Run("api error", func(t *testing.T) {
		client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			writeJSON(t, w, buildResponse{Code: 5001, Message: "build failed"})
		})

		build, err := client.BuildRoute(context.Background(), BuildRequest{
			ChainID:      constants.MainnetChainID,
			RouteSummary: json.RawMessage(`{"amountOut":"1"}`),
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "build API error")
		assert.Nil(t, build)
	})

	t.Run("parse error", func(t *testing.T) {
		client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			writeJSON(t, w, buildResponseFixture{
				Code: 0,
				Data: &buildDataFixture{
					AmountOut:     "not_a_number",
					Data:          "0xaa",
					RouterAddress: testRouterAddress,
				},
			})
		})

		build, err := client.BuildRoute(context.Background(), BuildRequest{
			ChainID:      constants.MainnetChainID,
			RouteSummary: json.RawMessage(`{"amountOut":"1"}`),
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to parse")
		assert.Nil(t, build)
	})
}

func TestBuildUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name            string
		raw             buildDataFixture
		wantAmountOut   *big.Int
		wantTxData      []byte
		wantTxValue     *big.Int
		wantErrContains string
	}{
		{
			name: "decimal transaction value",
			raw: buildDataFixture{
				AmountOut:        "2000000000000000000",
				Data:             "0xabcdef01",
				RouterAddress:    testRouterAddress,
				TransactionValue: "1000000000000000000",
			},
			wantAmountOut: big.NewInt(2000000000000000000),
			wantTxData:    common.FromHex("0xabcdef01"),
			wantTxValue:   big.NewInt(1000000000000000000),
		},
		{
			name: "hex transaction value",
			raw: buildDataFixture{
				AmountOut:        "500000000",
				Data:             "0x1234",
				RouterAddress:    testRouterAddress,
				TransactionValue: "0xde0b6b3a7640000",
			},
			wantAmountOut: big.NewInt(500000000),
			wantTxData:    common.FromHex("0x1234"),
			wantTxValue:   big.NewInt(1000000000000000000),
		},
		{
			name: "empty transaction value",
			raw: buildDataFixture{
				AmountOut:     "999999",
				Data:          "0xaa",
				RouterAddress: testRouterAddress,
			},
			wantAmountOut: big.NewInt(999999),
			wantTxData:    common.FromHex("0xaa"),
			wantTxValue:   big.NewInt(0),
		},
		{
			name: "invalid amountOut",
			raw: buildDataFixture{
				AmountOut:     "not_a_number",
				Data:          "0xaa",
				RouterAddress: testRouterAddress,
			},
			wantErrContains: "failed to parse amountOut",
		},
		{
			name: "empty tx data",
			raw: buildDataFixture{
				AmountOut:     "100",
				RouterAddress: testRouterAddress,
			},
			wantErrContains: "empty tx data",
		},
		{
			name: "invalid transaction value",
			raw: buildDataFixture{
				AmountOut:        "100",
				Data:             "0xaa",
				RouterAddress:    testRouterAddress,
				TransactionValue: "xyz_invalid",
			},
			wantErrContains: "failed to parse transactionValue",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.raw)
			require.NoError(t, err)

			got := &Build{}
			err = json.Unmarshal(data, got)
			if tt.wantErrContains != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErrContains)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, 0, tt.wantAmountOut.Cmp(got.AmountOut))
			assert.Equal(t, tt.wantTxData, got.TxData)
			assert.Equal(t, common.HexToAddress(testRouterAddress), got.RouterAddress)
			assert.Equal(t, 0, tt.wantTxValue.Cmp(got.TransactionValue))
		})
	}
}

func TestSupportedChains(t *testing.T) {
	client := New("client-id")

	assert.Contains(t, client.SupportedChains(), constants.MainnetChainID)
	assert.Contains(t, client.SupportedChains(), constants.MantleChainID)
}

type buildResponseFixture struct {
	Code    int               `json:"code"`
	Message string            `json:"message,omitempty"`
	Data    *buildDataFixture `json:"data,omitempty"`
}

type buildDataFixture struct {
	AmountOut        string `json:"amountOut"`
	Data             string `json:"data"`
	RouterAddress    string `json:"routerAddress"`
	TransactionValue string `json:"transactionValue"`
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	require.NoError(t, json.NewEncoder(w).Encode(v))
}
