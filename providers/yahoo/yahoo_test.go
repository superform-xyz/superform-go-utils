package yahoo

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustNew(t *testing.T, opts ...Option) Client {
	t.Helper()
	c, err := New(opts...)
	require.NoError(t, err)
	return c
}

func TestGetChartBuildsRangeQuery(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "/v8/finance/chart/AAPL", r.URL.Path)
		require.Equal(t, "5d", r.URL.Query().Get("range"))
		require.Equal(t, "5m", r.URL.Query().Get("interval"))
		require.Equal(t, "true", r.URL.Query().Get("includePrePost"))
		require.Equal(t, "div,splits", r.URL.Query().Get("events"))
		require.Equal(t, defaultUserAgent, r.Header.Get("User-Agent"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"chart":{"result":[{"meta":{"symbol":"AAPL","currency":"USD"},"timestamp":[],"indicators":{"quote":[]}}],"error":null}}`))
	}))
	defer srv.Close()

	c := mustNew(t, WithBaseURL(srv.URL), WithHTTPClient(srv.Client()))
	resp, err := c.GetChart(context.Background(), ChartRequest{
		Symbol:         " aapl ",
		Range:          "5d",
		Interval:       "5m",
		IncludePrePost: true,
		Events:         "div,splits",
	})
	require.NoError(t, err)
	require.Len(t, resp.Chart.Result, 1)
	assert.Equal(t, "AAPL", resp.Chart.Result[0].Meta.Symbol)
	assert.Nil(t, resp.Chart.Result[0].Events.Splits)
}

func TestGetChartDecodesSplitEvents(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "splits", r.URL.Query().Get("events"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"chart": {
				"result": [{
					"meta": {"symbol": "EXAMPLE"},
					"timestamp": [1717947000],
					"indicators": {"quote": [{"close": [135.58]}]},
					"events": {"splits": {
						"1718006400": {
							"date": 1718033400,
							"numerator": 2.5,
							"denominator": 1,
							"splitRatio": "5:2"
						}
					}}
				}],
				"error": null
			}
		}`))
	}))
	defer srv.Close()

	c := mustNew(t, WithBaseURL(srv.URL), WithHTTPClient(srv.Client()))
	resp, err := c.GetChart(context.Background(), ChartRequest{
		Symbol: "EXAMPLE",
		Events: "splits",
	})
	require.NoError(t, err)
	require.Len(t, resp.Chart.Result, 1)
	result := resp.Chart.Result[0]
	require.Len(t, result.Events.Splits, 1)
	assert.Equal(t, SplitEvent{
		Date:        1718033400,
		Numerator:   2.5,
		Denominator: 1,
		SplitRatio:  "5:2",
	}, result.Events.Splits["1718006400"])
	assert.Equal(t, []int64{1717947000}, result.Timestamp)
	require.Len(t, result.Indicators.Quote, 1)
	require.Len(t, result.Indicators.Quote[0].Close, 1)
	require.NotNil(t, result.Indicators.Quote[0].Close[0])
	assert.Equal(t, 135.58, *result.Indicators.Quote[0].Close[0])
}

func TestGetChartBuildsPeriodQuery(t *testing.T) {
	t.Parallel()

	start := time.Unix(100, 0).UTC()
	end := time.Unix(200, 0).UTC()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v8/finance/chart/NVDA", r.URL.Path)
		require.Equal(t, "100", r.URL.Query().Get("period1"))
		require.Equal(t, "200", r.URL.Query().Get("period2"))
		require.Equal(t, "", r.URL.Query().Get("range"))
		require.Equal(t, "1d", r.URL.Query().Get("interval"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"chart":{"result":[{"meta":{"symbol":"NVDA"},"timestamp":[],"indicators":{"quote":[]}}],"error":null}}`))
	}))
	defer srv.Close()

	c := mustNew(t, WithBaseURL(srv.URL), WithHTTPClient(srv.Client()))
	_, err := c.GetChart(context.Background(), ChartRequest{Symbol: "nvda", Start: start, End: end})
	require.NoError(t, err)
}

func TestYahooEmbeddedErrors(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"chart":{"result":null,"error":{"code":"Not Found","description":"No data found"}}}`))
	}))
	defer srv.Close()

	c := mustNew(t, WithBaseURL(srv.URL), WithHTTPClient(srv.Client()))
	_, err := c.GetChart(context.Background(), ChartRequest{Symbol: "missing"})
	require.Error(t, err)

	var apiErr *APIError
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, "chart", apiErr.Endpoint)
	assert.Equal(t, "Not Found", apiErr.Code)
}

func TestGetStockDetailMapsChart(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v8/finance/chart/AAPL", r.URL.Path)
		require.Equal(t, "1d", r.URL.Query().Get("range"))
		require.Equal(t, "1d", r.URL.Query().Get("interval"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"chart": {
				"result": [{
					"meta": {
						"symbol": "AAPL",
						"shortName": "Apple",
						"longName": "Apple Inc.",
						"currency": "USD",
						"regularMarketPrice": 312.66,
						"regularMarketTime": 1783368001,
						"regularMarketVolume": 49130304,
						"fiftyTwoWeekHigh": 317.4,
						"fiftyTwoWeekLow": 201.5
					},
					"timestamp": [1783368001],
					"indicators": {
						"quote": [{
							"open": [310.5],
							"high": [314.2],
							"low": [307.01],
							"close": [312.66],
							"volume": [49130304]
						}]
					}
				}],
				"error": null
			}
		}`))
	}))
	defer srv.Close()

	c := mustNew(t, WithBaseURL(srv.URL), WithHTTPClient(srv.Client()))
	detail, err := c.GetStockDetail(context.Background(), " aapl ")
	require.NoError(t, err)

	assert.Equal(t, "AAPL", detail.Symbol)
	assert.Equal(t, "Apple", detail.ShortName)
	assert.Equal(t, "Apple Inc.", detail.LongName)
	assert.Equal(t, "USD", detail.Currency)
	require.NotNil(t, detail.Price)
	assert.Equal(t, 312.66, *detail.Price)
	require.NotNil(t, detail.MarketTime)
	assert.Equal(t, time.Unix(1783368001, 0).UTC(), *detail.MarketTime)
	require.NotNil(t, detail.MarketVolume)
	assert.Equal(t, int64(49130304), *detail.MarketVolume)
	require.NotNil(t, detail.Open)
	assert.Equal(t, 310.5, *detail.Open)
	require.NotNil(t, detail.FiftyTwoWeekHigh)
	assert.Equal(t, 317.4, *detail.FiftyTwoWeekHigh)
	require.NotNil(t, detail.FiftyTwoWeekLow)
	assert.Equal(t, 201.5, *detail.FiftyTwoWeekLow)
}

func TestGetStockDetailSurfacesChartFailure(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v8/finance/chart/AAPL", r.URL.Path)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("Too Many Requests"))
	}))
	defer srv.Close()

	c := mustNew(t, WithBaseURL(srv.URL), WithHTTPClient(srv.Client()))
	_, err := c.GetStockDetail(context.Background(), "aapl")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "yahoo stock detail AAPL")
}

func TestGetPriceHistoryMapsBarsAndFiltersIncompleteRows(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v8/finance/chart/NVDA", r.URL.Path)
		require.Equal(t, "5m", r.URL.Query().Get("interval"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"chart": {
				"result": [{
					"meta": {"symbol": "NVDA"},
					"timestamp": [100, 200, 300],
					"indicators": {
						"quote": [{
							"open": [1.0, null, 3.0],
							"high": [2.0, 2.5, 4.0],
							"low": [0.5, 1.5, 2.5],
							"close": [1.5, 2.0, 3.5],
							"volume": [10, 20, null]
						}],
						"adjclose": [{"adjclose": [1.4, 1.9, 3.4]}]
					}
				}],
				"error": null
			}
		}`))
	}))
	defer srv.Close()

	c := mustNew(t, WithBaseURL(srv.URL), WithHTTPClient(srv.Client()))
	bars, err := c.GetPriceHistory(context.Background(), PriceHistoryRequest{
		Symbol:      "nvda",
		Range:       "1d",
		Granularity: "5m",
	})
	require.NoError(t, err)
	require.Len(t, bars, 2)

	assert.Equal(t, time.Unix(100, 0).UTC(), bars[0].Timestamp)
	assert.Equal(t, 1.0, bars[0].Open)
	assert.Equal(t, 2.0, bars[0].High)
	assert.Equal(t, 0.5, bars[0].Low)
	assert.Equal(t, 1.5, bars[0].Close)
	assert.Equal(t, int64(10), bars[0].Volume)
	require.NotNil(t, bars[0].AdjClose)
	assert.Equal(t, 1.4, *bars[0].AdjClose)

	assert.Equal(t, time.Unix(300, 0).UTC(), bars[1].Timestamp)
	assert.Equal(t, int64(0), bars[1].Volume)
	require.NotNil(t, bars[1].AdjClose)
	assert.Equal(t, 3.4, *bars[1].AdjClose)
}

func TestSymbolValidation(t *testing.T) {
	t.Parallel()

	c := mustNew(t)
	_, err := c.GetChart(context.Background(), ChartRequest{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "symbol is required")
}
