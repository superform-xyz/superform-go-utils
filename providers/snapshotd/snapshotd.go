// Package snapshotd implements an authenticated client for the SuperVault snapshot service.
package snapshotd

import (
	"bytes"
	"context"
	"encoding/hex"
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

	"github.com/ethereum/go-ethereum/common"
	"github.com/golang-jwt/jwt/v5"
)

const (
	defaultTimeout      = 10 * time.Second
	maxResponseBody     = 4 << 20
	maxErrorResponse    = 4 << 10
	minimumSecretLength = 32
)

var (
	ErrBadRequest      = errors.New("snapshotd bad request")
	ErrUnauthorized    = errors.New("snapshotd unauthorized")
	ErrNotFound        = errors.New("snapshotd not found")
	ErrRateLimited     = errors.New("snapshotd rate limited")
	ErrUpstream        = errors.New("snapshotd upstream error")
	ErrInvalidResponse = errors.New("snapshotd invalid response")
)

type client struct {
	baseURL      string
	jwtSecretHex string
	jwtSecret    []byte
	httpClient   *http.Client
	timeout      time.Duration
	now          func() time.Time
}

var _ Client = (*client)(nil)

// Option customizes a snapshotd client.
type Option func(*client)

// WithBaseURL configures the snapshotd service URL.
func WithBaseURL(baseURL string) Option {
	return func(c *client) {
		c.baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	}
}

// WithJWTSecretHex configures the hex-encoded HS256 signing secret.
func WithJWTSecretHex(secret string) Option {
	return func(c *client) {
		c.jwtSecretHex = strings.TrimSpace(secret)
	}
}

// WithHTTPClient injects an HTTP client.
func WithHTTPClient(httpClient *http.Client) Option {
	return func(c *client) {
		if httpClient != nil {
			c.httpClient = httpClient
		}
	}
}

// WithTimeout configures the default HTTP client timeout.
func WithTimeout(timeout time.Duration) Option {
	return func(c *client) {
		c.timeout = timeout
	}
}

func withClock(now func() time.Time) Option {
	return func(c *client) {
		if now != nil {
			c.now = now
		}
	}
}

// New creates a snapshotd client. A base URL and JWT secret are required.
func New(opts ...Option) (Client, error) {
	c := &client{
		timeout: defaultTimeout,
		now:     time.Now,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}

	if err := validateBaseURL(c.baseURL); err != nil {
		return nil, err
	}
	secret, err := decodeJWTSecret(c.jwtSecretHex)
	if err != nil {
		return nil, err
	}
	c.jwtSecret = secret
	c.jwtSecretHex = ""

	if c.httpClient == nil {
		if c.timeout <= 0 {
			return nil, errors.New("snapshotd: timeout must be positive")
		}
		c.httpClient = &http.Client{Timeout: c.timeout}
	}

	return c, nil
}

// GetPPS returns the integer-scaled calculated price per share for a strategy.
func (c *client) GetPPS(ctx context.Context, query Query) (*PPSResult, error) {
	if err := validateQuery(query); err != nil {
		return nil, err
	}

	var result PPSResult
	if err := c.getJSON(ctx, c.endpoint("pps", query), &result); err != nil {
		return nil, fmt.Errorf("snapshotd get PPS: %w", err)
	}
	if err := validateDecimal("pps", result.PPS); err != nil {
		return nil, err
	}
	return &result, nil
}

// GetAllocation returns the vault-asset-normalized allocation for a strategy.
func (c *client) GetAllocation(ctx context.Context, query Query) (*Allocation, error) {
	if err := validateQuery(query); err != nil {
		return nil, err
	}

	var result Allocation
	if err := c.getJSON(ctx, c.endpoint("allocation", query), &result); err != nil {
		return nil, fmt.Errorf("snapshotd get allocation: %w", err)
	}
	if err := validateAllocation(query, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// Close closes idle HTTP connections held by the client.
func (c *client) Close() error {
	if c.httpClient != nil {
		c.httpClient.CloseIdleConnections()
	}
	return nil
}

func (c *client) endpoint(resource string, query Query) string {
	endpoint := fmt.Sprintf("%s/v1/%s/%d/%s", c.baseURL, resource, query.ChainID, strings.ToLower(query.Strategy.Hex()))
	if query.BlockNumber != nil {
		endpoint += "?block=" + strconv.FormatUint(*query.BlockNumber, 10)
	}
	return endpoint
}

func (c *client) getJSON(ctx context.Context, endpoint string, out any) error {
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	token, err := c.mintJWT()
	if err != nil {
		return fmt.Errorf("mint JWT: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return decodeHTTPError(resp)
	}
	body, err := readBounded(resp.Body, maxResponseBody)
	if err != nil {
		return fmt.Errorf("read response: %w (%v)", ErrInvalidResponse, err)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode response: %w (%v)", ErrInvalidResponse, err)
	}
	return nil
}

func (c *client) mintJWT() (string, error) {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"iat": c.now().Unix(),
	})
	return token.SignedString(c.jwtSecret)
}

func validateBaseURL(baseURL string) error {
	if baseURL == "" {
		return errors.New("snapshotd: base URL is required")
	}
	parsed, err := url.ParseRequestURI(baseURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("snapshotd: invalid base URL %q", baseURL)
	}
	return nil
}

func decodeJWTSecret(secretHex string) ([]byte, error) {
	secretHex = strings.TrimPrefix(strings.TrimSpace(secretHex), "0x")
	if secretHex == "" {
		return nil, errors.New("snapshotd: JWT secret is required")
	}
	secret, err := hex.DecodeString(secretHex)
	if err != nil {
		return nil, fmt.Errorf("snapshotd: decode JWT secret: %w", err)
	}
	if len(secret) < minimumSecretLength {
		return nil, fmt.Errorf("snapshotd: JWT secret is %d bytes, want at least %d", len(secret), minimumSecretLength)
	}
	if bytes.Equal(secret, make([]byte, len(secret))) {
		return nil, errors.New("snapshotd: JWT secret cannot be all zeros")
	}
	return append([]byte(nil), secret...), nil
}

func validateQuery(query Query) error {
	if query.ChainID == 0 {
		return errors.New("snapshotd: chain ID must be positive")
	}
	if query.Strategy == (common.Address{}) {
		return errors.New("snapshotd: strategy is required")
	}
	return nil
}

func validateAllocation(query Query, allocation *Allocation) error {
	if allocation.Strategy != query.Strategy {
		return fmt.Errorf("snapshotd: allocation strategy %s does not match request %s: %w", allocation.Strategy.Hex(), query.Strategy.Hex(), ErrInvalidResponse)
	}
	if allocation.ChainID != query.ChainID {
		return fmt.Errorf("snapshotd: allocation chain ID %d does not match request %d: %w", allocation.ChainID, query.ChainID, ErrInvalidResponse)
	}
	if query.BlockNumber != nil && (allocation.BlockNumber == nil || *allocation.BlockNumber != *query.BlockNumber) {
		return fmt.Errorf("snapshotd: allocation block does not match requested block %d: %w", *query.BlockNumber, ErrInvalidResponse)
	}
	if allocation.Asset == (common.Address{}) {
		return fmt.Errorf("snapshotd: allocation asset is required: %w", ErrInvalidResponse)
	}
	for field, value := range map[string]string{
		"totalAssets": allocation.TotalAssets,
		"totalSupply": allocation.TotalSupply,
		"idleBalance": allocation.IdleBalance,
	} {
		if err := validateDecimal(field, value); err != nil {
			return err
		}
	}
	for i := range allocation.Sources {
		source := &allocation.Sources[i]
		if source.Source == (common.Address{}) {
			return fmt.Errorf("snapshotd: source %d address is required: %w", i, ErrInvalidResponse)
		}
		if source.Oracle == (common.Address{}) {
			return fmt.Errorf("snapshotd: source %d oracle is required: %w", i, ErrInvalidResponse)
		}
		if source.Error != "" {
			return fmt.Errorf("snapshotd: source %s failed: %s: %w", source.Source.Hex(), source.Error, ErrInvalidResponse)
		}
		if source.RawShares != "" {
			if err := validateDecimal(fmt.Sprintf("sources[%d].rawShares", i), source.RawShares); err != nil {
				return err
			}
		}
		if source.AssetTVL == "" && !source.Active {
			source.AssetTVL = "0"
		}
		if err := validateDecimal(fmt.Sprintf("sources[%d].assetTvl", i), source.AssetTVL); err != nil {
			return err
		}
	}
	return nil
}

func validateDecimal(field, value string) error {
	if value == "" {
		return fmt.Errorf("snapshotd: %s is required: %w", field, ErrInvalidResponse)
	}
	parsed, ok := new(big.Int).SetString(value, 10)
	if !ok || parsed.Sign() < 0 {
		return fmt.Errorf("snapshotd: %s is not a non-negative decimal integer: %w", field, ErrInvalidResponse)
	}
	return nil
}

func decodeHTTPError(resp *http.Response) error {
	body, readErr := readBounded(resp.Body, maxErrorResponse)
	if readErr != nil {
		return &HTTPError{
			StatusCode: resp.StatusCode,
			Message:    readErr.Error(),
			Err:        httpErrorSentinel(resp.StatusCode),
		}
	}

	var envelope errorEnvelope
	_ = json.Unmarshal(body, &envelope)
	message := strings.TrimSpace(envelope.Error.Message)
	if message == "" {
		message = strings.TrimSpace(string(body))
	}
	return &HTTPError{
		StatusCode: resp.StatusCode,
		Code:       envelope.Error.Code,
		Message:    message,
		RequestID:  envelope.Error.RequestID,
		Err:        httpErrorSentinel(resp.StatusCode),
	}
}

func readBounded(reader io.Reader, limit int64) ([]byte, error) {
	limited := &io.LimitedReader{R: reader, N: limit + 1}
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("response body exceeds %d bytes", limit)
	}
	return body, nil
}
