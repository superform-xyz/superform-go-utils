// Package axiom implements a client for the Superform Axiom valuation oracle.
//
// API: {baseURL}/v1/price/{chainID}/{tokenAddress}
// Response shape: {"value": "1850.500000000000000000"}
package axiom

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

const defaultTimeout = 5 * time.Second

var (
	ErrTokenNotFound    = errors.New("token not found")
	ErrUnauthorized     = errors.New("unauthorized")
	ErrRateLimited      = errors.New("rate limited")
	ErrUnsupportedChain = errors.New("unsupported chain")
	ErrUpstream         = errors.New("upstream error")
)

// Axiom represents the behavior of the Axiom valuation oracle.
type Axiom interface {
	HealthCheck(ctx context.Context) error
	GetTokenPrice(ctx context.Context, chainID uint64, tokenAddress common.Address) (*TokenPrice, error)
	SupportedChains() []uint64
	Close() error
}

type axiom struct {
	baseURL         string
	client          *http.Client
	supportedChains map[uint64]struct{}
}

var _ Axiom = (*axiom)(nil)

type Option func(*axiom)

func WithHTTPClient(client *http.Client) Option {
	return func(a *axiom) {
		if client != nil {
			a.client = client
		}
	}
}

func WithSupportedChains(chainIDs []uint64) Option {
	return func(a *axiom) {
		a.supportedChains = make(map[uint64]struct{}, len(chainIDs))
		for _, chainID := range chainIDs {
			a.supportedChains[chainID] = struct{}{}
		}
	}
}

// New creates a new Axiom valuation oracle client.
func New(baseURL string, opts ...Option) Axiom {
	a := &axiom{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		client:  &http.Client{Timeout: defaultTimeout},
	}
	WithSupportedChains(supportedChainIDs())(a)
	for _, opt := range opts {
		if opt != nil {
			opt(a)
		}
	}
	return a
}

func (a *axiom) HealthCheck(ctx context.Context) error {
	_, err := a.GetTokenPrice(ctx, 1, common.HexToAddress("0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"))
	if err != nil && !errors.Is(err, ErrTokenNotFound) {
		return err
	}
	return nil
}

func (a *axiom) GetTokenPrice(ctx context.Context, chainID uint64, tokenAddress common.Address) (*TokenPrice, error) {
	if !a.supportsChain(chainID) {
		return nil, fmt.Errorf("%w: %d", ErrUnsupportedChain, chainID)
	}
	if ctx == nil {
		ctx = context.Background()
	}

	endpoint := fmt.Sprintf("%s/v1/price/%d/%s", a.baseURL, chainID, strings.ToLower(tokenAddress.Hex()))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("axiom: build request: %w", err)
	}

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("axiom: http: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := decodeStatus(resp); err != nil {
		return nil, err
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("axiom: read response: %w", err)
	}

	var result priceResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("axiom: decode: %w", err)
	}

	price, err := strconv.ParseFloat(result.Value, 64)
	if err != nil {
		return nil, fmt.Errorf("axiom: parse value %q: %w", result.Value, err)
	}
	if price == 0 {
		return nil, ErrTokenNotFound
	}

	return &TokenPrice{
		ChainID:   chainID,
		Address:   tokenAddress,
		Price:     price,
		UpdatedAt: time.Now(),
	}, nil
}

func (a *axiom) SupportedChains() []uint64 {
	supportedChains := make([]uint64, 0, len(a.supportedChains))
	for chainID := range a.supportedChains {
		supportedChains = append(supportedChains, chainID)
	}
	return supportedChains
}

func (a *axiom) Close() error {
	a.client.CloseIdleConnections()
	return nil
}

func (a *axiom) supportsChain(chainID uint64) bool {
	_, ok := a.supportedChains[chainID]
	return ok
}

func supportedChainIDs() []uint64 {
	chains := make([]uint64, 0, len(defaultSupportedChains))
	for chainID := range defaultSupportedChains {
		chains = append(chains, chainID)
	}
	return chains
}

func decodeStatus(resp *http.Response) error {
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return ErrUnauthorized
	}
	if resp.StatusCode == http.StatusNotFound {
		return ErrTokenNotFound
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return fmt.Errorf("axiom: status 429: %w", ErrRateLimited)
	}
	if resp.StatusCode/100 == 2 {
		return nil
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	return fmt.Errorf("axiom: status %d: %s: %w", resp.StatusCode, strings.TrimSpace(string(body)), ErrUpstream)
}
