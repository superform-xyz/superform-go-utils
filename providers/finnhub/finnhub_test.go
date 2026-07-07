package finnhub

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustNew(t *testing.T, opts ...Option) Client {
	t.Helper()
	c, err := New("test-key", opts...)
	require.NoError(t, err)
	return c
}

func TestNewRequiresAPIKey(t *testing.T) {
	t.Parallel()

	_, err := New(" ")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "api key is required")
}

func TestGetCompanyProfileBuildsRequest(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, profilePath, r.URL.Path)
		require.Equal(t, "AAPL", r.URL.Query().Get("symbol"))
		require.Empty(t, r.URL.Query().Get("token"))
		require.Equal(t, "test-key", r.Header.Get(authHeader))
		require.Equal(t, defaultUserAgent, r.Header.Get("User-Agent"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"country": "US",
			"currency": "USD",
			"exchange": "NASDAQ NMS - GLOBAL MARKET",
			"finnhubIndustry": "Technology",
			"ipo": "1980-12-12",
			"logo": "https://static.finnhub.io/logo/aapl.png",
			"marketCapitalization": 4524203.12,
			"name": "Apple Inc",
			"phone": "14089961010",
			"shareOutstanding": 14840.32,
			"ticker": "AAPL",
			"weburl": "https://www.apple.com/"
		}`))
	}))
	defer srv.Close()

	c := mustNew(t, WithBaseURL(srv.URL), WithHTTPClient(srv.Client()))
	profile, err := c.GetCompanyProfile(context.Background(), " aapl ")
	require.NoError(t, err)

	assert.Equal(t, "AAPL", profile.Ticker)
	assert.Equal(t, "Apple Inc", profile.Name)
	assert.Equal(t, "USD", profile.Currency)
	require.NotNil(t, profile.MarketCapitalization)
	assert.Equal(t, 4524203.12, *profile.MarketCapitalization)
	require.NotNil(t, profile.ShareOutstanding)
	assert.Equal(t, 14840.32, *profile.ShareOutstanding)
}

func TestGetCompanyBasicFinancialsBuildsRequest(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, basicMetricsPath, r.URL.Path)
		require.Equal(t, "NVDA", r.URL.Query().Get("symbol"))
		require.Equal(t, "valuation", r.URL.Query().Get("metric"))
		require.Equal(t, "test-key", r.Header.Get(authHeader))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"symbol": "NVDA",
			"metricType": "valuation",
			"metric": {
				"52WeekHighDate": "2026-03-10",
				"marketCapitalization": 5213528.42,
				"peTTM": 34.21,
				"peNormalizedAnnual": null
			},
			"series": {
				"annual": {
					"peTTM": [{"period": "2025-01-31", "v": 31.5}]
				}
			}
		}`))
	}))
	defer srv.Close()

	c := mustNew(t, WithBaseURL(srv.URL), WithHTTPClient(srv.Client()))
	financials, err := c.GetCompanyBasicFinancials(context.Background(), BasicFinancialsRequest{
		Symbol: " nvda ",
		Metric: "valuation",
	})
	require.NoError(t, err)

	assert.Equal(t, "NVDA", financials.Symbol)
	assert.Equal(t, "valuation", financials.MetricType)
	value, ok := financials.MetricValue("peTTM")
	require.True(t, ok)
	assert.Equal(t, 34.21, value)
	dateValue, ok := financials.MetricString("52WeekHighDate")
	require.True(t, ok)
	assert.Equal(t, "2026-03-10", dateValue)
	_, ok = financials.MetricValue("peNormalizedAnnual")
	assert.False(t, ok)
	require.Len(t, financials.Series["annual"]["peTTM"], 1)
	assert.Equal(t, "2025-01-31", financials.Series["annual"]["peTTM"][0].Period)
	require.NotNil(t, financials.Series["annual"]["peTTM"][0].Value)
	assert.Equal(t, 31.5, *financials.Series["annual"]["peTTM"][0].Value)
}

func TestGetCompanyBasicFinancialsDefaultsMetric(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, defaultMetric, r.URL.Query().Get("metric"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"symbol":"TSLA","metricType":"all","metric":{}}`))
	}))
	defer srv.Close()

	c := mustNew(t, WithBaseURL(srv.URL), WithHTTPClient(srv.Client()))
	_, err := c.GetCompanyBasicFinancials(context.Background(), BasicFinancialsRequest{Symbol: "tsla"})
	require.NoError(t, err)
}

func TestGetStockFundamentalsCombinesProfileAndMetrics(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case profilePath:
			require.Equal(t, "MSFT", r.URL.Query().Get("symbol"))
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"currency": "USD",
				"exchange": "NASDAQ NMS - GLOBAL MARKET",
				"finnhubIndustry": "Technology",
				"marketCapitalization": 3712278.6,
				"name": "Microsoft Corp",
				"shareOutstanding": 7432.0,
				"ticker": "MSFT"
			}`))
		case basicMetricsPath:
			require.Equal(t, "MSFT", r.URL.Query().Get("symbol"))
			require.Equal(t, defaultMetric, r.URL.Query().Get("metric"))
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"symbol": "MSFT",
				"metricType": "all",
				"metric": {
					"marketCapitalization": 3700000.0,
					"peNormalizedAnnual": 29.4,
					"peTTM": 31.7
				}
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := mustNew(t, WithBaseURL(srv.URL), WithHTTPClient(srv.Client()))
	out, err := c.GetStockFundamentals(context.Background(), "msft")
	require.NoError(t, err)

	assert.Equal(t, "MSFT", out.Symbol)
	assert.Equal(t, "Microsoft Corp", out.Name)
	assert.Equal(t, "USD", out.Currency)
	require.NotNil(t, out.MarketCapitalization)
	assert.Equal(t, 3712278.6, *out.MarketCapitalization)
	require.NotNil(t, out.ShareOutstanding)
	assert.Equal(t, 7432.0, *out.ShareOutstanding)
	require.NotNil(t, out.PERatio)
	assert.Equal(t, 31.7, *out.PERatio)
	assert.Equal(t, "peTTM", out.PERatioMetric)
	assert.Equal(t, "MSFT", out.BasicFinancials.Symbol)
	assert.Equal(t, "MSFT", out.CompanyProfile.Ticker)
}

func TestGetStockFundamentalsUsesMetricMarketCapFallback(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case profilePath:
			_, _ = w.Write([]byte(`{"ticker":"TSLA","name":"Tesla Inc","currency":"USD"}`))
		case basicMetricsPath:
			_, _ = w.Write([]byte(`{
				"symbol": "TSLA",
				"metricType": "all",
				"metric": {
					"marketCapitalization": 978123.4,
					"peNormalizedAnnual": 62.5
				}
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := mustNew(t, WithBaseURL(srv.URL), WithHTTPClient(srv.Client()))
	out, err := c.GetStockFundamentals(context.Background(), "tsla")
	require.NoError(t, err)

	require.NotNil(t, out.MarketCapitalization)
	assert.Equal(t, 978123.4, *out.MarketCapitalization)
	require.NotNil(t, out.PERatio)
	assert.Equal(t, 62.5, *out.PERatio)
	assert.Equal(t, "peNormalizedAnnual", out.PERatioMetric)
}

func TestEmbeddedAPIError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error":"Invalid API key"}`))
	}))
	defer srv.Close()

	c := mustNew(t, WithBaseURL(srv.URL), WithHTTPClient(srv.Client()))
	_, err := c.GetCompanyProfile(context.Background(), "aapl")
	require.Error(t, err)

	var apiErr *APIError
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, "Invalid API key", apiErr.Message)
}

func TestHTTPError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("rate limit exceeded"))
	}))
	defer srv.Close()

	c := mustNew(t, WithBaseURL(srv.URL), WithHTTPClient(srv.Client()))
	_, err := c.GetCompanyProfile(context.Background(), "aapl")
	require.Error(t, err)

	var httpErr *HTTPError
	require.True(t, errors.As(err, &httpErr))
	assert.Equal(t, http.StatusTooManyRequests, httpErr.StatusCode)
	assert.Equal(t, "rate limit exceeded", httpErr.Body)
}

func TestSymbolValidation(t *testing.T) {
	t.Parallel()

	c := mustNew(t)
	_, err := c.GetCompanyProfile(context.Background(), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "symbol is required")
}
