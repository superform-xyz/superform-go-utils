package http_client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	BuildClient() *Client
	SetAuth(key, value string) ClientBuilder
	SetTimeout(timeout time.Duration) ClientBuilder
	SetRetry(maxRetries uint, retryDelay time.Duration) ClientBuilder
	SetTransportWrapper(wrapper func(http.RoundTripper) http.RoundTripper) ClientBuilder
}

// Client wraps http.Client with shared request helpers.
type Client struct {
	*http.Client
}

type statusError struct {
	statusCode int
	body       string
}

func (e *statusError) Error() string {
	if e.body == "" {
		return fmt.Sprintf("request failed with status %d, error: request failed, no response", e.statusCode)
	}

	return fmt.Sprintf("request failed with status %d, error: %s", e.statusCode, e.body)
}

// ResponseStatus returns status details from errors produced for non-2xx responses.
func ResponseStatus(err error) (statusCode int, body string, ok bool) {
	var statusErr *statusError
	if errors.As(err, &statusErr) {
		return statusErr.statusCode, statusErr.body, true
	}
	return 0, "", false
}

type clientBuilder struct {
	authKey       string
	authValue     string
	timeout       *time.Duration
	maxRetries    *uint
	retryDelay    *time.Duration
	wrapTransport func(http.RoundTripper) http.RoundTripper
}

var _ ClientBuilder = (*clientBuilder)(nil)

// NewClientBuilder creates a new ClientBuilder instance with default values
func NewClientBuilder() ClientBuilder {
	return &clientBuilder{}
}

// Get sends a GET request.
func (c *Client) Get(url string) (*http.Response, error) {
	return c.GetWithContext(context.Background(), url)
}

// GetWithContext sends a GET request using the provided context.
func (c *Client) GetWithContext(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating GET request: %w", err)
	}

	return c.Do(req)
}

// Post sends a POST request.
func (c *Client) Post(url, contentType string, body io.Reader) (*http.Response, error) {
	return c.PostWithContext(context.Background(), url, contentType, body)
}

// PostWithContext sends a POST request using the provided context.
func (c *Client) PostWithContext(ctx context.Context, url, contentType string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	if err != nil {
		return nil, fmt.Errorf("creating POST request: %w", err)
	}
	req.Header.Set("Content-Type", contentType)

	return c.Do(req)
}

// PostJSON sends a POST request with a JSON encoded body.
func (c *Client) PostJSON(url string, payload any) (*http.Response, error) {
	return c.PostJSONWithContext(context.Background(), url, payload)
}

// PostJSONWithContext sends a POST request with a JSON encoded body using the provided context.
func (c *Client) PostJSONWithContext(ctx context.Context, url string, payload any) (*http.Response, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshalling JSON request body: %w", err)
	}

	return c.PostWithContext(ctx, url, ContentTypeJSON, bytes.NewReader(body))
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

// SetTransportWrapper wraps the base transport used by the retry transport.
func (b *clientBuilder) SetTransportWrapper(wrapper func(http.RoundTripper) http.RoundTripper) ClientBuilder {
	b.wrapTransport = wrapper
	return b
}

func (b *clientBuilder) BuildClient() *Client {
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

	baseTransport := http.RoundTripper(&http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   dialContextTimeout,
			KeepAlive: dialContextKeepAlive,
		}).DialContext,
		TLSHandshakeTimeout:   tlsHandshakeTimeout,
		ExpectContinueTimeout: expectedContinueTimeout,
		IdleConnTimeout:       idleConnTimeout,
		MaxIdleConns:          maxIdleConns,
		MaxIdleConnsPerHost:   maxIdleConnsPerHost,
	})
	if b.wrapTransport != nil {
		if wrapped := b.wrapTransport(baseTransport); wrapped != nil {
			baseTransport = wrapped
		}
	}

	return &Client{
		Client: &http.Client{
			Timeout: clientTimeout,
			Transport: &Transport{
				BaseTransport: baseTransport,
				MaxRetries:    clientMaxRetries,
				RetryDelay:    clientRetryDelay,
				AuthHeaderKey: b.authKey,
				AuthHeaderVal: b.authValue,
			},
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
		retryReq := req.Clone(req.Context())

		// Add auth if its set
		if t.AuthHeaderKey != "" && t.AuthHeaderVal != "" {
			retryReq.Header.Set(t.AuthHeaderKey, t.AuthHeaderVal)
		}

		if resp, err = t.BaseTransport.RoundTrip(retryReq); err != nil {
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
			return newStatusError(resp), false
		}

		if resp.StatusCode >= http.StatusInternalServerError {
			return newStatusError(resp), true
		}

		return newStatusError(resp), true
	}, t.MaxRetries, t.RetryDelay)
	if err != nil {
		return nil, err
	}

	return resp, nil
}

func newStatusError(resp *http.Response) error {
	body, err := io.ReadAll(resp.Body)
	if closeErr := resp.Body.Close(); closeErr != nil && err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("request failed with status %d, error: failed to read response body, error: %s", resp.StatusCode, err)
	}

	return &statusError{
		statusCode: resp.StatusCode,
		body:       string(body),
	}
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
