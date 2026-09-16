// Package coinbase implements a stateless client for the Coinbase Developer
// Platform (CDP) Onramp APIs: minting hosted-widget session tokens, reading a
// partner user's buy transactions, and building the hosted buy URL.
//
// The package is transport only: configuration and policy belong to the caller.
package coinbase

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
	productionAPIBaseURL = "https://api.developer.coinbase.com"
	productionBuyBaseURL = "https://pay.coinbase.com/buy/select-asset"

	createSessionTokenPath = "/onramp/v1/token"
	buyTransactionsPathFmt = "/onramp/v1/buy/user/%s/transactions"

	// MaxBuyTransactionsPageSize is the largest page CDP documents. The client
	// does not enforce it.
	MaxBuyTransactionsPageSize = 50

	defaultTimeout       = 15 * time.Second
	maxResponseBody      = 4 << 20
	maxErrorResponseBody = 1024
)

var (
	ErrUnauthorized = errors.New("coinbase unauthorized")
	ErrRateLimited  = errors.New("coinbase rate limited")
	ErrNotFound     = errors.New("coinbase not found")
	ErrBadRequest   = errors.New("coinbase bad request")
	ErrUpstream     = errors.New("coinbase upstream error")
)

// APIError wraps non-2xx CDP responses with a stable sentinel.
type APIError struct {
	StatusCode int
	Body       string
	Err        error
}

func (e *APIError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("coinbase status %d: %v", e.StatusCode, e.Err)
	}
	return fmt.Sprintf("coinbase status %d: %s: %v", e.StatusCode, e.Body, e.Err)
}

func (e *APIError) Unwrap() error {
	return e.Err
}

// Client defines the stateless CDP Onramp surface.
type Client interface {
	CreateSessionToken(ctx context.Context, req CreateSessionTokenRequest) (*CreateSessionTokenResponse, error)
	GetBuyTransactions(ctx context.Context, req GetBuyTransactionsRequest) (*GetBuyTransactionsResponse, error)
	BuildBuyURL(req BuildBuyURLRequest) (string, error)
	Close() error
}

type client struct {
	apiKeyID     string
	apiKeySecret string
	signer       apiKeySigner
	projectID    string
	apiBaseURL   string
	apiHost      string
	buyBaseURL   string
	httpClient   *http.Client
}

var _ Client = (*client)(nil)

// Option customizes the Coinbase client.
type Option func(*client)

// WithAPIKeyID sets the CDP API key id (the key name from the CDP portal).
func WithAPIKeyID(apiKeyID string) Option {
	return func(c *client) {
		c.apiKeyID = strings.TrimSpace(apiKeyID)
	}
}

// WithAPIKeySecret sets the CDP API key secret. Both formats the portal issues
// are accepted: a base64 Ed25519 key or a PEM-encoded EC private key.
func WithAPIKeySecret(secret string) Option {
	return func(c *client) {
		c.apiKeySecret = secret
	}
}

// WithProjectID sets the CDP project id, emitted as the widget's appId when set.
func WithProjectID(projectID string) Option {
	return func(c *client) {
		c.projectID = strings.TrimSpace(projectID)
	}
}

// WithAPIBaseURL overrides the CDP API host.
func WithAPIBaseURL(baseURL string) Option {
	return func(c *client) {
		if baseURL = trimBaseURL(baseURL); baseURL != "" {
			c.apiBaseURL = baseURL
		}
	}
}

// WithBuyBaseURL overrides the hosted widget URL.
func WithBuyBaseURL(baseURL string) Option {
	return func(c *client) {
		if baseURL = trimBaseURL(baseURL); baseURL != "" {
			c.buyBaseURL = baseURL
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

// New creates a Coinbase CDP client.
func New(opts ...Option) (Client, error) {
	c := &client{
		apiBaseURL: productionAPIBaseURL,
		buyBaseURL: productionBuyBaseURL,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}
	if c.apiKeyID == "" {
		return nil, errors.New("coinbase: api key id is required")
	}

	signer, err := parseAPIKeySecret(c.apiKeySecret)
	if err != nil {
		return nil, fmt.Errorf("coinbase: %w", err)
	}
	c.signer = signer
	// Only needed to build the signer; drop the plaintext copy.
	c.apiKeySecret = ""

	host, err := hostOf(c.apiBaseURL)
	if err != nil {
		return nil, fmt.Errorf("coinbase: api base url: %w", err)
	}
	c.apiHost = host

	if c.httpClient == nil {
		c.httpClient = &http.Client{Timeout: defaultTimeout}
	}
	return c, nil
}

// CreateSessionToken mints a single-use token binding a hosted onramp session to
// the destination addresses given here.
func (c *client) CreateSessionToken(ctx context.Context, req CreateSessionTokenRequest) (*CreateSessionTokenResponse, error) {
	if len(req.Addresses) == 0 {
		return nil, errors.New("coinbase create session token: at least one address is required")
	}
	for i, addr := range req.Addresses {
		if strings.TrimSpace(addr.Address) == "" {
			return nil, fmt.Errorf("coinbase create session token: address %d is empty", i)
		}
		if len(addr.Blockchains) == 0 {
			return nil, fmt.Errorf("coinbase create session token: address %d has no blockchains", i)
		}
	}

	payload := createSessionTokenRequest{Addresses: req.Addresses}
	if len(req.Assets) > 0 {
		payload.Assets = req.Assets
	}

	var out createSessionTokenResponse
	if err := c.doJSON(ctx, http.MethodPost, createSessionTokenPath, "", payload, &out); err != nil {
		return nil, fmt.Errorf("coinbase create session token: %w", err)
	}
	if out.Token == "" {
		return nil, errors.New("coinbase create session token: missing token")
	}

	// CDP has shipped this field under both spellings.
	channelID := out.ChannelID
	if channelID == "" {
		channelID = out.ChannelIDSnake
	}
	return &CreateSessionTokenResponse{Token: out.Token, ChannelID: channelID}, nil
}

// GetBuyTransactions reads one page of a partner user's buy transactions. Unset
// paging fields are omitted, leaving CDP's own defaults in force.
func (c *client) GetBuyTransactions(ctx context.Context, req GetBuyTransactionsRequest) (*GetBuyTransactionsResponse, error) {
	partnerUserRef := strings.TrimSpace(req.PartnerUserRef)
	if partnerUserRef == "" {
		return nil, errors.New("coinbase get buy transactions: partner user ref is required")
	}

	values := url.Values{}
	if req.PageSize > 0 {
		values.Set("page_size", strconv.Itoa(req.PageSize))
	}
	if pageKey := strings.TrimSpace(req.PageKey); pageKey != "" {
		values.Set("page_key", pageKey)
	}

	path := fmt.Sprintf(buyTransactionsPathFmt, url.PathEscape(partnerUserRef))
	var out getBuyTransactionsResponse
	if err := c.doJSON(ctx, http.MethodGet, path, values.Encode(), nil, &out); err != nil {
		return nil, fmt.Errorf("coinbase get buy transactions: %w", err)
	}

	return &GetBuyTransactionsResponse{
		Transactions: out.Transactions,
		TotalCount:   json.Number(out.TotalCount),
		NextPageKey:  out.NextPageKey,
	}, nil
}

// BuildBuyURL returns the hosted Coinbase Onramp URL for one purchase. It makes
// no network call: every optional field is emitted when set and omitted when
// not, against whichever host the client was configured with.
func (c *client) BuildBuyURL(req BuildBuyURLRequest) (string, error) {
	sessionToken := strings.TrimSpace(req.SessionToken)
	if sessionToken == "" {
		return "", errors.New("coinbase build buy url: session token is required")
	}

	endpoint, err := url.Parse(c.buyBaseURL)
	if err != nil {
		return "", fmt.Errorf("coinbase build buy url: %w", err)
	}

	values := url.Values{}
	values.Set("sessionToken", sessionToken)

	if c.projectID != "" {
		values.Set("appId", c.projectID)
	}
	setIfPresent(values, "partnerUserRef", req.PartnerUserRef)
	setIfPresent(values, "presetFiatAmount", req.PresetFiatAmount.String())
	setIfPresent(values, "fiatCurrency", req.FiatCurrency)
	setIfPresent(values, "defaultPaymentMethod", req.DefaultPaymentMethod)
	setIfPresent(values, "redirectUrl", req.RedirectURL)

	if len(req.Addresses) > 0 {
		encoded, err := json.Marshal(req.Addresses)
		if err != nil {
			return "", fmt.Errorf("coinbase build buy url: encode addresses: %w", err)
		}
		values.Set("addresses", string(encoded))
	}
	if len(req.Assets) > 0 {
		encoded, err := json.Marshal(req.Assets)
		if err != nil {
			return "", fmt.Errorf("coinbase build buy url: encode assets: %w", err)
		}
		values.Set("assets", string(encoded))
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

func (c *client) doJSON(ctx context.Context, method, path, rawQuery string, payload, out any) error {
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

	endpoint := c.apiBaseURL + path
	if rawQuery != "" {
		endpoint += "?" + rawQuery
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}

	// Scoped to this method, host and path; the query string is excluded, as
	// CDP verifies the uris claim.
	token, err := mintJWT(c.signer, c.apiKeyID, method, c.apiHost, path)
	if err != nil {
		return fmt.Errorf("mint jwt: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
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

func hostOf(baseURL string) (string, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("missing host in %q", baseURL)
	}
	return parsed.Host, nil
}

func trimBaseURL(baseURL string) string {
	return strings.TrimRight(strings.TrimSpace(baseURL), "/")
}

type createSessionTokenRequest struct {
	Addresses []SessionTokenAddress `json:"addresses"`
	Assets    []string              `json:"assets,omitempty"`
}

type createSessionTokenResponse struct {
	Token          string `json:"token"`
	ChannelID      string `json:"channelId"`
	ChannelIDSnake string `json:"channel_id"`
}

type getBuyTransactionsResponse struct {
	Transactions []BuyTransaction `json:"transactions"`
	TotalCount   jsonCountNumber  `json:"total_count"`
	NextPageKey  string           `json:"next_page_key"`
}
