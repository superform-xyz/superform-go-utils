// Package polymarket provides a bounded HTTP boundary for Polymarket's V2
// CLOB. It has no L1 signing or Superform account-authorization capability.
package polymarket

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	protocol "github.com/superform-xyz/superform-go-utils/utils/polymarket"
)

const (
	// DefaultCLOBURL is the production Polymarket V2 CLOB origin.
	DefaultCLOBURL = "https://clob.polymarket.com"

	defaultTimeout       = 10 * time.Second
	maxPublicBodyBytes   = 8 << 20
	maxPrivateBodyBytes  = 2 << 20
	maxErrorBodyBytes    = 8 << 10
	maxRequestBodyBytes  = 1 << 20
	maxTransientAttempts = 2
)

var (
	// ErrRateLimited classifies HTTP 429 responses.
	ErrRateLimited = errors.New("polymarket: rate limited")
	// ErrTradingDisabled means new order submission was not explicitly enabled.
	ErrTradingDisabled = errors.New("polymarket: new order submission is disabled")
	// ErrCredentialsMissing means valid maker-bound L2 credentials are unavailable.
	ErrCredentialsMissing = errors.New("polymarket: maker-bound credentials are unavailable")
	// ErrOrderNotFound means the provider has no order with the requested ID.
	ErrOrderNotFound = errors.New("polymarket: order not found")
)

// HTTPError contains bounded provider context and never credential material.
type HTTPError struct {
	StatusCode int
	Body       string
	RetryAfter time.Duration
}

func (e *HTTPError) Error() string {
	if e == nil {
		return "polymarket: upstream request failed"
	}
	if e.RetryAfter > 0 {
		return fmt.Sprintf("polymarket: upstream status %d (retry after %s)", e.StatusCode, e.RetryAfter)
	}
	return fmt.Sprintf("polymarket: upstream status %d", e.StatusCode)
}

// Is makes HTTP 429 errors match ErrRateLimited.
func (e *HTTPError) Is(target error) bool {
	return target == ErrRateLimited && e != nil && e.StatusCode == http.StatusTooManyRequests
}

// IsDefinitiveSubmissionRejection reports whether a provider response proves
// the write was rejected. Transport failures, timeouts, and 5xx responses are
// ambiguous and must be reconciled instead of automatically resubmitted.
func IsDefinitiveSubmissionRejection(err error) bool {
	var providerError *HTTPError
	return errors.As(err, &providerError) && providerError.StatusCode >= http.StatusBadRequest &&
		providerError.StatusCode < http.StatusInternalServerError && providerError.StatusCode != http.StatusRequestTimeout
}

// Credentials are maker-bound L2 HMAC credentials provisioned out of band.
type Credentials struct {
	Address    string `json:"address"`
	APIKey     string `json:"api_key"`
	APISecret  string `json:"api_secret"`
	Passphrase string `json:"passphrase"`
}

func (Credentials) String() string { return "[redacted polymarket credentials]" }

func (Credentials) GoString() string { return "polymarket.Credentials{redacted}" }

// MarshalJSON prevents accidental credential serialization into logs/config dumps.
func (Credentials) MarshalJSON() ([]byte, error) {
	return []byte(`"[redacted polymarket credentials]"`), nil
}

// Validate checks a complete maker-bound L2 credential without exposing its values.
func (c Credentials) Validate() error {
	switch {
	case !isAddress(c.Address):
		return errors.New("polymarket: credential address is invalid")
	case strings.TrimSpace(c.APIKey) == "" || len(c.APIKey) > 256:
		return errors.New("polymarket: API key is invalid")
	case strings.TrimSpace(c.Passphrase) == "" || len(c.Passphrase) > 256:
		return errors.New("polymarket: passphrase is invalid")
	case strings.TrimSpace(c.APISecret) == "" || len(c.APISecret) > 512:
		return errors.New("polymarket: API secret is invalid")
	}
	secret, err := decodeURLBase64(c.APISecret)
	if err != nil {
		return errors.New("polymarket: API secret is not canonical URL-safe base64")
	}
	clear(secret)
	return nil
}

// Client is the complete public and authenticated Polymarket V2 CLOB boundary.
type Client interface {
	ListMarkets(ctx context.Context, nextCursor string) (*MarketPage, error)
	GetMarket(ctx context.Context, conditionID string, includeBooks bool) (*Market, error)
	PostOrder(ctx context.Context, credentials Credentials, order SignedOrder) (*OrderPlacement, error)
	GetOrder(ctx context.Context, credentials Credentials, orderID string) (*OpenOrder, error)
	UpdateBalanceAllowance(ctx context.Context, credentials Credentials, update BalanceAllowanceUpdate) error
	ListOpenOrders(ctx context.Context, credentials Credentials, filter OpenOrderFilter) (*OpenOrderPage, error)
	CancelOrder(ctx context.Context, credentials Credentials, orderID string) (*CancelResult, error)
	Close() error
}

type client struct {
	baseURL                string
	httpClient             *http.Client
	orderSubmissionEnabled bool
	now                    func() time.Time
}

var _ Client = (*client)(nil)

type clientConfig struct {
	baseURL                string
	httpClient             *http.Client
	timeout                time.Duration
	timeoutConfigured      bool
	orderSubmissionEnabled bool
	now                    func() time.Time
}

// Option configures a client before construction.
type Option func(*clientConfig)

// WithBaseURL overrides the production CLOB origin.
func WithBaseURL(baseURL string) Option {
	return func(config *clientConfig) { config.baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/") }
}

// WithHTTPClient injects an HTTP client. New copies it and still rejects redirects.
func WithHTTPClient(httpClient *http.Client) Option {
	return func(config *clientConfig) {
		if httpClient != nil {
			config.httpClient = httpClient
		}
	}
}

// WithTimeout configures the overall HTTP request timeout.
func WithTimeout(timeout time.Duration) Option {
	return func(config *clientConfig) {
		config.timeout = timeout
		config.timeoutConfigured = true
	}
}

// WithOrderSubmissionEnabled explicitly enables POST /order.
func WithOrderSubmissionEnabled(enabled bool) Option {
	return func(config *clientConfig) { config.orderSubmissionEnabled = enabled }
}

func withClock(now func() time.Time) Option {
	return func(config *clientConfig) { config.now = now }
}

// New constructs a redirect-safe client. New order submission is disabled by
// default; authenticated reads and cancellation remain available.
func New(options ...Option) (Client, error) {
	config := clientConfig{
		baseURL: DefaultCLOBURL,
		timeout: defaultTimeout,
		now:     time.Now,
	}
	for _, option := range options {
		if option != nil {
			option(&config)
		}
	}
	if config.baseURL == "" {
		config.baseURL = DefaultCLOBURL
	}
	parsed, err := url.Parse(config.baseURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("polymarket: CLOB URL must be an absolute HTTPS origin")
	}
	if config.timeout <= 0 {
		return nil, errors.New("polymarket: timeout must be positive")
	}
	if config.now == nil {
		return nil, errors.New("polymarket: clock is required")
	}

	var httpClient *http.Client
	if config.httpClient == nil {
		transport := http.DefaultTransport
		if defaultTransport, ok := http.DefaultTransport.(*http.Transport); ok {
			transport = defaultTransport.Clone()
		}
		httpClient = &http.Client{Timeout: config.timeout, Transport: transport}
	} else {
		copied := *config.httpClient
		if config.timeoutConfigured || copied.Timeout <= 0 {
			copied.Timeout = config.timeout
		}
		httpClient = &copied
	}
	httpClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}

	return &client{
		baseURL:                config.baseURL,
		httpClient:             httpClient,
		orderSubmissionEnabled: config.orderSubmissionEnabled,
		now:                    config.now,
	}, nil
}

func (c *client) ListMarkets(ctx context.Context, nextCursor string) (*MarketPage, error) {
	if err := validateCursor(nextCursor); err != nil {
		return nil, err
	}
	query := url.Values{}
	if nextCursor != "" {
		query.Set("next_cursor", nextCursor)
	}
	var page MarketPage
	if err := c.do(ctx, http.MethodGet, "/sampling-markets", query, nil, nil, maxPublicBodyBytes, &page); err != nil {
		return nil, err
	}
	if err := page.Validate(); err != nil {
		return nil, fmt.Errorf("polymarket: invalid markets response: %w", err)
	}
	return &page, nil
}

func (c *client) GetMarket(ctx context.Context, conditionID string, includeBooks bool) (*Market, error) {
	if !isHash(conditionID) {
		return nil, errors.New("polymarket: condition_id must be a nonzero 0x-prefixed bytes32")
	}
	var market Market
	if err := c.do(ctx, http.MethodGet, "/markets/"+strings.ToLower(conditionID), nil, nil, nil, maxPublicBodyBytes, &market); err != nil {
		return nil, err
	}
	if err := market.Validate(); err != nil {
		return nil, fmt.Errorf("polymarket: invalid market response: %w", err)
	}
	if !strings.EqualFold(market.ConditionID, conditionID) {
		return nil, errors.New("polymarket: market response condition_id mismatch")
	}
	if includeBooks {
		for index := range market.Tokens {
			book, err := c.getBook(ctx, market.Tokens[index].TokenID)
			if err != nil {
				return nil, err
			}
			market.Tokens[index].Book = book
		}
	}
	return &market, nil
}

func (c *client) getBook(ctx context.Context, tokenID string) (*OrderBook, error) {
	if !protocol.IsCanonicalUint256(tokenID, false) {
		return nil, errors.New("polymarket: token_id is invalid")
	}
	query := url.Values{"token_id": []string{tokenID}}
	var book OrderBook
	if err := c.do(ctx, http.MethodGet, "/book", query, nil, nil, maxPublicBodyBytes, &book); err != nil {
		return nil, err
	}
	if err := book.Validate(tokenID); err != nil {
		return nil, fmt.Errorf("polymarket: invalid order book: %w", err)
	}
	return &book, nil
}

func (c *client) PostOrder(ctx context.Context, credentials Credentials, order SignedOrder) (*OrderPlacement, error) {
	if err := c.requireSubmission(credentials); err != nil {
		return nil, err
	}
	if err := order.Validate(); err != nil {
		return nil, err
	}
	if !strings.EqualFold(credentials.Address, order.Order.Signer.Hex()) {
		return nil, errors.New("polymarket: credential address must equal order signer")
	}
	payload := order.payload(credentials.APIKey)
	var placement OrderPlacement
	if err := c.do(ctx, http.MethodPost, "/order", nil, payload, &credentials, maxPrivateBodyBytes, &placement); err != nil {
		return nil, err
	}
	if err := placement.Validate(); err != nil {
		return nil, fmt.Errorf("polymarket: invalid order placement response: %w", err)
	}
	return &placement, nil
}

func (c *client) ListOpenOrders(ctx context.Context, credentials Credentials, filter OpenOrderFilter) (*OpenOrderPage, error) {
	if err := requireCredentials(credentials); err != nil {
		return nil, err
	}
	if err := filter.Validate(); err != nil {
		return nil, err
	}
	query := url.Values{}
	if filter.ConditionID != "" {
		query.Set("market", strings.ToLower(filter.ConditionID))
	}
	if filter.TokenID != "" {
		query.Set("asset_id", filter.TokenID)
	}
	if filter.OrderID != "" {
		query.Set("id", filter.OrderID)
	}
	if filter.NextCursor != "" {
		query.Set("next_cursor", filter.NextCursor)
	}
	var page OpenOrderPage
	if err := c.do(ctx, http.MethodGet, "/data/orders", query, nil, &credentials, maxPrivateBodyBytes, &page); err != nil {
		return nil, err
	}
	if err := page.Validate(credentials.Address); err != nil {
		return nil, fmt.Errorf("polymarket: invalid open-orders response: %w", err)
	}
	return &page, nil
}

func (c *client) GetOrder(ctx context.Context, credentials Credentials, orderID string) (*OpenOrder, error) {
	if err := requireCredentials(credentials); err != nil {
		return nil, err
	}
	if err := validateOrderID(orderID); err != nil {
		return nil, err
	}
	path := "/data/order/" + strings.ToLower(orderID)
	var order OpenOrder
	if err := c.do(ctx, http.MethodGet, path, nil, nil, &credentials, maxPrivateBodyBytes, &order); err != nil {
		var providerError *HTTPError
		if errors.As(err, &providerError) && providerError.StatusCode == http.StatusNotFound {
			return nil, ErrOrderNotFound
		}
		return nil, err
	}
	if err := order.Validate(credentials.Address); err != nil {
		return nil, fmt.Errorf("polymarket: invalid order response: %w", err)
	}
	if !strings.EqualFold(order.ID, orderID) {
		return nil, errors.New("polymarket: order response ID mismatch")
	}
	return &order, nil
}

func (c *client) UpdateBalanceAllowance(ctx context.Context, credentials Credentials, update BalanceAllowanceUpdate) error {
	if err := requireCredentials(credentials); err != nil {
		return err
	}
	if err := update.Validate(); err != nil {
		return err
	}
	query := url.Values{
		"asset_type":     []string{string(update.AssetType)},
		"signature_type": []string{"3"},
	}
	if update.TokenID != "" {
		query.Set("token_id", update.TokenID)
	}
	if err := c.do(ctx, http.MethodGet, "/balance-allowance/update", query, nil, &credentials, maxPrivateBodyBytes, nil); err != nil {
		return fmt.Errorf("polymarket: update balance allowance: %w", err)
	}
	return nil
}

func (c *client) CancelOrder(ctx context.Context, credentials Credentials, orderID string) (*CancelResult, error) {
	if err := requireCredentials(credentials); err != nil {
		return nil, err
	}
	if err := validateOrderID(orderID); err != nil {
		return nil, err
	}
	payload := cancelRequest{OrderID: orderID}
	var result CancelResult
	if err := c.do(ctx, http.MethodDelete, "/order", nil, payload, &credentials, maxPrivateBodyBytes, &result); err != nil {
		return nil, err
	}
	if err := result.Validate(orderID); err != nil {
		return nil, fmt.Errorf("polymarket: invalid cancellation response: %w", err)
	}
	return &result, nil
}

func (c *client) Close() error {
	c.httpClient.CloseIdleConnections()
	return nil
}

func (c *client) requireSubmission(credentials Credentials) error {
	if !c.orderSubmissionEnabled {
		return ErrTradingDisabled
	}
	return requireCredentials(credentials)
}

func requireCredentials(credentials Credentials) error {
	if err := credentials.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrCredentialsMissing, err)
	}
	return nil
}

func (c *client) do(
	ctx context.Context,
	method string,
	path string,
	query url.Values,
	body any,
	credentials *Credentials,
	responseLimit int64,
	target any,
) error {
	endpoint, err := url.Parse(c.baseURL + path)
	if err != nil {
		return fmt.Errorf("polymarket: build endpoint: %w", err)
	}
	if query != nil {
		endpoint.RawQuery = query.Encode()
	}

	var payload []byte
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("polymarket: encode request: %w", err)
		}
		if len(payload) > maxRequestBodyBytes {
			return errors.New("polymarket: request body is too large")
		}
	}

	maxAttempts := 1
	if method == http.MethodGet {
		maxAttempts = maxTransientAttempts
	}
	for attempt := 0; attempt < maxAttempts; attempt++ {
		request, requestErr := http.NewRequestWithContext(ctx, method, endpoint.String(), bytes.NewReader(payload))
		if requestErr != nil {
			return fmt.Errorf("polymarket: create request: %w", requestErr)
		}
		request.Header.Set("Accept", "application/json")
		if body != nil {
			request.Header.Set("Content-Type", "application/json")
		}
		if credentials != nil {
			if err := setL2Headers(request, *credentials, path, payload, c.now()); err != nil {
				return err
			}
		}

		response, requestErr := c.httpClient.Do(request)
		if requestErr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return fmt.Errorf("polymarket: request cancelled: %w", ctxErr)
			}
			return fmt.Errorf("polymarket: request failed: %w", requestErr)
		}
		resultErr := c.decodeResponse(response, responseLimit, target)
		closeErr := response.Body.Close()
		if resultErr == nil && closeErr != nil {
			return fmt.Errorf("polymarket: close response: %w", closeErr)
		}
		if resultErr == nil {
			return nil
		}
		var httpErr *HTTPError
		if !errors.As(resultErr, &httpErr) || !isTransientStatus(httpErr.StatusCode) || attempt+1 >= maxAttempts {
			return resultErr
		}
		if httpErr.RetryAfter > 0 {
			timer := time.NewTimer(minDuration(httpErr.RetryAfter, time.Second))
			select {
			case <-ctx.Done():
				timer.Stop()
				return fmt.Errorf("polymarket: retry cancelled: %w", ctx.Err())
			case <-timer.C:
			}
		}
	}
	return errors.New("polymarket: request attempts exhausted")
}

func (c *client) decodeResponse(response *http.Response, limit int64, target any) error {
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		body, err := readBounded(response.Body, maxErrorBodyBytes)
		if err != nil {
			return err
		}
		return &HTTPError{
			StatusCode: response.StatusCode,
			Body:       string(body),
			RetryAfter: retryAfter(response),
		}
	}
	if target == nil {
		_, err := readBounded(response.Body, limit)
		return err
	}
	payload, err := readBounded(response.Body, limit)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("polymarket: decode response: %w", err)
	}
	return ensureEOF(decoder)
}

func setL2Headers(request *http.Request, credentials Credentials, path string, payload []byte, now time.Time) error {
	if err := credentials.Validate(); err != nil {
		return err
	}
	if now.IsZero() || now.Unix() <= 0 {
		return errors.New("polymarket: authentication clock is invalid")
	}
	timestamp := strconv.FormatInt(now.UTC().Unix(), 10)
	secret, err := decodeURLBase64(credentials.APISecret)
	if err != nil {
		return errors.New("polymarket: decode API secret")
	}
	defer clear(secret)
	message := timestamp + request.Method + path
	if len(payload) > 0 {
		message += string(payload)
	}
	mac := hmac.New(sha256.New, secret)
	if _, err := mac.Write([]byte(message)); err != nil {
		return fmt.Errorf("polymarket: sign request: %w", err)
	}
	signature := base64.URLEncoding.EncodeToString(mac.Sum(nil))

	request.Header.Set("POLY_ADDRESS", credentials.Address)
	request.Header.Set("POLY_SIGNATURE", signature)
	request.Header.Set("POLY_TIMESTAMP", timestamp)
	request.Header.Set("POLY_API_KEY", credentials.APIKey)
	request.Header.Set("POLY_PASSPHRASE", credentials.Passphrase)
	return nil
}

func decodeURLBase64(value string) ([]byte, error) {
	decoded, err := base64.URLEncoding.DecodeString(value)
	if err == nil && base64.URLEncoding.EncodeToString(decoded) == value {
		return decoded, nil
	}
	decoded, err = base64.RawURLEncoding.DecodeString(value)
	if err != nil || base64.RawURLEncoding.EncodeToString(decoded) != value {
		clear(decoded)
		return nil, errors.New("invalid URL-safe base64")
	}
	return decoded, nil
}

func readBounded(reader io.Reader, maximum int64) ([]byte, error) {
	limited := io.LimitReader(reader, maximum+1)
	payload, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("polymarket: read response: %w", err)
	}
	if int64(len(payload)) > maximum {
		return nil, fmt.Errorf("polymarket: response exceeds %d bytes", maximum)
	}
	return payload, nil
}

func ensureEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("polymarket: response contains multiple JSON values")
		}
		return fmt.Errorf("polymarket: invalid trailing response data: %w", err)
	}
	return nil
}

func retryAfter(response *http.Response) time.Duration {
	value := strings.TrimSpace(response.Header.Get("Retry-After"))
	if value == "" {
		return 0
	}
	seconds, err := strconv.ParseUint(value, 10, 32)
	if err == nil {
		return time.Duration(seconds) * time.Second
	}
	when, err := http.ParseTime(value)
	if err != nil {
		return 0
	}
	return maxDuration(time.Until(when), 0)
}

func isTransientStatus(status int) bool {
	return status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
}

func minDuration(left, right time.Duration) time.Duration {
	if left < right {
		return left
	}
	return right
}

func maxDuration(left, right time.Duration) time.Duration {
	if left > right {
		return left
	}
	return right
}
