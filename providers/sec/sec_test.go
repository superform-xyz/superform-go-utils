package sec

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

const testUserAgent = "Superform stock-stats engineering@example.com"

func mustNew(t *testing.T, opts ...Option) Client {
	t.Helper()
	client, err := New(testUserAgent, opts...)
	require.NoError(t, err)
	return client
}

func TestNewRequiresUserAgent(t *testing.T) {
	t.Parallel()

	_, err := New(" ")
	require.ErrorContains(t, err, "user agent is required")
}

func TestGetCompanyTickersNormalizesAndSorts(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, companyTickersPath, r.URL.Path)
		require.Equal(t, testUserAgent, r.Header.Get("User-Agent"))
		_, _ = w.Write([]byte(`{
			"1": {"cik_str": 789019, "ticker": " msft ", "title": "Microsoft Corp"},
			"0": {"cik_str": 320193, "ticker": "aapl", "title": "Apple Inc."},
			"2": {"cik_str": 0, "ticker": "", "title": "Invalid"}
		}`))
	}))
	defer server.Close()

	client := mustNew(
		t,
		WithWebsiteBaseURL(server.URL),
		WithDataBaseURL(server.URL),
		WithHTTPClient(server.Client()),
	)
	tickers, err := client.GetCompanyTickers(context.Background())
	require.NoError(t, err)
	require.Equal(t, []CompanyTicker{
		{CIK: 320193, Ticker: "AAPL", Title: "Apple Inc."},
		{CIK: 789019, Ticker: "MSFT", Title: "Microsoft Corp"},
	}, tickers)
}

func TestResolveCIKCachesTickerMetadata(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, companyTickersPath, r.URL.Path)
		calls.Add(1)
		_, _ = w.Write([]byte(`{"0":{"cik_str":320193,"ticker":"AAPL","title":"Apple Inc."}}`))
	}))
	defer server.Close()

	client := mustNew(t, WithWebsiteBaseURL(server.URL), WithHTTPClient(server.Client()))
	for range 2 {
		cik, err := client.ResolveCIK(context.Background(), " aapl ")
		require.NoError(t, err)
		require.Equal(t, uint64(320193), cik)
	}
	require.Equal(t, int32(1), calls.Load())

	_, err := client.ResolveCIK(context.Background(), "MISSING")
	require.ErrorIs(t, err, ErrTickerNotFound)
	require.Equal(t, int32(1), calls.Load())
}

func TestGetEquityFactsSelectsLatestValidFacts(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case companyTickersPath:
			_, _ = w.Write([]byte(`{"0":{"cik_str":320193,"ticker":"AAPL","title":"Apple Inc."}}`))
		case "/api/xbrl/companyfacts/CIK0000320193.json":
			require.Equal(t, testUserAgent, r.Header.Get("User-Agent"))
			_, _ = w.Write([]byte(`{
				"cik": 320193,
				"entityName": "Apple Inc.",
				"facts": {
					"dei": {
						"EntityCommonStockSharesOutstanding": {
							"units": {"shares": [
								{"end":"2025-10-17","val":14776353000,"accn":"old","form":"10-K","filed":"2025-10-31"},
								{"end":"2026-04-17","val":14687356000,"accn":"latest","form":"10-Q","filed":"2026-05-01"},
								{"end":"2027-01-01","val":999,"accn":"unsupported","form":"8-K","filed":"2027-01-02"}
							]}
						},
						"EntityPublicFloat": {
							"units": {"USD": [
								{"end":"2024-03-29","val":2600000000000,"accn":"old","form":"10-K","filed":"2024-11-01"},
								{"end":"2025-03-28","val":3253431000000,"accn":"latest","form":"10-K","filed":"2025-10-31"}
							]}
						}
					},
					"us-gaap": {
						"WeightedAverageNumberOfDilutedSharesOutstanding": {
							"units": {"shares": [
								{"start":"2025-09-28","end":"2026-03-28","val":14750000000,"accn":"ytd","form":"10-Q","filed":"2026-05-01"},
								{"start":"2025-12-28","end":"2026-03-28","val":14725873000,"accn":"quarter","form":"10-Q","filed":"2026-05-01"},
								{"start":"2025-12-28","end":"2026-03-28","val":14720000000,"accn":"amended","form":"10-Q/A","filed":"2026-05-03"}
							]}
						}
					}
				}
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := mustNew(
		t,
		WithWebsiteBaseURL(server.URL),
		WithDataBaseURL(server.URL),
		WithHTTPClient(server.Client()),
	)
	facts, err := client.GetEquityFacts(context.Background(), "aapl")
	require.NoError(t, err)
	require.Equal(t, uint64(320193), facts.CIK)
	require.Equal(t, "AAPL", facts.Ticker)
	require.Equal(t, "Apple Inc.", facts.EntityName)
	require.NotNil(t, facts.SharesOutstanding)
	require.Equal(t, 14_687_356_000.0, facts.SharesOutstanding.Value)
	require.NotNil(t, facts.DilutedWeightedAverageShares)
	require.Equal(t, 14_720_000_000.0, facts.DilutedWeightedAverageShares.Value)
	require.Equal(t, "amended", facts.DilutedWeightedAverageShares.Accession)
	require.NotNil(t, facts.PublicFloatUSD)
	require.Equal(t, 3_253_431_000_000.0, facts.PublicFloatUSD.Value)
	require.Equal(t, "2025-03-28", facts.PublicFloatUSD.End)
}

func TestGetCompanyFactsReturnsHTTPError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	defer server.Close()

	client := mustNew(t, WithDataBaseURL(server.URL), WithHTTPClient(server.Client()))
	_, err := client.GetCompanyFacts(context.Background(), 320193)
	require.Error(t, err)

	var httpErr *HTTPError
	require.True(t, errors.As(err, &httpErr))
	require.Equal(t, http.StatusTooManyRequests, httpErr.StatusCode)
	require.Equal(t, "rate limited", httpErr.Body)
}

func TestGetCompanyFactsRequiresCIK(t *testing.T) {
	t.Parallel()

	client := mustNew(t)
	_, err := client.GetCompanyFacts(context.Background(), 0)
	require.ErrorContains(t, err, "cik is required")
}

func TestFactSelectorsRejectInvalidValuesAndForms(t *testing.T) {
	t.Parallel()

	facts := &CompanyFacts{Facts: map[string]map[string]CompanyFact{
		"dei": {
			"EntityPublicFloat": {
				Units: map[string][]FactValue{"USD": {
					{End: "2026-01-01", Value: -1, Form: "10-K"},
					{End: "bad-date", Value: 1, Form: "10-K"},
					{End: "2026-01-01", Value: 1, Form: "8-K"},
				}},
			},
		},
	}}

	require.Nil(t, latestInstantFact(facts, "dei", "EntityPublicFloat", "USD"))
	require.Nil(t, latestDurationFact(facts, "us-gaap", "missing", "shares"))
}

func TestGetCompanyFactsFormatsCIK(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/xbrl/companyfacts/CIK0000000042.json", r.URL.Path)
		_, _ = fmt.Fprint(w, `{"cik":42,"entityName":"Example","facts":{}}`)
	}))
	defer server.Close()

	client := mustNew(t, WithDataBaseURL(server.URL), WithHTTPClient(server.Client()))
	facts, err := client.GetCompanyFacts(context.Background(), 42)
	require.NoError(t, err)
	require.Equal(t, uint64(42), facts.CIK)
}
