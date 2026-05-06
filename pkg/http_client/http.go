package http_client

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/superform-xyz/superform-go-utils/pkg/backoff"
)

const (
	// dialContextTimeout is the timeout to establish a connection
	dialContextTimeout = 30 * time.Second

	// dialContextKeepAlive is the keep-alive time for the connection
	dialContextKeepAlive = 30 * time.Second

	// tlsHandshakeTimeout is the time for TLS handshake
	tlsHandshakeTimeout = 30 * time.Second

	// expectContinueTimeout is the time to wait for an Expect-Continue response
	expectedContinueTimeout = 5 * time.Second

	// idleConnTimeout is the time to keep idle connections
	idleConnTimeout = 90 * time.Second

	// maxIdleConns is the maximum number of idle connections
	maxIdleConns = 100

	// maxIdleConnsPerHost is the maximum number of idle connections per host
	maxIdleConnsPerHost = 10

	// defaultClientTimeout is the overall request timeout
	defaultClientTimeout = 60 * time.Second

	// maxRetries is the maximum number of retries
	maxRetries = uint(3)

	retryTimeout = 2 * time.Second
)

const (
	// ContentTypeJSON is the content type for application/json to be used in http requests
	ContentTypeJSON = "application/json"
)

// ClientBuilder is an interface that allows for building an http.Client with custom settings
type ClientBuilder interface {
	Build() *http.Client
	SetAuth(key, value string) ClientBuilder
	SetTimeout(timeout time.Duration) ClientBuilder
	SetRetry(maxRetries uint, retryDelay time.Duration) ClientBuilder
}

type clientBuilder struct {
	authKey    string
	authValue  string
	timeout    *time.Duration
	maxRetries *uint
	retryDelay *time.Duration
}

var _ ClientBuilder = (*clientBuilder)(nil)

// NewClientBuilder creates a new ClientBuilder instance with default values
func NewClientBuilder() ClientBuilder {
	return &clientBuilder{}
}

// SetAuth sets the authentication header key and value
func (b *clientBuilder) SetAuth(key, value string) ClientBuilder {
	b.authKey = key
	b.authValue = value
	return b
}

func (b *clientBuilder) SetTimeout(timeout time.Duration) ClientBuilder {
	b.timeout = &timeout
	return b
}

// SetRetry sets the retry parameters
func (b *clientBuilder) SetRetry(maxRetries uint, retryDelay time.Duration) ClientBuilder {
	b.maxRetries = &maxRetries
	b.retryDelay = &retryDelay
	return b
}

func (b *clientBuilder) Build() *http.Client {
	var (
		clientRetryDelay = retryTimeout
		clientMaxRetries = maxRetries
		clientTimeout    = defaultClientTimeout
	)

	if b.retryDelay != nil {
		clientRetryDelay = *b.retryDelay
	}

	if b.maxRetries != nil {
		clientMaxRetries = *b.maxRetries + 1 // +1 because the first request is not retried
	}

	if b.timeout != nil {
		clientTimeout = *b.timeout
	}

	return &http.Client{
		Timeout: clientTimeout,
		Transport: &Transport{
			BaseTransport: &http.Transport{
				DialContext: (&net.Dialer{
					Timeout:   dialContextTimeout,
					KeepAlive: dialContextKeepAlive,
				}).DialContext,
				TLSHandshakeTimeout:   tlsHandshakeTimeout,
				ExpectContinueTimeout: expectedContinueTimeout,
				IdleConnTimeout:       idleConnTimeout,
				MaxIdleConns:          maxIdleConns,
				MaxIdleConnsPerHost:   maxIdleConnsPerHost,
			},
			MaxRetries:    clientMaxRetries,
			RetryDelay:    clientRetryDelay,
			AuthHeaderKey: b.authKey,
			AuthHeaderVal: b.authValue,
		},
	}
}

// Transport wraps a http.RoundTripper and adds retry logic/ any other custom params needed by the client
type Transport struct {
	BaseTransport http.RoundTripper // Underlying transport (usually http.DefaultTransport)

	// Retry logic
	MaxRetries uint          // Maximum number of retries
	RetryDelay time.Duration // Delay between retries

	// Auth logic
	AuthHeaderKey string // Auth header key
	AuthHeaderVal string // Auth header value
}

// RoundTrip implements the http.RoundTripper interface, it works as the transport for the client
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.BaseTransport == nil {
		t.BaseTransport = http.DefaultTransport
	}

	var resp *http.Response
	err := backoff.ExponentialWithContext(req.Context(), func() (err error, retry bool) {
		req := req.Clone(req.Context())

		// Add auth if its set
		if t.AuthHeaderKey != "" && t.AuthHeaderVal != "" {
			req.Header.Set(t.AuthHeaderKey, t.AuthHeaderVal)
		}

		if resp, err = t.BaseTransport.RoundTrip(req); err != nil {
			return fmt.Errorf("request failed: %w", err), true
		}

		if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
			return nil, false
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			return fmt.Errorf("request failed, rate limit exceeded"), true
		}

		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return fmt.Errorf("request failed, unauthorized"), false
		}

		if resp.StatusCode == http.StatusBadRequest {
			return fmt.Errorf("request failed with status %d, error: %s", resp.StatusCode, getResponseBodyError(resp)), false
		}

		if resp.StatusCode >= http.StatusInternalServerError {
			return fmt.Errorf("request failed with status %d, error: %s", resp.StatusCode, getResponseBodyError(resp)), true
		}

		return fmt.Errorf("request failed with status %d, error: %s", resp.StatusCode, getResponseBodyError(resp)), true
	}, t.MaxRetries, t.RetryDelay)
	if err != nil {
		return nil, err
	}

	return resp, nil
}

func getResponseBodyError(resp *http.Response) string {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Sprintf("failed to read response body, error: %s", err)
	}

	if len(body) == 0 {
		return "request failed, no response"
	}

	return string(body)
}
