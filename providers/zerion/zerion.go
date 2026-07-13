package zerion

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/pkg/errors"

	"github.com/superform-xyz/superform-go-utils/pkg/http_client"
)

const zerionBaseURL = "https://api.zerion.io/v1"

type Zerion interface {
	HealthCheck(ctx context.Context) error
	GetWalletPositions(ctx context.Context, address string, req WalletPositionsRequest) ([]Position, error)
	SupportedChains() []uint64
	Close() error
}

type zerion struct {
	apiKey     string
	baseURL    string
	client     *http_client.Client
	timeout    *time.Duration
	maxRetries *uint
	retryDelay *time.Duration
}

var _ Zerion = (*zerion)(nil)

type Option func(*zerion)

func WithBaseURL(baseURL string) Option {
	return func(z *zerion) {
		baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
		if baseURL != "" {
			z.baseURL = baseURL
		}
	}
}

func WithTimeout(timeout time.Duration) Option {
	return func(z *zerion) {
		z.timeout = &timeout
	}
}

func WithRetry(maxRetries uint, retryDelay time.Duration) Option {
	return func(z *zerion) {
		z.maxRetries = &maxRetries
		z.retryDelay = &retryDelay
	}
}

func New(apiKey string, opts ...Option) (Zerion, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, fmt.Errorf("zerion: apiKey is required")
	}
	z := &zerion{
		apiKey:  apiKey,
		baseURL: zerionBaseURL,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(z)
		}
	}
	if z.client == nil {
		builder := http_client.NewClientBuilder().SetRetry(0, 0)
		builder.SetAuth("Authorization", fmt.Sprintf("Basic %s", z.apiKey))
		if z.timeout != nil {
			builder.SetTimeout(*z.timeout)
		}
		if z.maxRetries != nil && z.retryDelay != nil {
			builder.SetRetry(*z.maxRetries, *z.retryDelay)
		}
		z.client = builder.BuildClient()
	}
	return z, nil
}

func (z *zerion) HealthCheck(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func (z *zerion) GetWalletPositions(ctx context.Context, address string, req WalletPositionsRequest) ([]Position, error) {
	address = strings.TrimSpace(address)
	if address == "" {
		return []Position{}, nil
	}

	u, err := url.Parse(fmt.Sprintf("%s/wallets/%s/positions/", z.baseURL, url.PathEscape(address)))
	if err != nil {
		return nil, err
	}

	q := u.Query()
	if chainFilter := walletPositionsChainFilter(req); chainFilter != "" {
		q.Set("filter[chain_ids]", chainFilter)
	}
	if req.PositionsFilter != "" {
		q.Set("filter[positions]", req.PositionsFilter)
	}
	if req.Currency != "" {
		q.Set("currency", req.Currency)
	}
	if req.TrashFilter != "" {
		q.Set("filter[trash]", req.TrashFilter)
	}
	if req.Sort != "" {
		q.Set("sort", req.Sort)
	}
	u.RawQuery = q.Encode()

	httpReq, err := z.newRequest(ctx, u.String())
	if err != nil {
		return nil, errors.Wrap(err, "failed to create zerion request")
	}

	resp, err := z.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.Body != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if err := decodeResponse(resp); err != nil {
		return nil, err
	}

	var parsed WalletPositionsResponse
	dec := json.NewDecoder(io.LimitReader(resp.Body, 64<<20))
	if err := dec.Decode(&parsed); err != nil {
		return nil, errors.Wrap(err, "failed to decode zerion wallet positions response")
	}

	return parsed.Data, nil
}

func (z *zerion) SupportedChains() []uint64 {
	return SupportedChains()
}

func (z *zerion) Close() error {
	z.client.CloseIdleConnections()
	return nil
}

func (z *zerion) newRequest(ctx context.Context, path string) (*http.Request, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	return req, nil
}

func walletPositionsChainFilter(req WalletPositionsRequest) string {
	if filter := ChainFilterFromSlugs(req.ChainSlugs); filter != "" {
		return filter
	}
	return ChainFilterFromChainIDs(req.ChainIDs)
}

func decodeResponse(resp *http.Response) error {
	if resp == nil {
		return fmt.Errorf("request failed: empty response")
	}
	if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
		return nil
	}
	body := ""
	if resp.Body != nil {
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if readErr != nil {
			return fmt.Errorf("request failed with status %d, error: %v", resp.StatusCode, readErr)
		}
		body = strings.TrimSpace(string(raw))
	}
	return fmt.Errorf("request failed with status %d, error: %s", resp.StatusCode, body)
}
