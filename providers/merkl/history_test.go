package merkl

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHistoricalReadsUseAsOfAndExactAmounts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "test-key", r.Header.Get("X-API-Key"))
		switch r.URL.Path {
		case "/v4/historical/tree":
			require.Equal(t, "2026-09-12T12:00:00Z", r.URL.Query().Get("date"))
			require.Equal(t, "8453", r.URL.Query().Get("chainId"))
			fmt.Fprint(w, `{"root":"0xroot","timestamp":"1789210392","breakdowns":[{"recipient":"0xrecipient","amount":"9007199254740993000"}]}`)
		case "/v4/claims/history/0xrecipient":
			fmt.Fprint(w, `[{"id":"a","chainId":8453,"amount":"9007199254740993000","metadata":{"transactionHash":"0xtx"}}]`)
		case "/v4/tokens/prices/history":
			require.Equal(t, "carry-forward", r.URL.Query().Get("fill"))
			fmt.Fprint(w, `[{"day":20707,"close":0.123456789012345678,"samples":3}]`)
		default:
			t.Fatal(r.URL.Path)
		}
	}))
	defer srv.Close()
	c, err := NewHistory(WithBaseURL(srv.URL), WithHTTPClient(srv.Client()), WithAPIKey("test-key"))
	require.NoError(t, err)
	tree, err := c.GetHistoricalTree(context.Background(), 8453, "0xrecipient", time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Equal(t, "9007199254740993000", tree.Breakdowns[0].Amount)
	claims, err := c.GetClaimHistory(context.Background(), "0xrecipient")
	require.NoError(t, err)
	require.Equal(t, "9007199254740993000", claims[0].Amount)
	prices, err := c.GetPriceHistory(context.Background(), 8453, "0xtoken", 20700, 20710)
	require.NoError(t, err)
	require.Equal(t, "0.123456789012345678", prices[0].Close.String())
}
