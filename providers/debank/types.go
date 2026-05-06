package debank

import (
	"encoding/json"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"

	"github.com/superform/superform-go-utils/utils/constants"
)

var chainToNameMap = map[uint64]string{
	constants.MainnetChainID:   "eth",
	constants.SonicChainID:     "sonic",
	constants.AvalancheChainID: "avax",
	constants.OptimismChainID:  "op",
	constants.ArbitrumChainID:  "arb",
	constants.BaseChainID:      "base",
	constants.BeraChainID:      "bera",
	constants.LineaChainID:     "linea",
	constants.BoBChainID:       "bob",
	constants.UnichainChainID:  "uni",
	constants.WorldChainID:     "world",
	constants.BscChainID:       "bsc",
	constants.MaticChainID:     "matic",
	constants.GnosisChainID:    "xdai",
	constants.PlumeChainID:     "plume",
	constants.HyperEvmChainID:  "hyper",
	constants.FlareChainID:     "flr",
}

// Token represents a token from Debank API
type Token struct {
	Address         common.Address `json:"-"`
	ChainID         uint64         `json:"chain_id,omitempty"`
	Name            string         `json:"name"`
	Symbol          string         `json:"symbol"`
	DisplaySymbol   string         `json:"display_symbol"`
	OptimizedSymbol string         `json:"optimized_symbol"`
	Decimals        uint32         `json:"decimals"`
	LogoURL         string         `json:"logo_url"`
	ProtocolId      string         `json:"protocol_id"`
	Price           float64        `json:"price"`
	IsVerified      bool           `json:"is_verified"`
	IsCore          bool           `json:"is_core"`
	IsWallet        bool           `json:"is_wallet"`
	Amount          float64        `json:"amount"`
	RawAmount       *big.Int       `json:"raw_amount"`
	CreditScore     float64        `json:"credit_score"`
}

// UnmarshalJSON implements custom JSON unmarshalling for Token
func (t *Token) UnmarshalJSON(data []byte) error {
	type auxToken struct {
		ID              string   `json:"id"`
		Chain           string   `json:"chain"`
		ChainID         uint64   `json:"chain_id,omitempty"`
		Name            string   `json:"name"`
		Symbol          string   `json:"symbol"`
		DisplaySymbol   string   `json:"display_symbol"`
		OptimizedSymbol string   `json:"optimized_symbol"`
		Decimals        uint32   `json:"decimals"`
		LogoURL         string   `json:"logo_url"`
		ProtocolId      string   `json:"protocol_id"`
		Price           float64  `json:"price"`
		IsVerified      bool     `json:"is_verified"`
		IsCore          bool     `json:"is_core"`
		IsWallet        bool     `json:"is_wallet"`
		Amount          float64  `json:"amount"`
		AmountCorrected string   `json:"amount_corrected"`
		RawAmount       *big.Int `json:"raw_amount"`
		CreditScore     float64  `json:"credit_score"`
	}

	aux := &auxToken{}
	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}

	*t = Token{
		Name:            aux.Name,
		Symbol:          aux.Symbol,
		DisplaySymbol:   aux.DisplaySymbol,
		OptimizedSymbol: aux.OptimizedSymbol,
		Decimals:        aux.Decimals,
		LogoURL:         aux.LogoURL,
		ProtocolId:      aux.ProtocolId,
		Price:           aux.Price,
		IsVerified:      aux.IsVerified,
		IsCore:          aux.IsCore,
		IsWallet:        aux.IsWallet,
		Amount:          aux.Amount,
		CreditScore:     aux.CreditScore,
	}

	// Set ChainID based on Chain field
	if aux.Chain != "" {
		t.ChainID = chainNameToID(aux.Chain)
	}

	// if the token ID is the same as the chain name, it's the native token
	if aux.ID == aux.Chain {
		aux.ID = constants.GetNativeToken().Hex()
	}

	t.Address = common.HexToAddress(aux.ID)

	// Handle nil RawAmount case
	if aux.RawAmount != nil {
		t.RawAmount = aux.RawAmount
	} else {
		t.RawAmount = big.NewInt(0)
	}

	return nil
}

func chainToName(chainId uint64) (string, error) {
	if name, ok := chainToNameMap[chainId]; ok {
		return name, nil
	}
	return "", fmt.Errorf("chain ID %d not found", chainId)
}

func chainNameToID(chainName string) uint64 {
	for id, name := range chainToNameMap {
		if name == chainName {
			return uint64(id)
		}
	}
	return 0
}
