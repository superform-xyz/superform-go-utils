package axiom

import (
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/superform-xyz/superform-go-utils/utils/constants"
)

var defaultSupportedChains = map[uint64]struct{}{
	constants.MainnetChainID:  {},
	constants.BaseChainID:     {},
	constants.HyperEvmChainID: {},
	constants.FlareChainID:    {},
}

// TokenPrice represents a USD token price returned by Axiom.
type TokenPrice struct {
	ChainID   uint64
	Address   common.Address
	Price     float64
	UpdatedAt time.Time
}

type priceResponse struct {
	Value string `json:"value"`
}
