package merkl

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCampaignEncodingTransport(t *testing.T) {
	config := JSONAirdropConfig{CampaignType: 4, DistributionChainID: 8453, Amount: "1000000", JSONURL: "https://example.com/frozen.json"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "key", r.Header.Get("X-API-Key"))
		if r.URL.Path == "/v4/tokens/reward/8453" {
			require.Equal(t, http.MethodGet, r.Method)
			_, _ = w.Write([]byte(`[{"address":"USDC","chainId":8453,"decimals":6,"minimumAmountPerHour":"900000"}]`))
			return
		}
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/v4/config/encode", r.URL.Path)
		var got JSONAirdropConfig
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		require.Equal(t, config, got)
		_, _ = w.Write([]byte(`{"args":{"amount":"1000000","campaignType":4,"duration":3600},"payload":"0x1234"}`))
	}))
	defer srv.Close()
	c, err := NewCampaignClient(WithBaseURL(srv.URL), WithHTTPClient(srv.Client()), WithAPIKey("key"))
	require.NoError(t, err)
	defer c.Close()
	encoded, err := c.EncodeCampaign(context.Background(), config)
	require.NoError(t, err)
	require.Equal(t, "0x1234", encoded.Payload)
	tokens, err := c.RewardTokens(context.Background(), 8453)
	require.NoError(t, err)
	require.Equal(t, "900000", tokens[0].MinimumAmountPerHour)
}

func TestCampaignEncodingFailureBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"server", "sensitive echoed allocation", 503},
		{"malformed", "{", 200}, {"missing", "{}", 200}, {"null", "null", 200},
		{"oversized", strings.Repeat(" ", maxResponseBody+1), 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			c, err := NewCampaignClient(WithBaseURL(srv.URL), WithHTTPClient(srv.Client()))
			require.NoError(t, err)
			defer c.Close()
			_, err = c.EncodeCampaign(context.Background(), JSONAirdropConfig{})
			require.Error(t, err)
			require.NotContains(t, err.Error(), "sensitive echoed allocation")
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			_, err = c.EncodeCampaign(ctx, JSONAirdropConfig{})
			require.Error(t, err)
		})
	}
}
