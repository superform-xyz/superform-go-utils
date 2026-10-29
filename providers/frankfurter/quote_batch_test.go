package frankfurter

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestQuoteBatchFiltersAndSharesOneRequest(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		assert.Equal(t, "/v2/rates", r.URL.Path)
		assert.Equal(t, "USD", r.URL.Query().Get("base"))
		assert.Equal(t, "EUR,CAD,KRW,TWD", r.URL.Query().Get("quotes"))
		assert.Equal(t, "2026-10-01", r.URL.Query().Get("from"))
		assert.Equal(t, "2026-10-29", r.URL.Query().Get("to"))
		_, _ = fmt.Fprint(w, `[
			{"date":"2026-10-29","base":"USD","quote":"EUR","rate":0.9},
			{"date":"2026-10-29","base":"USD","quote":"CAD","rate":1.4},
			{"date":"2026-10-29","base":"USD","quote":"KRW","rate":1333},
			{"date":"2026-10-29","base":"USD","quote":"TWD","rate":32.4}
		]`)
	}))
	t.Cleanup(server.Close)
	c := quoteBatchClient(t, server, now, WithQuotes(" eur ", "cad", "KRW", " twd ", "EUR", ""))

	_, err := c.GetRate(context.Background(), RateRequest{Base: "USD", Quote: "AUD"})
	require.ErrorIs(t, err, ErrRateUnavailable)
	require.Zero(t, requests.Load())

	identity, err := c.GetRate(context.Background(), RateRequest{Base: "USD", Quote: "USD"})
	require.NoError(t, err)
	require.Equal(t, 1.0, identity.Value)
	require.Zero(t, requests.Load())

	for _, quote := range []string{"EUR", "CAD", "KRW", "TWD"} {
		got, err := c.GetRate(context.Background(), RateRequest{Base: "USD", Quote: quote})
		require.NoError(t, err)
		require.Equal(t, quote, got.Quote)
		require.Equal(t, dateUTC(now), got.Date)
		require.False(t, got.Stale)
	}
	require.Equal(t, int64(1), requests.Load())
}

func TestQuoteBatchCapturedBCHCarryDoesNotBlockFiatRates(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		assert.Empty(t, r.URL.Query().Get("quotes"))
		_, _ = fmt.Fprint(w, `[
			{"date":"2026-09-30","base":"USD","quote":"BCH","rate":0.0032663},
			{"date":"2026-10-29","base":"USD","quote":"EUR","rate":0.89632},
			{"date":"2026-10-29","base":"USD","quote":"CAD","rate":1.4029},
			{"date":"2026-10-29","base":"USD","quote":"KRW","rate":1333.33},
			{"date":"2026-10-29","base":"USD","quote":"TWD","rate":32.454}
		]`)
	}))
	t.Cleanup(server.Close)
	c := quoteBatchClient(t, server, now)
	for quote, value := range map[string]float64{"EUR": 0.89632, "CAD": 1.4029, "KRW": 1333.33, "TWD": 32.454} {
		got, err := c.GetRate(context.Background(), RateRequest{Base: "USD", Quote: quote})
		require.NoError(t, err)
		require.Equal(t, value, got.Value)
		require.Equal(t, dateUTC(now), got.Date)
		require.False(t, got.Stale)
	}
	carryDate := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	carried, err := c.GetRate(context.Background(), RateRequest{Base: "USD", Quote: "BCH", EffectiveAt: carryDate})
	require.NoError(t, err)
	require.Equal(t, carryDate, carried.Date)
	require.Equal(t, 0.0032663, carried.Value)
	require.Equal(t, int64(1), requests.Load())
}

func TestQuoteBatchBadCurrencyDoesNotBlockOthers(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		row  string
	}{
		{"stale", `{"date":"2026-09-30","base":"USD","quote":"KRW","rate":1333}`},
		{"zero", `{"date":"2026-10-29","base":"USD","quote":"KRW","rate":0}`},
		{"bad date", `{"date":"invalid","base":"USD","quote":"KRW","rate":1333}`},
		{"bad number", `{"date":"2026-10-29","base":"USD","quote":"KRW","rate":"invalid"}`},
		{"future", `{"date":"2026-10-30","base":"USD","quote":"KRW","rate":1333}`},
		{"wrong base", `{"date":"2026-10-29","base":"EUR","quote":"KRW","rate":1333}`},
		{"duplicate", `{"date":"2026-10-29","base":"USD","quote":"KRW","rate":1333},
			{"date":"2026-10-29","base":"USD","quote":"KRW","rate":1334}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				_, _ = fmt.Fprintf(w, `[%s,
					{"date":"2026-10-29","base":"USD","quote":"EUR","rate":0.9},
					{"date":"2026-10-29","base":"USD","quote":"CAD","rate":1.4},
					{"date":"2026-10-29","base":"USD","quote":"TWD","rate":32.4}
				]`, tc.row)
			}))
			t.Cleanup(server.Close)
			c := quoteBatchClient(t, server, time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC), WithQuotes("EUR", "CAD", "KRW", "TWD"))

			// The failing currency is requested first, so it cannot poison the batch.
			_, err := c.GetRate(context.Background(), RateRequest{Base: "USD", Quote: "KRW"})
			require.ErrorIs(t, err, ErrRateUnavailable)
			for _, quote := range []string{"EUR", "CAD", "TWD"} {
				got, err := c.GetRate(context.Background(), RateRequest{Base: "USD", Quote: quote})
				require.NoError(t, err)
				require.Equal(t, quote, got.Quote)
				require.False(t, got.Stale)
			}
			require.Equal(t, int64(1), requests.Load())
		})
	}
}

func TestQuoteBatchMonthBoundaryPreservesAgeLimit(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		date string
		ok   bool
	}{
		{"previous day", "2026-09-30", true},
		{"72 hours", "2026-09-28", true},
		{"too old", "2026-09-27", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprintf(w, `[{"date":%q,"base":"USD","quote":"KRW","rate":1333}]`, tc.date)
			}))
			t.Cleanup(server.Close)
			c := quoteBatchClient(t, server, time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC), WithQuotes("KRW"))
			got, err := c.GetRate(context.Background(), RateRequest{Base: "USD", Quote: "KRW"})
			if !tc.ok {
				require.ErrorIs(t, err, ErrRateUnavailable)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.date, got.Date.Format(time.DateOnly))
			require.True(t, got.Stale)
		})
	}
}

func TestQuoteBatchRejectsMalformedPayload(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `[{"date":"2026-10-29","base":"USD","quote":"EUR","rate":0.9},`)
	}))
	t.Cleanup(server.Close)
	c := quoteBatchClient(t, server, time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC), WithQuotes("EUR"))
	_, err := c.GetRate(context.Background(), RateRequest{Base: "USD", Quote: "EUR"})
	require.ErrorIs(t, err, ErrRateUnavailable)
}

func quoteBatchClient(t *testing.T, server *httptest.Server, now time.Time, opts ...Option) Client {
	t.Helper()
	options := append([]Option{
		WithBaseURL(server.URL),
		WithHTTPClient(server.Client()),
		WithRateLimit(0),
		withClock(func() time.Time { return now }),
	}, opts...)
	c, err := New(options...)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, c.Close()) })
	return c
}
