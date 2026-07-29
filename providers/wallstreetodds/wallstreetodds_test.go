package wallstreetodds

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func mustNew(t *testing.T, opts ...Option) Client {
	t.Helper()
	client, err := New("test-key", opts...)
	require.NoError(t, err)
	return client
}

func TestNewRequiresAPIKey(t *testing.T) {
	t.Parallel()

	_, err := New(" ")
	require.ErrorContains(t, err, "api key is required")
}

func TestGetAllTimeHighsBuildsBatchedRequest(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, technicalPricingPath, r.URL.Path)
		require.Equal(t, "test-key", r.URL.Query().Get("apikey"))
		require.Equal(t, "symbol,allTimeHigh", r.URL.Query().Get("fields"))
		require.Equal(t, "json", r.URL.Query().Get("format"))
		require.Equal(t, "AAPL,MSFT", r.URL.Query().Get("symbols"))
		require.Equal(t, defaultUserAgent, r.Header.Get("User-Agent"))
		_, _ = w.Write([]byte(`{
			"response": [
				{"symbol": "MSFT", "alltimehigh": 555.45},
				{"symbol": "aapl", "allTimeHigh": 342.89}
			]
		}`))
	}))
	defer server.Close()

	client := mustNew(t, WithBaseURL(server.URL), WithHTTPClient(server.Client()))
	rows, err := client.GetAllTimeHighs(context.Background(), []string{" msft ", "AAPL", "aapl", ""})
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Equal(t, "MSFT", rows[0].Symbol)
	require.NotNil(t, rows[0].AllTimeHigh)
	require.Equal(t, 555.45, *rows[0].AllTimeHigh)
	require.Equal(t, "AAPL", rows[1].Symbol)
	require.NotNil(t, rows[1].AllTimeHigh)
	require.Equal(t, 342.89, *rows[1].AllTimeHigh)
}

func TestGetAllTimeHighsEmptyInputDoesNotCallAPI(t *testing.T) {
	t.Parallel()

	client := mustNew(t)
	rows, err := client.GetAllTimeHighs(context.Background(), nil)
	require.NoError(t, err)
	require.Empty(t, rows)
}

func TestGetAllTimeHighsRejectsUnsafeSymbol(t *testing.T) {
	t.Parallel()

	client := mustNew(t)
	_, err := client.GetAllTimeHighs(context.Background(), []string{"AAPL&apikey=other"})
	require.ErrorContains(t, err, "invalid wallstreetodds symbol")
}

func TestGetAllTimeHighsReturnsEmbeddedAPIError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"error":{"message":"credit limit exceeded"}}`))
	}))
	defer server.Close()

	client := mustNew(t, WithBaseURL(server.URL), WithHTTPClient(server.Client()))
	_, err := client.GetAllTimeHighs(context.Background(), []string{"AAPL"})
	require.Error(t, err)

	var apiErr *APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, "credit limit exceeded", apiErr.Message)
}

func TestGetAllTimeHighsReturnsHTTPErrorWithoutLeakingAPIKey(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	defer server.Close()

	client := mustNew(t, WithBaseURL(server.URL), WithHTTPClient(server.Client()))
	_, err := client.GetAllTimeHighs(context.Background(), []string{"AAPL"})
	require.Error(t, err)
	require.NotContains(t, err.Error(), "test-key")

	var httpErr *HTTPError
	require.True(t, errors.As(err, &httpErr))
	require.Equal(t, http.StatusTooManyRequests, httpErr.StatusCode)
	require.Equal(t, "rate limited", httpErr.Body)
}

func TestGetAllTimeHighsRedactsAPIKeyFromTransportError(t *testing.T) {
	t.Parallel()

	httpClient := &http.Client{
		Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("dial failed for ?apikey=test-key")
		}),
	}
	client := mustNew(t, WithHTTPClient(httpClient))

	_, err := client.GetAllTimeHighs(context.Background(), []string{"AAPL"})
	require.Error(t, err)
	require.NotContains(t, err.Error(), "test-key")
	require.Contains(t, err.Error(), "[REDACTED]")
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
