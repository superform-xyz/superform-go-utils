package defillama

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/pkg/errors"

	"github.com/superform-xyz/superform-go-utils/pkg/http_client"
	"github.com/superform-xyz/superform-go-utils/utils/constants"
)

const (
	coinsBaseUrl = "https://coins.llama.fi"
)

var (
	// ErrTokenNotFound is returned when the token is not found.
	ErrTokenNotFound    = errors.New("token not found")
	ErrUnsupportedChain = errors.New("unsupported chain")
)

// DefiLlama represents the behavior of the DeFi Llama service.
// Ref: https://defillama.com/docs/api
type DefiLlama interface {
	HealthCheck(ctx context.Context) error
	GetCoin(ctx context.Context, chainId uint64, tokenAddress common.Address) (*Coin, error)
	GetHistoricalCoin(ctx context.Context, chainId uint64, tokenAddress common.Address, timestamp time.Time) (*Coin, error)
	GetMultipleCoins(ctx context.Context, tokens []QueryTokenPrice) ([]*Coin, error)
	SupportedChains() []uint64
	Close() error
}

// defiLlama implements the DefiLlama interface.
type defiLlama struct {
	coinsBaseUrl string
	client       *http_client.Client
	chainToName  map[uint64]string
}

var _ DefiLlama = (*defiLlama)(nil)

type Option func(*defiLlama)

func WithBaseURL(baseURL string) Option {
	return func(d *defiLlama) {
		baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
		if baseURL != "" {
			d.coinsBaseUrl = baseURL
		}
	}
}

func WithHTTPClient(client *http.Client) Option {
	return func(d *defiLlama) {
		if client != nil {
			d.client = &http_client.Client{Client: client}
		}
	}
}

func WithChainMap(chainMap map[uint64]string) Option {
	return func(d *defiLlama) {
		if len(chainMap) > 0 {
			d.chainToName = cloneChainMap(chainMap)
		}
	}
}

// New creates a new DeFi Llama service.
func New(opts ...Option) (DefiLlama, error) {
	d := &defiLlama{
		coinsBaseUrl: coinsBaseUrl,
		chainToName:  cloneChainMap(chainToNameMap),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(d)
		}
	}
	if d.client == nil {
		d.client = http_client.NewClientBuilder().SetRetry(0, time.Second*2).BuildClient()
	}

	return d, nil
}

// HealthCheck returns an error if there is a problem with the service.
func (d *defiLlama) HealthCheck(ctx context.Context) error {
	var coins CoinsResponse
	return d.getCoins(ctx, "ethereum:0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48", &coins)
}

// GetTokenPrice returns the price of a token on a chain.
func (d *defiLlama) GetCoin(ctx context.Context, chainID uint64, tokenAddress common.Address) (*Coin, error) {
	var (
		token = tokenAddress
	)
	// defillama uses null address for native tokens
	if constants.IsNativeToken(tokenAddress) {
		token.SetBytes(constants.GetNullAddress().Bytes())
	}

	chainAndAddress, err := d.chainAndAddress(chainID, token)
	if err != nil {
		return nil, err
	}

	var coins CoinsResponse
	if err = d.getCoins(ctx, chainAndAddress, &coins); err != nil {
		return nil, err
	}

	coin, coinOk := coins.Coins[chainAndAddress]
	if !coinOk {
		return nil, ErrTokenNotFound
	}

	coin.Address = tokenAddress
	coin.ChainId = chainID

	return &coin, nil
}

// GetHistoricalCoin returns the historical coin details.
func (d *defiLlama) GetHistoricalCoin(ctx context.Context, chainId uint64, tokenAddress common.Address, timestamp time.Time) (*Coin, error) {
	var (
		token = tokenAddress
	)
	// defillama uses null address for native tokens
	if constants.IsNativeToken(tokenAddress) {
		token.SetBytes(constants.GetNullAddress().Bytes())
	}

	chainAndAddress, err := d.chainAndAddress(chainId, token)
	if err != nil {
		return nil, err
	}

	var coins CoinsResponse
	if err = d.getHistoricalCoins(ctx, timestamp, chainAndAddress, &coins); err != nil {
		return nil, err
	}

	coin, coinOk := coins.Coins[chainAndAddress]
	if !coinOk {
		return nil, ErrTokenNotFound
	}

	coin.Address = tokenAddress
	coin.ChainId = chainId

	return &coin, nil
}

// GetMultipleCoins returns tokens details for multiple tokens.
func (d *defiLlama) GetMultipleCoins(ctx context.Context, tokens []QueryTokenPrice) ([]*Coin, error) {
	tokenIds := make([]string, 0, len(tokens))
	for _, token := range tokens {
		chainName, chainNameOk := d.chainToName[token.ChainId]
		if !chainNameOk {
			continue
		}

		if constants.IsNativeToken(token.TokenAddress) {
			token.TokenAddress.SetBytes(constants.GetNullAddress().Bytes())
		}

		tokenIds = append(tokenIds, fmt.Sprintf("%s:%s", chainName, token.TokenAddress))
	}

	if len(tokenIds) == 0 {
		return []*Coin{}, nil
	}

	var coinsResp CoinsResponse
	if err := d.getCoins(ctx, strings.Join(tokenIds, ","), &coinsResp); err != nil {
		return nil, err
	}

	coins := make([]*Coin, 0, len(tokens))
	for _, token := range tokens {
		var (
			tokenToUse = token.TokenAddress
		)
		if constants.IsNativeToken(token.TokenAddress) {
			tokenToUse.SetBytes(constants.GetNullAddress().Bytes())
		}

		chainName, chainNameOk := d.chainToName[token.ChainId]
		if !chainNameOk {
			continue
		}
		chainAndAddress := fmt.Sprintf("%s:%s", chainName, tokenToUse)

		coin, coinOk := coinsResp.Coins[chainAndAddress]
		if !coinOk {
			continue
		}

		coin.ChainId = token.ChainId
		coin.Address = token.TokenAddress

		coins = append(coins, &coin)
	}

	return coins, nil
}

func (d *defiLlama) SupportedChains() []uint64 {
	supportedChains := make([]uint64, 0, len(d.chainToName))
	for chainID := range d.chainToName {
		supportedChains = append(supportedChains, chainID)
	}
	return supportedChains
}

func (d *defiLlama) Close() error {
	d.client.CloseIdleConnections()
	return nil
}

func (d *defiLlama) chainAndAddress(chainID uint64, token common.Address) (string, error) {
	chainName, ok := d.chainToName[chainID]
	if !ok {
		return "", fmt.Errorf("%w: %d", ErrUnsupportedChain, chainID)
	}
	return fmt.Sprintf("%s:%s", chainName, token), nil
}

func (d *defiLlama) getCoins(ctx context.Context, tokenIDs string, out *CoinsResponse) error {
	path := fmt.Sprintf("%s/prices/current/%s", d.coinsBaseUrl, tokenIDs)
	return d.get(ctx, path, out)
}

func (d *defiLlama) getHistoricalCoins(ctx context.Context, timestamp time.Time, tokenIDs string, out *CoinsResponse) error {
	path := fmt.Sprintf("%s/prices/historical/%d/%s", d.coinsBaseUrl, timestamp.Unix(), tokenIDs)
	return d.get(ctx, path, out)
}

func (d *defiLlama) get(ctx context.Context, path string, out *CoinsResponse) error {
	if ctx == nil {
		ctx = context.Background()
	}
	resp, err := d.client.GetWithContext(ctx, path)
	if err != nil {
		return errors.Wrap(err, "failed to get prices")
	}
	if resp.Body != nil {
		defer resp.Body.Close()
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("defillama API returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err = json.NewDecoder(resp.Body).Decode(out); err != nil {
		return errors.Wrap(err, "failed to decode prices")
	}
	return nil
}

func cloneChainMap(chainToName map[uint64]string) map[uint64]string {
	cloned := make(map[uint64]string, len(chainToName))
	for chainID, chainName := range chainToName {
		if chainName = strings.TrimSpace(chainName); chainName != "" {
			cloned[chainID] = chainName
		}
	}
	return cloned
}
