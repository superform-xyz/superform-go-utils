package zerion

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/superform-xyz/superform-go-utils/utils/constants"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) *zerion {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, ok := New("test-key", WithBaseURL(server.URL), WithRetry(0, time.Millisecond)).(*zerion)
	require.True(t, ok)
	return client
}

func jsonOK(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	require.NoError(t, json.NewEncoder(w).Encode(v))
}

func TestNew(t *testing.T) {
	z := New("my-key", WithBaseURL("https://example.com/v1/"), WithTimeout(15*time.Second), WithRetry(2, time.Second))
	require.NotNil(t, z)

	concrete, ok := z.(*zerion)
	require.True(t, ok)
	assert.Equal(t, "my-key", concrete.apiKey)
	assert.Equal(t, "https://example.com/v1", concrete.baseURL)
	assert.NotNil(t, concrete.client)
	require.NotNil(t, concrete.timeout)
	assert.Equal(t, 15*time.Second, *concrete.timeout)
	require.NotNil(t, concrete.maxRetries)
	assert.Equal(t, uint(2), *concrete.maxRetries)
	require.NotNil(t, concrete.retryDelay)
	assert.Equal(t, time.Second, *concrete.retryDelay)
}

func TestHealthCheck(t *testing.T) {
	z := &zerion{}
	assert.NoError(t, z.HealthCheck(context.Background()))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.ErrorIs(t, z.HealthCheck(ctx), context.Canceled)
}

func TestGetWalletPositions(t *testing.T) {
	z := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/wallets/0xabc/positions/", r.URL.Path)
		assert.Equal(t, "Basic test-key", r.Header.Get("Authorization"))
		assert.Equal(t, "arbitrum,base,ethereum", r.URL.Query().Get("filter[chain_ids]"))
		assert.Equal(t, WalletPositionsNoFilter, r.URL.Query().Get("filter[positions]"))
		assert.Equal(t, WalletPositionsOnlyNonTrash, r.URL.Query().Get("filter[trash]"))
		assert.Equal(t, WalletPositionsCurrencyUSD, r.URL.Query().Get("currency"))
		assert.Equal(t, WalletPositionsSortValue, r.URL.Query().Get("sort"))

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, err := w.Write([]byte(`{
			"data": [{
				"attributes": {
					"position_type": "wallet",
					"pool_address": "0x0000000000000000000000000000000000000001",
					"quantity": {"int": "1000000000000000000", "decimals": 18},
					"value": 12.34,
					"price": 1.23,
					"fungible_info": {
						"name": "Token",
						"symbol": "TOK",
						"icon": {"url": "https://example.com/tok.png"},
						"implementations": [{
							"chain_id": "base",
							"address": "0x1111111111111111111111111111111111111111",
							"decimals": 18
						}]
					},
					"flags": {"displayable": true}
				},
				"relationships": {
					"chain": {"data": {"id": "base"}}
				}
			}]
		}`))
		require.NoError(t, err)
	})

	positions, err := z.GetWalletPositions(context.Background(), "0xabc", DefaultWalletPositionsRequest([]uint64{
		constants.BaseChainID,
		constants.MainnetChainID,
		constants.ArbitrumChainID,
		constants.BaseChainID,
	}))
	require.NoError(t, err)
	require.Len(t, positions, 1)
	assert.Equal(t, constants.BaseChainID, positions[0].ChainID)
	assert.Equal(t, "wallet", positions[0].Attributes.PositionType)
	assert.Equal(t, "1000000000000000000", positions[0].Attributes.Quantity.RawAmount.String())
	assert.Equal(t, uint64(0x1), positions[0].Attributes.PoolAddress.Big().Uint64())
	require.NotNil(t, positions[0].Attributes.FungibleInfo)
	assert.Equal(t, "TOK", positions[0].Attributes.FungibleInfo.Symbol)
	require.Len(t, positions[0].Attributes.FungibleInfo.Implementations, 1)
	assert.Equal(t, constants.BaseChainID, positions[0].Attributes.FungibleInfo.Implementations[0].ChainID)
	assert.Equal(t, "0x1111111111111111111111111111111111111111", positions[0].Attributes.FungibleInfo.Implementations[0].Address.Hex())
}

func TestGetWalletPositionsAllowsExplicitChainSlugs(t *testing.T) {
	z := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "base,ethereum", r.URL.Query().Get("filter[chain_ids]"))
		jsonOK(t, w, WalletPositionsResponse{})
	})

	positions, err := z.GetWalletPositions(context.Background(), "0xabc", WalletPositionsRequest{
		ChainIDs:   []uint64{constants.ArbitrumChainID},
		ChainSlugs: []string{"ethereum", "base", "base"},
	})
	require.NoError(t, err)
	assert.Empty(t, positions)
}

func TestGetWalletPositionsEmptyAddress(t *testing.T) {
	z := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("server should not be called for an empty address")
	})

	positions, err := z.GetWalletPositions(context.Background(), " ", WalletPositionsRequest{})
	require.NoError(t, err)
	assert.Empty(t, positions)
}

func TestGetWalletPositionsNonOK(t *testing.T) {
	z := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("slow down"))
	})

	positions, err := z.GetWalletPositions(context.Background(), "0xabc", WalletPositionsRequest{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "request failed, rate limit exceeded")
	assert.Nil(t, positions)
}

func TestChainHelpers(t *testing.T) {
	assert.Equal(t, "arbitrum,base,ethereum", ChainFilterFromChainIDs([]uint64{
		constants.BaseChainID,
		constants.MainnetChainID,
		constants.ArbitrumChainID,
		constants.BaseChainID,
	}))

	chainID, ok := ChainID("Base")
	require.True(t, ok)
	assert.Equal(t, constants.BaseChainID, chainID)

	slug, ok := ChainSlug(constants.OptimismChainID)
	require.True(t, ok)
	assert.Equal(t, "optimism", slug)
}
