package zerion

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type countingTransport struct {
	base     http.RoundTripper
	attempts *int
}

func (t countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	*t.attempts++
	return t.base.RoundTrip(r)
}

func TestTransportWrapperObservesEveryRetry(t *testing.T) {
	attempts, requests := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()
	client, err := New("test", WithBaseURL(server.URL), WithRetry(1, time.Millisecond), WithTransportWrapper(func(base http.RoundTripper) http.RoundTripper { return countingTransport{base, &attempts} }))
	require.NoError(t, err)
	defer client.Close()
	_, err = client.GetWalletPositions(context.Background(), "0xabc", WalletPositionsRequest{})
	require.NoError(t, err)
	require.Equal(t, 2, requests)
	require.Equal(t, requests, attempts)
}
