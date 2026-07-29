// Package sec implements a client for the SEC EDGAR JSON APIs.
package sec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/superform-xyz/superform-go-utils/pkg/http_client"
	"golang.org/x/time/rate"
)

const (
	defaultDataBaseURL    = "https://data.sec.gov"
	defaultWebsiteBaseURL = "https://www.sec.gov"
	companyTickersPath    = "/files/company_tickers.json"
	companyFactsPath      = "/api/xbrl/companyfacts/CIK%010d.json"
	defaultTimeout        = 30 * time.Second
	defaultMaxRetries     = uint(0)
	defaultRetryDelay     = time.Second
	defaultQPS            = 5.0
	maxResponseBody       = 64 << 20
	maxErrorResponse      = 1 << 10
)

var acceptedEquityForms = map[string]struct{}{
	"10-K":   {},
	"10-K/A": {},
	"10-Q":   {},
	"10-Q/A": {},
}

type client struct {
	userAgent        string
	dataBaseURL      string
	websiteBaseURL   string
	httpClient       *http_client.Client
	limiter          *rate.Limiter
	transportWrapper func(http.RoundTripper) http.RoundTripper
	timeout          *time.Duration
	maxRetries       *uint
	retryDelay       *time.Duration

	tickersMu sync.Mutex
	tickers   map[string]CompanyTicker
}

var _ Client = (*client)(nil)

// Option customizes an SEC client.
type Option func(*client)

// WithDataBaseURL configures the data.sec.gov base URL.
func WithDataBaseURL(baseURL string) Option {
	return func(c *client) {
		baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
		if baseURL != "" {
			c.dataBaseURL = baseURL
		}
	}
}

// WithWebsiteBaseURL configures the www.sec.gov base URL used for ticker metadata.
func WithWebsiteBaseURL(baseURL string) Option {
	return func(c *client) {
		baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
		if baseURL != "" {
			c.websiteBaseURL = baseURL
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

// New creates an SEC client. SEC requires a descriptive User-Agent that
// identifies the caller and provides contact information.
func New(userAgent string, opts ...Option) (Client, error) {
	userAgent = strings.TrimSpace(userAgent)
	if userAgent == "" {
		return nil, errors.New("sec user agent is required")
	}

	c := &client{
		userAgent:      userAgent,
		dataBaseURL:    defaultDataBaseURL,
		websiteBaseURL: defaultWebsiteBaseURL,
		limiter:        rate.NewLimiter(rate.Limit(defaultQPS), int(defaultQPS)+1),
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

// GetCompanyTickers returns SEC ticker-to-CIK mappings.
func (c *client) GetCompanyTickers(ctx context.Context) ([]CompanyTicker, error) {
	body, err := c.getBody(ctx, c.websiteBaseURL+companyTickersPath)
	if err != nil {
		return nil, fmt.Errorf("sec company tickers: %w", err)
	}

	var payload map[string]CompanyTicker
	if err := decodeJSON(body, &payload); err != nil {
		return nil, fmt.Errorf("sec company tickers: %w", err)
	}

	tickers := make([]CompanyTicker, 0, len(payload))
	for _, ticker := range payload {
		ticker.Ticker = normalizeTicker(ticker.Ticker)
		ticker.Title = strings.TrimSpace(ticker.Title)
		if ticker.CIK == 0 || ticker.Ticker == "" {
			continue
		}
		tickers = append(tickers, ticker)
	}
	sort.Slice(tickers, func(i, j int) bool {
		return tickers[i].Ticker < tickers[j].Ticker
	})
	return tickers, nil
}

// ResolveCIK resolves a ticker through the SEC's ticker metadata. Mappings are
// cached for the lifetime of the client.
func (c *client) ResolveCIK(ctx context.Context, ticker string) (uint64, error) {
	ticker = normalizeTicker(ticker)
	if ticker == "" {
		return 0, errors.New("sec ticker is required")
	}

	c.tickersMu.Lock()
	defer c.tickersMu.Unlock()

	if company, ok := c.tickers[ticker]; ok {
		return company.CIK, nil
	}
	if c.tickers == nil {
		tickers, err := c.GetCompanyTickers(ctx)
		if err != nil {
			return 0, err
		}
		c.tickers = make(map[string]CompanyTicker, len(tickers))
		for _, company := range tickers {
			c.tickers[company.Ticker] = company
		}
		if company, ok := c.tickers[ticker]; ok {
			return company.CIK, nil
		}
	}
	return 0, fmt.Errorf("%w: %s", ErrTickerNotFound, ticker)
}

// GetCompanyFacts returns all standardized XBRL facts for a CIK.
func (c *client) GetCompanyFacts(ctx context.Context, cik uint64) (*CompanyFacts, error) {
	if cik == 0 {
		return nil, errors.New("sec cik is required")
	}

	path := fmt.Sprintf(companyFactsPath, cik)
	body, err := c.getBody(ctx, c.dataBaseURL+path)
	if err != nil {
		return nil, fmt.Errorf("sec company facts CIK%010d: %w", cik, err)
	}

	var facts CompanyFacts
	if err := decodeJSON(body, &facts); err != nil {
		return nil, fmt.Errorf("sec company facts CIK%010d: %w", cik, err)
	}
	if facts.CIK == 0 {
		facts.CIK = cik
	}
	return &facts, nil
}

// GetEquityFacts returns the latest SEC facts used for stock supply metrics.
func (c *client) GetEquityFacts(ctx context.Context, ticker string) (*EquityFacts, error) {
	normalizedTicker := normalizeTicker(ticker)
	cik, err := c.ResolveCIK(ctx, normalizedTicker)
	if err != nil {
		return nil, err
	}
	facts, err := c.GetCompanyFacts(ctx, cik)
	if err != nil {
		return nil, err
	}

	return &EquityFacts{
		CIK:                          facts.CIK,
		Ticker:                       normalizedTicker,
		EntityName:                   strings.TrimSpace(facts.EntityName),
		SharesOutstanding:            latestInstantFact(facts, "dei", "EntityCommonStockSharesOutstanding", "shares"),
		DilutedWeightedAverageShares: latestDurationFact(facts, "us-gaap", "WeightedAverageNumberOfDilutedSharesOutstanding", "shares"),
		PublicFloatUSD:               latestInstantFact(facts, "dei", "EntityPublicFloat", "USD"),
	}, nil
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

func latestInstantFact(facts *CompanyFacts, taxonomy, tag, unit string) *FactValue {
	values := factValues(facts, taxonomy, tag, unit)
	var selected *FactValue
	for i := range values {
		candidate := values[i]
		if candidate.Start != "" || !validFact(candidate) {
			continue
		}
		if selected == nil || compareFactDates(candidate, *selected) > 0 {
			copy := candidate
			selected = &copy
		}
	}
	return selected
}

func latestDurationFact(facts *CompanyFacts, taxonomy, tag, unit string) *FactValue {
	values := factValues(facts, taxonomy, tag, unit)
	var selected *FactValue
	for i := range values {
		candidate := values[i]
		if candidate.Start == "" || !validFact(candidate) {
			continue
		}
		if selected == nil || betterDurationFact(candidate, *selected) {
			copy := candidate
			selected = &copy
		}
	}
	return selected
}

func factValues(facts *CompanyFacts, taxonomy, tag, unit string) []FactValue {
	if facts == nil {
		return nil
	}
	taxonomyFacts, ok := facts.Facts[taxonomy]
	if !ok {
		return nil
	}
	fact, ok := taxonomyFacts[tag]
	if !ok {
		return nil
	}
	for candidateUnit, values := range fact.Units {
		if strings.EqualFold(candidateUnit, unit) {
			return values
		}
	}
	return nil
}

func validFact(value FactValue) bool {
	if _, ok := acceptedEquityForms[strings.ToUpper(strings.TrimSpace(value.Form))]; !ok {
		return false
	}
	if value.End == "" || !(value.Value > 0) || math.IsNaN(value.Value) || math.IsInf(value.Value, 0) {
		return false
	}
	_, err := time.Parse(time.DateOnly, value.End)
	return err == nil
}

func compareFactDates(left, right FactValue) int {
	if comparison := strings.Compare(left.End, right.End); comparison != 0 {
		return comparison
	}
	if comparison := strings.Compare(left.Filed, right.Filed); comparison != 0 {
		return comparison
	}
	return strings.Compare(left.Accession, right.Accession)
}

func betterDurationFact(candidate, selected FactValue) bool {
	if comparison := strings.Compare(candidate.End, selected.End); comparison != 0 {
		return comparison > 0
	}

	candidateDays := factDurationDays(candidate)
	selectedDays := factDurationDays(selected)
	if candidateDays > 0 && selectedDays > 0 && candidateDays != selectedDays {
		return candidateDays < selectedDays
	}
	return compareFactDates(candidate, selected) > 0
}

func factDurationDays(value FactValue) int {
	start, startErr := time.Parse(time.DateOnly, value.Start)
	end, endErr := time.Parse(time.DateOnly, value.End)
	if startErr != nil || endErr != nil || end.Before(start) {
		return 0
	}
	return int(end.Sub(start).Hours()/24) + 1
}

func normalizeTicker(ticker string) string {
	return strings.ToUpper(strings.TrimSpace(ticker))
}

func decodeJSON(body []byte, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}
