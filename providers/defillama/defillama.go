package defillama

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/pkg/errors"

	"github.com/superform/superform-go-utils/pkg/http_client"
	"github.com/superform/superform-go-utils/utils/constants"
)

const (
	coinsBaseUrl = "https://coins.llama.fi"
)

var (
	// ErrTokenNotFound is returned when the token is not found.
	ErrTokenNotFound = errors.New("token not found")
)

// DefiLlama represents the behavior of the DeFi Llama service.
// Ref: https://defillama.com/docs/api
type DefiLlama interface {
	HealthCheck() error
	GetCoin(chainId uint64, tokenAddress common.Address) (*Coin, error)
	GetHistoricalCoin(chainId uint64, tokenAddress common.Address, timestamp time.Time) (*Coin, error)
	GetMultipleCoins(tokens []QueryTokenPrice) ([]*Coin, error)
}

// defiLlama implements the DefiLlama interface.
type defiLlama struct {
	coinsBaseUrl string
	client       *http.Client
}

var _ DefiLlama = (*defiLlama)(nil)

// New creates a new DeFi Llama service.
func New() DefiLlama {
	return &defiLlama{
		coinsBaseUrl: coinsBaseUrl,
		client:       http_client.NewClientBuilder().SetRetry(0, time.Second*2).Build(),
	}
}

// HealthCheck returns an error if there is a problem with the service.
func (d *defiLlama) HealthCheck() error {
	path := fmt.Sprintf("%s/prices/current/ethereum:0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48", d.coinsBaseUrl)
	resp, err := d.client.Get(path)
	if err != nil {
		return errors.Wrap(err, "failed to get prices")
	}

	if resp.Body != nil {
		defer resp.Body.Close()
	}

	var coins CoinsResponse
	return json.NewDecoder(resp.Body).Decode(&coins)
}

// GetTokenPrice returns the price of a token on a chain.
func (d *defiLlama) GetCoin(chainID uint64, tokenAddress common.Address) (*Coin, error) {
	var (
		token = tokenAddress
	)
	// defillama uses null address for native tokens
	if constants.IsNativeToken(tokenAddress) {
		token.SetBytes(constants.GetNullAddress().Bytes())
	}

	chainAndAddress := fmt.Sprintf("%s:%s", chainToNameMap[chainID], token)
	path := fmt.Sprintf("%s/prices/current/%s", d.coinsBaseUrl, chainAndAddress)

	resp, err := d.client.Get(path)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get prices")
	}

	if resp.Body != nil {
		defer resp.Body.Close()
	}

	var coins CoinsResponse
	if err = json.NewDecoder(resp.Body).Decode(&coins); err != nil {
		return nil, errors.Wrap(err, "failed to decode prices")
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
func (d *defiLlama) GetHistoricalCoin(chainId uint64, tokenAddress common.Address, timestamp time.Time) (*Coin, error) {
	var (
		token = tokenAddress
	)
	// defillama uses null address for native tokens
	if constants.IsNativeToken(tokenAddress) {
		token.SetBytes(constants.GetNullAddress().Bytes())
	}

	chainAndAddress := fmt.Sprintf("%s:%s", chainToNameMap[chainId], token)
	path := fmt.Sprintf(d.coinsBaseUrl+"/prices/historical/%d/%s", timestamp.Unix(), chainAndAddress)

	var coins CoinsResponse

	resp, err := d.client.Get(path)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get prices")
	}

	if resp.Body != nil {
		defer resp.Body.Close()
	}

	if err = json.NewDecoder(resp.Body).Decode(&coins); err != nil {
		return nil, errors.Wrap(err, "failed to decode prices")
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
func (d *defiLlama) GetMultipleCoins(tokens []QueryTokenPrice) ([]*Coin, error) {
	tokenIds := make([]string, 0, len(tokens))
	for _, token := range tokens {
		chainName, chainNameOk := chainToNameMap[token.ChainId]
		if !chainNameOk {
			continue
		}

		if constants.IsNativeToken(token.TokenAddress) {
			token.TokenAddress.SetBytes(constants.GetNullAddress().Bytes())
		}

		tokenIds = append(tokenIds, fmt.Sprintf("%s:%s", chainName, token.TokenAddress))
	}

	path := fmt.Sprintf(d.coinsBaseUrl+"/prices/current/%s", strings.Join(tokenIds, ","))
	resp, err := d.client.Get(path)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get prices")
	}

	if resp.Body != nil {
		defer resp.Body.Close()
	}

	var coinsResp CoinsResponse
	if err = json.NewDecoder(resp.Body).Decode(&coinsResp); err != nil {
		return nil, errors.Wrap(err, "failed to decode prices")
	}

	coins := make([]*Coin, 0, len(tokens))
	for _, token := range tokens {
		var (
			tokenToUse = token.TokenAddress
		)
		if constants.IsNativeToken(token.TokenAddress) {
			tokenToUse.SetBytes(constants.GetNullAddress().Bytes())
		}

		chainAndAddress := fmt.Sprintf("%s:%s", chainToNameMap[token.ChainId], tokenToUse)

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
