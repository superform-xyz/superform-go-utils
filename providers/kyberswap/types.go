package kyberswap

import (
	"encoding/json"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"

	"github.com/superform-xyz/superform-go-utils/utils/constants"
)

var chainIDToName = map[uint64]string{
	constants.MainnetChainID:   "ethereum",
	constants.BscChainID:       "bsc",
	constants.ArbitrumChainID:  "arbitrum",
	constants.MaticChainID:     "polygon",
	constants.OptimismChainID:  "optimism",
	constants.AvalancheChainID: "avalanche",
	constants.BaseChainID:      "base",
	constants.LineaChainID:     "linea",
	constants.MantleChainID:    "mantle",
	constants.SonicChainID:     "sonic",
	constants.BeraChainID:      "berachain",
	constants.UnichainChainID:  "unichain",
	constants.HyperEvmChainID:  "hyperevm",
}

// RouteRequest contains the inputs for KyberSwap's GET /routes endpoint.
type RouteRequest struct {
	ChainID             uint64
	TokenIn             string
	TokenOut            string
	AmountIn            string
	OnlyScalableSources bool
}

// Route contains the route summary returned by KyberSwap's route endpoint.
type Route struct {
	RouteSummary  json.RawMessage
	RouterAddress string
}

type routeResponse struct {
	Code    int       `json:"code"`
	Message string    `json:"message"`
	Data    routeData `json:"data"`
}

type routeData struct {
	RouteSummary  json.RawMessage `json:"routeSummary"`
	RouterAddress string          `json:"routerAddress"`
}

// BuildRequest contains the inputs for KyberSwap's POST /route/build endpoint.
type BuildRequest struct {
	ChainID           uint64
	RouteSummary      json.RawMessage
	Sender            string
	Recipient         string
	SlippageTolerance int
}

type buildRequestJSON struct {
	RouteSummary      json.RawMessage `json:"routeSummary"`
	Sender            string          `json:"sender"`
	Recipient         string          `json:"recipient"`
	SlippageTolerance int             `json:"slippageTolerance"`
}

type buildResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    *Build `json:"data"`
}

// Build contains parsed transaction data returned by KyberSwap's build endpoint.
type Build struct {
	AmountOut        *big.Int
	TxData           []byte
	RouterAddress    common.Address
	TransactionValue *big.Int
}

// UnmarshalJSON implements custom JSON unmarshalling for KyberSwap build data.
func (b *Build) UnmarshalJSON(data []byte) error {
	type buildAux struct {
		AmountOut        string `json:"amountOut"`
		Data             string `json:"data"`
		RouterAddress    string `json:"routerAddress"`
		TransactionValue string `json:"transactionValue"`
	}

	aux := buildAux{}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	amountOut, ok := new(big.Int).SetString(aux.AmountOut, 10)
	if !ok {
		return fmt.Errorf("failed to parse amountOut: %s", aux.AmountOut)
	}

	txData := common.FromHex(aux.Data)
	if len(txData) == 0 {
		return fmt.Errorf("empty tx data in build response")
	}

	txValue := new(big.Int)
	if aux.TransactionValue != "" {
		txValue, ok = new(big.Int).SetString(aux.TransactionValue, 10)
		if !ok {
			txValue, ok = new(big.Int).SetString(aux.TransactionValue, 0)
			if !ok {
				return fmt.Errorf("failed to parse transactionValue: %s", aux.TransactionValue)
			}
		}
	}

	b.AmountOut = amountOut
	b.TxData = txData
	b.RouterAddress = common.HexToAddress(aux.RouterAddress)
	b.TransactionValue = txValue

	return nil
}

func chainToName(chainID uint64) (string, error) {
	name, ok := chainIDToName[chainID]
	if !ok {
		return "", fmt.Errorf("unsupported chain ID %d for kyberswap", chainID)
	}
	return name, nil
}
