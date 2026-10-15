package http_client

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"
)

func TestSetHeaderSendsEveryHeader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "key", r.Header.Get("X-Api-Key"))
		assert.Equal(t, "Bearer token", r.Header.Get("Authorization"))
		assert.Equal(t, "persephone/1.0", r.Header.Get("User-Agent"))
		assert.Equal(t, "legacy", r.Header.Get("X-Legacy"))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClientBuilder().
		SetHeader("X-Api-Key", "key").
		SetHeader("Authorization", "Bearer token").
		SetUserAgent("persephone/1.0").
		SetAuth("X-Legacy", "legacy").
		BuildClient()

	resp, err := client.Get(server.URL)
	require.NoError(t, err)
	_ = resp.Body.Close()
}

func TestRetryAfter(t *testing.T) {
	t.Run("honours delay-seconds before retrying", func(t *testing.T) {
		var calls atomic.Int32
		var first time.Time
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if calls.Add(1) == 1 {
				first = time.Now()
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			assert.GreaterOrEqual(t, time.Since(first), 900*time.Millisecond)
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		client := NewClientBuilder().SetRetry(1, time.Millisecond).BuildClient()
		resp, err := client.Get(server.URL)
		require.NoError(t, err)
		_ = resp.Body.Close()
		assert.Equal(t, int32(2), calls.Load())
	})

	t.Run("stops retrying when the server asks for longer than the cap", func(t *testing.T) {
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.Header().Set("Retry-After", "120")
			w.WriteHeader(http.StatusTooManyRequests)
		}))
		defer server.Close()

		client := NewClientBuilder().SetRetry(3, time.Millisecond).BuildClient()
		_, err := client.Get(server.URL)
		require.ErrorIs(t, err, ErrRateLimited)
		assert.Equal(t, int32(1), calls.Load())
		d, ok := RetryAfter(err)
		require.True(t, ok)
		assert.Equal(t, 120*time.Second, d)
	})

	t.Run("parses HTTP-date", func(t *testing.T) {
		now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
		assert.Equal(t, 30*time.Second, parseRetryAfter(now.Add(30*time.Second).Format(http.TimeFormat), now))
		assert.Zero(t, parseRetryAfter(now.Add(-time.Minute).Format(http.TimeFormat), now))
		assert.Zero(t, parseRetryAfter("soon", now))
		assert.Zero(t, parseRetryAfter("-5", now))
	})
}

func TestAttemptTimeoutRetriesSlowAttempt(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			select {
			case <-r.Context().Done():
			case <-time.After(2 * time.Second):
			}
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	client := NewClientBuilder().
		SetAttemptTimeout(100*time.Millisecond).
		SetRetry(1, time.Millisecond).
		BuildClient()

	// The body must stay readable after RoundTrip returns, even though the attempt has a deadline.
	out, err := GetJSON[map[string]bool](context.Background(), client, server.URL)
	require.NoError(t, err)
	assert.True(t, out["ok"])
	assert.Equal(t, int32(2), calls.Load())
}

func TestCancelledParentIsNotRetried(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		<-r.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	client := NewClientBuilder().SetRetry(3, time.Millisecond).BuildClient()
	_, err := client.GetWithContext(ctx, server.URL)
	require.Error(t, err)
	assert.Equal(t, int32(1), calls.Load())
}

func TestNoRedirect(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	}))
	defer server.Close()

	client := NewClientBuilder().SetNoRedirect().SetRetry(3, time.Millisecond).BuildClient()
	_, err := client.Get(server.URL)
	status, _, ok := ResponseStatus(err)
	require.True(t, ok)
	assert.Equal(t, http.StatusFound, status)
	assert.Equal(t, int32(1), calls.Load())
}

func TestMaxResponseBytes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("a", 10)))
	}))
	defer server.Close()

	t.Run("exact limit reads to EOF", func(t *testing.T) {
		resp, err := NewClientBuilder().SetMaxResponseBytes(10).BuildClient().Get(server.URL)
		require.NoError(t, err)
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.Len(t, body, 10)
	})

	t.Run("larger body fails", func(t *testing.T) {
		resp, err := NewClientBuilder().SetMaxResponseBytes(9).BuildClient().Get(server.URL)
		require.NoError(t, err)
		defer resp.Body.Close()
		_, err = io.ReadAll(resp.Body)
		require.ErrorIs(t, err, ErrResponseTooLarge)
	})
}

func TestRateLimiterSpacesAttempts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClientBuilder().SetRateLimiter(rate.NewLimiter(rate.Every(100*time.Millisecond), 1)).BuildClient()
	start := time.Now()
	for range 3 {
		resp, err := client.Get(server.URL)
		require.NoError(t, err)
		_ = resp.Body.Close()
	}
	assert.GreaterOrEqual(t, time.Since(start), 190*time.Millisecond)
}

func TestJSONHelpers(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/echo":
			assert.Equal(t, ContentTypeJSON, r.Header.Get("Content-Type"))
			body, _ := io.ReadAll(r.Body)
			_, _ = w.Write(body)
		case "/garbage":
			_, _ = w.Write([]byte("not json"))
		case "/missing":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("nope"))
		default:
			_, _ = w.Write([]byte(`{"name":"get"}`))
		}
	}))
	defer server.Close()

	ctx := context.Background()
	got, err := GetJSON[payload](ctx, NewClientBuilder().BuildClient(), server.URL)
	require.NoError(t, err)
	assert.Equal(t, "get", got.Name)

	echoed, err := PostJSON[payload](ctx, NewClientBuilder().BuildClient(), server.URL+"/echo", payload{Name: "post"})
	require.NoError(t, err)
	assert.Equal(t, "post", echoed.Name)

	// A plain *http.Client still yields a status error for non-2xx responses.
	_, err = GetJSON[payload](ctx, http.DefaultClient, server.URL+"/missing")
	status, body, ok := ResponseStatus(err)
	require.True(t, ok)
	assert.Equal(t, http.StatusNotFound, status)
	assert.Equal(t, "nope", body)

	_, err = GetJSON[payload](ctx, http.DefaultClient, server.URL+"/garbage")
	require.ErrorContains(t, err, "decoding JSON response")
	_, _, isStatus := ResponseStatus(err)
	assert.False(t, isStatus)
}
