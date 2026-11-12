package erebor

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/superform-xyz/superform-go-utils/pkg/http_client"
)

const testVault = "0x1111111111111111111111111111111111111111"
const testStrategy = "0x2222222222222222222222222222222222222222"
const testAsset = "0x3333333333333333333333333333333333333333"
const testSource = "0x4444444444444444444444444444444444444444"

func testAllocations() map[string]any {
	return map[string]any{
		"chain_id": 747474, "vault_address": testVault, "strategy_address": testStrategy,
		"asset_address": testAsset, "asset_symbol": "USDC", "asset_decimals": 0,
		"net_assets": "900719925474099312345", "idle_balance": "200",
		"allocations": []any{map[string]any{"name": "Morpho Gauntlet", "address": testSource, "assets": "-900719925474099312345", "position_type": "liability"}},
	}
}

func TestGetAllocationsPreservesNamedSignedAmounts(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "/api/v1/vaults/747474/"+testVault+"/allocations", r.URL.Path)
		require.Empty(t, r.Header.Get("Authorization"))
		require.Equal(t, "application/json", r.Header.Get("Accept"))
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"success": true, "data": testAllocations()}))
	}))
	defer upstream.Close()
	c, err := New(upstream.URL)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, c.Close()) })
	result, err := c.GetAllocations(t.Context(), 747474, testVault)
	require.NoError(t, err)
	require.Equal(t, uint8(0), result.AssetDecimals)
	require.Equal(t, "900719925474099312345", result.NetAssets)
	require.Equal(t, "200", result.IdleBalance)
	require.Len(t, result.Allocations, 1)
	require.Equal(t, "Morpho Gauntlet", result.Allocations[0].Name)
	require.Equal(t, "-900719925474099312345", result.Allocations[0].Assets)
}

func TestGetAllocationsRejectsMalformedResponses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"wrong chain", func(data map[string]any) { data["chain_id"] = 1 }},
		{"wrong vault", func(data map[string]any) { data["vault_address"] = testSource }},
		{"missing strategy", func(data map[string]any) { delete(data, "strategy_address") }},
		{"missing decimals", func(data map[string]any) { delete(data, "asset_decimals") }},
		{"missing NAV", func(data map[string]any) { delete(data, "net_assets") }},
		{"negative idle", func(data map[string]any) { data["idle_balance"] = "-1" }},
		{"missing allocations", func(data map[string]any) { delete(data, "allocations") }},
		{"invalid signed amount", func(data map[string]any) {
			data["allocations"] = []any{map[string]any{"address": testSource, "assets": "1", "position_type": "liability"}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				data := testAllocations()
				tc.mutate(data)
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"success": true, "data": data}))
			}))
			defer upstream.Close()
			c, err := New(upstream.URL)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, c.Close()) })
			result, err := c.GetAllocations(t.Context(), 747474, testVault)
			require.Nil(t, result)
			require.ErrorIs(t, err, ErrInvalidResponse)
		})
	}
}

func TestGetAllocationsRejectsFailureEnvelopesAndHTTPFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"failed envelope", http.StatusOK, `{"success":false,"error":{"code":"NOT_FOUND"}}`},
		{"missing data", http.StatusOK, `{"success":true}`},
		{"invalid JSON", http.StatusOK, `{`},
		{"upstream unavailable", http.StatusServiceUnavailable, `{"success":false}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, err := w.Write([]byte(tc.body))
				require.NoError(t, err)
			}))
			defer upstream.Close()
			c, err := New(upstream.URL)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, c.Close()) })
			result, err := c.GetAllocations(t.Context(), 747474, testVault)
			require.Nil(t, result)
			require.Error(t, err)
			if tc.status == http.StatusServiceUnavailable {
				status, _, ok := http_client.ResponseStatus(err)
				require.True(t, ok)
				require.Equal(t, tc.status, status)
			} else if tc.name != "invalid JSON" {
				require.True(t, errors.Is(err, ErrInvalidResponse))
			}
		})
	}
}

func TestNewAndRequestValidation(t *testing.T) {
	for _, base := range []string{"", "file:///tmp/erebor", "https://user:password@example.com", "https://example.com?token=value"} {
		_, err := New(base)
		require.Error(t, err)
	}
	c, err := New("https://example.com")
	require.NoError(t, err)
	_, err = c.GetAllocations(t.Context(), 0, testVault)
	require.Error(t, err)
	_, err = c.GetAllocations(t.Context(), 747474, "invalid")
	require.Error(t, err)
}
