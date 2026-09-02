// Package frankfurter implements a client for the Frankfurter v2 exchange-rate API.
package frankfurter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/superform-xyz/superform-go-utils/pkg/http_client"
	"golang.org/x/time/rate"
)

const (
	defaultBaseURL       = "https://api.frankfurter.dev"
	defaultUserAgent     = "superform-go-utils/frankfurter"
	defaultTimeout       = 15 * time.Second
	defaultMaxRetries    = uint(2)
	defaultRetryDelay    = 500 * time.Millisecond
	defaultQPS           = 1.0
	defaultMaxStaleness  = 72 * time.Hour
	defaultFailureTTL    = 5 * time.Minute
	defaultMaxCachedDays = 450
	maxResponseBody      = 4 << 20
	maxErrorResponse     = 1 << 10
	ratesPath            = "/v2/rates"
)

type client struct {
	baseURL          string
	userAgent        string
	httpClient       *http_client.Client
	limiter          *rate.Limiter
	transportWrapper func(http.RoundTripper) http.RoundTripper
	timeout          *time.Duration
	maxRetries       *uint
	retryDelay       *time.Duration
	now              func() time.Time
	maxStaleness     time.Duration
	failureTTL       time.Duration
	maxCacheDays     int

	cacheMu         sync.RWMutex
	ratesByDate     map[string]map[string]Rate
	cacheStoredAt   map[string]uint64
	cacheSequence   uint64
	failuresByMonth map[string]cachedFailure
	fetchGate       chan struct{}
}

var _ Client = (*client)(nil)

type Option func(*client)

type rateRow struct {
	Date  string  `json:"date"`
	Base  string  `json:"base"`
	Quote string  `json:"quote"`
	Rate  float64 `json:"rate"`
}

type cachedFailure struct {
	err        error
	retryAfter time.Time
}

// WithBaseURL overrides the Frankfurter API base URL.
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
		c.limiter = rate.NewLimiter(rate.Limit(qps), 1)
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

// WithUserAgent overrides the default HTTP User-Agent.
func WithUserAgent(userAgent string) Option {
	return func(c *client) {
		if userAgent = strings.TrimSpace(userAgent); userAgent != "" {
			c.userAgent = userAgent
		}
	}
}

// WithMaxStaleness sets how long a cached earlier rate may be returned when a
// current rate cannot be loaded. A zero duration disables stale fallback.
func WithMaxStaleness(maxStaleness time.Duration) Option {
	return func(c *client) {
		c.maxStaleness = maxStaleness
	}
}

// WithFailureTTL sets how long a failed monthly request suppresses identical
// retries. A zero duration disables failure caching.
func WithFailureTTL(ttl time.Duration) Option {
	return func(c *client) {
		c.failureTTL = ttl
	}
}

func withClock(now func() time.Time) Option {
	return func(c *client) {
		c.now = now
	}
}

// New creates a Frankfurter client. Calendar-month responses are cached in
// process so callers share one request across currencies and dates.
func New(opts ...Option) (Client, error) {
	c := &client{
		baseURL:         defaultBaseURL,
		userAgent:       defaultUserAgent,
		limiter:         rate.NewLimiter(rate.Limit(defaultQPS), 1),
		now:             time.Now,
		maxStaleness:    defaultMaxStaleness,
		failureTTL:      defaultFailureTTL,
		maxCacheDays:    defaultMaxCachedDays,
		ratesByDate:     make(map[string]map[string]Rate),
		cacheStoredAt:   make(map[string]uint64),
		failuresByMonth: make(map[string]cachedFailure),
		fetchGate:       make(chan struct{}, 1),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}
	if c.now == nil {
		c.now = time.Now
	}
	if c.maxStaleness < 0 {
		c.maxStaleness = 0
	}
	if c.failureTTL < 0 {
		c.failureTTL = 0
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

// GetRate returns one base-to-quote rate for the effective UTC calendar date.
func (c *client) GetRate(ctx context.Context, req RateRequest) (*Rate, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	base := normalizeCurrency(req.Base)
	quote := normalizeCurrency(req.Quote)
	if base == "" || quote == "" {
		return nil, fmt.Errorf("%w: base and quote currencies are required", ErrRateUnavailable)
	}

	effectiveDate := c.effectiveDate(req.EffectiveAt)
	if base == quote {
		return &Rate{Date: effectiveDate, Base: base, Quote: quote, Value: 1}, nil
	}
	if cached, ok := c.cachedRate(base, effectiveDate, quote); ok {
		return &cached, nil
	}
	if failure, ok := c.cachedFailure(base, effectiveDate); ok {
		return c.staleRateOrError(base, quote, effectiveDate, failure)
	}

	select {
	case c.fetchGate <- struct{}{}:
		defer func() { <-c.fetchGate }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	if cached, ok := c.cachedRate(base, effectiveDate, quote); ok {
		return &cached, nil
	}
	if failure, ok := c.cachedFailure(base, effectiveDate); ok {
		return c.staleRateOrError(base, quote, effectiveDate, failure)
	}
	if err := c.fetchMonth(ctx, base, effectiveDate); err != nil {
		if ctx.Err() == nil {
			c.storeFailure(base, effectiveDate, err)
		}
		return c.staleRateOrError(base, quote, effectiveDate, err)
	}
	if cached, ok := c.cachedRate(base, effectiveDate, quote); ok {
		return &cached, nil
	}
	if cached, ok := c.cachedStaleRate(base, effectiveDate, quote); ok {
		c.storeFallback(base, effectiveDate, cached)
		return &cached, nil
	}
	return nil, fmt.Errorf("%w: response has no %s/%s rate for %s", ErrRateUnavailable, base, quote, effectiveDate.Format(time.DateOnly))
}

func (c *client) staleRateOrError(base string, quote string, effectiveDate time.Time, cause error) (*Rate, error) {
	if cached, ok := c.cachedStaleRate(base, effectiveDate, quote); ok {
		c.storeFallback(base, effectiveDate, cached)
		return &cached, nil
	}
	return nil, fmt.Errorf("%w: %s/%s for %s: %w", ErrRateUnavailable, base, quote, effectiveDate.Format(time.DateOnly), cause)
}

// Close closes idle HTTP connections held by the client.
func (c *client) Close() error {
	if c.httpClient != nil {
		c.httpClient.CloseIdleConnections()
	}
	return nil
}

func (c *client) effectiveDate(effectiveAt time.Time) time.Time {
	today := dateUTC(c.now())
	if effectiveAt.IsZero() {
		return today
	}
	effectiveDate := dateUTC(effectiveAt)
	if effectiveDate.After(today) {
		return today
	}
	return effectiveDate
}

func (c *client) fetchMonth(ctx context.Context, base string, effectiveDate time.Time) error {
	monthStart := time.Date(effectiveDate.Year(), effectiveDate.Month(), 1, 0, 0, 0, 0, time.UTC)
	monthEnd := monthStart.AddDate(0, 1, -1)
	if today := dateUTC(c.now()); monthEnd.After(today) {
		monthEnd = today
	}

	query := url.Values{}
	query.Set("base", base)
	query.Set("from", monthStart.Format(time.DateOnly))
	query.Set("to", monthEnd.Format(time.DateOnly))
	body, err := c.getBody(ctx, c.baseURL+ratesPath+"?"+query.Encode())
	if err != nil {
		return err
	}
	rates, err := decodeRates(body, base, monthStart, monthEnd)
	if err != nil {
		return err
	}
	c.storeRates(rates)
	c.clearFailure(base, effectiveDate)
	return nil
}

func (c *client) getBody(ctx context.Context, endpoint string) ([]byte, error) {
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
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorResponse))
		return nil, &HTTPError{StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(body))}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody+1))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if len(body) > maxResponseBody {
		return nil, fmt.Errorf("response exceeds %d bytes", maxResponseBody)
	}
	return body, nil
}

func decodeRates(body []byte, expectedBase string, from time.Time, to time.Time) (map[string]map[string]Rate, error) {
	var rows []rateRow
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if len(rows) == 0 {
		return nil, errors.New("response contains no rates")
	}

	ratesByDate := make(map[string]map[string]Rate)
	for _, row := range rows {
		rowDate, err := time.Parse(time.DateOnly, strings.TrimSpace(row.Date))
		if err != nil {
			return nil, fmt.Errorf("parse rate date %q: %w", row.Date, err)
		}
		if rowDate.Before(from) || rowDate.After(to) {
			return nil, fmt.Errorf("response date %s is outside requested range %s to %s", row.Date, from.Format(time.DateOnly), to.Format(time.DateOnly))
		}
		base := normalizeCurrency(row.Base)
		if base != expectedBase {
			return nil, fmt.Errorf("response has unexpected base currency %q", row.Base)
		}
		quote := normalizeCurrency(row.Quote)
		if quote == "" {
			return nil, errors.New("response has an empty quote currency")
		}
		if !positiveFinite(row.Rate) {
			return nil, fmt.Errorf("response has invalid %s rate for %s", quote, row.Date)
		}

		dateKey := cacheDateKey(base, rowDate)
		dailyRates := ratesByDate[dateKey]
		if dailyRates == nil {
			dailyRates = make(map[string]Rate)
			ratesByDate[dateKey] = dailyRates
		}
		if _, exists := dailyRates[quote]; exists {
			return nil, fmt.Errorf("response has duplicate %s rate for %s", quote, row.Date)
		}
		dailyRates[quote] = Rate{Date: rowDate, Base: base, Quote: quote, Value: row.Rate}
	}
	return ratesByDate, nil
}

func (c *client) cachedRate(base string, effectiveDate time.Time, quote string) (Rate, bool) {
	c.cacheMu.RLock()
	defer c.cacheMu.RUnlock()
	rate, ok := c.ratesByDate[cacheDateKey(base, effectiveDate)][quote]
	return rate, ok
}

func (c *client) cachedStaleRate(base string, effectiveDate time.Time, quote string) (Rate, bool) {
	if c.maxStaleness <= 0 {
		return Rate{}, false
	}

	c.cacheMu.RLock()
	defer c.cacheMu.RUnlock()

	var selected Rate
	for key, dailyRates := range c.ratesByDate {
		keyBase, _, ok := parseCacheDateKey(key)
		if !ok || keyBase != base {
			continue
		}
		candidate, ok := dailyRates[quote]
		if !ok || !candidate.Date.Before(effectiveDate) || effectiveDate.Sub(candidate.Date) > c.maxStaleness {
			continue
		}
		if selected.Date.IsZero() || candidate.Date.After(selected.Date) {
			selected = candidate
		}
	}
	if selected.Date.IsZero() {
		return Rate{}, false
	}
	selected.Stale = true
	return selected, true
}

func (c *client) cachedFailure(base string, effectiveDate time.Time) (error, bool) {
	if c.failureTTL <= 0 {
		return nil, false
	}

	key := cacheMonthKey(base, effectiveDate)
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	failure, ok := c.failuresByMonth[key]
	if !ok {
		return nil, false
	}
	if !c.now().Before(failure.retryAfter) {
		delete(c.failuresByMonth, key)
		return nil, false
	}
	return failure.err, true
}

func (c *client) storeFailure(base string, effectiveDate time.Time, err error) {
	if c.failureTTL <= 0 {
		return
	}
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	now := c.now()
	for key, failure := range c.failuresByMonth {
		if !now.Before(failure.retryAfter) {
			delete(c.failuresByMonth, key)
		}
	}
	c.failuresByMonth[cacheMonthKey(base, effectiveDate)] = cachedFailure{
		err:        err,
		retryAfter: now.Add(c.failureTTL),
	}
}

func (c *client) clearFailure(base string, effectiveDate time.Time) {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	delete(c.failuresByMonth, cacheMonthKey(base, effectiveDate))
}

func (c *client) storeFallback(base string, effectiveDate time.Time, rate Rate) {
	rate.Stale = true
	c.storeRates(map[string]map[string]Rate{
		cacheDateKey(base, effectiveDate): {rate.Quote: rate},
	})
}

func (c *client) storeRates(ratesByDate map[string]map[string]Rate) {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	for dateKey, dailyRates := range ratesByDate {
		cachedRates := c.ratesByDate[dateKey]
		if cachedRates == nil {
			cachedRates = make(map[string]Rate, len(dailyRates))
			c.ratesByDate[dateKey] = cachedRates
		}
		for quote, rate := range dailyRates {
			cachedRates[quote] = rate
		}
		c.cacheSequence++
		c.cacheStoredAt[dateKey] = c.cacheSequence
	}
	for len(c.ratesByDate) > c.maxCacheDays {
		oldestKey := ""
		var oldestSequence uint64
		for dateKey, sequence := range c.cacheStoredAt {
			if oldestKey == "" || sequence < oldestSequence {
				oldestKey = dateKey
				oldestSequence = sequence
			}
		}
		delete(c.ratesByDate, oldestKey)
		delete(c.cacheStoredAt, oldestKey)
	}
}

func cacheDateKey(base string, date time.Time) string {
	return base + "|" + date.Format(time.DateOnly)
}

func cacheMonthKey(base string, date time.Time) string {
	return base + "|" + date.Format("2006-01")
}

func parseCacheDateKey(key string) (string, time.Time, bool) {
	base, dateValue, found := strings.Cut(key, "|")
	if !found {
		return "", time.Time{}, false
	}
	date, err := time.Parse(time.DateOnly, dateValue)
	if err != nil {
		return "", time.Time{}, false
	}
	return base, date, true
}

func normalizeCurrency(value string) string {
	return strings.ToUpper(strings.TrimSpace(value))
}

func positiveFinite(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func dateUTC(value time.Time) time.Time {
	value = value.UTC()
	return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, time.UTC)
}
