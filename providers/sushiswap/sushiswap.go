// Package sushiswap provides a client for Sushi's v7 quote and swap APIs.
package sushiswap

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/superform-xyz/superform-go-utils/pkg/http_client"
	"github.com/superform-xyz/superform-go-utils/utils/constants"
	"github.com/superform-xyz/superform-go-utils/utils/units"
)

const baseURL = "https://api.sushi.com"

var (
	ErrNoRoute      = errors.New("sushiswap: no route found")
	ErrPartialRoute = errors.New("sushiswap: route cannot fill the entire input amount")
)

// Client fetches exact-input quotes and unsigned swap transactions. It does not
// submit transactions or manage token approvals.
type Client interface {
	GetQuote(ctx context.Context, req QuoteRequest) (*Quote, error)
	GetSwap(ctx context.Context, req SwapRequest) (*Swap, error)
	Close() error
}

type sushiswap struct {
	baseURL    string
	apiKey     string
	client     *http_client.Client
	timeout    time.Duration
	maxRetries uint
	retryDelay time.Duration
}

var _ Client = (*sushiswap)(nil)

type Option func(*sushiswap)

func WithBaseURL(baseURL string) Option {
	return func(s *sushiswap) {
		if baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/"); baseURL != "" {
			s.baseURL = baseURL
		}
	}
}

// WithAPIKey sets the optional apiKey query parameter supported by Sushi.
func WithAPIKey(apiKey string) Option {
	return func(s *sushiswap) { s.apiKey = strings.TrimSpace(apiKey) }
}

func WithHTTPClient(client *http.Client) Option {
	return func(s *sushiswap) {
		if client != nil {
			s.client = &http_client.Client{Client: client}
		}
	}
}

// WithTimeout sets the timeout of the built-in HTTP client.
func WithTimeout(timeout time.Duration) Option {
	return func(s *sushiswap) { s.timeout = timeout }
}

// WithRetry configures the built-in HTTP client. By default requests are not retried.
func WithRetry(maxRetries uint, retryDelay time.Duration) Option {
	return func(s *sushiswap) {
		s.maxRetries = maxRetries
		s.retryDelay = retryDelay
	}
}

// New creates a client. API keys are optional; unauthenticated requests are supported.
func New(opts ...Option) (Client, error) {
	s := &sushiswap{baseURL: baseURL, timeout: 60 * time.Second, retryDelay: 10 * time.Second}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	if s.client == nil {
		s.client = http_client.NewClientBuilder().
			SetTimeout(s.timeout).
			SetRetry(s.maxRetries, s.retryDelay).
			BuildClient()
	}
	return s, nil
}

// GetQuote returns a complete route, rejecting NoWay and Partial responses.
func (s *sushiswap) GetQuote(ctx context.Context, req QuoteRequest) (*Quote, error) {
	params, err := quoteParams(req)
	if err != nil {
		return nil, err
	}
	response, err := getResponse(ctx, s, fmt.Sprintf("/quote/v7/%d", req.ChainID), params)
	if err != nil {
		return nil, err
	}
	return response.quote(req)
}

// GetSwap returns unsigned transaction data for a complete route. Recipient
// defaults to Sender. Simulation is disabled unless requested.
func (s *sushiswap) GetSwap(ctx context.Context, req SwapRequest) (*Swap, error) {
	quoteReq := QuoteRequest{
		ChainID: req.ChainID, TokenIn: req.TokenIn, TokenOut: req.TokenOut,
		Amount: req.Amount, SlippageBps: req.SlippageBps, Referrer: req.Referrer,
	}
	params, err := quoteParams(quoteReq)
	if err != nil {
		return nil, err
	}
	if req.Sender == (common.Address{}) {
		return nil, fmt.Errorf("sushiswap: sender is required")
	}
	params.Set("sender", req.Sender.Hex())
	if req.Recipient != (common.Address{}) {
		params.Set("recipient", req.Recipient.Hex())
	}
	params.Set("simulate", strconv.FormatBool(req.Simulate))
	response, err := getResponse(ctx, s, fmt.Sprintf("/swap/v7/%d", req.ChainID), params)
	if err != nil {
		return nil, err
	}
	quote, err := response.quote(quoteReq)
	if err != nil {
		return nil, err
	}
	tx, err := response.Tx.transaction()
	if err != nil {
		return nil, err
	}
	if tx.From != req.Sender {
		return nil, fmt.Errorf("sushiswap: transaction sender does not match request")
	}
	return &Swap{Quote: *quote, Tx: *tx}, nil
}

func (s *sushiswap) Close() error {
	s.client.CloseIdleConnections()
	return nil
}

func quoteParams(req QuoteRequest) (url.Values, error) {
	if req.ChainID == 0 {
		return nil, fmt.Errorf("sushiswap: chain ID is required")
	}
	if tokenAddress(req.TokenIn) == tokenAddress(req.TokenOut) {
		return nil, fmt.Errorf("sushiswap: from and to tokens are the same")
	}
	if req.Amount == nil || req.Amount.Sign() <= 0 || req.Amount.BitLen() > 256 {
		return nil, fmt.Errorf("sushiswap: amount must be a positive uint256")
	}
	if req.SlippageBps < 0 || req.SlippageBps >= units.BpsBase {
		return nil, fmt.Errorf("sushiswap: slippage must be in [0, 10000) basis points")
	}
	params := url.Values{
		"tokenIn":     {tokenAddress(req.TokenIn).Hex()},
		"tokenOut":    {tokenAddress(req.TokenOut).Hex()},
		"amount":      {req.Amount.String()},
		"maxSlippage": {strconv.FormatFloat(float64(req.SlippageBps)/units.BpsBase, 'f', -1, 64)},
	}
	if req.Referrer != "" {
		params.Set("referrer", req.Referrer)
	}
	return params, nil
}

func tokenAddress(token common.Address) common.Address {
	if token == (common.Address{}) {
		return constants.GetNativeToken()
	}
	return token
}

func getResponse(ctx context.Context, s *sushiswap, path string, params url.Values) (*apiResponse, error) {
	if s.apiKey != "" {
		params.Set("apiKey", s.apiKey)
	}
	response, err := http_client.GetJSON[apiResponse](ctx, s.client, s.baseURL+path+"?"+params.Encode())
	if err != nil {
		// net/http errors include the request URL, which can contain the API key.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return nil, fmt.Errorf("sushiswap %s: %w", path, err)
	}
	return &response, nil
}

func (r *apiResponse) quote(req QuoteRequest) (*Quote, error) {
	switch r.Status {
	case StatusNoWay:
		return nil, ErrNoRoute
	case StatusPartial:
		return nil, ErrPartialRoute
	case StatusSuccess:
	default:
		return nil, fmt.Errorf("sushiswap: unexpected route status %q", r.Status)
	}
	amountIn, err := parseAmount("amountIn", r.AmountIn)
	if err != nil {
		return nil, err
	}
	if amountIn.Sign() == 0 || amountIn.Cmp(req.Amount) != 0 {
		return nil, fmt.Errorf("sushiswap: amountIn does not match requested amount")
	}
	amountOut, err := parseAmount("assumedAmountOut", r.AssumedAmountOut)
	if err != nil {
		return nil, err
	}
	if amountOut.Sign() == 0 {
		return nil, fmt.Errorf("sushiswap: assumedAmountOut must be positive")
	}
	if r.TokenFrom == nil || r.TokenTo == nil || *r.TokenFrom < 0 || *r.TokenTo < 0 ||
		*r.TokenFrom >= len(r.Tokens) || *r.TokenTo >= len(r.Tokens) {
		return nil, fmt.Errorf("sushiswap: invalid token indexes in response")
	}
	if r.Tokens[*r.TokenFrom].Address != tokenAddress(req.TokenIn) ||
		r.Tokens[*r.TokenTo].Address != tokenAddress(req.TokenOut) {
		return nil, fmt.Errorf("sushiswap: response tokens do not match request")
	}
	return &Quote{
		Status: r.Status, Tokens: r.Tokens, TokenFrom: *r.TokenFrom, TokenTo: *r.TokenTo,
		SwapPrice: r.SwapPrice, PriceImpact: r.PriceImpact, GasSpent: r.GasSpent,
		AmountIn: amountIn, AssumedAmountOut: amountOut,
		EstimatedAmountOutMin: units.ApplySlippageBps(amountOut, req.SlippageBps),
	}, nil
}

func parseAmount(field, value string) (*big.Int, error) {
	if value == "" || strings.IndexFunc(value, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
		return nil, fmt.Errorf("sushiswap: invalid %s %q", field, value)
	}
	amount, ok := new(big.Int).SetString(value, 10)
	if !ok || amount.BitLen() > 256 {
		return nil, fmt.Errorf("sushiswap: invalid %s %q", field, value)
	}
	return amount, nil
}
