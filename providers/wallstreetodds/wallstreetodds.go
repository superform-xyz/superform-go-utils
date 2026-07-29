// Package wallstreetodds implements a client for the WallStreetOdds stock API.
package wallstreetodds

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/superform-xyz/superform-go-utils/pkg/http_client"
	"golang.org/x/time/rate"
)

const (
	defaultBaseURL       = "https://www.wallstreetoddsapi.com"
	technicalPricingPath = "/api/technicalstockpricing"
	defaultUserAgent     = "superform-go-utils/wallstreetodds"
	defaultTimeout       = 30 * time.Second
	defaultMaxRetries    = uint(0)
	defaultRetryDelay    = time.Second
	maxSymbolsPerRequest = 100
	maxResponseBody      = 4 << 20
	maxErrorResponse     = 1 << 10
)

type client struct {
	apiKey           string
	baseURL          string
	userAgent        string
	httpClient       *http_client.Client
	limiter          *rate.Limiter
	transportWrapper func(http.RoundTripper) http.RoundTripper
	timeout          *time.Duration
	maxRetries       *uint
	retryDelay       *time.Duration
}

var _ Client = (*client)(nil)

// Option customizes a WallStreetOdds client.
type Option func(*client)

// WithBaseURL configures the WallStreetOdds service URL.
func WithBaseURL(baseURL string) Option {
	return func(c *client) {
		baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
		if baseURL != "" {
			c.baseURL = baseURL
		}
	}
}

// WithHTTPClient injects an HTTP client.
func WithHTTPClient(httpClient *http.Client) Option {
	return func(c *client) {
		if httpClient != nil {
			c.httpClient = &http_client.Client{Client: httpClient}
		}
	}
}

// WithRateLimit limits requests per second. A non-positive value disables limiting.
func WithRateLimit(qps float64) Option {
	return func(c *client) {
		if qps <= 0 {
			c.limiter = nil
			return
		}
		burst := int(qps) + 1
		if burst < 1 {
			burst = 1
		}
		c.limiter = rate.NewLimiter(rate.Limit(qps), burst)
	}
}

// WithTimeout configures the default HTTP client timeout.
func WithTimeout(timeout time.Duration) Option {
	return func(c *client) {
		c.timeout = &timeout
	}
}

// WithRetry configures retries after the initial request.
func WithRetry(maxRetries uint, retryDelay time.Duration) Option {
	return func(c *client) {
		c.maxRetries = &maxRetries
		c.retryDelay = &retryDelay
	}
}

// WithTransportWrapper wraps the client's base HTTP transport.
func WithTransportWrapper(wrapper func(http.RoundTripper) http.RoundTripper) Option {
	return func(c *client) {
		c.transportWrapper = wrapper
	}
}

// WithUserAgent overrides the default User-Agent.
func WithUserAgent(userAgent string) Option {
	return func(c *client) {
		userAgent = strings.TrimSpace(userAgent)
		if userAgent != "" {
			c.userAgent = userAgent
		}
	}
}

// New creates a WallStreetOdds client.
func New(apiKey string, opts ...Option) (Client, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, errors.New("wallstreetodds api key is required")
	}

	c := &client{
		apiKey:    apiKey,
		baseURL:   defaultBaseURL,
		userAgent: defaultUserAgent,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}
	if c.httpClient == nil {
		timeout := defaultTimeout
		if c.timeout != nil {
			timeout = *c.timeout
		}
		maxRetries := defaultMaxRetries
		if c.maxRetries != nil {
			maxRetries = *c.maxRetries
		}
		retryDelay := defaultRetryDelay
		if c.retryDelay != nil {
			retryDelay = *c.retryDelay
		}
		builder := http_client.NewClientBuilder().
			SetTimeout(timeout).
			SetRetry(maxRetries, retryDelay)
		if c.transportWrapper != nil {
			builder = builder.SetTransportWrapper(c.transportWrapper)
		}
		c.httpClient = builder.BuildClient()
	}
	return c, nil
}

// GetAllTimeHighs returns each stock's highest split-adjusted price since 2010.
// WallStreetOdds supports multiple comma-separated symbols, so requests are
// chunked to keep URLs comfortably below its documented 2,000-character limit.
func (c *client) GetAllTimeHighs(ctx context.Context, symbols []string) ([]StockAllTimeHigh, error) {
	normalized, err := normalizeSymbols(symbols)
	if err != nil {
		return nil, err
	}
	if len(normalized) == 0 {
		return []StockAllTimeHigh{}, nil
	}

	out := make([]StockAllTimeHigh, 0, len(normalized))
	for start := 0; start < len(normalized); start += maxSymbolsPerRequest {
		end := min(start+maxSymbolsPerRequest, len(normalized))
		rows, err := c.getAllTimeHighChunk(ctx, normalized[start:end])
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
	}
	return out, nil
}

func (c *client) getAllTimeHighChunk(ctx context.Context, symbols []string) ([]StockAllTimeHigh, error) {
	values := url.Values{}
	values.Set("apikey", c.apiKey)
	values.Set("fields", "symbol,allTimeHigh")
	values.Set("format", "json")
	values.Set("symbols", strings.Join(symbols, ","))

	body, err := c.getBody(ctx, c.endpoint(technicalPricingPath, values))
	if err != nil {
		return nil, fmt.Errorf("wallstreetodds all-time highs: %w", err)
	}

	var envelope struct {
		Response []StockAllTimeHigh `json:"response"`
		Error    json.RawMessage    `json:"error"`
		Message  string             `json:"message"`
	}
	if err := decodeJSON(body, &envelope); err != nil {
		return nil, fmt.Errorf("wallstreetodds all-time highs: %w", err)
	}
	if message := embeddedErrorMessage(envelope.Error, envelope.Message); message != "" {
		return nil, &APIError{Message: c.redact(message)}
	}
	if envelope.Response == nil {
		return []StockAllTimeHigh{}, nil
	}
	for i := range envelope.Response {
		envelope.Response[i].Symbol = strings.ToUpper(strings.TrimSpace(envelope.Response[i].Symbol))
	}
	return envelope.Response, nil
}

// Close closes idle HTTP connections held by the client.
func (c *client) Close() error {
	if c.httpClient != nil {
		c.httpClient.CloseIdleConnections()
	}
	return nil
}

func (c *client) getBody(ctx context.Context, endpoint string) ([]byte, error) {
	resp, err := c.doGET(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorResponse))
		return nil, &HTTPError{StatusCode: resp.StatusCode, Body: c.redact(strings.TrimSpace(string(body)))}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	return body, nil
}

func (c *client) doGET(ctx context.Context, endpoint string) (*http.Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if c.limiter != nil {
		if err := c.limiter.Wait(ctx); err != nil {
			return nil, fmt.Errorf("rate limit wait: %w", err)
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", c.redactedError(err))
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", c.redactedError(err))
	}
	return resp, nil
}

func (c *client) redact(value string) string {
	if c == nil || c.apiKey == "" {
		return value
	}
	return strings.ReplaceAll(value, c.apiKey, "[REDACTED]")
}

func (c *client) redactedError(err error) error {
	if err == nil {
		return nil
	}
	return &secretRedactedError{
		err:     err,
		message: c.redact(err.Error()),
	}
}

type secretRedactedError struct {
	err     error
	message string
}

func (e *secretRedactedError) Error() string {
	return e.message
}

func (e *secretRedactedError) Unwrap() error {
	return e.err
}

func (c *client) endpoint(path string, values url.Values) string {
	return c.baseURL + path + "?" + values.Encode()
}

func normalizeSymbols(symbols []string) ([]string, error) {
	seen := make(map[string]struct{}, len(symbols))
	normalized := make([]string, 0, len(symbols))
	for _, symbol := range symbols {
		symbol = strings.ToUpper(strings.TrimSpace(symbol))
		if symbol == "" {
			continue
		}
		if len(symbol) > 32 || strings.ContainsAny(symbol, ",&=?#") {
			return nil, fmt.Errorf("invalid wallstreetodds symbol %q", symbol)
		}
		if _, ok := seen[symbol]; ok {
			continue
		}
		seen[symbol] = struct{}{}
		normalized = append(normalized, symbol)
	}
	sort.Strings(normalized)
	return normalized, nil
}

func decodeJSON(body []byte, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func embeddedErrorMessage(raw json.RawMessage, fallback string) string {
	fallback = strings.TrimSpace(fallback)
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fallback
	}

	var message string
	if err := json.Unmarshal(raw, &message); err == nil {
		if message = strings.TrimSpace(message); message != "" {
			return message
		}
	}

	var object struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &object); err == nil {
		if message = strings.TrimSpace(object.Message); message != "" {
			return message
		}
	}
	return fallback
}
