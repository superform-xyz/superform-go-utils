// Package everlong provides a client for the Everlong (foreverlong.io) Katana swap API.
package everlong

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/superform-xyz/superform-go-utils/pkg/http_client"
	"github.com/superform-xyz/superform-go-utils/utils/constants"
)

const baseURL = "https://swap.foreverlong.io"

var (
	ErrNoRoute      = errors.New("everlong: no route found")
	ErrPartialRoute = errors.New("everlong: route cannot fill the entire input amount")
)

// Client fetches exact-input quotes and unsigned swap transactions.
type Client interface {
	GetQuote(ctx context.Context, req QuoteRequest) (*Quote, error)
	GetSwap(ctx context.Context, req SwapRequest) (*Swap, error)
	Close() error
}

type everlong struct {
	baseURL string
	apiKey  string
	client  *http.Client
	timeout time.Duration
}

var _ Client = (*everlong)(nil)

type Option func(*everlong)

func WithBaseURL(baseURL string) Option {
	return func(e *everlong) { e.baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/") }
}

func WithAPIKey(apiKey string) Option {
	return func(e *everlong) { e.apiKey = strings.TrimSpace(apiKey) }
}

func WithHTTPClient(client *http.Client) Option {
	return func(e *everlong) { e.client = client }
}

func WithTimeout(timeout time.Duration) Option {
	return func(e *everlong) { e.timeout = timeout }
}

// New requires a production API key. The public documentation key is never a fallback.
func New(opts ...Option) (Client, error) {
	e := &everlong{baseURL: baseURL, timeout: 60 * time.Second}
	for _, opt := range opts {
		if opt != nil {
			opt(e)
		}
	}
	if e.apiKey == "" {
		return nil, fmt.Errorf("everlong: API key is required")
	}
	u, err := url.Parse(e.baseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("everlong: invalid base URL")
	}
	if e.client == nil {
		e.client = http_client.NewClientBuilder().SetTimeout(e.timeout).SetRetry(0, 0).SetNoRedirect().BuildClient().Client
	}
	return e, nil
}

func (e *everlong) GetQuote(ctx context.Context, req QuoteRequest) (*Quote, error) {
	params, err := quoteParams(req)
	if err != nil {
		return nil, fmt.Errorf("building Everlong quote request: %w", err)
	}
	response, err := e.getResponse(ctx, "/v1/quote", params)
	if err != nil {
		return nil, fmt.Errorf("getting Everlong quote: %w", err)
	}
	quote, err := response.quote(req, 0)
	if err != nil {
		return nil, fmt.Errorf("parsing Everlong quote: %w", err)
	}
	return quote, nil
}

// GetSwap asks the API to build calldata without any balance/allowance simulation.
// Recipient controls the output receiver; the API has no separate sender parameter.
func (e *everlong) GetSwap(ctx context.Context, req SwapRequest) (*Swap, error) {
	params, err := quoteParams(req.QuoteRequest)
	if err != nil {
		return nil, fmt.Errorf("building Everlong swap request: %w", err)
	}
	if req.Recipient == (common.Address{}) {
		return nil, fmt.Errorf("everlong: recipient is required")
	}
	if req.SlippageBps < 1 || req.SlippageBps > 1000 {
		return nil, fmt.Errorf("everlong: slippage must be in [1, 1000] basis points")
	}
	params.Set("recipient", req.Recipient.Hex())
	params.Set("slippage", strconv.Itoa(req.SlippageBps))
	response, err := e.getResponse(ctx, "/v1/swap", params)
	if err != nil {
		return nil, fmt.Errorf("getting Everlong swap: %w", err)
	}
	quote, err := response.quote(req.QuoteRequest, req.SlippageBps)
	if err != nil {
		return nil, fmt.Errorf("parsing Everlong swap quote: %w", err)
	}
	tx, err := response.Tx.transaction()
	if err != nil {
		return nil, fmt.Errorf("parsing Everlong swap transaction: %w", err)
	}
	return &Swap{Quote: *quote, Tx: *tx}, nil
}

func (e *everlong) Close() error {
	e.client.CloseIdleConnections()
	return nil
}

func quoteParams(req QuoteRequest) (url.Values, error) {
	if req.ChainID != constants.KatanaChainID {
		return nil, fmt.Errorf("everlong: unsupported chain %d (Katana only)", req.ChainID)
	}
	tokenIn, tokenOut := tokenAddress(req.TokenIn), tokenAddress(req.TokenOut)
	if tokenIn == tokenOut {
		return nil, fmt.Errorf("everlong: from and to tokens are the same")
	}
	if req.Amount == nil || req.Amount.Sign() <= 0 || req.Amount.BitLen() > 256 {
		return nil, fmt.Errorf("everlong: amount must be a positive uint256")
	}
	params := url.Values{"tokenIn": {tokenIn.Hex()}, "tokenOut": {tokenOut.Hex()}, "amountIn": {req.Amount.String()}}
	if len(req.ExcludePools) > 0 {
		pools := make([]string, len(req.ExcludePools))
		for i, pool := range req.ExcludePools {
			pools[i] = pool.Hex()
		}
		params.Set("excludePools", strings.Join(pools, ","))
	}
	return params, nil
}

func tokenAddress(token common.Address) common.Address {
	if token == (common.Address{}) {
		return constants.GetNativeToken()
	}
	return token
}

func (e *everlong) getResponse(ctx context.Context, path string, params url.Values) (*apiResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.baseURL+path+"?"+params.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("creating Everlong request: %w", err)
	}
	req.Header.Set("Accept", http_client.ContentTypeJSON)
	req.Header.Set("x-api-key", e.apiKey)
	response, err := http_client.DoJSON[apiResponse](e.client, req)
	if err != nil {
		return nil, fmt.Errorf("everlong %s: %w", path, err)
	}
	return &response, nil
}
