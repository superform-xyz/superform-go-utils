package hyperliquid

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
)

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

// RawExtraAgent preserves the venue's address text, including malformed values,
// so callers can apply their own validation or approval policy per row.
type RawExtraAgent struct {
	Address    string `json:"address"`
	Name       string `json:"name"`
	ValidUntil int64  `json:"validUntil"`
}

// PerpMeta is the raw perpetual market metadata; universe position is the
// venue's asset index. No market eligibility is inferred.
type PerpMeta struct {
	Universe []PerpMarket `json:"universe"`
}

type PerpMarket struct {
	Name         string `json:"name"`
	SizeDecimals int    `json:"szDecimals"`
	Delisted     bool   `json:"isDelisted"`
	MaxLeverage  int    `json:"maxLeverage"`
	OnlyIsolated bool   `json:"onlyIsolated"`
}

// UserRateLimit retains missing counters as nil rather than interpreting them
// as an available request allowance.
type UserRateLimit struct {
	Cap    *int64 `json:"nRequestsCap"`
	Used   *int64 `json:"nRequestsUsed"`
	CumVlm string `json:"cumVlm"`
}

func (u *UserRateLimit) UnmarshalJSON(data []byte) error {
	var wire struct {
		Cap    *int64          `json:"nRequestsCap"`
		Used   *int64          `json:"nRequestsUsed"`
		CumVlm decimalWireText `json:"cumVlm"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	*u = UserRateLimit{Cap: wire.Cap, Used: wire.Used, CumVlm: string(wire.CumVlm)}
	return nil
}

// OrderStatusResponse is a point-in-time venue fact, not an execution verdict.
type OrderStatusResponse struct {
	Status string            `json:"status"`
	Order  *OrderStatusEntry `json:"order"`
	Raw    json.RawMessage   `json:"-"`
}

func (s *OrderStatusResponse) UnmarshalJSON(data []byte) error {
	type wire OrderStatusResponse
	var decoded wire
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	decoded.Raw = bytes.Clone(data)
	*s = OrderStatusResponse(decoded)
	return nil
}

type OrderStatusEntry struct {
	Order           OrderStatusOrder `json:"order"`
	Status          string           `json:"status"`
	StatusTimestamp int64            `json:"statusTimestamp,omitempty"`
}

type OrderStatusOrder struct {
	OrderID uint64 `json:"oid"`
	Cloid   string `json:"cloid"`
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
	// Raw retains fields outside this compatibility view, including unknown
	// variants. DecodeLedgerDelta offers a typed view without PnL policy.
	Raw json.RawMessage `json:"-"`
}

// UnmarshalJSON accepts the venue's numeric and quoted decimal encodings while
// preserving this type's existing string fields and unknown delta payload.
func (d *LedgerDelta) UnmarshalJSON(data []byte) error {
	if d == nil {
		return fmt.Errorf("cannot unmarshal a ledger delta into a nil receiver")
	}
	var wire struct {
		Type        string          `json:"type"`
		Usdc        decimalWireText `json:"usdc"`
		UsdcValue   decimalWireText `json:"usdcValue"`
		Fee         decimalWireText `json:"fee"`
		User        string          `json:"user"`
		Destination string          `json:"destination"`
		Token       string          `json:"token"`
		Amount      decimalWireText `json:"amount"`
		Vault       string          `json:"vault"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	*d = LedgerDelta{Type: wire.Type, Usdc: string(wire.Usdc), UsdcValue: string(wire.UsdcValue), Fee: string(wire.Fee), User: wire.User, Destination: wire.Destination, Token: wire.Token, Amount: string(wire.Amount), Vault: wire.Vault, Raw: bytes.Clone(data)}
	return nil
}

type LedgerUpdate struct {
	Delta LedgerDelta     `json:"delta"`
	Time  int64           `json:"time"`
	Hash  string          `json:"hash"`
	Raw   json.RawMessage `json:"-"`
}

// UnmarshalJSON preserves the original envelope without requiring an execution
// evidence hash, deduplicating rows or assigning a capital effect.
func (u *LedgerUpdate) UnmarshalJSON(data []byte) error {
	if u == nil {
		return fmt.Errorf("cannot unmarshal a ledger update into a nil receiver")
	}
	type wire LedgerUpdate
	var decoded wire
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	decoded.Raw = bytes.Clone(data)
	*u = LedgerUpdate(decoded)
	return nil
}
