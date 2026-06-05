package http_client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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
	defer resp.Body.Close()

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
	defer resp.Body.Close()

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
		defer resp.Body.Close()

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
	defer resp.Body.Close()

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
		resp.Body.Close()
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
		resp.Body.Close()
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
		resp.Body.Close()
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
		resp.Body.Close()
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
		resp.Body.Close()
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

func TestGetResponseBodyError(t *testing.T) {
	t.Run("with body", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("error details"))
		}))
		defer server.Close()

		resp, err := http.Get(server.URL)
		require.NoError(t, err)
		defer resp.Body.Close()

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
		defer resp.Body.Close()

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
