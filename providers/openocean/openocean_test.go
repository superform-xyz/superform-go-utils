package openocean

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

const testRouterAddress = "0x6352a56caadC4F1E25CD6c75970Fa768A3304e64"

func newTestClient(t *testing.T, handler http.HandlerFunc) *openOcean {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	return &openOcean{
		apiKey:  "test-openocean-api-key",
		baseURL: server.URL,
		client: http_client.NewClientBuilder().
			SetAuth(apiKeyHeader, "test-openocean-api-key").
			SetRetry(0, time.Millisecond).
			BuildClient(),
	}
}

func TestNew(t *testing.T) {
	client := New("client-id")

	require.NotNil(t, client)

	concrete, ok := client.(*openOcean)
	require.True(t, ok)
	assert.Equal(t, "client-id", concrete.apiKey)
	assert.Equal(t, openOceanBaseURL, concrete.baseURL)
	assert.NotNil(t, concrete.client)
}

func TestGetSwap(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		amountIn := big.NewInt(1_000000000000000000)
		outputToken := common.HexToAddress("0x657097cC15fdEc9e383dB8628B57eA4a763F2ba0")
		account := common.HexToAddress("0x1111111111111111111111111111111111111111")
		referrer := common.HexToAddress("0x2222222222222222222222222222222222222222")
		gasPrice := big.NewInt(25_000_000_000)

		client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodGet, r.Method)
			assert.Equal(t, "test-openocean-api-key", r.Header.Get(apiKeyHeader))
			assert.Equal(t, "/v4/14/swap", r.URL.Path)
			assert.Equal(t, "14", r.URL.Query().Get("chain"))
			assert.Equal(t, constants.GetNullAddress().Hex(), r.URL.Query().Get("inTokenAddress"))
			assert.Equal(t, outputToken.Hex(), r.URL.Query().Get("outTokenAddress"))
			assert.Equal(t, amountIn.String(), r.URL.Query().Get("amountDecimals"))
			assert.Equal(t, "1", r.URL.Query().Get("slippage"))
			assert.Equal(t, account.Hex(), r.URL.Query().Get("account"))
			assert.Equal(t, "2,6", r.URL.Query().Get("enabledDexIds"))
			assert.Equal(t, gasPrice.String(), r.URL.Query().Get("gasPriceDecimals"))
			assert.Equal(t, referrer.Hex(), r.URL.Query().Get("referrer"))
			assert.Empty(t, r.URL.Query().Get("gasPrice"))

			writeJSON(t, w, swapResponseFixture{
				Code: 200,
				Data: &swapDataFixture{
					InAmount:     amountIn.String(),
					OutAmount:    "623712576760226704",
					MinOutAmount: "617475451000000000",
					To:           testRouterAddress,
					Value:        amountIn.String(),
					Data:         "0x90411a32abcdef",
					ChainID:      constants.FlareChainID,
				},
			})
		})

		swap, err := client.GetSwap(context.Background(), SwapRequest{
			ChainID:          constants.FlareChainID,
			InTokenAddress:   constants.GetNullAddress().Hex(),
			OutTokenAddress:  outputToken.Hex(),
			AmountDecimals:   amountIn.String(),
			Slippage:         "1",
			Account:          account.Hex(),
			EnabledDexIDs:    "2,6",
			GasPriceDecimals: gasPrice.String(),
			Referrer:         referrer.Hex(),
		})
		require.NoError(t, err)
		assert.Equal(t, amountIn.String(), swap.AmountIn.String())
		assert.Equal(t, "623712576760226704", swap.AmountOut.String())
		assert.Equal(t, "617475451000000000", swap.MinAmountOut.String())
		assert.Equal(t, amountIn.String(), swap.TransactionValue.String())
		assert.Equal(t, common.FromHex("0x90411a32abcdef"), swap.TxData)
		assert.Equal(t, common.HexToAddress(testRouterAddress), swap.RouterAddress)
	})

	t.Run("http error", func(t *testing.T) {
		client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "internal server error", http.StatusInternalServerError)
		})

		swap, err := client.GetSwap(context.Background(), SwapRequest{ChainID: constants.FlareChainID})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "status 500")
		assert.Nil(t, swap)
	})

	t.Run("api error", func(t *testing.T) {
		client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			writeJSON(t, w, swapResponseFixture{Code: 400, Message: "bad swap"})
		})

		swap, err := client.GetSwap(context.Background(), SwapRequest{ChainID: constants.FlareChainID})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "OpenOcean swap API error")
		assert.Nil(t, swap)
	})

	t.Run("invalid json", func(t *testing.T) {
		client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("bad json"))
		})

		swap, err := client.GetSwap(context.Background(), SwapRequest{ChainID: constants.FlareChainID})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to decode")
		assert.Nil(t, swap)
	})
}

func TestSwapUnmarshalJSONTransactionValue(t *testing.T) {
	makeRaw := func(value string) []byte {
		data, err := json.Marshal(swapDataFixture{
			InAmount:     "100",
			OutAmount:    "200",
			MinOutAmount: "190",
			To:           testRouterAddress,
			Value:        value,
			Data:         "0x90411a32abcdef",
			ChainID:      constants.FlareChainID,
		})
		require.NoError(t, err)
		return data
	}

	t.Run("decimal with leading zero is not treated as octal", func(t *testing.T) {
		parsed := &Swap{}

		require.NoError(t, json.Unmarshal(makeRaw("0755"), parsed))
		assert.Equal(t, "755", parsed.TransactionValue.String())
	})

	t.Run("explicit hex value is supported", func(t *testing.T) {
		parsed := &Swap{}

		require.NoError(t, json.Unmarshal(makeRaw("0x10"), parsed))
		assert.Equal(t, "16", parsed.TransactionValue.String())
	})

	t.Run("invalid value is rejected", func(t *testing.T) {
		err := json.Unmarshal(makeRaw("0x"), &Swap{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to parse value")
	})
}

func TestSwapUnmarshalJSONRejectsNonPositiveAmounts(t *testing.T) {
	makeRaw := func() swapDataFixture {
		return swapDataFixture{
			InAmount:     "100",
			OutAmount:    "200",
			MinOutAmount: "190",
			To:           testRouterAddress,
			Value:        "0",
			Data:         "0x90411a32abcdef",
			ChainID:      constants.FlareChainID,
		}
	}

	tests := []struct {
		name    string
		mutate  func(*swapDataFixture)
		wantErr string
	}{
		{
			name: "zero input amount",
			mutate: func(raw *swapDataFixture) {
				raw.InAmount = "0"
			},
			wantErr: "failed to parse inAmount: 0",
		},
		{
			name: "negative input amount",
			mutate: func(raw *swapDataFixture) {
				raw.InAmount = "-1"
			},
			wantErr: "failed to parse inAmount: -1",
		},
		{
			name: "zero output amount",
			mutate: func(raw *swapDataFixture) {
				raw.OutAmount = "0"
			},
			wantErr: "failed to parse outAmount: 0",
		},
		{
			name: "negative output amount",
			mutate: func(raw *swapDataFixture) {
				raw.OutAmount = "-1"
			},
			wantErr: "failed to parse outAmount: -1",
		},
		{
			name: "zero min output amount",
			mutate: func(raw *swapDataFixture) {
				raw.MinOutAmount = "0"
			},
			wantErr: "failed to parse minOutAmount: 0",
		},
		{
			name: "negative min output amount",
			mutate: func(raw *swapDataFixture) {
				raw.MinOutAmount = "-1"
			},
			wantErr: "failed to parse minOutAmount: -1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := makeRaw()
			tt.mutate(&raw)
			data, err := json.Marshal(raw)
			require.NoError(t, err)

			err = json.Unmarshal(data, &Swap{})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

type swapResponseFixture struct {
	Code    int              `json:"code"`
	Message string           `json:"message,omitempty"`
	Data    *swapDataFixture `json:"data,omitempty"`
}

type swapDataFixture struct {
	InAmount     string `json:"inAmount"`
	OutAmount    string `json:"outAmount"`
	MinOutAmount string `json:"minOutAmount"`
	To           string `json:"to"`
	Value        string `json:"value"`
	Data         string `json:"data"`
	ChainID      uint64 `json:"chainId"`
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	require.NoError(t, json.NewEncoder(w).Encode(v))
}
