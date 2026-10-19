package snapshotd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
)

func marketAllocation(t *testing.T) *Allocation {
	t.Helper()
	payload, err := os.ReadFile("testdata/morpho_allocation.json")
	require.NoError(t, err)
	var allocation Allocation
	require.NoError(t, json.Unmarshal(payload, &allocation))
	return &allocation
}

func TestGetAllocationPreservesCombinedMarketWireData(t *testing.T) {
	payload, err := os.ReadFile("testdata/morpho_allocation.json")
	require.NoError(t, err)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer server.Close()
	allocation, err := mustNew(t, WithBaseURL(server.URL)).GetAllocation(context.Background(), Query{ChainID: 8453, Strategy: testStrategy})
	require.NoError(t, err)
	require.Equal(t, marketAllocation(t), allocation)
	require.Equal(t, "900719925474099312345", allocation.GrossAssets)
	require.Equal(t, "900719925474099312343", allocation.NetAssets)
	require.Equal(t, uint64(123456), *allocation.BlockNumber)
	require.Equal(t, uint8(0), *allocation.Sources[0].UnderlyingDecimals)
	require.Equal(t, "0", allocation.Sources[0].UnderlyingTVL)
	require.Equal(t, "1", allocation.Sources[0].RawShares)
}

func TestMarketAllocationRequiresCompleteMetadata(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Allocation)
	}{
		{"market key", func(a *Allocation) { a.Sources[0].MarketKey = common.Address{} }},
		{"market ID", func(a *Allocation) { a.Sources[0].MarketID = common.Hash{} }},
		{"protocol", func(a *Allocation) { a.Sources[0].ProtocolAddress = common.Address{} }},
		{"underlying", func(a *Allocation) { a.Sources[0].UnderlyingAsset = common.Address{} }},
		{"decimals", func(a *Allocation) { a.Sources[0].UnderlyingDecimals = nil }},
		{"underlying amount", func(a *Allocation) { a.Sources[0].UnderlyingTVL = "" }},
		{"negative underlying", func(a *Allocation) { a.Sources[0].UnderlyingTVL = "-1" }},
		{"shares", func(a *Allocation) { a.Sources[0].RawShares = "" }},
		{"negative shares", func(a *Allocation) { a.Sources[0].RawShares = "-1" }},
		{"side", func(a *Allocation) { a.Sources[0].PositionSide = "" }},
		{"role", func(a *Allocation) { a.Sources[2].PositionType = "asset" }},
		{"block", func(a *Allocation) { a.BlockNumber = nil }},
		{"gross", func(a *Allocation) { a.GrossAssets = "" }},
		{"liabilities", func(a *Allocation) { a.TotalLiabilities = "" }},
		{"negative gross", func(a *Allocation) { a.GrossAssets = "-1" }},
		{"negative liabilities", func(a *Allocation) { a.TotalLiabilities = "-1" }},
		{"lending leg", func(a *Allocation) { a.Sources = a.Sources[1:] }},
		{"debt leg", func(a *Allocation) { a.Sources = a.Sources[:2] }},
		{"duplicate leg", func(a *Allocation) { a.Sources = append(a.Sources, a.Sources[0]) }},
		{"duplicate side", func(a *Allocation) { a.Sources[1].PositionSide = "lending" }},
		{"accounting key", func(a *Allocation) { a.Sources[2].Source = a.Sources[0].Source }},
		{"conflicting key", func(a *Allocation) { a.Sources[2].MarketKey = testSource }},
		{"loan token", func(a *Allocation) { a.Sources[2].UnderlyingAsset = testSource }},
		{"loan decimals", func(a *Allocation) { value := uint8(18); a.Sources[2].UnderlyingDecimals = &value }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			allocation := marketAllocation(t)
			tt.mutate(allocation)
			require.ErrorIs(t, validateAndNormalizeAllocation(Query{ChainID: 8453, Strategy: testStrategy}, allocation), ErrInvalidResponse)
		})
	}
}

func TestMarketAllocationAllowsNoCollateralAndSharedLoanTokens(t *testing.T) {
	allocation := marketAllocation(t)
	allocation.Sources = []Source{allocation.Sources[0], allocation.Sources[2]}
	for _, source := range append([]Source(nil), allocation.Sources...) {
		source.MarketID = common.HexToHash("0xb2")
		source.MarketKey = common.HexToAddress("0xb2")
		source.Source[19] += 10
		allocation.Sources = append(allocation.Sources, source)
	}
	require.NoError(t, validateAndNormalizeAllocation(Query{ChainID: 8453, Strategy: testStrategy}, allocation))
	require.Len(t, allocation.Sources, 4)
}
