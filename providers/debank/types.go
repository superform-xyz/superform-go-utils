package debank

import (
	"encoding/json"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"

	"github.com/superform-xyz/superform-go-utils/utils/constants"
)

var chainToNameMap = map[uint64]string{
	constants.MainnetChainID:   "eth",
	constants.FlareChainID:     "flr",
	constants.StableChainID:    "stable",
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
	ProtocolID      string         `json:"protocol_id"`
	Price           float64        `json:"price"`
	TimeAt          int64          `json:"time_at"`
	IsScam          bool           `json:"is_scam"`
	IsSuspicious    bool           `json:"is_suspicious"`
	LowCreditScore  bool           `json:"low_credit_score"`
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
		ProtocolID      string   `json:"protocol_id"`
		Price           float64  `json:"price"`
		TimeAt          float64  `json:"time_at"`
		IsScam          bool     `json:"is_scam"`
		IsSuspicious    bool     `json:"is_suspicious"`
		LowCreditScore  bool     `json:"low_credit_score"`
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
		ProtocolID:      aux.ProtocolID,
		Price:           aux.Price,
		TimeAt:          int64(aux.TimeAt),
		IsScam:          aux.IsScam,
		IsSuspicious:    aux.IsSuspicious,
		LowCreditScore:  aux.LowCreditScore,
		IsVerified:      aux.IsVerified,
		IsCore:          aux.IsCore,
		IsWallet:        aux.IsWallet,
		Amount:          aux.Amount,
		CreditScore:     aux.CreditScore,
	}

	// Set ChainID based on Chain field
	if aux.Chain != "" {
		t.ChainID = ChainNameToID(aux.Chain)
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

// ProtocolPortfolio models Debank protocol-level portfolio items for one account.
type ProtocolPortfolio struct {
	ID                    string          `json:"id"`
	Chain                 string          `json:"chain"`
	Name                  string          `json:"name"`
	SiteURL               string          `json:"site_url"`
	LogoURL               string          `json:"logo_url"`
	HasSupportedPortfolio bool            `json:"has_supported_portfolio"`
	TVL                   float64         `json:"tvl"`
	PortfolioItemList     []PortfolioItem `json:"portfolio_item_list"`
}

type PortfolioItem struct {
	Stats           Stats                  `json:"stats"`
	AssetDict       map[string]float64     `json:"asset_dict"`
	AssetTokenList  []AssetToken           `json:"asset_token_list"`
	WithdrawActions []interface{}          `json:"withdraw_actions"`
	UpdateAt        float64                `json:"update_at"`
	Name            string                 `json:"name"`
	DetailTypes     []string               `json:"detail_types"`
	Detail          PortfolioDetail        `json:"detail"`
	ProxyDetail     map[string]interface{} `json:"proxy_detail"`
	Pool            Pool                   `json:"pool"`
}

type Stats struct {
	AssetUSDValue float64 `json:"asset_usd_value"`
	DebtUSDValue  float64 `json:"debt_usd_value"`
	NetUSDValue   float64 `json:"net_usd_value"`
}

type AssetToken struct {
	ID              string  `json:"id"`
	Chain           string  `json:"chain"`
	Name            string  `json:"name"`
	Symbol          string  `json:"symbol"`
	DisplaySymbol   *string `json:"display_symbol"`
	OptimizedSymbol string  `json:"optimized_symbol"`
	Decimals        int     `json:"decimals"`
	LogoURL         string  `json:"logo_url"`
	ProtocolID      string  `json:"protocol_id"`
	Price           float64 `json:"price"`
	IsVerified      bool    `json:"is_verified"`
	IsCore          bool    `json:"is_core"`
	IsWallet        bool    `json:"is_wallet"`
	TimeAt          float64 `json:"time_at"`
	Amount          float64 `json:"amount"`
}

type PortfolioDetail struct {
	SupplyTokenList []AssetToken `json:"supply_token_list"`
	Description     string       `json:"description"`
}

type Pool struct {
	ID         string  `json:"id"`
	Chain      string  `json:"chain"`
	ProjectID  string  `json:"project_id"`
	AdapterID  string  `json:"adapter_id"`
	Controller string  `json:"controller"`
	Index      *string `json:"index"`
	TimeAt     int64   `json:"time_at"`
}

type AccountCredits struct {
	Balance int64               `json:"balance"`
	Stats   []AccountCreditStat `json:"stats"`
}

type AccountCreditStat struct {
	Usage   int64  `json:"usage"`
	Remains int64  `json:"remains"`
	Date    string `json:"date"`
}

// ChainToName returns the Debank chain slug for a supported EVM chain ID.
func ChainToName(chainID uint64) (string, error) {
	if name, ok := chainToNameMap[chainID]; ok {
		return name, nil
	}
	return "", fmt.Errorf("chain ID %d not found", chainID)
}

// ChainNameToID returns the EVM chain ID for a Debank chain slug, or 0 when unsupported.
func ChainNameToID(chainName string) uint64 {
	for id, name := range chainToNameMap {
		if name == chainName {
			return uint64(id)
		}
	}
	return 0
}
