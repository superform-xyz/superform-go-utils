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
	"strconv"
	"strings"
	"time"

	"golang.org/x/time/rate"
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

	// defaultMaxRetryAfter is the longest Retry-After honoured before giving up
	defaultMaxRetryAfter = 30 * time.Second

	// maxErrorBodyBytes bounds how much of a non-2xx body is kept in the error
	maxErrorBodyBytes = 64 << 10
)

const (
	// ContentTypeJSON is the content type for application/json to be used in http requests
	ContentTypeJSON = "application/json"
)

// ClientBuilder is an interface that allows for building an http.Client with custom settings
type ClientBuilder interface {
	BuildClient() *Client
	// SetAuth sets a single auth header; a later call replaces the previous one.
	// Use SetHeader to send several headers.
	SetAuth(key, value string) ClientBuilder
	// SetHeader adds a header sent on every attempt; repeated keys replace the earlier value.
	SetHeader(key, value string) ClientBuilder
	SetUserAgent(userAgent string) ClientBuilder
	// SetTimeout bounds the whole request, including every retry.
	SetTimeout(timeout time.Duration) ClientBuilder
	// SetAttemptTimeout bounds each individual attempt, so one slow attempt can still be retried.
	SetAttemptTimeout(timeout time.Duration) ClientBuilder
	SetRetry(maxRetries uint, retryDelay time.Duration) ClientBuilder
	// SetMaxRetryAfter caps how long a Retry-After header may delay a retry. A longer
	// Retry-After stops retrying and surfaces the error; RetryAfter reports the value.
	SetMaxRetryAfter(max time.Duration) ClientBuilder
	// SetNoRedirect returns 3xx responses as non-retryable errors instead of following them.
	SetNoRedirect() ClientBuilder
	// SetMaxResponseBytes fails reads of successful bodies larger than max with ErrResponseTooLarge.
	SetMaxResponseBytes(max int64) ClientBuilder
	// SetRateLimiter waits on limiter before every attempt, retries included.
	SetRateLimiter(limiter *rate.Limiter) ClientBuilder
	SetTransportWrapper(wrapper func(http.RoundTripper) http.RoundTripper) ClientBuilder
}

// Client wraps http.Client with shared request helpers.
type Client struct {
	*http.Client
}

// ErrRateLimited and ErrUnauthorized classify the responses the transport
// treats specially. Both wrap a *statusError, so ResponseStatus still reports
// the status code and body alongside them.
var (
	ErrRateLimited  = errors.New("request failed, rate limit exceeded")
	ErrUnauthorized = errors.New("request failed, unauthorized")
	// ErrResponseTooLarge is returned while reading a body that exceeds SetMaxResponseBytes.
	ErrResponseTooLarge = errors.New("response body exceeds size limit")
)

type statusError struct {
	statusCode int
	body       string
	retryAfter time.Duration
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

// RetryAfter returns the server-provided Retry-After delay of a failed response, if any.
func RetryAfter(err error) (time.Duration, bool) {
	var statusErr *statusError
	if errors.As(err, &statusErr) && statusErr.retryAfter > 0 {
		return statusErr.retryAfter, true
	}
	return 0, false
}

type clientBuilder struct {
	authKey          string
	authValue        string
	headers          http.Header
	timeout          *time.Duration
	attemptTimeout   time.Duration
	maxRetries       *uint
	retryDelay       *time.Duration
	maxRetryAfter    *time.Duration
	noRedirect       bool
	maxResponseBytes int64
	limiter          *rate.Limiter
	wrapTransport    func(http.RoundTripper) http.RoundTripper
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

func (b *clientBuilder) SetHeader(key, value string) ClientBuilder {
	if b.headers == nil {
		b.headers = make(http.Header)
	}
	b.headers.Set(key, value)
	return b
}

func (b *clientBuilder) SetUserAgent(userAgent string) ClientBuilder {
	return b.SetHeader("User-Agent", userAgent)
}

func (b *clientBuilder) SetTimeout(timeout time.Duration) ClientBuilder {
	b.timeout = &timeout
	return b
}

func (b *clientBuilder) SetAttemptTimeout(timeout time.Duration) ClientBuilder {
	b.attemptTimeout = timeout
	return b
}

// SetRetry sets the retry parameters
func (b *clientBuilder) SetRetry(maxRetries uint, retryDelay time.Duration) ClientBuilder {
	b.maxRetries = &maxRetries
	b.retryDelay = &retryDelay
	return b
}

func (b *clientBuilder) SetMaxRetryAfter(max time.Duration) ClientBuilder {
	b.maxRetryAfter = &max
	return b
}

func (b *clientBuilder) SetNoRedirect() ClientBuilder {
	b.noRedirect = true
	return b
}

func (b *clientBuilder) SetMaxResponseBytes(max int64) ClientBuilder {
	b.maxResponseBytes = max
	return b
}

func (b *clientBuilder) SetRateLimiter(limiter *rate.Limiter) ClientBuilder {
	b.limiter = limiter
	return b
}

// SetTransportWrapper wraps the base transport used by the retry transport.
func (b *clientBuilder) SetTransportWrapper(wrapper func(http.RoundTripper) http.RoundTripper) ClientBuilder {
	b.wrapTransport = wrapper
	return b
}

func (b *clientBuilder) BuildClient() *Client {
	var (
		clientRetryDelay    = retryTimeout
		clientMaxRetries    = maxRetries
		clientTimeout       = defaultClientTimeout
		clientMaxRetryAfter = defaultMaxRetryAfter
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

	if b.maxRetryAfter != nil {
		clientMaxRetryAfter = *b.maxRetryAfter
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

	client := &http.Client{
		Timeout: clientTimeout,
		Transport: &Transport{
			BaseTransport:    baseTransport,
			MaxRetries:       clientMaxRetries,
			RetryDelay:       clientRetryDelay,
			MaxRetryAfter:    clientMaxRetryAfter,
			AttemptTimeout:   b.attemptTimeout,
			AuthHeaderKey:    b.authKey,
			AuthHeaderVal:    b.authValue,
			Headers:          b.headers.Clone(),
			NoRedirect:       b.noRedirect,
			MaxResponseBytes: b.maxResponseBytes,
			RateLimiter:      b.limiter,
		},
	}
	if b.noRedirect {
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}

	return &Client{Client: client}
}

// Transport wraps a http.RoundTripper and adds retry logic/ any other custom params needed by the client
type Transport struct {
	BaseTransport http.RoundTripper // Underlying transport (usually http.DefaultTransport)

	// Retry logic
	MaxRetries     uint          // Maximum number of attempts, including the first
	RetryDelay     time.Duration // Delay before the first retry, doubled after each attempt
	MaxRetryAfter  time.Duration // Longest honoured Retry-After; zero ignores Retry-After
	AttemptTimeout time.Duration // Per-attempt timeout; zero means only the request context applies

	// Auth logic
	AuthHeaderKey string // Auth header key
	AuthHeaderVal string // Auth header value

	Headers          http.Header   // Extra headers set on every attempt
	NoRedirect       bool          // Return 3xx as non-retryable errors
	MaxResponseBytes int64         // Fail successful body reads past this size; zero disables
	RateLimiter      *rate.Limiter // Waited on before every attempt
}

// RoundTrip implements the http.RoundTripper interface, it works as the transport for the client
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.BaseTransport == nil {
		t.BaseTransport = http.DefaultTransport
	}

	ctx := req.Context()
	attempts := max(t.MaxRetries, 1)
	wait := t.RetryDelay

	var lastErr error
	for i := uint(0); i < attempts; i++ {
		resp, err, retry, retryAfter := t.attempt(req)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if !retry || i == attempts-1 {
			break
		}

		delay := wait
		if retryAfter > 0 && t.MaxRetryAfter > 0 {
			if retryAfter > t.MaxRetryAfter {
				break
			}
			delay = max(delay, retryAfter)
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("%w: %w", ctx.Err(), lastErr)
		case <-time.After(delay):
		}
		wait *= 2
	}

	return nil, lastErr
}

// attempt performs one try and reports whether a failure is worth retrying.
func (t *Transport) attempt(req *http.Request) (resp *http.Response, err error, retry bool, retryAfter time.Duration) {
	ctx := req.Context()
	if t.RateLimiter != nil {
		if err := t.RateLimiter.Wait(ctx); err != nil {
			return nil, fmt.Errorf("waiting for rate limiter: %w", err), false, 0
		}
	}

	cancel := context.CancelFunc(func() {})
	if t.AttemptTimeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, t.AttemptTimeout)
	}

	retryReq := req.Clone(ctx)
	for key, values := range t.Headers {
		retryReq.Header[key] = append([]string(nil), values...)
	}

	// Add auth if its set
	if t.AuthHeaderKey != "" && t.AuthHeaderVal != "" {
		retryReq.Header.Set(t.AuthHeaderKey, t.AuthHeaderVal)
	}

	attemptResp, rtErr := t.BaseTransport.RoundTrip(retryReq)
	if rtErr != nil {
		cancel()
		// Only the attempt deadline makes a context error retryable; a cancelled parent is final.
		return nil, fmt.Errorf("request failed: %w", rtErr), req.Context().Err() == nil, 0
	}

	if attemptResp.StatusCode >= http.StatusOK && attemptResp.StatusCode < http.StatusMultipleChoices {
		// Only a response handed back to the caller keeps an open body; the
		// caller owns closing it from here, which also releases the attempt context.
		attemptResp.Body = &responseBody{ReadCloser: attemptResp.Body, cancel: cancel, remaining: t.MaxResponseBytes, limited: t.MaxResponseBytes > 0}
		return attemptResp, nil, false, 0
	}

	// Every non-2xx path consumes the body so the connection goes back to the
	// idle pool instead of being dropped mid-response on the next attempt.
	statusErr := newStatusError(attemptResp)
	cancel()

	switch {
	case attemptResp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("%w: %w", ErrRateLimited, statusErr), true, retryAfterOf(statusErr)
	case attemptResp.StatusCode == http.StatusUnauthorized, attemptResp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("%w: %w", ErrUnauthorized, statusErr), false, 0
	case attemptResp.StatusCode == http.StatusBadRequest:
		return nil, statusErr, false, 0
	case t.NoRedirect && attemptResp.StatusCode < http.StatusBadRequest:
		return nil, statusErr, false, 0
	default:
		return nil, statusErr, true, retryAfterOf(statusErr)
	}
}

func retryAfterOf(err error) time.Duration {
	d, _ := RetryAfter(err)
	return d
}

// parseRetryAfter accepts both forms allowed by RFC 9110: delay-seconds and HTTP-date.
func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds <= 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil {
		if d := at.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}

// responseBody releases the attempt context on Close and enforces the size limit.
type responseBody struct {
	io.ReadCloser
	cancel    context.CancelFunc
	remaining int64
	limited   bool
}

func (b *responseBody) Read(p []byte) (int, error) {
	if !b.limited {
		return b.ReadCloser.Read(p)
	}
	if b.remaining <= 0 {
		// Probe one byte so a body of exactly the limit still reads to EOF.
		var probe [1]byte
		n, err := b.ReadCloser.Read(probe[:])
		if n > 0 {
			return 0, ErrResponseTooLarge
		}
		return 0, err
	}
	if int64(len(p)) > b.remaining {
		p = p[:b.remaining]
	}
	n, err := b.ReadCloser.Read(p)
	b.remaining -= int64(n)
	return n, err
}

func (b *responseBody) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}

func newStatusError(resp *http.Response) error {
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
	// Drain what the limit left behind so the connection can be reused.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBodyBytes))
	if closeErr := resp.Body.Close(); closeErr != nil && err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("request failed with status %d, error: failed to read response body, error: %s", resp.StatusCode, err)
	}

	return &statusError{
		statusCode: resp.StatusCode,
		body:       string(body),
		retryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), time.Now()),
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
