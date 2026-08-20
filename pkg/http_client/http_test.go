package http_client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type errorReader struct{}

func (e *errorReader) Read(p []byte) (n int, err error) {
	return 0, errors.New("read error")
}

func (e *errorReader) Close() error {
	return nil
}

// trackedBody records whether a response body was read to EOF and closed.
type trackedBody struct {
	reader  *strings.Reader
	closed  int
	drained bool
}

func (b *trackedBody) Read(p []byte) (int, error) {
	n, err := b.reader.Read(p)
	if errors.Is(err, io.EOF) {
		b.drained = true
	}
	return n, err
}

func (b *trackedBody) Close() error {
	b.closed++
	return nil
}

// recordingTransport serves one status per attempt, repeating the last entry
// once the sequence is exhausted, and keeps every body it handed out. Attempts
// are sequential inside a single RoundTrip, so no locking is needed.
type recordingTransport struct {
	statuses []int
	body     string
	bodies   []*trackedBody
}

func (rt *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	status := rt.statuses[min(len(rt.bodies), len(rt.statuses)-1)]

	body := &trackedBody{reader: strings.NewReader(rt.body)}
	rt.bodies = append(rt.bodies, body)

	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Body:       body,
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

func TestNewClientBuilder(t *testing.T) {
	builder := NewClientBuilder()
	assert.NotNil(t, builder)
}

func TestClientGet(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClientBuilder().BuildClient()
	resp, err := client.Get(server.URL)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestClientGetWithContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClientBuilder().BuildClient()
	resp, err := client.GetWithContext(context.Background(), server.URL)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestClientPostJSONWithContext(t *testing.T) {
	t.Run("sends JSON POST request", func(t *testing.T) {
		payload := map[string]string{"key": "value"}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, ContentTypeJSON, r.Header.Get("Content-Type"))

			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)

			var got map[string]string
			require.NoError(t, json.Unmarshal(body, &got))
			assert.Equal(t, payload, got)
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		client := NewClientBuilder().BuildClient()
		resp, err := client.PostJSONWithContext(context.Background(), server.URL, payload)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("returns marshal error", func(t *testing.T) {
		client := NewClientBuilder().BuildClient()
		_, err := client.PostJSONWithContext(context.Background(), "https://example.com/path", make(chan struct{}))
		require.ErrorContains(t, err, "marshalling JSON request body")
	})
}

func TestClientPost(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "text/plain", r.Header.Get("Content-Type"))

		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		assert.Equal(t, "hello", string(body))
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	client := NewClientBuilder().BuildClient()
	resp, err := client.Post(server.URL, "text/plain", strings.NewReader("hello"))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusCreated, resp.StatusCode)
}

func TestClientBuilderSetAuth(t *testing.T) {
	builder := NewClientBuilder().SetAuth("X-Api-Key", "test-key")
	assert.NotNil(t, builder)
}

func TestClientBuilderSetTimeout(t *testing.T) {
	builder := NewClientBuilder().SetTimeout(10 * time.Second)
	assert.NotNil(t, builder)
}

func TestClientBuilderSetRetry(t *testing.T) {
	builder := NewClientBuilder().SetRetry(2, time.Second)
	assert.NotNil(t, builder)
}

func TestClientBuilderBuildClient(t *testing.T) {
	t.Run("default settings", func(t *testing.T) {
		client := NewClientBuilder().BuildClient()
		assert.NotNil(t, client)
		assert.Equal(t, defaultClientTimeout, client.Timeout)
	})

	t.Run("custom timeout", func(t *testing.T) {
		timeout := 5 * time.Second
		client := NewClientBuilder().SetTimeout(timeout).BuildClient()
		assert.Equal(t, timeout, client.Timeout)
	})

	t.Run("custom retry", func(t *testing.T) {
		client := NewClientBuilder().SetRetry(5, 500*time.Millisecond).BuildClient()
		assert.NotNil(t, client)
		transport := client.Transport.(*Transport)
		assert.Equal(t, uint(6), transport.MaxRetries)
		assert.Equal(t, 500*time.Millisecond, transport.RetryDelay)
	})

	t.Run("with auth", func(t *testing.T) {
		client := NewClientBuilder().SetAuth("Authorization", "Bearer token").BuildClient()
		transport := client.Transport.(*Transport)
		assert.Equal(t, "Authorization", transport.AuthHeaderKey)
		assert.Equal(t, "Bearer token", transport.AuthHeaderVal)
	})

	t.Run("with transport wrapper", func(t *testing.T) {
		wrapped := false
		client := NewClientBuilder().
			SetTransportWrapper(func(base http.RoundTripper) http.RoundTripper {
				require.NotNil(t, base)
				wrapped = true
				return base
			}).
			BuildClient()
		transport := client.Transport.(*Transport)
		require.NotNil(t, transport.BaseTransport)
		require.True(t, wrapped)
	})

	t.Run("all options", func(t *testing.T) {
		client := NewClientBuilder().
			SetAuth("X-Key", "val").
			SetTimeout(30*time.Second).
			SetRetry(2, time.Second).
			BuildClient()
		assert.NotNil(t, client)
		assert.Equal(t, 30*time.Second, client.Timeout)
	})

	t.Run("client wrapper", func(t *testing.T) {
		client := NewClientBuilder().SetTimeout(5 * time.Second).BuildClient()
		require.NotNil(t, client)
		require.NotNil(t, client.Client)
		assert.Equal(t, 5*time.Second, client.Timeout)
	})
}

func TestTransportRoundTrip(t *testing.T) {
	t.Run("successful 200 request", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		}))
		defer server.Close()

		transport := &Transport{
			BaseTransport: http.DefaultTransport,
			MaxRetries:    1,
			RetryDelay:    time.Millisecond,
		}

		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
		require.NoError(t, err)

		resp, err := transport.RoundTrip(req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		require.NoError(t, resp.Body.Close())
	})

	t.Run("successful 2xx request (201)", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusCreated)
		}))
		defer server.Close()

		transport := &Transport{
			BaseTransport: http.DefaultTransport,
			MaxRetries:    1,
			RetryDelay:    time.Millisecond,
		}

		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL, nil)
		require.NoError(t, err)

		resp, err := transport.RoundTrip(req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusCreated, resp.StatusCode)
		require.NoError(t, resp.Body.Close())
	})

	t.Run("auth headers set", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "test-value", r.Header.Get("X-Api-Key"))
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		transport := &Transport{
			BaseTransport: http.DefaultTransport,
			MaxRetries:    1,
			RetryDelay:    time.Millisecond,
			AuthHeaderKey: "X-Api-Key",
			AuthHeaderVal: "test-value",
		}

		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
		require.NoError(t, err)

		resp, err := transport.RoundTrip(req)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
	})

	t.Run("nil BaseTransport uses default", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		transport := &Transport{
			MaxRetries: 1,
			RetryDelay: time.Millisecond,
		}

		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
		require.NoError(t, err)

		resp, err := transport.RoundTrip(req)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
	})

	t.Run("rate limit 429 retries then fails", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
		}))
		defer server.Close()

		transport := &Transport{
			BaseTransport: http.DefaultTransport,
			MaxRetries:    2,
			RetryDelay:    time.Millisecond,
		}

		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
		require.NoError(t, err)

		_, err = transport.RoundTrip(req)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "rate limit exceeded")
	})

	t.Run("401 unauthorized no retry", func(t *testing.T) {
		callCount := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			callCount++
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer server.Close()

		transport := &Transport{
			BaseTransport: http.DefaultTransport,
			MaxRetries:    3,
			RetryDelay:    time.Millisecond,
		}

		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
		require.NoError(t, err)

		_, err = transport.RoundTrip(req)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "unauthorized")
		assert.Equal(t, 1, callCount)
	})

	t.Run("403 forbidden no retry", func(t *testing.T) {
		callCount := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			callCount++
			w.WriteHeader(http.StatusForbidden)
		}))
		defer server.Close()

		transport := &Transport{
			BaseTransport: http.DefaultTransport,
			MaxRetries:    3,
			RetryDelay:    time.Millisecond,
		}

		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
		require.NoError(t, err)

		_, err = transport.RoundTrip(req)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "unauthorized")
		assert.Equal(t, 1, callCount)
	})

	t.Run("400 bad request no retry", func(t *testing.T) {
		callCount := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			callCount++
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("bad request body"))
		}))
		defer server.Close()

		transport := &Transport{
			BaseTransport: http.DefaultTransport,
			MaxRetries:    3,
			RetryDelay:    time.Millisecond,
		}

		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
		require.NoError(t, err)

		_, err = transport.RoundTrip(req)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "bad request body")
		statusCode, body, ok := ResponseStatus(err)
		require.True(t, ok)
		assert.Equal(t, http.StatusBadRequest, statusCode)
		assert.Equal(t, "bad request body", body)
		assert.Equal(t, 1, callCount)
	})

	t.Run("500 server error retries", func(t *testing.T) {
		callCount := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			callCount++
			if callCount < 2 {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte("server error"))
				return
			}
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		transport := &Transport{
			BaseTransport: http.DefaultTransport,
			MaxRetries:    3,
			RetryDelay:    time.Millisecond,
		}

		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
		require.NoError(t, err)

		resp, err := transport.RoundTrip(req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		require.NoError(t, resp.Body.Close())
	})

	t.Run("other 4xx status retries", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusMethodNotAllowed)
			_, _ = w.Write([]byte("method not allowed"))
		}))
		defer server.Close()

		transport := &Transport{
			BaseTransport: http.DefaultTransport,
			MaxRetries:    2,
			RetryDelay:    time.Millisecond,
		}

		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
		require.NoError(t, err)

		_, err = transport.RoundTrip(req)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "method not allowed")
	})

	t.Run("transport error", func(t *testing.T) {
		transport := &Transport{
			BaseTransport: http.DefaultTransport,
			MaxRetries:    2,
			RetryDelay:    time.Millisecond,
		}

		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://localhost:1", nil)
		require.NoError(t, err)

		_, err = transport.RoundTrip(req)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "request failed")
	})

	t.Run("empty response body on error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
		}))
		defer server.Close()

		transport := &Transport{
			BaseTransport: http.DefaultTransport,
			MaxRetries:    1,
			RetryDelay:    time.Millisecond,
		}

		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
		require.NoError(t, err)

		_, err = transport.RoundTrip(req)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "no response")
	})
}

func TestTransportRoundTripBodyHandling(t *testing.T) {
	const errBody = `{"error":"nope"}`

	t.Run("non-2xx responses are drained and closed on every attempt", func(t *testing.T) {
		tests := []struct {
			name         string
			statusCode   int
			wantAttempts int
			wantSentinel error
		}{
			{"429 rate limited retries", http.StatusTooManyRequests, 3, ErrRateLimited},
			{"401 unauthorized no retry", http.StatusUnauthorized, 1, ErrUnauthorized},
			{"403 forbidden no retry", http.StatusForbidden, 1, ErrUnauthorized},
			{"400 bad request no retry", http.StatusBadRequest, 1, nil},
			{"500 server error retries", http.StatusInternalServerError, 3, nil},
			{"other 4xx retries", http.StatusMethodNotAllowed, 3, nil},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				base := &recordingTransport{statuses: []int{tt.statusCode}, body: errBody}
				transport := &Transport{
					BaseTransport: base,
					MaxRetries:    3,
					RetryDelay:    time.Millisecond,
				}

				req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.invalid", nil)
				require.NoError(t, err)

				resp, err := transport.RoundTrip(req)
				require.Error(t, err)
				assert.Nil(t, resp)

				require.Len(t, base.bodies, tt.wantAttempts)
				for i, body := range base.bodies {
					assert.True(t, body.drained, "attempt %d: body was not drained", i)
					assert.Equal(t, 1, body.closed, "attempt %d: body was not closed exactly once", i)
				}

				// Every non-2xx response carries its status through the error, so
				// callers can classify it without parsing the message.
				statusCode, gotBody, ok := ResponseStatus(err)
				require.True(t, ok)
				assert.Equal(t, tt.statusCode, statusCode)
				assert.Equal(t, errBody, gotBody)

				if tt.wantSentinel != nil {
					assert.ErrorIs(t, err, tt.wantSentinel)
				}
			})
		}
	})

	t.Run("body of a failed attempt is closed before the next attempt overwrites it", func(t *testing.T) {
		base := &recordingTransport{
			statuses: []int{http.StatusTooManyRequests, http.StatusOK},
			body:     errBody,
		}
		transport := &Transport{
			BaseTransport: base,
			MaxRetries:    3,
			RetryDelay:    time.Millisecond,
		}

		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.invalid", nil)
		require.NoError(t, err)

		resp, err := transport.RoundTrip(req)
		require.NoError(t, err)
		require.Len(t, base.bodies, 2)

		assert.True(t, base.bodies[0].drained)
		assert.Equal(t, 1, base.bodies[0].closed, "the 429 body leaked when the retry replaced it")

		// The successful response is the caller's to close.
		assert.Equal(t, 0, base.bodies[1].closed)
		require.NoError(t, resp.Body.Close())
		assert.Equal(t, 1, base.bodies[1].closed)
	})

	t.Run("429 leaves the connection reusable", func(t *testing.T) {
		var (
			mu    sync.Mutex
			conns = make(map[string]int)
		)

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			conns[r.RemoteAddr]++
			mu.Unlock()

			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(errBody))
		}))
		defer server.Close()

		base := &http.Transport{MaxIdleConns: maxIdleConns, MaxIdleConnsPerHost: maxIdleConnsPerHost}
		defer base.CloseIdleConnections()

		transport := &Transport{
			BaseTransport: base,
			MaxRetries:    3,
			// Comfortably longer than the transport's asynchronous hand-off of a
			// drained connection back to the idle pool.
			RetryDelay: 20 * time.Millisecond,
		}

		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
		require.NoError(t, err)

		_, err = transport.RoundTrip(req)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrRateLimited)
		assert.Contains(t, err.Error(), "rate limit exceeded")

		statusCode, gotBody, ok := ResponseStatus(err)
		require.True(t, ok)
		assert.Equal(t, http.StatusTooManyRequests, statusCode)
		assert.Equal(t, errBody, gotBody)

		mu.Lock()
		defer mu.Unlock()

		attempts := 0
		for _, count := range conns {
			attempts += count
		}
		assert.Equal(t, 3, attempts)
		assert.Len(t, conns, 1, "each retry opened a new connection instead of reusing one: %v", conns)
	})
}

func TestGetResponseBodyError(t *testing.T) {
	t.Run("with body", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("error details"))
		}))
		defer server.Close()

		resp, err := http.Get(server.URL)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		result := getResponseBodyError(resp)
		assert.Equal(t, "error details", result)
	})

	t.Run("empty body", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		resp, err := http.Get(server.URL)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		result := getResponseBodyError(resp)
		assert.Equal(t, "request failed, no response", result)
	})

	t.Run("read error", func(t *testing.T) {
		resp := &http.Response{
			Body: &errorReader{},
		}

		result := getResponseBodyError(resp)
		assert.Contains(t, result, "failed to read response body")
	})
}

func TestClientBuilderChaining(t *testing.T) {
	builder := NewClientBuilder()
	result := builder.SetAuth("key", "val").SetTimeout(10*time.Second).SetRetry(3, time.Second)
	assert.NotNil(t, result)

	client := result.BuildClient()
	assert.NotNil(t, client)

	transport := client.Transport.(*Transport)
	assert.Equal(t, "key", transport.AuthHeaderKey)
	assert.Equal(t, "val", transport.AuthHeaderVal)
	assert.Equal(t, uint(4), transport.MaxRetries)
	assert.Equal(t, time.Second, transport.RetryDelay)
	assert.Equal(t, 10*time.Second, client.Timeout)
}

func TestRoundTripContextCancelled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())

	transport := &Transport{
		BaseTransport: http.DefaultTransport,
		MaxRetries:    3,
		RetryDelay:    100 * time.Millisecond,
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	require.NoError(t, err)

	cancel()

	_, err = transport.RoundTrip(req)
	assert.Error(t, err)
}
