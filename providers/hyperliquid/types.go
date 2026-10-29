package hyperliquid

import "github.com/ethereum/go-ethereum/common"

// Numeric HTTP fields retain the venue's decimal text. No price, PnL, or
// account eligibility is inferred by these wire types.
type MarginSummary struct {
	AccountValue    string `json:"accountValue"`
	TotalNtlPos     string `json:"totalNtlPos"`
	TotalRawUsd     string `json:"totalRawUsd"`
	TotalMarginUsed string `json:"totalMarginUsed"`
}

type Position struct {
	Coin          string  `json:"coin"`
	Szi           string  `json:"szi"`
	EntryPx       *string `json:"entryPx"`
	PositionValue string  `json:"positionValue"`
	UnrealizedPnl string  `json:"unrealizedPnl"`
	MarginUsed    string  `json:"marginUsed"`
	LiquidationPx *string `json:"liquidationPx"`
}

type AssetPosition struct {
	Type     string   `json:"type"`
	Position Position `json:"position"`
}

type PerpState struct {
	MarginSummary              MarginSummary   `json:"marginSummary"`
	CrossMarginSummary         MarginSummary   `json:"crossMarginSummary"`
	CrossMaintenanceMarginUsed string          `json:"crossMaintenanceMarginUsed"`
	Withdrawable               string          `json:"withdrawable"`
	AssetPositions             []AssetPosition `json:"assetPositions"`
	Time                       int64           `json:"time"`
}

// SpotBalance amounts are decimal strings, not precompile wei amounts.
type SpotBalance struct {
	Coin     string `json:"coin"`
	Token    int64  `json:"token"`
	Hold     string `json:"hold"`
	Total    string `json:"total"`
	EntryNtl string `json:"entryNtl"`
}

type SpotState struct {
	Balances []SpotBalance `json:"balances"`
}

type ExtraAgent struct {
	Address    common.Address `json:"address"`
	Name       string         `json:"name"`
	ValidUntil int64          `json:"validUntil"`
}

type SpotToken struct {
	Name        string `json:"name"`
	Index       int64  `json:"index"`
	SzDecimals  int32  `json:"szDecimals"`
	WeiDecimals int32  `json:"weiDecimals"`
	EvmContract *struct {
		Address             string `json:"address"`
		EvmExtraWeiDecimals int32  `json:"evm_extra_wei_decimals"`
	} `json:"evmContract"`
}

type SpotPair struct {
	Name        string  `json:"name"`
	Tokens      []int64 `json:"tokens"`
	Index       int64   `json:"index"`
	IsCanonical bool    `json:"isCanonical"`
}

type SpotMeta struct {
	Tokens   []SpotToken `json:"tokens"`
	Universe []SpotPair  `json:"universe"`
}

type SpotAssetCtx struct {
	DayNtlVlm string  `json:"dayNtlVlm"`
	MarkPx    *string `json:"markPx"`
	MidPx     *string `json:"midPx"`
	PrevDayPx *string `json:"prevDayPx"`
}

// LedgerDelta is the provider's union wire payload. Interpreting its effect on
// capital, fees or PnL belongs to the consuming application.
type LedgerDelta struct {
	Type        string `json:"type"`
	Usdc        string `json:"usdc"`
	UsdcValue   string `json:"usdcValue"`
	Fee         string `json:"fee"`
	User        string `json:"user"`
	Destination string `json:"destination"`
	Token       string `json:"token"`
	Amount      string `json:"amount"`
	Vault       string `json:"vault"`
}

type LedgerUpdate struct {
	Delta LedgerDelta `json:"delta"`
	Time  int64       `json:"time"`
	Hash  string      `json:"hash"`
}
