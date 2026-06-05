package oneinch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/superform-xyz/superform-go-utils/pkg/http_client"
	"github.com/superform-xyz/superform-go-utils/utils/constants"
)

const (
	oneInchBaseURL = "https://api.1inch.dev"
	authHeader     = "Authorization"
)

// Client represents the behavior of the 1inch API.
type Client interface {
	GetQuote(ctx context.Context, req QuoteRequest) (*Quote, error)
	GetSwap(ctx context.Context, req SwapRequest) (*Swap, error)
	SupportedChains() []uint64
	GetRouter(chainID uint64) common.Address
	Close() error
}

type oneInch struct {
	apiKey          string
	baseURL         string
	client          *http_client.Client
	sourceBlacklist []string
	routers         map[uint64]common.Address
	timeout         *time.Duration
	maxRetries      *uint
	retryDelay      *time.Duration
}

var _ Client = (*oneInch)(nil)

type Option func(*oneInch)

func WithBaseURL(baseURL string) Option {
	return func(o *oneInch) {
		baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
		if baseURL != "" {
			o.baseURL = baseURL
		}
	}
}

func WithHTTPClient(client *http.Client) Option {
	return func(o *oneInch) {
		if client != nil {
			o.client = &http_client.Client{Client: client}
		}
	}
}

func WithSourceBlacklist(sourceBlacklist []string) Option {
	return func(o *oneInch) {
		o.sourceBlacklist = append([]string(nil), sourceBlacklist...)
	}
}

func WithRouters(routers map[uint64]common.Address) Option {
	return func(o *oneInch) {
		o.routers = cloneRouters(routers)
	}
}

func WithTimeout(timeout time.Duration) Option {
	return func(o *oneInch) {
		o.timeout = &timeout
	}
}

func WithRetry(maxRetries uint, retryDelay time.Duration) Option {
	return func(o *oneInch) {
		o.maxRetries = &maxRetries
		o.retryDelay = &retryDelay
	}
}

// New creates a new 1inch API client.
func New(apiKey string, opts ...Option) Client {
	o := &oneInch{
		apiKey:  strings.TrimSpace(apiKey),
		baseURL: oneInchBaseURL,
		routers: map[uint64]common.Address{},
	}
	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}
	if o.client == nil {
		builder := http_client.NewClientBuilder().SetRetry(0, 10*time.Second)
		if o.apiKey != "" {
			builder.SetAuth(authHeader, fmt.Sprintf("Bearer %s", o.apiKey))
		}
		if o.timeout != nil {
			builder.SetTimeout(*o.timeout)
		}
		if o.maxRetries != nil && o.retryDelay != nil {
			builder.SetRetry(*o.maxRetries, *o.retryDelay)
		}
		o.client = builder.BuildClient()
	}
	return o
}

func (o *oneInch) GetQuote(ctx context.Context, req QuoteRequest) (*Quote, error) {
	if err := validateSwapTokens(req.FromToken, req.ToToken); err != nil {
		return nil, err
	}
	if req.Amount == nil || req.Amount.Sign() <= 0 {
		return nil, fmt.Errorf("amount must be greater than zero")
	}

	v := url.Values{}
	v.Set("src", oneInchTokenAddress(req.FromToken).Hex())
	v.Set("dst", oneInchTokenAddress(req.ToToken).Hex())
	v.Set("amount", req.Amount.String())
	v.Set("excludedProtocols", strings.Join(o.sourceBlacklist, ","))
	v.Set("includeTokensInfo", "true")
	v.Set("includeGas", "true")

	var quote Quote
	if err := o.getJSON(ctx, fmt.Sprintf("/swap/v6.0/%d/quote?%s", req.ChainID, v.Encode()), "1inch quote", &quote); err != nil {
		return nil, err
	}

	quote.ToAmountMin = scaleBigInt(quote.DstAmount, -req.Slippage)
	quote.Router = o.GetRouter(req.ChainID)
	quote.ChainID = req.ChainID
	quote.FromToken = req.FromToken
	quote.ToToken = req.ToToken
	quote.FromAmount = new(big.Int).Set(req.Amount)
	quote.Slippage = req.Slippage

	return &quote, nil
}

func (o *oneInch) GetSwap(ctx context.Context, req SwapRequest) (*Swap, error) {
	if err := validateSwapTokens(req.FromToken, req.ToToken); err != nil {
		return nil, err
	}
	if req.Amount == nil || req.Amount.Sign() <= 0 {
		return nil, fmt.Errorf("amount must be greater than zero")
	}
	if constants.IsNullAddress(req.FromAddress) {
		return nil, fmt.Errorf("from address is not set")
	}
	if constants.IsNullAddress(req.ToAddress) {
		return nil, fmt.Errorf("to address is not set")
	}

	v := url.Values{}
	v.Set("src", oneInchTokenAddress(req.FromToken).Hex())
	v.Set("dst", oneInchTokenAddress(req.ToToken).Hex())
	v.Set("amount", req.Amount.String())
	v.Set("from", req.FromAddress.Hex())
	v.Set("origin", req.FromAddress.Hex())
	v.Set("slippage", fmt.Sprintf("%f", req.Slippage))
	v.Set("receiver", req.ToAddress.Hex())
	v.Set("excludedProtocols", strings.Join(o.sourceBlacklist, ","))
	v.Set("disableEstimate", "true")
	v.Set("includeProtocols", "true")
	v.Set("usePatching", "true")

	var swap Swap
	if err := o.getJSON(ctx, fmt.Sprintf("/swap/v6.0/%d/swap?%s", req.ChainID, v.Encode()), "1inch swap", &swap); err != nil {
		return nil, err
	}

	return &swap, nil
}

func (o *oneInch) SupportedChains() []uint64 {
	supportedChains := make([]uint64, 0, len(o.routers))
	for chainID, router := range o.routers {
		if !constants.IsNullAddress(router) {
			supportedChains = append(supportedChains, chainID)
		}
	}
	sort.Slice(supportedChains, func(i, j int) bool { return supportedChains[i] < supportedChains[j] })
	return supportedChains
}

func (o *oneInch) GetRouter(chainID uint64) common.Address {
	return o.routers[chainID]
}

func (o *oneInch) Close() error {
	o.client.CloseIdleConnections()
	return nil
}

func (o *oneInch) getJSON(ctx context.Context, path, operation string, responseBody any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("failed to create %s request: %w", operation, err)
	}

	resp, err := o.client.Do(req)
	if err != nil {
		if statusCode, body, ok := http_client.ResponseStatus(err); ok {
			body = strings.TrimSpace(body)
			if body == "" {
				return fmt.Errorf("%s API returned status %d", operation, statusCode)
			}
			return fmt.Errorf("%s API returned status %d: %s", operation, statusCode, body)
		}
		return fmt.Errorf("failed to get %s: %w", operation, err)
	}
	if resp.Body != nil {
		defer resp.Body.Close()
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body := ""
		if resp.Body != nil {
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			body = strings.TrimSpace(string(raw))
		}
		if body == "" {
			return fmt.Errorf("%s API returned status %d", operation, resp.StatusCode)
		}
		return fmt.Errorf("%s API returned status %d: %s", operation, resp.StatusCode, body)
	}

	if err := json.NewDecoder(resp.Body).Decode(responseBody); err != nil {
		return fmt.Errorf("failed to decode %s response: %w", operation, err)
	}

	return nil
}

func oneInchTokenAddress(token common.Address) common.Address {
	if constants.IsNullAddress(token) {
		return constants.GetNativeToken()
	}
	return token
}

func validateSwapTokens(fromToken, toToken common.Address) error {
	if oneInchTokenAddress(fromToken).Cmp(oneInchTokenAddress(toToken)) == 0 {
		return fmt.Errorf("from and to tokens are the same")
	}
	return nil
}

func cloneRouters(routers map[uint64]common.Address) map[uint64]common.Address {
	out := make(map[uint64]common.Address, len(routers))
	for chainID, router := range routers {
		out[chainID] = router
	}
	return out
}
