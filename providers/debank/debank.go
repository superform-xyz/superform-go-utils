package debank

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/pkg/errors"

	"github.com/superform-xyz/superform-go-utils/pkg/http_client"
	"github.com/superform-xyz/superform-go-utils/utils/constants"
	"github.com/superform-xyz/superform-go-utils/utils/filter"
)

const (
	debankBaseURL = "https://pro-openapi.debank.com/v1"
)

// Debank represents the behavior of the Debank service.
type Debank interface {
	HealthCheck() error
	GetToken(chainId uint64, tokenAddress common.Address) (*Token, error)
	GetHistoryTokenPrice(chainId uint64, tokenAddress common.Address, timestamp time.Time) (*float64, error)
	GetTokenBalances(address string, filterTokens bool) ([]Token, error)
	SupportedChains() []uint64
	Close() error
}

// debank implements the Debank interface.
type debank struct {
	accessKey string
	baseUrl   string
	client    *http.Client
}

var _ Debank = (*debank)(nil)

// New creates a new Debank service.
func New(accessKey string) Debank {
	return &debank{
		accessKey: accessKey,
		baseUrl:   debankBaseURL,
		client:    http_client.NewClientBuilder().SetAuth("AccessKey", accessKey).SetRetry(0, 0).Build(),
	}
}

// HealthCheck returns an error if there is a problem with the service.
func (d *debank) HealthCheck() error {
	// TODO: Implement
	return nil
}

// GetToken see https://docs.cloud.debank.com/en/readme/api-pro-reference/token
func (d *debank) GetToken(chainId uint64, tokenAddress common.Address) (*Token, error) {
	chainName, err := chainToName(chainId)
	if err != nil {
		return nil, err
	}

	// Debank API uses chain name for native token
	tokenAddressRaw := tokenAddress.String()
	if constants.IsNullAddress(tokenAddress) || constants.IsNativeToken(tokenAddress) {
		tokenAddressRaw = chainName
	}

	path := fmt.Sprintf("%s/token?chain_id=%s&id=%s", d.baseUrl, chainName, tokenAddressRaw)
	req, err := http.NewRequest(http.MethodGet, path, nil)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create debank request")
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get token")
	}

	if resp.Body != nil {
		defer resp.Body.Close()
	}

	var dbt Token
	if err = json.NewDecoder(resp.Body).Decode(&dbt); err != nil {
		return nil, errors.Wrap(err, "failed to decode debank token response")
	}

	if dbt.ChainID == 0 {
		return nil, errors.New("token not found")
	}

	return &dbt, nil
}

// GetHistoryTokenPrice see https://docs.cloud.debank.com/en/readme/api-pro-reference/token
func (d *debank) GetHistoryTokenPrice(chainId uint64, tokenAddress common.Address, timestamp time.Time) (*float64, error) {
	chainName, err := chainToName(chainId)
	if err != nil {
		return nil, err
	}

	tokenAddressRaw := tokenAddress.Hex()
	if constants.IsNullAddress(tokenAddress) || constants.IsNativeToken(tokenAddress) {
		tokenAddressRaw = chainName
	}

	formattedDate := timestamp.Format("2006-01-02")

	path := fmt.Sprintf(d.baseUrl+"/token/history_price?chain_id=%s&id=%s&date_at=%s", chainName, tokenAddressRaw, formattedDate)
	req, err := http.NewRequest(http.MethodGet, path, nil)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create debank request")
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get token")
	}

	if resp.Body != nil {
		defer resp.Body.Close()
	}

	var dbt Token
	if err = json.NewDecoder(resp.Body).Decode(&dbt); err != nil {
		return nil, errors.Wrap(err, "failed to decode debank token history response")
	}

	return &dbt.Price, nil
}

// GetTokenBalances returns the token balances for the user
func (d *debank) GetTokenBalances(address string, filterTokens bool) ([]Token, error) {
	path := fmt.Sprintf("%s/user/all_token_list?id=%s&is_all=true", d.baseUrl, constants.ChecksumAddress(address))

	req, err := http.NewRequest(http.MethodGet, path, nil)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create debank request")
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get token balances for user")
	}

	if resp.Body != nil {
		defer resp.Body.Close()
	}

	var dbt []Token
	if err = json.NewDecoder(resp.Body).Decode(&dbt); err != nil {
		return nil, errors.Wrap(err, "failed to decode debank token list response")
	}

	// if the user wants to filter tokens, filter them
	if filterTokens {
		return filter.Filter(dbt, tokenFilter), nil
	}

	return dbt, nil
}

func (d *debank) SupportedChains() []uint64 {
	supportedChains := make([]uint64, 0, len(chainToNameMap))
	for chainID := range chainToNameMap {
		supportedChains = append(supportedChains, chainID)
	}
	return supportedChains
}

func (d *debank) Close() error {
	d.client.CloseIdleConnections()
	return nil
}
