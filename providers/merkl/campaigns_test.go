package merkl

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestListAllOpportunitiesPreservesCampaignTargetsAcrossPages(t *testing.T) {
	t.Parallel()
	var pages []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "true", r.URL.Query().Get("campaigns"))
		require.Equal(t, "superform", r.URL.Query().Get("mainProtocolId"))
		pages = append(pages, r.URL.Query().Get("page"))
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "0" {
			_, _ = w.Write([]byte(`[{"id":"3107847555429442009","tokens":[{"chainId":8453,"address":"0xb20000000000000000000078ee7ce2fe4908108c","symbol":"NVDAc"}],"campaigns":[{"id":"4201916777515881806","campaignId":"0x9166f9df19b5945df2dd3bb2064b2d532bbf7678dc8490b580792365c9fe3675","type":"ERC20_MULTI_TOKEN_CROSS_CHAIN","params":{"tokens":[{"chainId":8453,"tokenAddress":"0xC441A2cc3a528B6312740448a70cC4D40F4d7BfC","symbol":"superNVDA","decimals":8},{"chainId":8453,"tokenAddress":"0x02b12a394f0A98b80E53d85F6831041932F1b621","symbol":"superSPCX","decimals":8},{"chainId":8453,"tokenAddress":"0xaF0EdF09eC7f9357292F6bc6445a09Aa84a31FA7","symbol":"superTSLA","decimals":8}]}}]}]`))
			return
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	c := mustNew(t, WithBaseURL(srv.URL), WithHTTPClient(srv.Client()))
	opps, err := c.ListAllOpportunities(context.Background(), OpportunityQuery{Items: 1, MainProtocolID: "superform", Campaigns: true})
	require.NoError(t, err)
	require.Equal(t, []string{"0", "1"}, pages)
	require.Len(t, opps, 1)
	require.Len(t, opps[0].Campaigns, 1)
	campaign := opps[0].Campaigns[0]
	require.Equal(t, "4201916777515881806", campaign.ID)
	require.Equal(t, "0x9166f9df19b5945df2dd3bb2064b2d532bbf7678dc8490b580792365c9fe3675", campaign.CampaignID)
	params, err := campaign.MultiTokenParams()
	require.NoError(t, err)
	require.Equal(t, []CampaignToken{
		{ChainID: 8453, TokenAddress: "0xC441A2cc3a528B6312740448a70cC4D40F4d7BfC", Symbol: "superNVDA", Decimals: 8},
		{ChainID: 8453, TokenAddress: "0x02b12a394f0A98b80E53d85F6831041932F1b621", Symbol: "superSPCX", Decimals: 8},
		{ChainID: 8453, TokenAddress: "0xaF0EdF09eC7f9357292F6bc6445a09Aa84a31FA7", Symbol: "superTSLA", Decimals: 8},
	}, params.Tokens)
}

func TestMultiTokenParamsRejectsMissingAndMalformedConfiguration(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"", "null", "{}", `{"tokens":[]}`, `{"tokens":["0xabc"]}`} {
		t.Run(raw, func(t *testing.T) {
			_, err := (Campaign{Type: CampaignTypeERC20MultiTokenCrossChain, Params: json.RawMessage(raw)}).MultiTokenParams()
			require.Error(t, err)
		})
	}
}

func TestCampaignPreservesOtherParameterSchemas(t *testing.T) {
	t.Parallel()
	var campaign Campaign
	require.NoError(t, json.Unmarshal([]byte(`{"type":"OTHER","params":{"tokens":["token-a"],"targetToken":"token-b"}}`), &campaign))
	require.JSONEq(t, `{"tokens":["token-a"],"targetToken":"token-b"}`, string(campaign.Params))
	_, err := campaign.MultiTokenParams()
	require.ErrorContains(t, err, "unsupported multi-token type")
}
