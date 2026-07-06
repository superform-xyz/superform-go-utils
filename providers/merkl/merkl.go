// Package merkl implements a generic client for Merkl's public API.
package merkl

import (
	"bytes"
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
	"golang.org/x/time/rate"
)

const (
	defaultBaseURL         = "https://api.merkl.xyz"
	apiKeyHeader           = "X-API-Key"
	rootsLivePath          = "/v4/roots/live"
	opportunitiesPath      = "/v4/opportunities"
	opportunitiesCountPath = "/v4/opportunities/count"
	userRewardsPath        = "/v4/users/%s/rewards"
	defaultItemsLimit      = 100
	defaultMaxRetries      = uint(4)
	defaultRetryDelay      = 2 * time.Second
	maxResponseBody        = 8 << 20
	maxErrorResponseBytes  = 512
	maxOpportunityPages    = 1000
)

// Client defines the generic Merkl API surface shared across backend services.
type Client interface {
	// GetOpportunities retrieves Merkl opportunities for a chain and optionally filters by main protocol.
	GetOpportunities(ctx context.Context, chainID uint64, limit int, mainProtocolID string) ([]Opportunity, error)
	// ListOpportunities fetches one Merkl opportunities page.
	ListOpportunities(ctx context.Context, query OpportunityQuery) ([]Opportunity, error)
	// ListAllOpportunities fetches all pages for the provided opportunity filters.
	ListAllOpportunities(ctx context.Context, query OpportunityQuery) ([]Opportunity, error)
	// CountOpportunities returns the number of opportunities matching the provided filters.
	CountOpportunities(ctx context.Context, query OpportunityCountQuery) (int, error)
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
	limiter    *rate.Limiter
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

// WithRateLimit limits outbound Merkl API requests when qps is positive.
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
	return c.ListAllOpportunities(ctx, OpportunityQuery{
		ChainID:        chainID,
		Items:          limit,
		MainProtocolID: mainProtocolID,
	})
}

// ListOpportunities fetches one Merkl opportunities page.
func (c *client) ListOpportunities(ctx context.Context, query OpportunityQuery) ([]Opportunity, error) {
	values, _, err := opportunityQueryValues(query)
	if err != nil {
		return nil, err
	}

	var out []Opportunity
	if err := c.getJSON(ctx, opportunitiesPath, values, &out); err != nil {
		return nil, fmt.Errorf("merkl opportunities: %w", err)
	}
	return out, nil
}

// ListAllOpportunities fetches all pages for the provided opportunity filters.
func (c *client) ListAllOpportunities(ctx context.Context, query OpportunityQuery) ([]Opportunity, error) {
	_, items, err := opportunityQueryValues(query)
	if err != nil {
		return nil, err
	}

	var all []Opportunity
	page := query.Page
	for pages := 0; ; pages++ {
		if pages >= maxOpportunityPages {
			return all, fmt.Errorf("merkl pagination exceeded %d pages", maxOpportunityPages)
		}
		query.Page = page
		query.Items = items

		pageItems, err := c.ListOpportunities(ctx, query)
		if err != nil {
			return nil, err
		}

		all = append(all, pageItems...)
		if len(pageItems) < items {
			break
		}
		page++
	}

	return all, nil
}

// CountOpportunities returns the number of opportunities matching the provided filters.
func (c *client) CountOpportunities(ctx context.Context, query OpportunityCountQuery) (int, error) {
	body, err := c.getBody(ctx, opportunitiesCountPath, opportunityCountQueryValues(query))
	if err != nil {
		return 0, fmt.Errorf("merkl opportunities count: %w", err)
	}

	count, err := strconv.Atoi(strings.TrimSpace(string(body)))
	if err != nil {
		return 0, fmt.Errorf("parse merkl opportunities count %q: %w", body, err)
	}
	return count, nil
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

func opportunityQueryValues(query OpportunityQuery) (url.Values, int, error) {
	items, err := normalizeOpportunityItems(query.Items)
	if err != nil {
		return nil, 0, err
	}
	if query.Page < 0 {
		return nil, 0, fmt.Errorf("merkl opportunities page must be >= 0, got %d", query.Page)
	}

	values := url.Values{}
	values.Set("items", strconv.Itoa(items))
	values.Set("page", strconv.Itoa(query.Page))
	if query.ChainID != 0 {
		values.Set("chainId", strconv.FormatUint(query.ChainID, 10))
	}
	if mainProtocolID := strings.TrimSpace(query.MainProtocolID); mainProtocolID != "" {
		values.Set("mainProtocolId", mainProtocolID)
	}
	if status := strings.TrimSpace(query.Status); status != "" {
		values.Set("status", status)
	}
	if query.Campaigns {
		values.Set("campaigns", "true")
	}

	return values, items, nil
}

func opportunityCountQueryValues(query OpportunityCountQuery) url.Values {
	values := url.Values{}
	if query.ChainID != 0 {
		values.Set("chainId", strconv.FormatUint(query.ChainID, 10))
	}
	if mainProtocolID := strings.TrimSpace(query.MainProtocolID); mainProtocolID != "" {
		values.Set("mainProtocolId", mainProtocolID)
	}
	if status := strings.TrimSpace(query.Status); status != "" {
		values.Set("status", status)
	}
	return values
}

func normalizeOpportunityItems(items int) (int, error) {
	if items <= 0 {
		return defaultItemsLimit, nil
	}
	if items > MaxOpportunityItems {
		return 0, fmt.Errorf("merkl opportunities items must be in [1, %d], got %d", MaxOpportunityItems, items)
	}
	return items, nil
}

func (c *client) getJSON(ctx context.Context, path string, values url.Values, out any) error {
	body, err := c.getBody(ctx, path, values)
	if err != nil {
		return err
	}
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func (c *client) getBody(ctx context.Context, path string, values url.Values) ([]byte, error) {
	resp, err := c.doGET(ctx, path, values)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorResponseBytes))
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	return body, nil
}

func (c *client) doGET(ctx context.Context, path string, values url.Values) (*http.Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if c.limiter != nil {
		if err := c.limiter.Wait(ctx); err != nil {
			return nil, fmt.Errorf("rate limit wait: %w", err)
		}
	}

	endpoint := c.baseURL + path
	if len(values) > 0 {
		endpoint += "?" + values.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if c.apiKey != "" {
		req.Header.Set(apiKeyHeader, c.apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	return resp, nil
}
