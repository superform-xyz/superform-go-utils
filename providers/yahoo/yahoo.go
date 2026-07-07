// Package yahoo implements a small client for Yahoo Finance's public endpoints.
package yahoo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/superform-xyz/superform-go-utils/pkg/http_client"
	"golang.org/x/time/rate"
)

const (
	defaultBaseURL    = "https://query1.finance.yahoo.com"
	chartPath         = "/v8/finance/chart/%s"
	defaultUserAgent  = "superform-go-utils/yahoo"
	defaultTimeout    = 30 * time.Second
	defaultMaxRetries = uint(0)
	defaultRetryDelay = time.Second
	defaultRange      = "1mo"
	defaultInterval   = "1d"
	maxResponseBody   = 16 << 20
	maxErrorResponse  = 1 << 10
)

type Client interface {
	GetChart(ctx context.Context, req ChartRequest) (*ChartResponse, error)
	GetStockDetail(ctx context.Context, symbol string) (*StockDetail, error)
	GetPriceHistory(ctx context.Context, req PriceHistoryRequest) ([]PriceBar, error)
	Close() error
}

type client struct {
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

func New(opts ...Option) (Client, error) {
	c := &client{
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

func (c *client) GetChart(ctx context.Context, req ChartRequest) (*ChartResponse, error) {
	symbol, err := normalizeSymbol(req.Symbol)
	if err != nil {
		return nil, err
	}

	values := chartQueryValues(req)
	path := fmt.Sprintf(chartPath, url.PathEscape(symbol))
	body, err := c.getBody(ctx, c.endpoint(path, values))
	if err != nil {
		return nil, fmt.Errorf("yahoo chart %s: %w", symbol, err)
	}

	var out ChartResponse
	if err := decodeJSON(body, &out); err != nil {
		return nil, fmt.Errorf("yahoo chart %s: %w", symbol, err)
	}
	if out.Chart.Error != nil {
		return nil, &APIError{Endpoint: "chart", YahooError: *out.Chart.Error}
	}
	return &out, nil
}

func (c *client) GetStockDetail(ctx context.Context, symbol string) (*StockDetail, error) {
	normalizedSymbol, err := normalizeSymbol(symbol)
	if err != nil {
		return nil, err
	}

	chart, err := c.GetChart(ctx, ChartRequest{
		Symbol:   normalizedSymbol,
		Range:    "1d",
		Interval: "1d",
	})
	if err != nil {
		return nil, fmt.Errorf("yahoo stock detail %s: %w", normalizedSymbol, err)
	}
	if len(chart.Chart.Result) == 0 {
		return nil, fmt.Errorf("yahoo stock detail %s: empty chart result", normalizedSymbol)
	}

	return stockDetailFromChart(normalizedSymbol, chart.Chart.Result[0]), nil
}

func (c *client) GetPriceHistory(ctx context.Context, req PriceHistoryRequest) ([]PriceBar, error) {
	symbol, err := normalizeSymbol(req.Symbol)
	if err != nil {
		return nil, err
	}
	interval := normalizeInterval(req.Granularity)
	chart, err := c.GetChart(ctx, ChartRequest{
		Symbol:         symbol,
		Range:          req.Range,
		Interval:       interval,
		Start:          req.Start,
		End:            req.End,
		IncludePrePost: req.IncludePrePost,
		Events:         req.Events,
	})
	if err != nil {
		return nil, err
	}
	if len(chart.Chart.Result) == 0 {
		return []PriceBar{}, nil
	}
	return priceBarsFromChart(symbol, interval, chart.Chart.Result[0]), nil
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

func chartQueryValues(req ChartRequest) url.Values {
	values := url.Values{}
	if req.Range != "" {
		values.Set("range", strings.TrimSpace(req.Range))
	} else if !req.Start.IsZero() || !req.End.IsZero() {
		if !req.Start.IsZero() {
			values.Set("period1", strconv.FormatInt(req.Start.Unix(), 10))
		}
		if !req.End.IsZero() {
			values.Set("period2", strconv.FormatInt(req.End.Unix(), 10))
		}
	} else {
		values.Set("range", defaultRange)
	}
	values.Set("interval", normalizeInterval(req.Interval))
	if req.IncludePrePost {
		values.Set("includePrePost", "true")
	}
	if events := strings.TrimSpace(req.Events); events != "" {
		values.Set("events", events)
	}
	return values
}

func normalizeSymbol(symbol string) (string, error) {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	if symbol == "" {
		return "", errors.New("yahoo symbol is required")
	}
	return symbol, nil
}

func normalizeInterval(interval string) string {
	interval = strings.TrimSpace(interval)
	if interval == "" {
		return defaultInterval
	}
	return interval
}

func decodeJSON(body []byte, out any) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func stockDetailFromChart(symbol string, result ChartResult) *StockDetail {
	meta := result.Meta
	out := &StockDetail{
		Symbol:           firstString(meta.Symbol, symbol),
		ShortName:        meta.ShortName,
		LongName:         meta.LongName,
		Currency:         meta.Currency,
		Price:            meta.RegularMarketPrice,
		MarketVolume:     meta.RegularMarketVolume,
		FiftyTwoWeekHigh: meta.FiftyTwoWeekHigh,
		FiftyTwoWeekLow:  meta.FiftyTwoWeekLow,
	}
	if meta.RegularMarketTime > 0 {
		marketTime := time.Unix(meta.RegularMarketTime, 0).UTC()
		out.MarketTime = &marketTime
	}
	if len(result.Indicators.Quote) > 0 {
		if open, ok := floatAt(result.Indicators.Quote[0].Open, 0); ok {
			out.Open = &open
		}
	}
	return out
}

func priceBarsFromChart(symbol, interval string, result ChartResult) []PriceBar {
	if len(result.Indicators.Quote) == 0 {
		return []PriceBar{}
	}
	quote := result.Indicators.Quote[0]
	var adjClose []*float64
	if len(result.Indicators.AdjClose) > 0 {
		adjClose = result.Indicators.AdjClose[0].AdjClose
	}

	bars := make([]PriceBar, 0, len(result.Timestamp))
	for i, timestamp := range result.Timestamp {
		open, ok := floatAt(quote.Open, i)
		if !ok {
			continue
		}
		high, ok := floatAt(quote.High, i)
		if !ok {
			continue
		}
		low, ok := floatAt(quote.Low, i)
		if !ok {
			continue
		}
		closePrice, ok := floatAt(quote.Close, i)
		if !ok {
			continue
		}
		volume, _ := intAt(quote.Volume, i)
		bar := PriceBar{
			Symbol:    symbol,
			Interval:  interval,
			Timestamp: time.Unix(timestamp, 0).UTC(),
			Open:      open,
			High:      high,
			Low:       low,
			Close:     closePrice,
			Volume:    volume,
		}
		if adj, ok := floatAt(adjClose, i); ok {
			bar.AdjClose = &adj
		}
		bars = append(bars, bar)
	}
	return bars
}

func firstString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func floatAt(values []*float64, idx int) (float64, bool) {
	if idx < 0 || idx >= len(values) || values[idx] == nil {
		return 0, false
	}
	return *values[idx], true
}

func intAt(values []*int64, idx int) (int64, bool) {
	if idx < 0 || idx >= len(values) || values[idx] == nil {
		return 0, false
	}
	return *values[idx], true
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
	Endpoint string
	YahooError
}

func (e *APIError) Error() string {
	if e.Endpoint == "" {
		return e.YahooError.Error()
	}
	return e.Endpoint + ": " + e.YahooError.Error()
}
