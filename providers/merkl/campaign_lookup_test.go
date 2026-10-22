package merkl

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCampaignLookupFiltersChainAndOnChainID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v4/campaigns", r.URL.Path)
		require.Equal(t, "8453", r.URL.Query().Get("chainId"))
		require.Equal(t, "0xcampaign", r.URL.Query().Get("campaignId"))
		require.Equal(t, "test-key", r.Header.Get("X-API-Key"))
		_, _ = w.Write([]byte(`[{"campaignId":"0xcampaign","opportunityId":"7152877682025371083"}]`))
	}))
	defer server.Close()
	client, err := NewCampaignLookup(WithBaseURL(server.URL), WithHTTPClient(server.Client()), WithAPIKey("test-key"))
	require.NoError(t, err)
	defer client.Close()
	campaigns, err := client.ListCampaigns(context.Background(), 8453, "0xcampaign")
	require.NoError(t, err)
	require.Len(t, campaigns, 1)
	require.Equal(t, "7152877682025371083", campaigns[0].OpportunityID)
}
