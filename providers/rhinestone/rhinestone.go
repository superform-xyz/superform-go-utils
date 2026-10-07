// Package rhinestone implements stateless discovery for the Rhinestone deposit
// service. Caching, trusted geography and payment eligibility belong to callers.
package rhinestone

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultAPIBaseURL = "https://v1.orchestrator.rhinestone.dev/deposit-processor"
	defaultTimeout    = 5 * time.Second
	maxResponseBody   = 8 << 20
)

// Client reads discovery data without creating orders or payments.
type Client interface {
	GetOnrampOptions(context.Context, GetOnrampOptionsRequest) (*GetOnrampOptionsResponse, error)
	GetChains(context.Context) (map[string]Chain, error)
	Close() error
}

type client struct {
	apiKey, apiBaseURL string
	httpClient         *http.Client
}

type Option func(*client)

func WithAPIKey(key string) Option {
	return func(c *client) { c.apiKey = strings.TrimSpace(key) }
}

// WithAPIBaseURL overrides the deposit-service root, including its path prefix.
func WithAPIBaseURL(baseURL string) Option {
	return func(c *client) { c.apiBaseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/") }
}

// WithHTTPClient injects transport and timeouts. Redirects remain disabled to
// keep the project credential and asserted country on the configured endpoint.
// The supplied client is copied, never modified.
func WithHTTPClient(httpClient *http.Client) Option {
	return func(c *client) { c.httpClient = httpClient }
}

func New(opts ...Option) (Client, error) {
	c := &client{apiBaseURL: defaultAPIBaseURL}
	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}
	if c.apiKey == "" || strings.ContainsAny(c.apiKey, "\r\n") {
		return nil, errors.New("rhinestone: valid api key is required")
	}
	u, err := url.Parse(c.apiBaseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("rhinestone: invalid api base url")
	}
	httpClient := http.Client{Timeout: defaultTimeout}
	if c.httpClient != nil {
		httpClient = *c.httpClient
	}
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	c.httpClient = &httpClient
	return c, nil
}

// APIError contains only the HTTP status; vendor response bodies may contain
// sensitive data and are deliberately excluded from errors.
type APIError struct {
	StatusCode int
}

func (e *APIError) Error() string { return fmt.Sprintf("rhinestone: HTTP %d", e.StatusCode) }

func (c *client) GetOnrampOptions(ctx context.Context, input GetOnrampOptionsRequest) (*GetOnrampOptionsResponse, error) {
	if input.Provider == "" || input.Kind == "" {
		return nil, errors.New("rhinestone: provider and kind are required")
	}
	if input.Country != "" && (len(input.Country) != 2 || strings.IndexFunc(input.Country, func(r rune) bool { return r < 'A' || r > 'Z' }) >= 0) {
		return nil, errors.New("rhinestone: country must be an uppercase two-letter code")
	}
	query := url.Values{"provider": {input.Provider}, "kind": {input.Kind}}
	var out GetOnrampOptionsResponse
	if err := c.read(ctx, "/onramp/options?"+query.Encode(), input.Country, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *client) GetChains(ctx context.Context) (map[string]Chain, error) {
	var out map[string]Chain
	if err := c.read(ctx, "/chains", "", &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *client) read(ctx context.Context, path, country string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiBaseURL+path, nil)
	if err != nil {
		return errors.New("rhinestone: invalid request")
	}
	req.Header.Set("x-api-key", c.apiKey)
	if country != "" {
		req.Header.Set("x-user-country", country)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("rhinestone: request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return &APIError{StatusCode: resp.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody+1))
	if err != nil || len(body) > maxResponseBody {
		return errors.New("rhinestone: invalid response body")
	}
	if err := json.Unmarshal(body, out); err != nil {
		return errors.New("rhinestone: invalid response json")
	}
	return nil
}

func (c *client) Close() error {
	c.httpClient.CloseIdleConnections()
	return nil
}
