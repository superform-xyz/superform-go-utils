package merkl

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustNew(t *testing.T, opts ...Option) Client {
	t.Helper()
	c, err := New(opts...)
	require.NoError(t, err)
	return c
}

func TestGetOpportunities(t *testing.T) {
	t.Parallel()

	var gotMainProtocol string
	var gotChainID string
	var gotAPIKey string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "/v4/opportunities", r.URL.Path)
		gotMainProtocol = r.URL.Query().Get("mainProtocolId")
		gotChainID = r.URL.Query().Get("chainId")
		gotAPIKey = r.Header.Get(apiKeyHeader)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{
				"id": "9366490728830419848",
				"name": "Hold superUSDC across 3 chains",
				"type": "ERC20LOGPROCESSOR",
				"chainId": 8453,
				"identifier": "opaque",
				"status": "LIVE",
				"action": "HOLD",
				"apr": 2.5,
				"aprRecord": {
					"cumulated": 2.5,
					"timestamp": "2026-06-16T00:00:00.000Z",
					"breakdowns": [{"type": "CAMPAIGN", "value": 2.5}]
				},
				"nativeAprRecord": {
					"title": "Native APR",
					"description": "Underlying native yield",
					"value": 5.5,
					"timestamp": "2026-06-16T00:00:00.000Z"
				},
				"tokens": [{"chainId": 8453, "address": "0xabc", "symbol": "superUSDC", "icon": "https://example.com/icon.png"}],
				"rewardsRecord": {
					"breakdowns": [{"token": {"chainId": 8453, "address": "0xdef", "symbol": "SUP"}, "value": 100, "onChainCampaignId": "0x123"}]
				}
			}
		]`))
	}))
	defer srv.Close()

	c := mustNew(t, WithBaseURL(srv.URL), WithHTTPClient(srv.Client()), WithAPIKey(" test-key "))
	opps, err := c.GetOpportunities(context.Background(), 8453, 100, "superform")
	require.NoError(t, err)
	require.Len(t, opps, 1)

	assert.Equal(t, "superform", gotMainProtocol)
	assert.Equal(t, "8453", gotChainID)
	assert.Equal(t, "test-key", gotAPIKey)
	assert.Equal(t, "9366490728830419848", opps[0].ID)
	assert.Equal(t, "Native APR", opps[0].NativeAPRRecord.Title)
	assert.Equal(t, "superUSDC", opps[0].Tokens[0].Symbol)
	assert.Equal(t, "0x123", opps[0].RewardsRecord.Breakdowns[0].OnChainCampaignID)
}

func TestGetOpportunities_Paginates(t *testing.T) {
	t.Parallel()

	var pages []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages = append(pages, r.URL.Query().Get("page"))
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("page") {
		case "0":
			_, _ = w.Write([]byte(`[{"id":"a"},{"id":"b"}]`))
		default:
			_, _ = w.Write([]byte(`[{"id":"c"}]`))
		}
	}))
	defer srv.Close()

	c := mustNew(t, WithBaseURL(srv.URL), WithHTTPClient(srv.Client()))
	opps, err := c.GetOpportunities(context.Background(), 1, 2, "")
	require.NoError(t, err)

	assert.Equal(t, []string{"0", "1"}, pages)
	require.Len(t, opps, 3)
	assert.Equal(t, "c", opps[2].ID)
}

func TestGetUserRewards(t *testing.T) {
	t.Parallel()

	var gotPath string
	var gotChainID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotChainID = r.URL.Query().Get("chainId")
		assert.Empty(t, r.Header.Get(apiKeyHeader))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{
				"chain": { "id": 8453, "name": "Base" },
				"rewards": [
					{
						"root": "0xroot",
						"distributionChainId": 8453,
						"recipient": "0xUser",
						"amount": "1000",
						"claimed": "200",
						"pending": "800",
						"proofs": ["0xproof"],
						"token": { "address": "0xToken", "chainId": 8453, "symbol": "ARB", "decimals": 18, "price": 1.23 },
						"breakdowns": [{"root":"0xroot","distributionChainId":8453,"reason":"test","amount":"1000","claimed":"200","pending":"800","campaignId":"campaign","subCampaignId":"sub"}]
					}
				]
			}
		]`))
	}))
	defer srv.Close()

	c := mustNew(t, WithBaseURL(srv.URL), WithHTTPClient(srv.Client()))
	rewards, err := c.GetUserRewards(context.Background(), "0xUser", 8453)
	require.NoError(t, err)
	require.Len(t, rewards, 1)

	assert.Equal(t, "/v4/users/0xUser/rewards", gotPath)
	assert.Equal(t, "8453", gotChainID)
	assert.Equal(t, "0xroot", rewards[0].Rewards[0].Root)
	assert.Equal(t, []string{"0xproof"}, rewards[0].Rewards[0].Proofs)
	assert.Equal(t, uint32(18), rewards[0].Rewards[0].Token.Decimals)
}

func TestGetLiveRoots(t *testing.T) {
	t.Parallel()

	var gotPath string
	var gotAPIKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAPIKey = r.Header.Get(apiKeyHeader)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"8453":{"live":"0xroot"}}`))
	}))
	defer srv.Close()

	c := mustNew(t, WithBaseURL(srv.URL), WithHTTPClient(srv.Client()), WithAPIKey("secret"))
	roots, err := c.GetLiveRoots(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "/v4/roots/live", gotPath)
	assert.Equal(t, "secret", gotAPIKey)
	assert.Equal(t, "0xroot", roots["8453"].Live)
}

func TestGetUserRewards_RequiresUser(t *testing.T) {
	t.Parallel()

	c := mustNew(t)
	_, err := c.GetUserRewards(context.Background(), " ", 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires user address")
}

func TestNon2xx(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer srv.Close()

	c := mustNew(t, WithBaseURL(srv.URL), WithHTTPClient(srv.Client()))
	_, err := c.GetLiveRoots(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "status 502")
}
