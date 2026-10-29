package yahoo

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/superform-xyz/superform-go-utils/pkg/http_client"
)

const capturedINFQShareStatistics = `{"quoteSummary":{"result":[{
	"price":{"symbol":"INFQ","currency":"USD","quoteType":"EQUITY","exchange":"NYQ","exchangeName":"NYSE"},
	"defaultKeyStatistics":{"floatShares":195573864,"sharesOutstanding":225357052,"mostRecentQuarter":1782777600,"dateShortInterest":1792022400}
}],"error":null}}`

func TestGetShareStatisticsCapturedINFQAndCachedSession(t *testing.T) {
	t.Parallel()
	var cookies, crumbs, quotes atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expectedAgent := defaultUserAgent
		if r.URL.Path == "/v1/test/getcrumb" {
			expectedAgent = defaultCrumbAgent
		}
		assert.Equal(t, expectedAgent, r.UserAgent())
		switch r.URL.Path {
		case "/cookie":
			cookies.Add(1)
			http.SetCookie(w, &http.Cookie{Name: "A3", Value: "cookie-secret", Path: "/"})
			w.WriteHeader(http.StatusNotFound)
		case "/v1/test/getcrumb":
			crumbs.Add(1)
			assert.Equal(t, "text/plain", r.Header.Get("Accept"))
			cookie, err := r.Cookie("A3")
			if assert.NoError(t, err) {
				assert.Equal(t, "cookie-secret", cookie.Value)
			}
			_, _ = w.Write([]byte("crumb/secret+value"))
		case "/v10/finance/quoteSummary/INFQ":
			quotes.Add(1)
			assert.Equal(t, "price,defaultKeyStatistics", r.URL.Query().Get("modules"))
			assert.Equal(t, "false", r.URL.Query().Get("formatted"))
			assert.Equal(t, "crumb/secret+value", r.URL.Query().Get("crumb"))
			_, err := r.Cookie("A3")
			assert.NoError(t, err)
			_, _ = w.Write([]byte(capturedINFQShareStatistics))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	// Use the real retry transport, whose handling of a 404 used to discard
	// Set-Cookie before a normal http.Client cookie jar could observe it.
	c := mustNew(t, WithBaseURL(server.URL), WithCookieURL(server.URL+"/cookie"), WithRetry(2, time.Millisecond))
	defer func() { require.NoError(t, c.Close()) }()
	for range 2 {
		stats, err := c.GetShareStatistics(context.Background(), " infq ")
		require.NoError(t, err)
		require.Equal(t, "INFQ", stats.Symbol)
		require.Equal(t, "USD", stats.Currency)
		require.Equal(t, "EQUITY", stats.QuoteType)
		require.Equal(t, "NYQ", stats.Exchange)
		require.NotNil(t, stats.FloatShares)
		require.Equal(t, 195_573_864.0, *stats.FloatShares)
		require.NotNil(t, stats.SharesOutstanding)
		require.Equal(t, 225_357_052.0, *stats.SharesOutstanding)
	}
	require.Equal(t, int64(1), cookies.Load())
	require.Equal(t, int64(1), crumbs.Load())
	require.Equal(t, int64(2), quotes.Load())
}

func TestGetShareStatisticsRefreshesUnauthorizedOnce(t *testing.T) {
	t.Parallel()
	for _, alwaysUnauthorized := range []bool{false, true} {
		t.Run(fmt.Sprint(alwaysUnauthorized), func(t *testing.T) {
			t.Parallel()
			var cookies, quotes atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/cookie":
					generation := cookies.Add(1)
					http.SetCookie(w, &http.Cookie{Name: "A3", Value: fmt.Sprint(generation), Path: "/"})
					w.WriteHeader(http.StatusNotFound)
				case "/v1/test/getcrumb":
					_, _ = fmt.Fprintf(w, "crumb-%d", cookies.Load())
				default:
					attempt := quotes.Add(1)
					if alwaysUnauthorized || attempt == 1 {
						w.WriteHeader(http.StatusUnauthorized)
						_, _ = fmt.Fprintf(w, "rejected %s; %s", r.URL.Query().Get("crumb"), r.Header.Get("Cookie"))
						return
					}
					assert.Equal(t, "crumb-2", r.URL.Query().Get("crumb"))
					_, _ = w.Write([]byte(capturedINFQShareStatistics))
				}
			}))
			defer server.Close()
			c := mustNew(t, WithBaseURL(server.URL), WithCookieURL(server.URL+"/cookie"))
			_, err := c.GetShareStatistics(context.Background(), "INFQ")
			if alwaysUnauthorized {
				require.Error(t, err)
				require.NotContains(t, err.Error(), "crumb-")
				require.NotContains(t, err.Error(), "A3=")
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, int64(2), cookies.Load())
			require.Equal(t, int64(2), quotes.Load())
		})
	}
}

func TestShareStatisticsMissingNullAndZeroFields(t *testing.T) {
	t.Parallel()
	for _, fields := range []string{`{}`, `{"floatShares":null,"sharesOutstanding":null}`, `{"floatShares":0,"sharesOutstanding":0}`} {
		t.Run(fields, func(t *testing.T) {
			t.Parallel()
			stats, err := decodeShareStatistics([]byte(`{"quoteSummary":{"result":[{"price":{"symbol":"INFQ"},"defaultKeyStatistics":` + fields + `}],"error":null}}`))
			assert.NoError(t, err)
			if fields == `{"floatShares":0,"sharesOutstanding":0}` {
				require.NotNil(t, stats.FloatShares)
				require.Zero(t, *stats.FloatShares)
				require.NotNil(t, stats.SharesOutstanding)
				require.Zero(t, *stats.SharesOutstanding)
			} else {
				require.Nil(t, stats.FloatShares)
				require.Nil(t, stats.SharesOutstanding)
			}
		})
	}
}

func TestGetShareStatisticsCoalescesParallelAuthentication(t *testing.T) {
	t.Parallel()
	var cookies, crumbs atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cookie":
			cookies.Add(1)
			http.SetCookie(w, &http.Cookie{Name: "A3", Value: "cookie-secret", Path: "/"})
			w.WriteHeader(http.StatusNotFound)
		case "/v1/test/getcrumb":
			crumbs.Add(1)
			_, _ = w.Write([]byte("crumb-secret"))
		default:
			_, _ = w.Write([]byte(capturedINFQShareStatistics))
		}
	}))
	defer server.Close()
	c := mustNew(t, WithBaseURL(server.URL), WithCookieURL(server.URL+"/cookie"))
	var group sync.WaitGroup
	start := make(chan struct{})
	for range 20 {
		group.Go(func() {
			<-start
			_, err := c.GetShareStatistics(context.Background(), "INFQ")
			assert.NoError(t, err)
		})
	}
	close(start)
	group.Wait()
	require.Equal(t, int64(1), cookies.Load())
	require.Equal(t, int64(1), crumbs.Load())
}

func TestGetShareStatisticsContextCancellation(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	c := mustNew(t, WithBaseURL(server.URL), WithCookieURL(server.URL+"/cookie"))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := c.GetShareStatistics(ctx, "INFQ")
		done <- err
	}()
	<-started
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
}

type failingYahooTransport struct{}

func (failingYahooTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return nil, &url.Error{Op: "Get", URL: r.URL.String(), Err: errors.New("network error")}
}

func TestYahooSessionErrorsDoNotExposeCredentials(t *testing.T) {
	t.Parallel()
	err := safeRequestError(&url.Error{Op: "Get", URL: "https://example.test/?crumb=crumb-secret", Err: errors.New("cookie-secret")})
	require.NotContains(t, err.Error(), "crumb-secret")
	require.NotContains(t, err.Error(), "cookie-secret")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cookie":
			http.SetCookie(w, &http.Cookie{Name: "A3", Value: "cookie-secret", Path: "/"})
		case "/v1/test/getcrumb":
			_, _ = w.Write([]byte("crumb-secret"))
		case "/v8/finance/chart/INFQ":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprintf(w, "%s crumb-secret", r.Header.Get("Cookie"))
		default:
			_, _ = w.Write([]byte(capturedINFQShareStatistics))
		}
	}))
	defer server.Close()
	c := mustNew(t, WithBaseURL(server.URL), WithCookieURL(server.URL+"/cookie"))
	_, err = c.GetShareStatistics(context.Background(), "INFQ")
	require.NoError(t, err)
	_, err = c.GetChart(context.Background(), ChartRequest{Symbol: "INFQ"})
	require.Error(t, err)
	require.NotContains(t, err.Error(), "cookie-secret")
	require.NotContains(t, err.Error(), "crumb-secret")
	c.(*client).httpClient.Transport = failingYahooTransport{}
	_, err = c.GetShareStatistics(context.Background(), "INFQ")
	require.Error(t, err)
	require.NotContains(t, err.Error(), "cookie-secret")
	require.NotContains(t, err.Error(), "crumb-secret")
}

func TestYahooClientRetainsDefaultBaseOfCustomRetryTransport(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"chart":{"result":[{"meta":{"symbol":"INFQ"}}],"error":null}}`))
	}))
	defer server.Close()
	c := mustNew(t, WithBaseURL(server.URL), WithHTTPClient(&http.Client{Transport: &http_client.Transport{MaxRetries: 1}}))
	chart, err := c.GetChart(context.Background(), ChartRequest{Symbol: "INFQ"})
	require.NoError(t, err)
	require.Len(t, chart.Chart.Result, 1)
	require.Equal(t, "INFQ", chart.Chart.Result[0].Meta.Symbol)
}

func TestGetShareStatisticsLive(t *testing.T) {
	if os.Getenv("YAHOO_LIVE_TESTS") != "1" {
		t.Skip("set YAHOO_LIVE_TESTS=1 to verify Yahoo's free anonymous share statistics")
	}
	c := mustNew(t, WithTimeout(15*time.Second), WithRateLimit(2))
	defer func() { require.NoError(t, c.Close()) }()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	stats, err := c.GetShareStatistics(ctx, "INFQ")
	require.NoError(t, err)
	require.Equal(t, "INFQ", stats.Symbol)
	require.Equal(t, "USD", stats.Currency)
	require.Equal(t, "EQUITY", stats.QuoteType)
	require.NotNil(t, stats.FloatShares)
	require.NotNil(t, stats.SharesOutstanding)
	require.Positive(t, *stats.FloatShares)
	require.GreaterOrEqual(t, *stats.SharesOutstanding, *stats.FloatShares)
	t.Logf("INFQ float %.0f; shares outstanding %.0f", *stats.FloatShares, *stats.SharesOutstanding)
}
