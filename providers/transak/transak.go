// Package transak implements a stateless client for Transak partner APIs.
package transak

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
)

const (
	productionAPIBaseURL     = "https://api.transak.com"
	productionGatewayBaseURL = "https://api-gateway.transak.com"

	refreshTokenPath        = "/partners/api/v2/refresh-token"
	createWidgetSessionPath = "/api/v2/auth/session"
	getOrdersPath           = "/partners/api/v2/orders"
	getFiatCurrenciesPath   = "/fiat/public/v1/currencies/fiat-currencies"

	apiSecretHeader   = "api-secret"
	apiKeyHeader      = "x-api-key"
	accessTokenHeader = "access-token"
	userIPHeader      = "x-user-ip"

	defaultTimeout           = 15 * time.Second
	maxResponseBody          = 4 << 20
	maxErrorResponseBody     = 1024
	defaultGetOrdersPageSize = 10
)

var (
	ErrUnauthorized = errors.New("transak unauthorized")
	ErrRateLimited  = errors.New("transak rate limited")
	ErrNotFound     = errors.New("transak not found")
	ErrBadRequest   = errors.New("transak bad request")
	ErrUpstream     = errors.New("transak upstream error")
)

// APIError wraps non-2xx Transak responses with a stable sentinel.
type APIError struct {
	StatusCode int
	Body       string
	Err        error
}

func (e *APIError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("transak status %d: %v", e.StatusCode, e.Err)
	}
	return fmt.Sprintf("transak status %d: %s: %v", e.StatusCode, e.Body, e.Err)
}

func (e *APIError) Unwrap() error {
	return e.Err
}

// Client defines the stateless Transak partner API surface.
type Client interface {
	RefreshToken(ctx context.Context) (*AccessToken, error)
	CreateWidgetSession(ctx context.Context, accessToken string, req CreateWidgetSessionRequest) (*CreateWidgetSessionResponse, error)
	GetOrders(ctx context.Context, accessToken string, req GetOrdersRequest) (*GetOrdersResponse, error)
	GetFiatCurrencies(ctx context.Context) (*GetFiatCurrenciesResponse, error)
	Close() error
}

type client struct {
	apiKey         string
	apiSecret      string
	apiBaseURL     string
	gatewayBaseURL string
	httpClient     *http.Client
}

var _ Client = (*client)(nil)

// Option customizes the Transak client.
type Option func(*client)

// WithAPIKey sets the Transak partner API key.
func WithAPIKey(apiKey string) Option {
	return func(c *client) {
		c.apiKey = strings.TrimSpace(apiKey)
	}
}

// WithAPISecret sets the Transak partner API secret.
func WithAPISecret(apiSecret string) Option {
	return func(c *client) {
		c.apiSecret = strings.TrimSpace(apiSecret)
	}
}

// WithAPIBaseURL overrides the partner API host for tests.
func WithAPIBaseURL(baseURL string) Option {
	return func(c *client) {
		if baseURL = trimBaseURL(baseURL); baseURL != "" {
			c.apiBaseURL = baseURL
		}
	}
}

// WithGatewayBaseURL overrides the API gateway host for tests.
func WithGatewayBaseURL(baseURL string) Option {
	return func(c *client) {
		if baseURL = trimBaseURL(baseURL); baseURL != "" {
			c.gatewayBaseURL = baseURL
		}
	}
}

// WithHTTPClient injects a custom HTTP client.
func WithHTTPClient(httpClient *http.Client) Option {
	return func(c *client) {
		if httpClient != nil {
			c.httpClient = httpClient
		}
	}
}

// New creates a Transak client.
func New(opts ...Option) (Client, error) {
	c := &client{
		apiBaseURL:     productionAPIBaseURL,
		gatewayBaseURL: productionGatewayBaseURL,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}
	if c.apiKey == "" {
		return nil, errors.New("transak: api key is required")
	}
	if c.apiSecret == "" {
		return nil, errors.New("transak: api secret is required")
	}
	if c.httpClient == nil {
		c.httpClient = &http.Client{Timeout: defaultTimeout}
	}
	return c, nil
}

// RefreshToken mints a partner access token. Transak allows one active token.
func (c *client) RefreshToken(ctx context.Context) (*AccessToken, error) {
	var out refreshTokenResponse
	payload := refreshTokenRequest{APIKey: c.apiKey}
	err := c.doJSON(ctx, http.MethodPost, c.apiBaseURL+refreshTokenPath, payload, &out, func(r *http.Request) {
		r.Header.Set(apiKeyHeader, c.apiKey)
		r.Header.Set(apiSecretHeader, c.apiSecret)
	})
	if err != nil {
		return nil, fmt.Errorf("transak refresh token: %w", err)
	}
	if out.Data.AccessToken == "" {
		return nil, errors.New("transak refresh token: missing access token")
	}
	expiresAt := out.Data.ExpiresAt.Time
	if expiresAt.IsZero() {
		return nil, errors.New("transak refresh token: missing expiresAt")
	}
	return &AccessToken{Token: out.Data.AccessToken, ExpiresAt: expiresAt}, nil
}

// CreateWidgetSession creates a single-use widget URL.
func (c *client) CreateWidgetSession(ctx context.Context, accessToken string, req CreateWidgetSessionRequest) (*CreateWidgetSessionResponse, error) {
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return nil, errors.New("transak create widget session: access token is required")
	}
	params := req.WidgetParams
	if strings.TrimSpace(params.APIKey) == "" {
		params.APIKey = c.apiKey
	}

	var out createWidgetSessionResponse
	payload := createWidgetSessionRequest{WidgetParams: params}
	err := c.doJSON(ctx, http.MethodPost, c.gatewayBaseURL+createWidgetSessionPath, payload, &out, func(r *http.Request) {
		r.Header.Set(apiKeyHeader, c.apiKey)
		r.Header.Set(accessTokenHeader, accessToken)
		if userIP := strings.TrimSpace(req.UserIP); userIP != "" {
			r.Header.Set(userIPHeader, userIP)
		}
	})
	if err != nil {
		return nil, fmt.Errorf("transak create widget session: %w", err)
	}
	if out.Data.WidgetURL == "" {
		return nil, errors.New("transak create widget session: missing widgetUrl")
	}
	return &CreateWidgetSessionResponse{WidgetURL: out.Data.WidgetURL}, nil
}

// GetOrders returns orders matching an exact partnerOrderId filter.
func (c *client) GetOrders(ctx context.Context, accessToken string, req GetOrdersRequest) (*GetOrdersResponse, error) {
	accessToken = strings.TrimSpace(accessToken)
	partnerOrderID := strings.TrimSpace(req.PartnerOrderID)
	if accessToken == "" {
		return nil, errors.New("transak get orders: access token is required")
	}
	if partnerOrderID == "" {
		return nil, errors.New("transak get orders: partner order id is required")
	}

	limit := req.Limit
	if limit <= 0 {
		limit = defaultGetOrdersPageSize
	}
	values := url.Values{}
	values.Set("filter[partnerOrderId]", partnerOrderID)
	values.Set("limit", strconv.Itoa(limit))
	if req.Skip > 0 {
		values.Set("skip", strconv.Itoa(req.Skip))
	}

	endpoint := c.apiBaseURL + getOrdersPath + "?" + values.Encode()
	var out getOrdersEnvelope
	err := c.doJSON(ctx, http.MethodGet, endpoint, nil, &out, func(r *http.Request) {
		r.Header.Set(apiKeyHeader, c.apiKey)
		r.Header.Set(accessTokenHeader, accessToken)
	})
	if err != nil {
		return nil, fmt.Errorf("transak get orders: %w", err)
	}

	orders, err := out.Orders()
	if err != nil {
		return nil, fmt.Errorf("transak get orders: %w", err)
	}
	return &GetOrdersResponse{Orders: orders}, nil
}

// GetFiatCurrencies returns Transak's public fiat-currency catalog, including
// payment method options, limits, and country support.
func (c *client) GetFiatCurrencies(ctx context.Context) (*GetFiatCurrenciesResponse, error) {
	endpoint, err := url.Parse(c.apiBaseURL + getFiatCurrenciesPath)
	if err != nil {
		return nil, fmt.Errorf("transak get fiat currencies: build endpoint: %w", err)
	}
	values := endpoint.Query()
	values.Set("apiKey", c.apiKey)
	endpoint.RawQuery = values.Encode()

	var raw json.RawMessage
	err = c.doJSON(ctx, http.MethodGet, endpoint.String(), nil, &raw, func(r *http.Request) {
		r.Header.Set(apiKeyHeader, c.apiKey)
	})
	if err != nil {
		return nil, fmt.Errorf("transak get fiat currencies: %w", err)
	}
	currencies, err := decodeFiatCurrencies(raw)
	if err != nil {
		return nil, fmt.Errorf("transak get fiat currencies: %w", err)
	}
	if len(currencies) == 0 {
		return nil, errors.New("transak get fiat currencies: empty catalog")
	}
	return &GetFiatCurrenciesResponse{Currencies: currencies}, nil
}

func (c *client) Close() error {
	if c.httpClient != nil {
		c.httpClient.CloseIdleConnections()
	}
	return nil
}

func (c *client) doJSON(ctx context.Context, method, endpoint string, payload, out any, decorate func(*http.Request)) error {
	if ctx == nil {
		ctx = context.Background()
	}

	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		body = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if decorate != nil {
		decorate(req)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := decodeStatus(resp); err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBody)).Decode(out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func decodeStatus(resp *http.Response) error {
	if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorResponseBody))
	bodyText := strings.TrimSpace(string(body))

	err := ErrUpstream
	switch resp.StatusCode {
	case http.StatusBadRequest:
		err = ErrBadRequest
	case http.StatusUnauthorized, http.StatusForbidden:
		err = ErrUnauthorized
	case http.StatusNotFound:
		err = ErrNotFound
	case http.StatusTooManyRequests:
		err = ErrRateLimited
	}
	return &APIError{StatusCode: resp.StatusCode, Body: bodyText, Err: err}
}

type refreshTokenRequest struct {
	APIKey string `json:"apiKey"`
}

type refreshTokenResponse struct {
	Data struct {
		AccessToken string   `json:"accessToken"`
		ExpiresAt   unixTime `json:"expiresAt"`
	} `json:"data"`
}

type createWidgetSessionRequest struct {
	WidgetParams WidgetParams `json:"widgetParams"`
}

type createWidgetSessionResponse struct {
	Data struct {
		WidgetURL string `json:"widgetUrl"`
	} `json:"data"`
}

type getOrdersEnvelope struct {
	Data json.RawMessage `json:"data"`
}

func (e getOrdersEnvelope) Orders() ([]Order, error) {
	if len(e.Data) == 0 || string(e.Data) == "null" {
		return nil, nil
	}
	var orders []Order
	if err := json.Unmarshal(e.Data, &orders); err == nil {
		return orders, nil
	}
	var object struct {
		Orders []Order `json:"orders"`
	}
	if err := json.Unmarshal(e.Data, &object); err != nil {
		return nil, fmt.Errorf("decode data: %w", err)
	}
	return object.Orders, nil
}

type fiatCurrenciesEnvelope struct {
	Response []FiatCurrency `json:"response"`
}

func decodeFiatCurrencies(raw json.RawMessage) ([]FiatCurrency, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var envelope fiatCurrenciesEnvelope
	if err := json.Unmarshal(raw, &envelope); err == nil && envelope.Response != nil {
		return envelope.Response, nil
	}
	var direct []FiatCurrency
	if err := json.Unmarshal(raw, &direct); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return direct, nil
}

type unixTime struct {
	time.Time
}

func (t *unixTime) UnmarshalJSON(data []byte) error {
	raw := strings.Trim(strings.TrimSpace(string(data)), `"`)
	if raw == "" || raw == "null" {
		t.Time = time.Time{}
		return nil
	}
	if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil {
		t.Time = time.Unix(seconds, 0).UTC()
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return err
	}
	t.Time = parsed
	return nil
}

func trimBaseURL(baseURL string) string {
	return strings.TrimRight(strings.TrimSpace(baseURL), "/")
}
