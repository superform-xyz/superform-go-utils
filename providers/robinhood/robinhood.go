// Package robinhood implements a stateless client for Robinhood Connect, the
// transfer flow that moves crypto from a user's Robinhood account (buying it
// there first if needed) to an address the partner supplies.
//
// It exists so the partner API key stays server-side. The mobile app has been
// calling api.robinhood.com directly with a bundled key whenever the backend
// proxy was unavailable; this package is the Go implementation that proxy is
// built on.
package robinhood

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	productionAPIBaseURL = "https://api.robinhood.com"

	// productionConnectBaseURL is a universal link: it opens the Robinhood app
	// when installed and the web flow otherwise.
	productionConnectBaseURL = "https://applink.robinhood.com/u/connect"

	createConnectIDPath = "/catpay/v1/connect_id/"
	getOrderPathFmt     = "/catpay/v1/external/order/%s"

	applicationIDHeader = "application-id"
	apiKeyHeader        = "x-api-key"

	defaultNetwork   = "ETHEREUM"
	defaultAsset     = "USDC"
	defaultFiatCode  = "USD"
	defaultAssetCode = "USDC"

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

	// ErrInvalidOrderStatus marks an order whose status is outside the
	// documented set. Treating it as terminal would risk crediting or failing a
	// purchase on a value nobody has reasoned about, so it is surfaced instead.
	ErrInvalidOrderStatus = errors.New("robinhood invalid order status")
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
// secret — it also appears in the connect URL — but every API call requires it.
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

// WithAPIBaseURL overrides the API host for tests.
func WithAPIBaseURL(baseURL string) Option {
	return func(c *client) {
		if baseURL = trimBaseURL(baseURL); baseURL != "" {
			c.apiBaseURL = baseURL
		}
	}
}

// WithConnectBaseURL overrides the connect universal link for tests.
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

// GetOrder reads one order's state by connect id.
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
	if !isKnownOrderStatus(out.Status) {
		return nil, fmt.Errorf("robinhood get order: %w: %q", ErrInvalidOrderStatus, out.Status)
	}

	order := out.toOrder()
	// The order-details API omits connectId on some responses; the caller asked
	// by id, so echo the one it already knows rather than hand back a blank.
	if order.ID == "" {
		order.ID = connectID
	}
	return &order, nil
}

// BuildConnectURL returns the Robinhood Connect handoff URL for one session. It
// is pure — no network call — so a caller holding a connect id can build the
// URL without another round trip.
func (c *client) BuildConnectURL(req BuildConnectURLRequest) (string, error) {
	connectID := strings.TrimSpace(req.ConnectID)
	walletAddress := strings.TrimSpace(req.WalletAddress)
	redirectURL := strings.TrimSpace(req.RedirectURL)
	if connectID == "" {
		return "", errors.New("robinhood build connect url: connect id is required")
	}
	if walletAddress == "" {
		return "", errors.New("robinhood build connect url: wallet address is required")
	}
	if redirectURL == "" {
		return "", errors.New("robinhood build connect url: redirect url is required")
	}

	endpoint, err := url.Parse(c.connectBaseURL)
	if err != nil {
		return "", fmt.Errorf("robinhood build connect url: %w", err)
	}

	values := url.Values{}
	values.Set("applicationId", c.applicationID)
	values.Set("connectId", connectID)
	values.Set("walletAddress", walletAddress)
	values.Set("supportedNetworks", joinOrDefault(req.SupportedNetworks, defaultNetwork))
	values.Set("supportedAssets", joinOrDefault(req.SupportedAssets, defaultAsset))
	values.Set("redirectUrl", redirectURL)

	// An amount the caller already quoted is locked, so the user cannot buy a
	// different size than the one the rest of the flow was sized for.
	if amount := strings.TrimSpace(req.FiatAmount.String()); amount != "" {
		values.Set("fiatAmount", amount)
		values.Set("fiatCode", valueOrDefault(req.FiatCode, defaultFiatCode))
		values.Set("assetCode", valueOrDefault(req.AssetCode, defaultAssetCode))
		values.Set("lockAmount", "true")
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

// IsAvailable reports whether Robinhood Connect serves a user standing in the
// given country and region. Robinhood Connect is US-only and does not operate
// in New York. An unresolved country fails closed: showing the rail and failing
// at handoff is worse than not showing it.
func IsAvailable(country, region string) bool {
	return strings.EqualFold(strings.TrimSpace(country), "US") &&
		!strings.EqualFold(strings.TrimSpace(region), "NY")
}

// NewReferenceID mints the partner-side handle for one transfer attempt. The
// shape matches what the mobile app has been generating client-side: the
// address tail keeps it greppable, the timestamp orders it, and the random tail
// keeps two rapid attempts from colliding.
func NewReferenceID(walletAddress string) (string, error) {
	walletAddress = strings.TrimSpace(walletAddress)
	if walletAddress == "" {
		return "", errors.New("robinhood reference id: wallet address is required")
	}

	suffix, err := randomBase36(8)
	if err != nil {
		return "", fmt.Errorf("robinhood reference id: %w", err)
	}
	shortAddress := strings.ToLower(tail(walletAddress, 8))
	return fmt.Sprintf("%s_%s_%s", shortAddress, strconv.FormatInt(time.Now().UnixMilli(), 36), suffix), nil
}

func tail(value string, n int) string {
	if len(value) <= n {
		return value
	}
	return value[len(value)-n:]
}

const base36Alphabet = "0123456789abcdefghijklmnopqrstuvwxyz"

func randomBase36(n int) (string, error) {
	out := make([]byte, n)
	max := big.NewInt(int64(len(base36Alphabet)))
	for i := range out {
		idx, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", fmt.Errorf("generate random suffix: %w", err)
		}
		out[i] = base36Alphabet[idx.Int64()]
	}
	return string(out), nil
}

func joinOrDefault(values []string, fallback string) string {
	cleaned := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			cleaned = append(cleaned, value)
		}
	}
	if len(cleaned) == 0 {
		return fallback
	}
	return strings.Join(cleaned, ",")
}

func valueOrDefault(value, fallback string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return fallback
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
