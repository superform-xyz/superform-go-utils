package everlong

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/superform-xyz/superform-go-utils/utils/units"
)

type RouteStatus string

const (
	StatusSuccess RouteStatus = "Success"
	StatusOK      RouteStatus = "Ok"
	StatusNoWay   RouteStatus = "NoWay"
	StatusPartial RouteStatus = "Partial"
)

// QuoteRequest requests an exact-input Katana quote in token base units.
type QuoteRequest struct {
	ChainID      uint64
	TokenIn      common.Address
	TokenOut     common.Address
	Amount       *big.Int
	ExcludePools []common.Address
}

type SwapRequest struct {
	QuoteRequest
	Recipient   common.Address
	SlippageBps int // 1–1000 inclusive: 50 means 0.5%; never rounded up to a minimum.
}

type Token struct {
	Address  common.Address `json:"address"`
	Decimals int            `json:"decimals"`
	Symbol   string         `json:"symbol"`
	Name     string         `json:"name"`
}

type Quote struct {
	Status      RouteStatus
	Tokens      []Token
	TokenFrom   int
	TokenTo     int
	SwapPrice   float64
	PriceImpact float64
	AmountIn    *big.Int
	// AssumedAmountOut is already net of the API's output fee.
	AssumedAmountOut *big.Int
	// EstimatedAmountOutMin is computed locally; unmodified calldata enforces the on-chain minimum.
	EstimatedAmountOutMin *big.Int
	FeeBps                int
	FeeAmountOut          *big.Int
}

type Swap struct {
	Quote
	Tx Transaction
}

type Transaction struct {
	From  common.Address
	To    common.Address
	Data  []byte
	Value *big.Int
}

type apiResponse struct {
	Status           RouteStatus      `json:"status"`
	Tokens           []Token          `json:"tokens"`
	TokenFrom        *int             `json:"tokenFrom"`
	TokenTo          *int             `json:"tokenTo"`
	SwapPrice        float64          `json:"swapPrice"`
	PriceImpact      float64          `json:"priceImpact"`
	AmountIn         string           `json:"amountIn"`
	AssumedAmountOut string           `json:"assumedAmountOut"`
	FeeBps           int              `json:"feeBps"`
	FeeAmountOut     string           `json:"feeAmountOut"`
	Tx               *transactionJSON `json:"tx"`
}

type transactionJSON struct {
	From  common.Address `json:"from"`
	To    common.Address `json:"to"`
	Data  string         `json:"data"`
	Value string         `json:"value"`
}

func (r *apiResponse) quote(req QuoteRequest, slippageBps int) (*Quote, error) {
	switch r.Status {
	case StatusNoWay:
		return nil, ErrNoRoute
	case StatusPartial:
		return nil, ErrPartialRoute
	case StatusSuccess, StatusOK:
	default:
		return nil, fmt.Errorf("everlong: unexpected route status %q", r.Status)
	}
	amountIn, err := parseAmount("amountIn", r.AmountIn)
	if err != nil {
		return nil, fmt.Errorf("parsing input amount: %w", err)
	}
	if amountIn.Cmp(req.Amount) != 0 {
		return nil, fmt.Errorf("everlong: amountIn does not match requested amount")
	}
	amountOut, err := parseAmount("assumedAmountOut", r.AssumedAmountOut)
	if err != nil {
		return nil, fmt.Errorf("parsing output amount: %w", err)
	}
	if amountOut.Sign() == 0 {
		return nil, fmt.Errorf("everlong: assumedAmountOut must be positive")
	}
	if r.TokenFrom == nil || r.TokenTo == nil || *r.TokenFrom < 0 || *r.TokenTo < 0 || *r.TokenFrom >= len(r.Tokens) || *r.TokenTo >= len(r.Tokens) {
		return nil, fmt.Errorf("everlong: invalid token indexes in response")
	}
	if r.Tokens[*r.TokenFrom].Address != tokenAddress(req.TokenIn) || r.Tokens[*r.TokenTo].Address != tokenAddress(req.TokenOut) {
		return nil, fmt.Errorf("everlong: response tokens do not match request")
	}
	fee := new(big.Int)
	if r.FeeAmountOut != "" {
		fee, err = parseAmount("feeAmountOut", r.FeeAmountOut)
		if err != nil {
			return nil, fmt.Errorf("parsing output fee: %w", err)
		}
	}
	return &Quote{
		Status:                r.Status,
		Tokens:                r.Tokens,
		TokenFrom:             *r.TokenFrom,
		TokenTo:               *r.TokenTo,
		SwapPrice:             r.SwapPrice,
		PriceImpact:           r.PriceImpact,
		AmountIn:              amountIn,
		AssumedAmountOut:      amountOut,
		EstimatedAmountOutMin: units.ApplySlippageBps(amountOut, slippageBps),
		FeeBps:                r.FeeBps,
		FeeAmountOut:          fee,
	}, nil
}

func (t *transactionJSON) transaction() (*Transaction, error) {
	if t == nil {
		return nil, fmt.Errorf("everlong: swap response missing transaction")
	}
	if t.To == (common.Address{}) {
		return nil, fmt.Errorf("everlong: transaction router is required")
	}
	if !strings.HasPrefix(t.Data, "0x") {
		return nil, fmt.Errorf("everlong: transaction data must have a 0x prefix")
	}
	data, err := hex.DecodeString(t.Data[2:])
	if err != nil || len(data) < 4 {
		return nil, fmt.Errorf("everlong: invalid transaction data")
	}
	value := new(big.Int)
	if t.Value != "" {
		value, err = parseAmount("tx.value", t.Value)
		if err != nil {
			return nil, fmt.Errorf("parsing transaction value: %w", err)
		}
	}
	return &Transaction{From: t.From, To: t.To, Data: data, Value: value}, nil
}

func parseAmount(field, value string) (*big.Int, error) {
	if value == "" || strings.IndexFunc(value, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
		return nil, fmt.Errorf("everlong: invalid %s %q", field, value)
	}
	amount, ok := new(big.Int).SetString(value, 10)
	if !ok || amount.BitLen() > 256 {
		return nil, fmt.Errorf("everlong: invalid %s %q", field, value)
	}
	return amount, nil
}
