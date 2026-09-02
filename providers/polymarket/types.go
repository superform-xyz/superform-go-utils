package polymarket

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"

	protocol "github.com/superform-xyz/superform-go-utils/utils/polymarket"
)

var orderIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,256}$`)

// BalanceAllowanceAsset identifies the CLOB cache entry to refresh.
type BalanceAllowanceAsset string

const (
	// BalanceAllowanceCollateral refreshes the collateral balance and allowance.
	BalanceAllowanceCollateral BalanceAllowanceAsset = "COLLATERAL"
	// BalanceAllowanceConditional refreshes one conditional-token balance and allowance.
	BalanceAllowanceConditional BalanceAllowanceAsset = "CONDITIONAL"

	// OrderTypeGTC is a good-till-cancelled provider order.
	OrderTypeGTC = "GTC"
	// OrderTypeGTD is a good-till-date provider order.
	OrderTypeGTD = "GTD"
	// OrderTypeFOK is a fill-or-kill provider order.
	OrderTypeFOK = "FOK"
	// OrderTypeFAK is a fill-and-kill provider order.
	OrderTypeFAK = "FAK"
)

// BalanceAllowanceUpdate requests one authenticated provider-cache refresh.
type BalanceAllowanceUpdate struct {
	AssetType BalanceAllowanceAsset
	TokenID   string
}

// Validate checks a balance/allowance cache refresh request.
func (u BalanceAllowanceUpdate) Validate() error {
	switch u.AssetType {
	case BalanceAllowanceCollateral:
		if u.TokenID != "" {
			return errors.New("polymarket: collateral allowance update must not include token_id")
		}
	case BalanceAllowanceConditional:
		if !protocol.IsCanonicalUint256(u.TokenID, false) {
			return errors.New("polymarket: conditional allowance update requires token_id")
		}
	default:
		return errors.New("polymarket: balance allowance asset type is invalid")
	}
	return nil
}

// MarketPage is one bounded CLOB market page.
type MarketPage struct {
	Data       []Market `json:"data"`
	NextCursor string   `json:"next_cursor"`
	Limit      int      `json:"limit,omitempty"`
	Count      int      `json:"count,omitempty"`
}

// Validate checks the complete market page and duplicate condition IDs.
func (p MarketPage) Validate() error {
	if len(p.Data) > 2_000 {
		return errors.New("market page exceeds 2000 entries")
	}
	if err := validateCursor(p.NextCursor); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(p.Data))
	for index := range p.Data {
		if err := p.Data[index].Validate(); err != nil {
			return fmt.Errorf("market %d: %w", index, err)
		}
		key := strings.ToLower(p.Data[index].ConditionID)
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("market %d duplicates condition_id", index)
		}
		seen[key] = struct{}{}
	}
	return nil
}

// Market is the validated V2 market subset used by Superform consumers.
type Market struct {
	EnableOrderBook   bool        `json:"enable_order_book"`
	Active            bool        `json:"active"`
	Closed            bool        `json:"closed"`
	Archived          bool        `json:"archived"`
	AcceptingOrders   bool        `json:"accepting_orders"`
	AcceptingOrdersAt *time.Time  `json:"accepting_order_timestamp"`
	MinimumOrderSize  json.Number `json:"minimum_order_size"`
	MinimumTickSize   json.Number `json:"minimum_tick_size"`
	ConditionID       string      `json:"condition_id"`
	QuestionID        string      `json:"question_id"`
	Question          string      `json:"question"`
	Description       string      `json:"description"`
	Slug              string      `json:"market_slug"`
	EndDate           string      `json:"end_date_iso"`
	NegRisk           bool        `json:"neg_risk"`
	NegRiskMarketID   string      `json:"neg_risk_market_id"`
	NegRiskRequestID  string      `json:"neg_risk_request_id"`
	Icon              string      `json:"icon"`
	Image             string      `json:"image"`
	Tokens            []Token     `json:"tokens"`
	Tags              []string    `json:"tags"`
}

// Validate checks provider-controlled market fields and bounds.
func (m Market) Validate() error {
	if !isHash(m.ConditionID) {
		return errors.New("condition_id is invalid")
	}
	if strings.TrimSpace(m.Question) == "" || len(m.Question) > 16_384 {
		return errors.New("question is invalid")
	}
	if len(m.Description) > 256*1024 || len(m.Slug) > 1024 || len(m.Icon) > 4096 || len(m.Image) > 4096 {
		return errors.New("market text field exceeds bounds")
	}
	if _, err := protocol.DecimalToE6(m.MinimumTickSize.String()); err != nil {
		return fmt.Errorf("minimum_tick_size: %w", err)
	}
	if _, err := protocol.DecimalToE6(m.MinimumOrderSize.String()); err != nil {
		return fmt.Errorf("minimum_order_size: %w", err)
	}
	if len(m.Tokens) == 0 || len(m.Tokens) > 64 {
		return errors.New("tokens must contain between 1 and 64 entries")
	}
	seen := make(map[string]struct{}, len(m.Tokens))
	for index := range m.Tokens {
		if err := m.Tokens[index].Validate(); err != nil {
			return fmt.Errorf("token %d: %w", index, err)
		}
		if _, duplicate := seen[m.Tokens[index].TokenID]; duplicate {
			return fmt.Errorf("token %d duplicates token_id", index)
		}
		seen[m.Tokens[index].TokenID] = struct{}{}
	}
	return nil
}

// TickSizeE6 returns the market tick in e6 units.
func (m Market) TickSizeE6() (uint64, error) {
	return protocol.DecimalToE6(m.MinimumTickSize.String())
}

// MinimumOrderSizeE6 returns the market minimum size in e6 units.
func (m Market) MinimumOrderSizeE6() (*big.Int, error) {
	return protocol.ParseFixedPoint(m.MinimumOrderSize.String(), 6)
}

// ContainsToken reports whether the market advertises tokenID.
func (m Market) ContainsToken(tokenID string) bool {
	for index := range m.Tokens {
		if m.Tokens[index].TokenID == tokenID {
			return true
		}
	}
	return false
}

// Token is one outcome token advertised by a market.
type Token struct {
	TokenID string      `json:"token_id"`
	Outcome string      `json:"outcome"`
	Price   json.Number `json:"price"`
	Winner  bool        `json:"winner"`
	Book    *OrderBook  `json:"book,omitempty"`
}

// Validate checks an outcome token.
func (t Token) Validate() error {
	if !protocol.IsCanonicalUint256(t.TokenID, false) {
		return errors.New("token_id is invalid")
	}
	if strings.TrimSpace(t.Outcome) == "" || len(t.Outcome) > 1024 {
		return errors.New("outcome is invalid")
	}
	price, err := protocol.ParseFixedPoint(t.Price.String(), 6)
	if err != nil || price.Sign() < 0 || price.Cmp(big.NewInt(1_000_000)) > 0 {
		return errors.New("price is outside [0,1]")
	}
	return nil
}

// OrderBook is the bounded provider book for one outcome token.
type OrderBook struct {
	Market         string      `json:"market"`
	AssetID        string      `json:"asset_id"`
	Timestamp      string      `json:"timestamp"`
	Bids           []BookLevel `json:"bids"`
	Asks           []BookLevel `json:"asks"`
	MinimumOrder   string      `json:"min_order_size"`
	NegRisk        bool        `json:"neg_risk"`
	TickSize       string      `json:"tick_size"`
	LastTradePrice string      `json:"last_trade_price"`
	Hash           string      `json:"hash"`
}

// Validate checks a book and all of its levels.
func (b OrderBook) Validate(tokenID string) error {
	if !isHash(b.Market) || b.AssetID != tokenID {
		return errors.New("book market or asset_id is invalid")
	}
	if len(b.Bids) > 20_000 || len(b.Asks) > 20_000 {
		return errors.New("book depth exceeds bounds")
	}
	for index := range b.Bids {
		if err := b.Bids[index].Validate(); err != nil {
			return fmt.Errorf("bid %d: %w", index, err)
		}
	}
	for index := range b.Asks {
		if err := b.Asks[index].Validate(); err != nil {
			return fmt.Errorf("ask %d: %w", index, err)
		}
	}
	return nil
}

// BookLevel is one price/size level.
type BookLevel struct {
	Price string `json:"price"`
	Size  string `json:"size"`
}

// Validate checks one book level.
func (l BookLevel) Validate() error {
	price, err := protocol.ParseFixedPoint(l.Price, 6)
	if err != nil || price.Sign() <= 0 || price.Cmp(big.NewInt(1_000_000)) >= 0 {
		return errors.New("price is invalid")
	}
	size, err := protocol.ParseFixedPoint(l.Size, 6)
	if err != nil || size.Sign() <= 0 {
		return errors.New("size is invalid")
	}
	return nil
}

// SignedOrder contains an already-authorized V2 order and explicit wire-only
// submission fields. Expiration, OrderType, and PostOnly are not EIP-712 fields.
type SignedOrder struct {
	Order      protocol.Order
	Signature  string
	Expiration uint64
	OrderType  string
	PostOnly   bool
}

// Validate checks the signed and wire-level provider request.
func (o SignedOrder) Validate() error {
	if err := o.Order.Validate(); err != nil {
		return err
	}
	if !isHexBytes(o.Signature, 1, 128*1024) {
		return errors.New("polymarket: order signature is invalid")
	}
	switch o.OrderType {
	case OrderTypeGTD:
		if o.Expiration == 0 || o.Expiration > 1<<63-1 {
			return errors.New("polymarket: GTD expiration is invalid")
		}
	case OrderTypeGTC, OrderTypeFOK, OrderTypeFAK:
		if o.Expiration != 0 {
			return errors.New("polymarket: expiration is only valid for GTD")
		}
	default:
		return errors.New("polymarket: order type is invalid")
	}
	if o.PostOnly && o.OrderType != OrderTypeGTC && o.OrderType != OrderTypeGTD {
		return errors.New("polymarket: post-only requires GTC or GTD")
	}
	return nil
}

type createOrderPayload struct {
	Order     providerOrder `json:"order"`
	Owner     string        `json:"owner"`
	OrderType string        `json:"orderType"`
	DeferExec bool          `json:"deferExec"`
	PostOnly  bool          `json:"postOnly"`
}

type providerOrder struct {
	Salt          uint64 `json:"salt"`
	Maker         string `json:"maker"`
	Signer        string `json:"signer"`
	TokenID       string `json:"tokenId"`
	MakerAmount   string `json:"makerAmount"`
	TakerAmount   string `json:"takerAmount"`
	Side          string `json:"side"`
	Expiration    string `json:"expiration"`
	SignatureType uint8  `json:"signatureType"`
	Timestamp     string `json:"timestamp"`
	Metadata      string `json:"metadata"`
	Builder       string `json:"builder"`
	Signature     string `json:"signature"`
}

func (o SignedOrder) payload(owner string) createOrderPayload {
	side := "BUY"
	if o.Order.Side == protocol.OrderSideSell {
		side = "SELL"
	}
	return createOrderPayload{
		Order: providerOrder{
			Salt:          o.Order.Salt,
			Maker:         strings.ToLower(o.Order.Maker.Hex()),
			Signer:        strings.ToLower(o.Order.Signer.Hex()),
			TokenID:       o.Order.TokenID,
			MakerAmount:   o.Order.MakerAmount,
			TakerAmount:   o.Order.TakerAmount,
			Side:          side,
			Expiration:    strconv.FormatUint(o.Expiration, 10),
			SignatureType: o.Order.SignatureType,
			Timestamp:     strconv.FormatUint(o.Order.Timestamp, 10),
			Metadata:      o.Order.Metadata.Hex(),
			Builder:       o.Order.Builder.Hex(),
			Signature:     o.Signature,
		},
		Owner:     owner,
		OrderType: o.OrderType,
		DeferExec: false,
		PostOnly:  o.PostOnly,
	}
}

// OrderPlacement is the provider response to a successful order POST.
type OrderPlacement struct {
	Success           bool     `json:"success"`
	ErrorMessage      string   `json:"errorMsg"`
	OrderID           string   `json:"orderID"`
	Status            string   `json:"status"`
	TransactionHashes []string `json:"transactionsHashes"`
	TradeIDs          []string `json:"tradeIDs"`
	MakingAmount      string   `json:"makingAmount"`
	TakingAmount      string   `json:"takingAmount"`
}

// Validate checks a placement response.
func (p OrderPlacement) Validate() error {
	if !p.Success || validateOrderID(p.OrderID) != nil {
		return errors.New("placement did not return a successful order ID")
	}
	if len(p.ErrorMessage) > 16*1024 || len(p.Status) > 128 || len(p.TransactionHashes) > 256 || len(p.TradeIDs) > 256 {
		return errors.New("placement response exceeds bounds")
	}
	return nil
}

// OpenOrderFilter restricts an authenticated open-order listing.
type OpenOrderFilter struct {
	ConditionID string
	TokenID     string
	OrderID     string
	NextCursor  string
}

// Validate checks an open-order filter.
func (f OpenOrderFilter) Validate() error {
	if f.ConditionID != "" && !isHash(f.ConditionID) {
		return errors.New("polymarket: market filter is invalid")
	}
	if f.TokenID != "" && !protocol.IsCanonicalUint256(f.TokenID, false) {
		return errors.New("polymarket: asset filter is invalid")
	}
	if f.OrderID != "" {
		if err := validateOrderID(f.OrderID); err != nil {
			return err
		}
	}
	return validateCursor(f.NextCursor)
}

// OpenOrderPage is one authenticated open-order page.
type OpenOrderPage struct {
	Data       []OpenOrder `json:"data"`
	NextCursor string      `json:"next_cursor"`
	Limit      int         `json:"limit,omitempty"`
	Count      int         `json:"count,omitempty"`
}

// Validate checks a complete open-order page for one maker.
func (p OpenOrderPage) Validate(maker string) error {
	if len(p.Data) > 2_000 {
		return errors.New("open-order page exceeds bounds")
	}
	if err := validateCursor(p.NextCursor); err != nil {
		return err
	}
	for index := range p.Data {
		if err := p.Data[index].Validate(maker); err != nil {
			return fmt.Errorf("open order %d: %w", index, err)
		}
	}
	return nil
}

// OpenOrder is the validated provider order subset used for reconciliation.
type OpenOrder struct {
	ID              string   `json:"id"`
	Status          string   `json:"status"`
	Owner           string   `json:"owner"`
	MakerAddress    string   `json:"maker_address"`
	Market          string   `json:"market"`
	AssetID         string   `json:"asset_id"`
	Side            string   `json:"side"`
	OriginalSize    string   `json:"original_size"`
	SizeMatched     string   `json:"size_matched"`
	Price           string   `json:"price"`
	AssociateTrades []string `json:"associate_trades"`
	CreatedAt       int64    `json:"created_at"`
	Expiration      string   `json:"expiration"`
	OrderType       string   `json:"order_type"`
}

// Validate checks an open order belongs to maker and has bounded identifiers.
func (o OpenOrder) Validate(maker string) error {
	switch {
	case validateOrderID(o.ID) != nil:
		return errors.New("order id is invalid")
	case !isAddress(o.MakerAddress) || !strings.EqualFold(o.MakerAddress, maker):
		return errors.New("maker address mismatch")
	case !isHash(o.Market), !protocol.IsCanonicalUint256(o.AssetID, false):
		return errors.New("market or asset id is invalid")
	case o.Side != "BUY" && o.Side != "SELL":
		return errors.New("side is invalid")
	case len(o.Status) > 128 || len(o.AssociateTrades) > 256:
		return errors.New("order response exceeds bounds")
	}
	return nil
}

type cancelRequest struct {
	OrderID string `json:"orderID"`
}

// CancelResult is the provider response for one cancel request.
type CancelResult struct {
	Canceled    []string          `json:"canceled"`
	NotCanceled map[string]string `json:"not_canceled"`
}

// Validate requires the result to mention the requested order.
func (r CancelResult) Validate(orderID string) error {
	if len(r.Canceled) > 256 || len(r.NotCanceled) > 256 {
		return errors.New("cancellation response exceeds bounds")
	}
	for _, canceled := range r.Canceled {
		if validateOrderID(canceled) != nil {
			return errors.New("cancellation response has invalid order id")
		}
		if canceled == orderID {
			return nil
		}
	}
	if _, found := r.NotCanceled[orderID]; found {
		return nil
	}
	return errors.New("cancellation response does not mention requested order")
}

func isAddress(value string) bool {
	if len(value) != 42 || !strings.HasPrefix(value, "0x") {
		return false
	}
	return isHex(value[2:]) && strings.Trim(value[2:], "0") != ""
}

func isHash(value string) bool {
	return len(value) == 66 && strings.HasPrefix(value, "0x") && isHex(value[2:]) && strings.Trim(value[2:], "0") != ""
}

func isHexBytes(value string, minimum, maximum int) bool {
	return len(value) >= 2+minimum*2 && len(value) <= 2+maximum*2 && len(value)%2 == 0 && strings.HasPrefix(value, "0x") && isHex(value[2:])
}

func isHex(value string) bool {
	for _, character := range value {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f') || (character >= 'A' && character <= 'F')) {
			return false
		}
	}
	return value != ""
}

func validateCursor(cursor string) error {
	if len(cursor) > 512 || strings.TrimSpace(cursor) != cursor || strings.ContainsAny(cursor, "\r\n") {
		return errors.New("polymarket: cursor is invalid")
	}
	return nil
}

func validateOrderID(orderID string) error {
	if !orderIDPattern.MatchString(orderID) {
		return errors.New("polymarket: order ID is invalid")
	}
	return nil
}
