package debank

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/pkg/errors"

	"github.com/superform-xyz/superform-go-utils/pkg/http_client"
	"github.com/superform-xyz/superform-go-utils/utils/constants"
	"github.com/superform-xyz/superform-go-utils/utils/filter"
)

const (
	debankBaseURL     = "https://pro-openapi.debank.com/v1"
	maxTokenListByIDs = 100
)

// Debank represents the behavior of the Debank service.
type Debank interface {
	HealthCheck(ctx context.Context) error
	GetToken(ctx context.Context, chainID uint64, tokenAddress common.Address) (*Token, error)
	GetHistoryTokenPrice(ctx context.Context, chainID uint64, tokenAddress common.Address, timestamp time.Time) (*float64, error)
	GetTokenBalances(ctx context.Context, address string, filterTokens bool) ([]Token, error)
	GetTokens(ctx context.Context, chainID uint64, tokenAddresses []common.Address) ([]Token, error)
	GetAccountCredits(ctx context.Context) (AccountCredits, error)
	GetProtocolPortfolios(ctx context.Context, userAddress string) ([]ProtocolPortfolio, error)
	SupportedChains() []uint64
	Close() error
}

// debank implements the Debank interface.
type debank struct {
	accessKey  string
	baseUrl    string
	client     *http_client.Client
	maxRetries *uint
	retryDelay *time.Duration
}

var _ Debank = (*debank)(nil)

type Option func(*debank)

func WithBaseURL(baseURL string) Option {
	return func(d *debank) {
		baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
		if baseURL != "" {
			d.baseUrl = baseURL
		}
	}
}

func WithHTTPClient(client *http.Client) Option {
	return func(d *debank) {
		if client != nil {
			d.client = &http_client.Client{Client: client}
		}
	}
}

func WithRetry(maxRetries uint, retryDelay time.Duration) Option {
	return func(d *debank) {
		d.maxRetries = &maxRetries
		d.retryDelay = &retryDelay
	}
}

// New creates a new Debank service.
func New(accessKey string, opts ...Option) (Debank, error) {
	accessKey = strings.TrimSpace(accessKey)
	if accessKey == "" {
		return nil, fmt.Errorf("debank: accessKey is required")
	}
	d := &debank{
		accessKey: accessKey,
		baseUrl:   debankBaseURL,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(d)
		}
	}
	if d.client == nil {
		builder := http_client.NewClientBuilder().SetRetry(0, 0)
		if d.maxRetries != nil && d.retryDelay != nil {
			builder.SetRetry(*d.maxRetries, *d.retryDelay)
		}
		d.client = builder.BuildClient()
	}
	return d, nil
}

// HealthCheck returns an error if there is a problem with the service.
func (d *debank) HealthCheck(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

// GetToken see https://docs.cloud.debank.com/en/readme/api-pro-reference/token
func (d *debank) GetToken(ctx context.Context, chainID uint64, tokenAddress common.Address) (*Token, error) {
	chainName, err := ChainToName(chainID)
	if err != nil {
		return nil, err
	}

	// Debank API uses chain name for native token
	tokenAddressRaw := tokenAddress.String()
	if constants.IsNullAddress(tokenAddress) || constants.IsNativeToken(tokenAddress) {
		tokenAddressRaw = chainName
	}

	path := fmt.Sprintf("%s/token?chain_id=%s&id=%s", d.baseUrl, url.QueryEscape(chainName), url.QueryEscape(tokenAddressRaw))
	req, err := d.newRequest(ctx, path)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create debank request")
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get token")
	}

	if resp.Body != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if err := decodeDebankResponse(resp, "debank token"); err != nil {
		return nil, err
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
func (d *debank) GetHistoryTokenPrice(ctx context.Context, chainID uint64, tokenAddress common.Address, timestamp time.Time) (*float64, error) {
	chainName, err := ChainToName(chainID)
	if err != nil {
		return nil, err
	}

	tokenAddressRaw := tokenAddress.Hex()
	if constants.IsNullAddress(tokenAddress) || constants.IsNativeToken(tokenAddress) {
		tokenAddressRaw = chainName
	}

	formattedDate := timestamp.Format("2006-01-02")

	path := fmt.Sprintf("%s/token/history_price?chain_id=%s&id=%s&date_at=%s", d.baseUrl, url.QueryEscape(chainName), url.QueryEscape(tokenAddressRaw), url.QueryEscape(formattedDate))
	req, err := d.newRequest(ctx, path)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create debank request")
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get token")
	}

	if resp.Body != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if err := decodeDebankResponse(resp, "debank token history"); err != nil {
		return nil, err
	}

	var dbt Token
	if err = json.NewDecoder(resp.Body).Decode(&dbt); err != nil {
		return nil, errors.Wrap(err, "failed to decode debank token history response")
	}

	return &dbt.Price, nil
}

// GetTokenBalances returns the token balances for the user
func (d *debank) GetTokenBalances(ctx context.Context, address string, filterTokens bool) ([]Token, error) {
	address = strings.TrimSpace(address)
	if address == "" {
		return []Token{}, nil
	}
	if common.IsHexAddress(address) {
		address = constants.ChecksumAddress(address)
	}

	path := fmt.Sprintf("%s/user/all_token_list?id=%s&is_all=true", d.baseUrl, url.QueryEscape(address))
	req, err := d.newRequest(ctx, path)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create debank request")
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get token balances for user")
	}

	if resp.Body != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if err := decodeDebankResponse(resp, "debank token list"); err != nil {
		return nil, err
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

// GetTokens retrieves metadata/pricing for up to 100 tokens on one chain.
func (d *debank) GetTokens(ctx context.Context, chainID uint64, tokenAddresses []common.Address) ([]Token, error) {
	chainName, ids, err := debankTokenIDList(chainID, tokenAddresses)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return []Token{}, nil
	}

	path := fmt.Sprintf("%s/token/list_by_ids?chain_id=%s&ids=%s", d.baseUrl, url.QueryEscape(chainName), url.QueryEscape(strings.Join(ids, ",")))
	req, err := d.newRequest(ctx, path)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create debank request")
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get token list by ids")
	}
	if resp.Body != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if err := decodeDebankResponse(resp, "debank token/list_by_ids"); err != nil {
		return nil, err
	}

	var dbt []Token
	if err = json.NewDecoder(resp.Body).Decode(&dbt); err != nil {
		return nil, errors.Wrap(err, "failed to decode debank token/list_by_ids response")
	}

	filtered := make([]Token, 0, len(dbt))
	for _, token := range dbt {
		if token.ChainID == 0 {
			continue
		}
		filtered = append(filtered, token)
	}
	return filtered, nil
}

func (d *debank) GetAccountCredits(ctx context.Context) (AccountCredits, error) {
	path := fmt.Sprintf("%s/account/units", d.baseUrl)
	req, err := d.newRequest(ctx, path)
	if err != nil {
		return AccountCredits{}, errors.Wrap(err, "failed to create debank request")
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return AccountCredits{}, errors.Wrap(err, "failed to get account credits")
	}
	if resp.Body != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if err := decodeDebankResponse(resp, "debank account/units"); err != nil {
		return AccountCredits{}, err
	}

	var out AccountCredits
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return AccountCredits{}, errors.Wrap(err, "failed to decode debank account/units response")
	}
	return out, nil
}

func (d *debank) GetProtocolPortfolios(ctx context.Context, userAddress string) ([]ProtocolPortfolio, error) {
	userAddress = strings.TrimSpace(userAddress)
	path := fmt.Sprintf("%s/user/all_complex_protocol_list?id=%s", d.baseUrl, url.QueryEscape(userAddress))
	req, err := d.newRequest(ctx, path)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create debank request")
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get complex protocols")
	}
	if resp.Body != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if err := decodeDebankResponse(resp, "debank all_complex_protocol_list"); err != nil {
		return nil, err
	}

	var out []ProtocolPortfolio
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, errors.Wrap(err, "failed to decode debank all_complex_protocol_list response")
	}
	return out, nil
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

func (d *debank) newRequest(ctx context.Context, path string) (*http.Request, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	if d.accessKey != "" {
		req.Header.Set("AccessKey", d.accessKey)
	}
	return req, nil
}

func decodeDebankResponse(resp *http.Response, operation string) error {
	if resp == nil {
		return fmt.Errorf("%s request failed: empty response", operation)
	}
	if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
		return nil
	}
	body := ""
	if resp.Body != nil {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		body = strings.TrimSpace(string(raw))
	}
	if body == "" {
		return fmt.Errorf("%s non-200 status: %d", operation, resp.StatusCode)
	}
	return fmt.Errorf("%s non-200 status: %d: %s", operation, resp.StatusCode, body)
}

func debankTokenIDList(chainID uint64, tokenAddresses []common.Address) (string, []string, error) {
	chainName, err := ChainToName(chainID)
	if err != nil {
		return "", nil, err
	}

	seen := make(map[string]struct{}, len(tokenAddresses))
	ids := make([]string, 0, len(tokenAddresses))
	for _, tokenAddress := range tokenAddresses {
		tokenID := tokenAddress.Hex()
		if constants.IsNullAddress(tokenAddress) || constants.IsNativeToken(tokenAddress) {
			tokenID = chainName
		}
		tokenID = strings.TrimSpace(tokenID)
		if tokenID == "" {
			continue
		}
		if _, ok := seen[tokenID]; ok {
			continue
		}
		seen[tokenID] = struct{}{}
		ids = append(ids, tokenID)
	}
	sort.Strings(ids)
	if len(ids) > maxTokenListByIDs {
		return "", nil, fmt.Errorf("debank token/list_by_ids supports at most %d ids, got %d", maxTokenListByIDs, len(ids))
	}
	return chainName, ids, nil
}
