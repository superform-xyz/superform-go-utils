// Package erebor reads named SuperVault allocations from Erebor.
package erebor

import "context"

type Client interface {
	GetAllocations(ctx context.Context, chainID uint64, vaultAddress string) (*Allocations, error)
	Close() error
}

type Allocations struct {
	ChainID          uint64       `json:"chain_id"`
	VaultAddress     string       `json:"vault_address"`
	StrategyAddress  string       `json:"strategy_address"`
	BlockNumber      *uint64      `json:"block_number,omitempty"`
	AssetAddress     string       `json:"asset_address"`
	AssetSymbol      string       `json:"asset_symbol"`
	AssetDecimals    uint8        `json:"asset_decimals"`
	GrossAssets      string       `json:"gross_assets,omitempty"`
	TotalLiabilities string       `json:"total_liabilities,omitempty"`
	NetAssets        string       `json:"net_assets"`
	IdleBalance      string       `json:"idle_balance"`
	Allocations      []Allocation `json:"allocations"`
}

type Allocation struct {
	Name               string `json:"name"`
	Address            string `json:"address"`
	Oracle             string `json:"oracle"`
	Assets             string `json:"assets"`
	PositionType       string `json:"position_type"`
	Kind               uint8  `json:"kind,omitempty"`
	MarketKey          string `json:"market_key,omitempty"`
	MarketID           string `json:"market_id,omitempty"`
	ProtocolAddress    string `json:"protocol_address,omitempty"`
	PositionSide       string `json:"position_side,omitempty"`
	UnderlyingAsset    string `json:"underlying_asset,omitempty"`
	UnderlyingDecimals *uint8 `json:"underlying_decimals,omitempty"`
	UnderlyingTVL      string `json:"underlying_tvl,omitempty"`
	RawShares          string `json:"raw_shares,omitempty"`
}
