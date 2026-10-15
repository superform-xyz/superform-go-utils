package defillama

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/ethereum/go-ethereum/common"

	"github.com/superform-xyz/superform-go-utils/utils/constants"
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
	constants.RobinhoodChainID: "robinhood",
	constants.ArcChainID:       "arc",
	constants.GnosisChainID:    "xdai",
	constants.SonicChainID:     "sonic",
	constants.WorldChainID:     "worldchain",
	constants.MantleChainID:    "mantle",
	constants.LineaChainID:     "linea",
	constants.BeraChainID:      "berachain",
	constants.StableChainID:    "stable",
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

// UnmarshalJSON accepts price as a JSON number or a quoted number; DefiLlama sends both.
func (c *Coin) UnmarshalJSON(data []byte) error {
	type plain Coin
	aux := struct {
		*plain
		Price json.RawMessage `json:"price"`
	}{plain: (*plain)(c)}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	raw := strings.TrimSpace(string(aux.Price))
	if unquoted, err := strconv.Unquote(raw); err == nil {
		raw = strings.TrimSpace(unquoted)
	}
	if raw == "" || raw == "null" {
		c.Price = 0
		return nil
	}
	price, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return fmt.Errorf("defillama: invalid price %s: %w", aux.Price, err)
	}
	c.Price = price
	return nil
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
