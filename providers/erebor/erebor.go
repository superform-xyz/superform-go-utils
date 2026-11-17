package erebor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/superform-xyz/superform-go-utils/pkg/http_client"
)

var ErrInvalidResponse = errors.New("erebor invalid allocation response")

type client struct {
	baseURL string
	http    *http_client.Client
}

var _ Client = (*client)(nil)

func New(baseURL string) (Client, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("erebor: a valid HTTP base URL is required")
	}
	return &client{baseURL: baseURL, http: http_client.NewClientBuilder().
		SetTimeout(8*time.Second).
		SetRetry(0, 0).
		SetNoRedirect().
		SetMaxResponseBytes(4 << 20).
		BuildClient()}, nil
}

func (c *client) GetAllocations(ctx context.Context, chainID uint64, vaultAddress string) (*Allocations, error) {
	if chainID == 0 || !validAddress(vaultAddress) {
		return nil, errors.New("erebor: a positive chain ID and valid vault address are required")
	}
	endpoint := fmt.Sprintf("%s/api/v1/vaults/%d/%s/allocations", c.baseURL, chainID, strings.ToLower(vaultAddress))
	response, err := http_client.GetJSON[struct {
		Success bool         `json:"success"`
		Data    *Allocations `json:"data"`
	}](ctx, c.http, endpoint)
	if err != nil {
		return nil, fmt.Errorf("erebor get allocations: %w", err)
	}
	if !response.Success || response.Data == nil {
		return nil, ErrInvalidResponse
	}
	if err := response.Data.validate(chainID, vaultAddress); err != nil {
		return nil, err
	}
	return response.Data, nil
}

func (c *client) Close() error {
	c.http.CloseIdleConnections()
	return nil
}

func (a *Allocations) UnmarshalJSON(data []byte) error {
	type plain Allocations
	wire := struct {
		*plain
		AssetDecimals *uint8 `json:"asset_decimals"`
	}{plain: (*plain)(a)}
	if err := json.Unmarshal(data, &wire); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidResponse, err)
	}
	if wire.AssetDecimals == nil {
		return fmt.Errorf("%w: asset_decimals is required", ErrInvalidResponse)
	}
	a.AssetDecimals = *wire.AssetDecimals
	return nil
}

func (a *Allocations) validate(chainID uint64, vaultAddress string) error {
	if a.ChainID != chainID || !strings.EqualFold(a.VaultAddress, vaultAddress) || !validAddress(a.StrategyAddress) || !validAddress(a.AssetAddress) || a.Allocations == nil {
		return fmt.Errorf("%w: allocation identity or positions are invalid", ErrInvalidResponse)
	}
	for field, raw := range map[string]string{"net_assets": a.NetAssets, "idle_balance": a.IdleBalance} {
		if !nonNegative(raw) {
			return fmt.Errorf("%w: %s must be a non-negative integer", ErrInvalidResponse, field)
		}
	}
	for _, raw := range []string{a.GrossAssets, a.TotalLiabilities} {
		if raw != "" && !nonNegative(raw) {
			return fmt.Errorf("%w: accounting total must be a non-negative integer", ErrInvalidResponse)
		}
	}
	for i, source := range a.Allocations {
		amount, ok := new(big.Int).SetString(source.Assets, 10)
		if !validAddress(source.Address) || !ok {
			return fmt.Errorf("%w: source %d identity or amount is invalid", ErrInvalidResponse, i)
		}
		switch source.PositionType {
		case "asset":
			if amount.Sign() < 0 {
				return fmt.Errorf("%w: source %d asset amount is negative", ErrInvalidResponse, i)
			}
		case "liability":
			if amount.Sign() > 0 {
				return fmt.Errorf("%w: source %d liability amount is positive", ErrInvalidResponse, i)
			}
		default:
			return fmt.Errorf("%w: source %d position type is invalid", ErrInvalidResponse, i)
		}
	}
	return nil
}

func validAddress(address string) bool {
	return len(address) == 42 && common.IsHexAddress(address) && common.HexToAddress(address) != (common.Address{})
}

func nonNegative(raw string) bool {
	amount, ok := new(big.Int).SetString(raw, 10)
	return ok && amount.Sign() >= 0
}
