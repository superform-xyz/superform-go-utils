package defillama

import (
	"github.com/ethereum/go-ethereum/common"

	"github.com/superform/superform-go-utils/utils/constants"
)

var chainToNameMap = map[uint64]string{
	constants.MainnetChainID:   "ethereum",
	constants.OptimismChainID:  "optimism",
	constants.BscChainID:       "bsc",
	constants.MaticChainID:     "polygon",
	constants.ArbitrumChainID:  "arbitrum",
	constants.AvalancheChainID: "avax",
	constants.BaseChainID:      "base",
	constants.UnichainChainID:  "unichain",
	constants.HyperEvmChainID:  "hyperliquid",
	constants.FlareChainID:     "flare",
}

// Coin represents a coin.
type Coin struct {
	ChainId uint64         `json:"-"`
	Address common.Address `json:"-"`

	Decimals   int32   `json:"decimals"`
	Price      float64 `json:"price"`
	Symbol     string  `json:"symbol"`
	Timestamp  int     `json:"timestamp"`
	Confidence float64 `json:"confidence"`
}

// CoinsResponse represents the response from the coins API.
type CoinsResponse struct {
	Coins map[string]Coin `json:"coins"`
}

// QueryTokenPrice represents a query for a token price.
type QueryTokenPrice struct {
	ChainId      uint64
	TokenAddress common.Address
}
