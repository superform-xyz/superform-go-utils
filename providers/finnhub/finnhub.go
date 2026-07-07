// Package finnhub implements a small client for Finnhub's public REST API.
package finnhub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/superform-xyz/superform-go-utils/pkg/http_client"
	"golang.org/x/time/rate"
)

const (
	defaultBaseURL    = "https://finnhub.io/api/v1"
	profilePath       = "/stock/profile2"
	basicMetricsPath  = "/stock/metric"
	authHeader        = "X-Finnhub-Token"
	defaultUserAgent  = "superform-go-utils/finnhub"
	defaultMetric     = "all"
	defaultTimeout    = 30 * time.Second
	defaultMaxRetries = uint(0)
	defaultRetryDelay = time.Second
	maxResponseBody   = 16 << 20
	maxErrorResponse  = 1 << 10
)

var peMetricPriority = []string{
	"peTTM",
	"peNormalizedAnnual",
	"peBasicExclExtraTTM",
	"peExclExtraTTM",
	"peInclExtraTTM",
	"peAnnual",
}

type Client interface {
	GetCompanyProfile(ctx context.Context, symbol string) (*CompanyProfile, error)
	GetCompanyBasicFinancials(ctx context.Context, req BasicFinancialsRequest) (*BasicFinancials, error)
	GetStockFundamentals(ctx context.Context, symbol string) (*StockFundamentals, error)
	Close() error
}

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

type Option func(*client)

func WithBaseURL(baseURL string) Option {
	return func(c *client) {
		baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
		if baseURL != "" {
			c.baseURL = baseURL
		}
	}
}

func WithHTTPClient(httpClient *http.Client) Option {
	return func(c *client) {
		if httpClient != nil {
			c.httpClient = &http_client.Client{Client: httpClient}
		}
	}
}

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

func WithTimeout(timeout time.Duration) Option {
	return func(c *client) {
		c.timeout = &timeout
	}
}

func WithRetry(maxRetries uint, retryDelay time.Duration) Option {
	return func(c *client) {
		c.maxRetries = &maxRetries
		c.retryDelay = &retryDelay
	}
}

func WithTransportWrapper(wrapper func(http.RoundTripper) http.RoundTripper) Option {
	return func(c *client) {
		c.transportWrapper = wrapper
	}
}

func WithUserAgent(userAgent string) Option {
	return func(c *client) {
		userAgent = strings.TrimSpace(userAgent)
		if userAgent != "" {
			c.userAgent = userAgent
		}
	}
}

func New(apiKey string, opts ...Option) (Client, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, errors.New("finnhub api key is required")
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

func (c *client) GetCompanyProfile(ctx context.Context, symbol string) (*CompanyProfile, error) {
	symbol, err := normalizeSymbol(symbol)
	if err != nil {
		return nil, err
	}

	values := url.Values{}
	values.Set("symbol", symbol)
	body, err := c.getBody(ctx, c.endpoint(profilePath, values))
	if err != nil {
		return nil, fmt.Errorf("finnhub company profile %s: %w", symbol, err)
	}

	var out CompanyProfile
	if err := decodeJSON(body, &out); err != nil {
		return nil, fmt.Errorf("finnhub company profile %s: %w", symbol, err)
	}
	return &out, nil
}

func (c *client) GetCompanyBasicFinancials(ctx context.Context, req BasicFinancialsRequest) (*BasicFinancials, error) {
	symbol, err := normalizeSymbol(req.Symbol)
	if err != nil {
		return nil, err
	}
	metric := normalizeMetric(req.Metric)

	values := url.Values{}
	values.Set("symbol", symbol)
	values.Set("metric", metric)
	body, err := c.getBody(ctx, c.endpoint(basicMetricsPath, values))
	if err != nil {
		return nil, fmt.Errorf("finnhub company basic financials %s: %w", symbol, err)
	}

	var out BasicFinancials
	if err := decodeJSON(body, &out); err != nil {
		return nil, fmt.Errorf("finnhub company basic financials %s: %w", symbol, err)
	}
	return &out, nil
}

func (c *client) GetStockFundamentals(ctx context.Context, symbol string) (*StockFundamentals, error) {
	symbol, err := normalizeSymbol(symbol)
	if err != nil {
		return nil, err
	}

	profile, err := c.GetCompanyProfile(ctx, symbol)
	if err != nil {
		return nil, err
	}
	basicFinancials, err := c.GetCompanyBasicFinancials(ctx, BasicFinancialsRequest{
		Symbol: symbol,
		Metric: defaultMetric,
	})
	if err != nil {
		return nil, err
	}

	out := &StockFundamentals{
		Symbol:               firstString(profile.Ticker, basicFinancials.Symbol, symbol),
		Name:                 profile.Name,
		Currency:             profile.Currency,
		Exchange:             profile.Exchange,
		FinnhubIndustry:      profile.FinnhubIndustry,
		MarketCapitalization: profile.MarketCapitalization,
		ShareOutstanding:     profile.ShareOutstanding,
		BasicFinancials:      *basicFinancials,
		CompanyProfile:       *profile,
	}
	if out.MarketCapitalization == nil {
		if marketCap, ok := basicFinancials.MetricValue("marketCapitalization"); ok {
			out.MarketCapitalization = &marketCap
		}
	}
	if peRatio, metricName, ok := firstMetricValue(*basicFinancials, peMetricPriority...); ok {
		out.PERatio = &peRatio
		out.PERatioMetric = metricName
	}
	return out, nil
}

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
		return nil, &HTTPError{StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(body))}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if err := embeddedAPIError(body); err != nil {
		return nil, err
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
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set(authHeader, c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	return resp, nil
}

func (c *client) endpoint(path string, values url.Values) string {
	endpoint := c.baseURL + path
	if len(values) > 0 {
		endpoint += "?" + values.Encode()
	}
	return endpoint
}

func normalizeSymbol(symbol string) (string, error) {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	if symbol == "" {
		return "", errors.New("finnhub symbol is required")
	}
	return symbol, nil
}

func normalizeMetric(metric string) string {
	metric = strings.TrimSpace(metric)
	if metric == "" {
		return defaultMetric
	}
	return metric
}

func decodeJSON(body []byte, out any) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func embeddedAPIError(body []byte) error {
	var out struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil
	}
	out.Error = strings.TrimSpace(out.Error)
	if out.Error == "" {
		return nil
	}
	return &APIError{Message: out.Error}
}

func firstMetricValue(financials BasicFinancials, names ...string) (float64, string, bool) {
	for _, name := range names {
		if value, ok := financials.MetricValue(name); ok {
			return value, name, true
		}
	}
	return 0, "", false
}

func firstString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

type HTTPError struct {
	StatusCode int
	Body       string
}

func (e *HTTPError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("status %d", e.StatusCode)
	}
	return fmt.Sprintf("status %d: %s", e.StatusCode, e.Body)
}

type APIError struct {
	Message string
}

func (e *APIError) Error() string {
	return e.Message
}
