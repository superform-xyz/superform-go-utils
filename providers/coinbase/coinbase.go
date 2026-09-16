// Package coinbase implements a stateless client for the Coinbase Developer
// Platform (CDP) Onramp APIs: minting hosted-widget session tokens, building
// the hosted buy URL, and reading a partner user's buy transactions.
//
// It exists so the credentials stay server-side. The mobile and web clients
// previously reached CDP through per-app route handlers; this package is the
// single Go implementation those backends share.
package coinbase

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
	productionAPIBaseURL = "https://api.developer.coinbase.com"

	// productionBuyBaseURL is the hosted widget entry point. The sandbox host
	// answers on an /buy/select-asset path instead of plain /buy.
	productionBuyBaseURL = "https://pay.coinbase.com/buy"
	sandboxBuyBaseURL    = "https://pay-sandbox.coinbase.com/buy/select-asset"

	createSessionTokenPath = "/onramp/v1/token"
	buyTransactionsPathFmt = "/onramp/v1/buy/user/%s/transactions"

	defaultFiatCurrency = "USD"

	// MaxBuyTransactionsPageSize is the largest page CDP will return.
	MaxBuyTransactionsPageSize = 50

	defaultBuyTransactionsPageSize = 1

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
	sandbox      bool
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

// WithProjectID sets the CDP project id, sent as the widget's appId. Required
// to build a production buy URL; unused in sandbox.
func WithProjectID(projectID string) Option {
	return func(c *client) {
		c.projectID = strings.TrimSpace(projectID)
	}
}

// WithSandbox routes the buy URL at Coinbase's sandbox widget host.
func WithSandbox(sandbox bool) Option {
	return func(c *client) {
		c.sandbox = sandbox
	}
}

// WithAPIBaseURL overrides the CDP API host for tests.
func WithAPIBaseURL(baseURL string) Option {
	return func(c *client) {
		if baseURL = trimBaseURL(baseURL); baseURL != "" {
			c.apiBaseURL = baseURL
		}
	}
}

// WithBuyBaseURL overrides the hosted widget URL for tests.
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
	c := &client{apiBaseURL: productionAPIBaseURL}
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
	// The secret is only ever needed to build the signer; drop the plaintext
	// copy so it cannot leak through a later dump of the struct.
	c.apiKeySecret = ""

	host, err := hostOf(c.apiBaseURL)
	if err != nil {
		return nil, fmt.Errorf("coinbase: api base url: %w", err)
	}
	c.apiHost = host

	if c.buyBaseURL == "" {
		c.buyBaseURL = productionBuyBaseURL
		if c.sandbox {
			c.buyBaseURL = sandboxBuyBaseURL
		}
	}
	if c.httpClient == nil {
		c.httpClient = &http.Client{Timeout: defaultTimeout}
	}
	return c, nil
}

// CreateSessionToken mints a single-use token binding a hosted onramp session
// to the destination addresses given here.
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

	// CDP has shipped this field under both spellings; take whichever arrived.
	channelID := out.ChannelID
	if channelID == "" {
		channelID = out.ChannelIDSnake
	}
	return &CreateSessionTokenResponse{Token: out.Token, ChannelID: channelID}, nil
}

// GetBuyTransactions reads one page of a partner user's buy transactions.
func (c *client) GetBuyTransactions(ctx context.Context, req GetBuyTransactionsRequest) (*GetBuyTransactionsResponse, error) {
	partnerUserRef := strings.TrimSpace(req.PartnerUserRef)
	if partnerUserRef == "" {
		return nil, errors.New("coinbase get buy transactions: partner user ref is required")
	}

	pageSize := req.PageSize
	if pageSize <= 0 {
		pageSize = defaultBuyTransactionsPageSize
	}
	if pageSize > MaxBuyTransactionsPageSize {
		return nil, fmt.Errorf("coinbase get buy transactions: page size %d exceeds max %d", pageSize, MaxBuyTransactionsPageSize)
	}

	values := url.Values{}
	values.Set("page_size", strconv.Itoa(pageSize))
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

// BuildBuyURL returns the hosted Coinbase Onramp URL for one purchase. It is
// pure — no network call — so a caller that already holds a session token can
// build the URL without another round trip.
//
// The parameter set mirrors what the widget is known to accept in each
// environment: production carries the project id, destination addresses and
// asset allowlist alongside the session token, while sandbox takes only the
// session token and the order's own fields.
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

	fiatCurrency := strings.TrimSpace(req.FiatCurrency)
	if fiatCurrency == "" {
		fiatCurrency = defaultFiatCurrency
	}
	values.Set("fiatCurrency", fiatCurrency)

	if ref := strings.TrimSpace(req.PartnerUserRef); ref != "" {
		values.Set("partnerUserRef", ref)
	}
	if amount := strings.TrimSpace(req.PresetFiatAmount.String()); amount != "" {
		values.Set("presetFiatAmount", amount)
	}
	if redirectURL := strings.TrimSpace(req.RedirectURL); redirectURL != "" {
		values.Set("redirectUrl", redirectURL)
	}

	if !c.sandbox {
		if c.projectID == "" {
			return "", errors.New("coinbase build buy url: project id is required outside sandbox")
		}
		values.Set("appId", c.projectID)

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
		if method := strings.TrimSpace(req.DefaultPaymentMethod); method != "" {
			values.Set("defaultPaymentMethod", method)
		}
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

	// The token is scoped to this exact method, host and path; the query string
	// is deliberately excluded, matching how CDP verifies the `uris` claim.
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

// NewPartnerUserRef mints the handle that ties a hosted buy session to the
// transactions endpoint that reads it back. The shape matches what the mobile
// app has been generating client-side: the address tail keeps it greppable, the
// timestamp orders it, and the random tail keeps two rapid "buy again" taps
// from colliding.
func NewPartnerUserRef(walletAddress string) (string, error) {
	walletAddress = strings.TrimSpace(walletAddress)
	if walletAddress == "" {
		return "", errors.New("coinbase partner user ref: wallet address is required")
	}

	suffix, err := randomBase36(4)
	if err != nil {
		return "", fmt.Errorf("coinbase partner user ref: %w", err)
	}
	return fmt.Sprintf("%s_%s_%s", tail(walletAddress, 8), strconv.FormatInt(time.Now().UnixMilli(), 36), suffix), nil
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
