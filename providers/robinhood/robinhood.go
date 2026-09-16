// Package robinhood implements a stateless client for Robinhood Connect, the
// transfer flow that moves crypto from a user's Robinhood account to an address
// the partner supplies.
//
// The package is transport only: configuration and policy belong to the caller.
package robinhood

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
	productionAPIBaseURL = "https://api.robinhood.com"

	// A universal link: opens the Robinhood app when installed, web otherwise.
	productionConnectBaseURL = "https://applink.robinhood.com/u/connect"

	createConnectIDPath = "/catpay/v1/connect_id/"
	getOrderPathFmt     = "/catpay/v1/external/order/%s"

	applicationIDHeader = "application-id"
	apiKeyHeader        = "x-api-key"

	defaultTimeout       = 15 * time.Second
	maxResponseBody      = 4 << 20
	maxErrorResponseBody = 1024
)

var (
	ErrUnauthorized = errors.New("robinhood unauthorized")
	ErrRateLimited  = errors.New("robinhood rate limited")
	ErrNotFound     = errors.New("robinhood not found")
	ErrBadRequest   = errors.New("robinhood bad request")
	ErrUpstream     = errors.New("robinhood upstream error")
)

// APIError wraps non-2xx Robinhood responses with a stable sentinel.
type APIError struct {
	StatusCode int
	Body       string
	Err        error
}

func (e *APIError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("robinhood status %d: %v", e.StatusCode, e.Err)
	}
	return fmt.Sprintf("robinhood status %d: %s: %v", e.StatusCode, e.Body, e.Err)
}

func (e *APIError) Unwrap() error {
	return e.Err
}

// Client defines the stateless Robinhood Connect surface.
type Client interface {
	CreateConnectID(ctx context.Context, req CreateConnectIDRequest) (*CreateConnectIDResponse, error)
	GetOrder(ctx context.Context, connectID string) (*Order, error)
	BuildConnectURL(req BuildConnectURLRequest) (string, error)
	Close() error
}

type client struct {
	applicationID  string
	apiKey         string
	apiBaseURL     string
	connectBaseURL string
	httpClient     *http.Client
}

var _ Client = (*client)(nil)

// Option customizes the Robinhood client.
type Option func(*client)

// WithApplicationID sets the Robinhood Connect application id. It is not a
// secret — it also appears in the connect URL — but every call requires it.
func WithApplicationID(applicationID string) Option {
	return func(c *client) {
		c.applicationID = strings.TrimSpace(applicationID)
	}
}

// WithAPIKey sets the Robinhood partner API key.
func WithAPIKey(apiKey string) Option {
	return func(c *client) {
		c.apiKey = strings.TrimSpace(apiKey)
	}
}

// WithAPIBaseURL overrides the API host.
func WithAPIBaseURL(baseURL string) Option {
	return func(c *client) {
		if baseURL = trimBaseURL(baseURL); baseURL != "" {
			c.apiBaseURL = baseURL
		}
	}
}

// WithConnectBaseURL overrides the connect universal link.
func WithConnectBaseURL(baseURL string) Option {
	return func(c *client) {
		if baseURL = trimBaseURL(baseURL); baseURL != "" {
			c.connectBaseURL = baseURL
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

// New creates a Robinhood Connect client.
func New(opts ...Option) (Client, error) {
	c := &client{
		apiBaseURL:     productionAPIBaseURL,
		connectBaseURL: productionConnectBaseURL,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}
	if c.applicationID == "" {
		return nil, errors.New("robinhood: application id is required")
	}
	if c.apiKey == "" {
		return nil, errors.New("robinhood: api key is required")
	}
	if c.httpClient == nil {
		c.httpClient = &http.Client{Timeout: defaultTimeout}
	}
	return c, nil
}

// CreateConnectID mints the connect id for one transfer session, bound to the
// destination address it is created with.
func (c *client) CreateConnectID(ctx context.Context, req CreateConnectIDRequest) (*CreateConnectIDResponse, error) {
	walletAddress := strings.TrimSpace(req.WalletAddress)
	referenceID := strings.TrimSpace(req.ReferenceID)
	if walletAddress == "" {
		return nil, errors.New("robinhood create connect id: wallet address is required")
	}
	if referenceID == "" {
		return nil, errors.New("robinhood create connect id: reference id is required")
	}

	payload := createConnectIDRequest{
		WithdrawalAddress: walletAddress,
		ReferenceID:       referenceID,
	}

	var out createConnectIDResponse
	if err := c.doJSON(ctx, http.MethodPost, createConnectIDPath, payload, &out); err != nil {
		return nil, fmt.Errorf("robinhood create connect id: %w", err)
	}
	if out.ConnectID == "" {
		return nil, errors.New("robinhood create connect id: missing connectId")
	}
	return &CreateConnectIDResponse{ConnectID: out.ConnectID}, nil
}

// GetOrder reads one order's state by connect id. The status is returned as
// Robinhood reported it, including a value this package does not name.
func (c *client) GetOrder(ctx context.Context, connectID string) (*Order, error) {
	connectID = strings.TrimSpace(connectID)
	if connectID == "" {
		return nil, errors.New("robinhood get order: connect id is required")
	}

	var out orderResponse
	path := fmt.Sprintf(getOrderPathFmt, url.PathEscape(connectID))
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, fmt.Errorf("robinhood get order: %w", err)
	}

	order := out.toOrder()
	return &order, nil
}

// BuildConnectURL returns the Robinhood Connect handoff URL for one session. It
// makes no network call: every optional field is emitted when set and omitted
// when not.
func (c *client) BuildConnectURL(req BuildConnectURLRequest) (string, error) {
	connectID := strings.TrimSpace(req.ConnectID)
	walletAddress := strings.TrimSpace(req.WalletAddress)
	if connectID == "" {
		return "", errors.New("robinhood build connect url: connect id is required")
	}
	if walletAddress == "" {
		return "", errors.New("robinhood build connect url: wallet address is required")
	}

	endpoint, err := url.Parse(c.connectBaseURL)
	if err != nil {
		return "", fmt.Errorf("robinhood build connect url: %w", err)
	}

	values := url.Values{}
	values.Set("applicationId", c.applicationID)
	values.Set("connectId", connectID)
	values.Set("walletAddress", walletAddress)

	setIfPresent(values, "redirectUrl", req.RedirectURL)
	setIfPresent(values, "supportedNetworks", joinValues(req.SupportedNetworks))
	setIfPresent(values, "supportedAssets", joinValues(req.SupportedAssets))
	setIfPresent(values, "fiatAmount", req.FiatAmount.String())
	setIfPresent(values, "fiatCode", req.FiatCode)
	setIfPresent(values, "assetCode", req.AssetCode)
	if req.LockAmount {
		values.Set("lockAmount", strconv.FormatBool(true))
	}

	endpoint.RawQuery = values.Encode()
	return endpoint.String(), nil
}

func (c *client) Close() error {
	if c.httpClient != nil {
		c.httpClient.CloseIdleConnections()
	}
	return nil
}

func (c *client) doJSON(ctx context.Context, method, path string, payload, out any) error {
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

	req, err := http.NewRequestWithContext(ctx, method, c.apiBaseURL+path, body)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set(applicationIDHeader, c.applicationID)
	req.Header.Set(apiKeyHeader, c.apiKey)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
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

func setIfPresent(values url.Values, key, value string) {
	if value = strings.TrimSpace(value); value != "" {
		values.Set(key, value)
	}
}

func joinValues(values []string) string {
	cleaned := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			cleaned = append(cleaned, value)
		}
	}
	return strings.Join(cleaned, ",")
}

func trimBaseURL(baseURL string) string {
	return strings.TrimRight(strings.TrimSpace(baseURL), "/")
}

type createConnectIDRequest struct {
	WithdrawalAddress string `json:"withdrawal_address"`
	ReferenceID       string `json:"referenceId"`
}

type createConnectIDResponse struct {
	ConnectID string `json:"connectId"`
}
