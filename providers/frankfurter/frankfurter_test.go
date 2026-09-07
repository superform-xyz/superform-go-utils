package frankfurter

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGetRateBatchesCurrenciesAndDatesByMonth(t *testing.T) {
	t.Parallel()

	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != ratesPath ||
			r.URL.Query().Get("base") != "USD" ||
			r.URL.Query().Get("from") != "2026-09-01" ||
			r.URL.Query().Get("to") != "2026-09-02" ||
			r.Header.Get("User-Agent") != defaultUserAgent {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		_, _ = fmt.Fprint(w, `[
			{"date":"2026-09-01","base":"USD","quote":"TWD","rate":31.5},
			{"date":"2026-09-01","base":"USD","quote":"KRW","rate":1370},
			{"date":"2026-09-02","base":"USD","quote":"TWD","rate":31.697},
			{"date":"2026-09-02","base":"USD","quote":"KRW","rate":1372.02}
		]`)
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, server, time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC))
	twd, err := client.GetRate(context.Background(), RateRequest{
		Base:        " usd ",
		Quote:       " twd ",
		EffectiveAt: time.Date(2026, 9, 1, 18, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	require.Equal(t, Rate{
		Date:  time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		Base:  "USD",
		Quote: "TWD",
		Value: 31.5,
	}, *twd)

	krw, err := client.GetRate(context.Background(), RateRequest{
		Base:        "USD",
		Quote:       "KRW",
		EffectiveAt: time.Date(2026, 9, 2, 18, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	require.Equal(t, 1372.02, krw.Value)
	require.Equal(t, int64(1), requests.Load())
}

func TestGetRateDoesNotCallFrankfurterForIdenticalCurrencies(t *testing.T) {
	t.Parallel()

	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, server, time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC))
	rate, err := client.GetRate(context.Background(), RateRequest{Base: " usd ", Quote: "USD"})
	require.NoError(t, err)
	require.Equal(t, 1.0, rate.Value)
	require.Zero(t, requests.Load())
}

func TestGetRateCoalescesConcurrentCacheMisses(t *testing.T) {
	t.Parallel()

	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		time.Sleep(20 * time.Millisecond)
		_, _ = fmt.Fprint(w, `[
			{"date":"2026-09-02","base":"USD","quote":"TWD","rate":31.697},
			{"date":"2026-09-02","base":"USD","quote":"KRW","rate":1372.02}
		]`)
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, server, time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC))
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := range 20 {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			quote := "TWD"
			if index%2 == 0 {
				quote = "KRW"
			}
			_, err := client.GetRate(context.Background(), RateRequest{
				Base:        "USD",
				Quote:       quote,
				EffectiveAt: time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC),
			})
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, int64(1), requests.Load())
}

func TestGetRateUsesRecentCachedRateWhenUpstreamFails(t *testing.T) {
	t.Parallel()

	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			_, _ = fmt.Fprint(w, `[{"date":"2026-09-01","base":"USD","quote":"TWD","rate":31.5}]`)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, server, time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC))
	first, err := client.GetRate(context.Background(), RateRequest{
		Base: "USD", Quote: "TWD", EffectiveAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)

	fallback, err := client.GetRate(context.Background(), RateRequest{
		Base: "USD", Quote: "TWD", EffectiveAt: time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	require.Equal(t, first.Value, fallback.Value)
	require.True(t, fallback.Stale)
	require.Equal(t, first.Date, fallback.Date)
	require.Equal(t, int64(2), requests.Load())

	_, err = client.GetRate(context.Background(), RateRequest{
		Base: "USD", Quote: "TWD", EffectiveAt: time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	require.Equal(t, int64(2), requests.Load())
}

func TestGetRateCachesMonthlyFetchFailureAcrossCurrencies(t *testing.T) {
	t.Parallel()

	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)

	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	client, err := New(
		WithBaseURL(server.URL),
		WithHTTPClient(server.Client()),
		WithRateLimit(0),
		WithFailureTTL(5*time.Minute),
		withClock(func() time.Time { return now }),
	)
	require.NoError(t, err)
	for _, quote := range []string{"TWD", "KRW"} {
		_, err = client.GetRate(context.Background(), RateRequest{Base: "USD", Quote: quote})
		require.ErrorIs(t, err, ErrRateUnavailable)
	}
	require.Equal(t, int64(1), requests.Load())

	now = now.Add(6 * time.Minute)
	_, err = client.GetRate(context.Background(), RateRequest{Base: "USD", Quote: "TWD"})
	require.ErrorIs(t, err, ErrRateUnavailable)
	require.Equal(t, int64(2), requests.Load())
}

func TestGetRateUsesPriorRateWhenCurrentDayIsNotPublished(t *testing.T) {
	t.Parallel()

	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = fmt.Fprint(w, `[
			{"date":"2026-09-01","base":"USD","quote":"TWD","rate":31.5},
			{"date":"2026-09-02","base":"USD","quote":"KRW","rate":1372.02}
		]`)
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, server, time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC))
	for range 2 {
		rate, err := client.GetRate(context.Background(), RateRequest{
			Base: "USD", Quote: "TWD", EffectiveAt: time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC),
		})
		require.NoError(t, err)
		require.True(t, rate.Stale)
		require.Equal(t, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), rate.Date)
	}
	krw, err := client.GetRate(context.Background(), RateRequest{
		Base: "USD", Quote: "KRW", EffectiveAt: time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	require.Equal(t, 1372.02, krw.Value)
	require.False(t, krw.Stale)
	require.Equal(t, int64(1), requests.Load())
}

func TestGetRateFailsClosedWhenCachedRateIsTooOld(t *testing.T) {
	t.Parallel()

	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			_, _ = fmt.Fprint(w, `[{"date":"2026-09-01","base":"USD","quote":"KRW","rate":1370}]`)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, server, time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC))
	_, err := client.GetRate(context.Background(), RateRequest{
		Base: "USD", Quote: "KRW", EffectiveAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)

	_, err = client.GetRate(context.Background(), RateRequest{
		Base: "USD", Quote: "KRW", EffectiveAt: time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC),
	})
	require.ErrorIs(t, err, ErrRateUnavailable)
}

func TestGetRateFailsClosedForInvalidOrMissingRates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
	}{
		{name: "invalid rate", body: `[{"date":"2026-09-02","base":"USD","quote":"TWD","rate":0}]`},
		{name: "wrong base", body: `[{"date":"2026-09-02","base":"EUR","quote":"TWD","rate":31.697}]`},
		{name: "missing currency", body: `[{"date":"2026-09-02","base":"USD","quote":"EUR","rate":0.85}]`},
		{name: "invalid JSON", body: `not-json`},
		{name: "duplicate currency", body: `[
			{"date":"2026-09-02","base":"USD","quote":"TWD","rate":31.697},
			{"date":"2026-09-02","base":"USD","quote":"TWD","rate":31.698}
		]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprint(w, tt.body)
			}))
			t.Cleanup(server.Close)

			client := newTestClient(t, server, time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC))
			_, err := client.GetRate(context.Background(), RateRequest{
				Base: "USD", Quote: "TWD", EffectiveAt: time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC),
			})
			require.ErrorIs(t, err, ErrRateUnavailable)
		})
	}
}

func TestGetRateReturnsHTTPError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, server, time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC))
	_, err := client.GetRate(context.Background(), RateRequest{Base: "USD", Quote: "TWD"})
	require.Error(t, err)
	var httpErr *HTTPError
	require.True(t, errors.As(err, &httpErr))
	require.Equal(t, http.StatusTooManyRequests, httpErr.StatusCode)
}

func TestGetRateRequiresBothCurrencies(t *testing.T) {
	t.Parallel()

	client, err := New()
	require.NoError(t, err)
	_, err = client.GetRate(context.Background(), RateRequest{Base: "USD"})
	require.ErrorIs(t, err, ErrRateUnavailable)
}

func TestStoreRatesKeepsNewlyFetchedHistoricalDate(t *testing.T) {
	t.Parallel()

	client := &client{
		maxCacheDays:  2,
		ratesByDate:   make(map[string]map[string]Rate),
		cacheStoredAt: make(map[string]uint64),
	}
	store := func(date time.Time) {
		client.storeRates(map[string]map[string]Rate{
			cacheDateKey("USD", date): {
				"TWD": {Date: date, Base: "USD", Quote: "TWD", Value: 31.5},
			},
		})
	}
	septemberFirst := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	septemberSecond := septemberFirst.AddDate(0, 0, 1)
	historical := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	store(septemberFirst)
	store(septemberSecond)
	store(historical)

	_, firstStillCached := client.cachedRate("USD", septemberFirst, "TWD")
	_, secondStillCached := client.cachedRate("USD", septemberSecond, "TWD")
	_, historicalCached := client.cachedRate("USD", historical, "TWD")
	require.False(t, firstStillCached)
	require.True(t, secondStillCached)
	require.True(t, historicalCached)
}

func newTestClient(t *testing.T, server *httptest.Server, now time.Time) Client {
	t.Helper()
	client, err := New(
		WithBaseURL(server.URL),
		WithHTTPClient(server.Client()),
		WithRateLimit(0),
		withClock(func() time.Time { return now }),
	)
	require.NoError(t, err)
	return client
}
