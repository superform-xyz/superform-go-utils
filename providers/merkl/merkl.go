// Package merkl implements a generic client for Merkl's public API.
package merkl

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/superform-xyz/superform-go-utils/pkg/http_client"
)

const (
	defaultBaseURL        = "https://api.merkl.xyz"
	apiKeyHeader          = "X-API-Key"
	rootsLivePath         = "/v4/roots/live"
	opportunitiesPath     = "/v4/opportunities"
	userRewardsPath       = "/v4/users/%s/rewards"
	defaultItemsLimit     = 100
	defaultMaxRetries     = uint(4)
	defaultRetryDelay     = 2 * time.Second
	maxResponseBody       = 8 << 20
	maxErrorResponseBytes = 512
)

// Client defines the generic Merkl API surface shared across backend services.
type Client interface {
	// GetOpportunities retrieves Merkl opportunities for a chain and optionally filters by main protocol.
	GetOpportunities(ctx context.Context, chainID uint64, limit int, mainProtocolID string) ([]Opportunity, error)
	// GetUserRewards retrieves claimable rewards for a user, optionally filtered by chain.
	GetUserRewards(ctx context.Context, user string, chainID uint64) ([]UserRewardsChain, error)
	// GetLiveRoots retrieves the current live Merkl root per chain.
	GetLiveRoots(ctx context.Context) (map[string]RootInfo, error)
	Close() error
}

type client struct {
	baseURL    string
	apiKey     string
	httpClient *http_client.Client
}

var _ Client = (*client)(nil)

// Option customizes the Merkl client.
type Option func(*client)

// WithBaseURL overrides the default Merkl API base URL.
func WithBaseURL(baseURL string) Option {
	return func(c *client) {
		baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
		if baseURL != "" {
			c.baseURL = baseURL
		}
	}
}

// WithHTTPClient injects a custom http.Client.
func WithHTTPClient(httpClient *http.Client) Option {
	return func(c *client) {
		if httpClient != nil {
			c.httpClient = &http_client.Client{Client: httpClient}
		}
	}
}

// WithAPIKey sets the optional X-API-Key header used by Merkl for higher quotas.
func WithAPIKey(apiKey string) Option {
	return func(c *client) {
		c.apiKey = strings.TrimSpace(apiKey)
	}
}

// New creates a Merkl API client.
func New(opts ...Option) (Client, error) {
	c := &client{
		baseURL: defaultBaseURL,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}
	if c.httpClient == nil {
		builder := http_client.NewClientBuilder().SetRetry(defaultMaxRetries, defaultRetryDelay)
		if c.apiKey != "" {
			builder = builder.SetAuth(apiKeyHeader, c.apiKey)
		}
		c.httpClient = builder.BuildClient()
	}

	return c, nil
}

// GetOpportunities retrieves Merkl opportunities filtered by chain and optional main protocol.
func (c *client) GetOpportunities(ctx context.Context, chainID uint64, limit int, mainProtocolID string) ([]Opportunity, error) {
	if limit <= 0 {
		limit = defaultItemsLimit
	}

	var all []Opportunity
	page := 0
	for {
		values := url.Values{}
		values.Set("items", strconv.Itoa(limit))
		values.Set("page", strconv.Itoa(page))
		if chainID != 0 {
			values.Set("chainId", strconv.FormatUint(chainID, 10))
		}
		if mainProtocolID = strings.TrimSpace(mainProtocolID); mainProtocolID != "" {
			values.Set("mainProtocolId", mainProtocolID)
		}

		var pageItems []Opportunity
		if err := c.getJSON(ctx, opportunitiesPath, values, &pageItems); err != nil {
			return nil, fmt.Errorf("merkl opportunities: %w", err)
		}
		all = append(all, pageItems...)
		if len(pageItems) < limit {
			break
		}

		page++
		if page > 1000 {
			return all, fmt.Errorf("merkl pagination exceeded 1000 pages for chain %d", chainID)
		}
	}

	return all, nil
}

// GetUserRewards retrieves claimable rewards for a user, optionally filtered by chain.
func (c *client) GetUserRewards(ctx context.Context, user string, chainID uint64) ([]UserRewardsChain, error) {
	user = strings.TrimSpace(user)
	if user == "" {
		return nil, fmt.Errorf("merkl user rewards requires user address")
	}

	values := url.Values{}
	if chainID != 0 {
		values.Set("chainId", strconv.FormatUint(chainID, 10))
	}

	var out []UserRewardsChain
	path := fmt.Sprintf(userRewardsPath, url.PathEscape(user))
	if err := c.getJSON(ctx, path, values, &out); err != nil {
		return nil, fmt.Errorf("merkl user rewards: %w", err)
	}
	return out, nil
}

// GetLiveRoots retrieves the current live Merkl root per chain.
func (c *client) GetLiveRoots(ctx context.Context) (map[string]RootInfo, error) {
	out := make(map[string]RootInfo)
	if err := c.getJSON(ctx, rootsLivePath, nil, &out); err != nil {
		return nil, fmt.Errorf("merkl live roots: %w", err)
	}
	return out, nil
}

func (c *client) Close() error {
	if c.httpClient != nil {
		c.httpClient.CloseIdleConnections()
	}
	return nil
}

func (c *client) getJSON(ctx context.Context, path string, values url.Values, out any) error {
	if ctx == nil {
		ctx = context.Background()
	}

	endpoint := c.baseURL + path
	if len(values) > 0 {
		endpoint += "?" + values.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if c.apiKey != "" {
		req.Header.Set(apiKeyHeader, c.apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorResponseBytes))
		return fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBody)).Decode(out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}
