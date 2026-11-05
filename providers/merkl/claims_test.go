package merkl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListClaimsEncodesFiltersAndDecodesRows(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v4/claims/", r.URL.Path)
		require.Equal(t, "test-key", r.Header.Get("X-API-Key"))
		q := r.URL.Query()
		require.Equal(t, []string{"1", "8453"}, q["chainIds"])
		require.Empty(t, q["chainId"])
		require.Equal(t, "0xrecipient", q.Get("recipient"))
		require.Equal(t, "0xtoken", q.Get("token"))
		require.Equal(t, "0xcampaign", q.Get("campaignId"))
		require.Equal(t, "0xroot", q.Get("root"))
		require.Equal(t, "referral-bonuses/batch/award", q.Get("reason"))
		require.Equal(t, "1793864853", q.Get("fromTimestamp"))
		require.Equal(t, "1793864900", q.Get("toTimestamp"))
		fmt.Fprint(w, `[
			{"id":"1","chainId":8453,"recipient":"0xRecipient","blockNumber":53537753,"timestamp":1793864853,
			 "token":"0xToken","campaignId":"0xcampaign","reason":"referral-bonuses/batch/award","root":"0xroot",
			 "amount":"9007199254740993000","isLatest":true,
			 "metadata":{"transactionHash":"0xtx","eventAmount":"234376527","tokenPrice":0.99}},
			{"id":"2","chainId":"8453","recipient":"0xRecipient","blockNumber":1,"timestamp":1,
			 "token":"0xToken","campaignId":"0xold","reason":"legacy","root":"0xold","amount":"1","isLatest":true,
			 "metadata":{"isMigrated":true,"transactionHash":""}}
		]`)
	}))
	defer srv.Close()

	c, err := NewClaimLookup(WithBaseURL(srv.URL), WithHTTPClient(srv.Client()), WithAPIKey("test-key"))
	require.NoError(t, err)
	claims, err := c.ListClaims(context.Background(), ClaimsQuery{
		ChainIDs: []uint64{8453, 1, 8453}, Recipient: " 0xrecipient ", Token: "0xtoken", CampaignID: "0xcampaign", Root: "0xroot",
		Reason: "referral-bonuses/batch/award", From: time.Unix(1793864853, 0), Through: time.Unix(1793864900, 0),
	})
	require.NoError(t, err)
	require.Len(t, claims, 2)
	assert.Equal(t, ChainID(8453), claims[0].ChainID)
	assert.Equal(t, uint64(53537753), claims[0].BlockNumber)
	assert.Equal(t, int64(1793864853), claims[0].Timestamp)
	assert.Equal(t, "9007199254740993000", claims[0].Amount)
	assert.Equal(t, ClaimMetadata{TransactionHash: "0xtx", EventAmount: "234376527"}, claims[0].Metadata)
	assert.Equal(t, ChainID(8453), claims[1].ChainID)
	assert.Equal(t, ClaimMetadata{}, claims[1].Metadata)
}

func TestListClaimsRejectsInvalidQueriesBeforeRequest(t *testing.T) {
	t.Parallel()

	var requested bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requested = true
		fmt.Fprint(w, `[]`)
	}))
	defer srv.Close()

	c, err := NewClaimLookup(WithBaseURL(srv.URL), WithHTTPClient(srv.Client()))
	require.NoError(t, err)
	_, err = c.ListClaims(context.Background(), ClaimsQuery{ChainIDs: []uint64{0}})
	require.ErrorContains(t, err, "non-zero chain ids")
	_, err = c.ListClaims(context.Background(), ClaimsQuery{From: time.Unix(2, 0), Through: time.Unix(1, 0)})
	require.ErrorContains(t, err, "inverted")
	assert.False(t, requested, "invalid queries must not reach Merkl")
}

func TestListClaimsReturnsTypedStatusError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, "slow down")
	}))
	defer srv.Close()

	c, err := NewClaimLookup(WithBaseURL(srv.URL), WithHTTPClient(srv.Client()))
	require.NoError(t, err)
	_, err = c.ListClaims(context.Background(), ClaimsQuery{Recipient: "0xrecipient"})
	var status *StatusError
	require.True(t, errors.As(err, &status))
	assert.Equal(t, http.StatusTooManyRequests, status.Code)
	assert.Equal(t, 7*time.Second, status.RetryAfter)
	assert.Equal(t, "merkl claims: status 429: slow down", err.Error())
}

func TestChainIDRejectsNullZeroAndNonNumeric(t *testing.T) {
	t.Parallel()

	for _, body := range []string{`{"chainId":null}`, `{"chainId":0}`, `{"chainId":"0"}`, `{"chainId":""}`, `{"chainId":"base"}`, `{"chainId":-1}`, `{"chainId":1.5}`, `{"chainId":18446744073709551616}`} {
		var claim Claim
		assert.Error(t, json.Unmarshal([]byte(body), &claim), body)
	}
}

func TestListClaimsClassifiesInvalidResponses(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"malformed JSON":    `[{`,
		"invalid chain":     `[{"chainId":"base"}]`,
		"multiple values":   `[] []`,
		"oversized payload": `[]` + strings.Repeat(" ", maxResponseBody),
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(w, body)
			}))
			defer srv.Close()
			c, err := NewClaimLookup(WithBaseURL(srv.URL), WithHTTPClient(srv.Client()))
			require.NoError(t, err)
			_, err = c.ListClaims(context.Background(), ClaimsQuery{Recipient: "0xrecipient"})
			var invalid *ResponseError
			require.ErrorAs(t, err, &invalid)
		})
	}
}

func TestListClaimsKeepsServerFailuresDistinctFromInvalidResponses(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprint(w, "upstream unavailable")
	}))
	defer srv.Close()
	c, err := NewClaimLookup(WithBaseURL(srv.URL), WithHTTPClient(srv.Client()))
	require.NoError(t, err)
	_, err = c.ListClaims(context.Background(), ClaimsQuery{Recipient: "0xrecipient"})
	var status *StatusError
	require.ErrorAs(t, err, &status)
	assert.Equal(t, http.StatusBadGateway, status.Code)
	var invalid *ResponseError
	assert.False(t, errors.As(err, &invalid))
}

func TestListClaimsKeepsBodyReadFailuresTransient(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "100")
		fmt.Fprint(w, "[")
	}))
	defer srv.Close()
	c, err := NewClaimLookup(WithBaseURL(srv.URL), WithHTTPClient(srv.Client()))
	require.NoError(t, err)
	_, err = c.ListClaims(context.Background(), ClaimsQuery{Recipient: "0xrecipient"})
	require.ErrorContains(t, err, "read response")
	var invalid *ResponseError
	assert.False(t, errors.As(err, &invalid))
}

func TestParseRetryAfter(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	assert.Equal(t, 30*time.Second, parseRetryAfter("30", now))
	assert.Equal(t, 90*time.Second, parseRetryAfter(now.Add(90*time.Second).Format(http.TimeFormat), now))
	for _, value := range []string{"", "0", "-5", "soon", now.Add(-time.Minute).Format(http.TimeFormat)} {
		assert.Zero(t, parseRetryAfter(value, now), value)
	}
}
