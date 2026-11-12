package sushiswap

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"github.com/ethereum/go-ethereum/common"

	"github.com/superform-xyz/superform-go-utils/utils/units"
)

type RouteStatus string

const (
	StatusSuccess RouteStatus = "Success"
	StatusPartial RouteStatus = "Partial"
	StatusNoWay   RouteStatus = "NoWay"
)

// QuoteRequest requests an exact-input quote. Amount is in token base units.
// SlippageBps is in [0, 10000): 50 means 0.5%. Zero is passed through explicitly.
// Both the zero address and 0xEeee… identify the native token.
type QuoteRequest struct {
	ChainID     uint64
	TokenIn     common.Address
	TokenOut    common.Address
	Amount      *big.Int
	SlippageBps int
	Referrer    string
}

type SwapRequest struct {
	ChainID     uint64
	TokenIn     common.Address
	TokenOut    common.Address
	Amount      *big.Int
	SlippageBps int
	Sender      common.Address
	Recipient   common.Address // zero omits recipient, defaulting to Sender
	Simulate    bool           // opt in to Sushi's balance/allowance and gas simulation
	Referrer    string
}

type Token struct {
	Address  common.Address `json:"address"`
	Decimals int            `json:"decimals"`
	Symbol   string         `json:"symbol"`
	Name     string         `json:"name"`
}

// Quote describes a complete route. TokenFrom and TokenTo index Tokens.
type Quote struct {
	Status           RouteStatus
	Tokens           []Token
	TokenFrom        int
	TokenTo          int
	SwapPrice        float64
	PriceImpact      float64
	AmountIn         *big.Int
	AssumedAmountOut *big.Int
	// EstimatedAmountOutMin is calculated locally from AssumedAmountOut and slippage.
	// The transaction calldata returned by Sushi determines the on-chain minimum.
	EstimatedAmountOutMin *big.Int
	GasSpent              uint64
}

type Swap struct {
	Quote
	Tx Transaction
}

type Transaction struct {
	From     common.Address
	To       common.Address
	Data     []byte
	Value    *big.Int
	GasPrice json.Number // Sushi returns a JSON number; preserve its decimal precision
	Gas      uint64      // zero when the API does not return a simulated gas limit
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
	GasSpent         uint64           `json:"gasSpent"`
	Tx               *transactionJSON `json:"tx"`
}

type transactionJSON struct {
	From     common.Address `json:"from"`
	To       common.Address `json:"to"`
	Data     string         `json:"data"`
	Value    string         `json:"value"`
	GasPrice json.Number    `json:"gasPrice"`
	Gas      string         `json:"gas"`
}

func (r *apiResponse) quote(req QuoteRequest) (*Quote, error) {
	switch r.Status {
	case StatusNoWay:
		return nil, ErrNoRoute
	case StatusPartial:
		return nil, ErrPartialRoute
	case StatusSuccess:
	default:
		return nil, fmt.Errorf("sushiswap: unexpected route status %q", r.Status)
	}
	amountIn, err := parseAmount("amountIn", r.AmountIn)
	if err != nil {
		return nil, err
	}
	if amountIn.Sign() == 0 || amountIn.Cmp(req.Amount) != 0 {
		return nil, fmt.Errorf("sushiswap: amountIn does not match requested amount")
	}
	amountOut, err := parseAmount("assumedAmountOut", r.AssumedAmountOut)
	if err != nil {
		return nil, err
	}
	if amountOut.Sign() == 0 {
		return nil, fmt.Errorf("sushiswap: assumedAmountOut must be positive")
	}
	if r.TokenFrom == nil || r.TokenTo == nil || *r.TokenFrom < 0 || *r.TokenTo < 0 ||
		*r.TokenFrom >= len(r.Tokens) || *r.TokenTo >= len(r.Tokens) {
		return nil, fmt.Errorf("sushiswap: invalid token indexes in response")
	}
	if r.Tokens[*r.TokenFrom].Address != tokenAddress(req.TokenIn) ||
		r.Tokens[*r.TokenTo].Address != tokenAddress(req.TokenOut) {
		return nil, fmt.Errorf("sushiswap: response tokens do not match request")
	}
	return &Quote{
		Status:                r.Status,
		Tokens:                r.Tokens,
		TokenFrom:             *r.TokenFrom,
		TokenTo:               *r.TokenTo,
		SwapPrice:             r.SwapPrice,
		PriceImpact:           r.PriceImpact,
		GasSpent:              r.GasSpent,
		AmountIn:              amountIn,
		AssumedAmountOut:      amountOut,
		EstimatedAmountOutMin: units.ApplySlippageBps(amountOut, req.SlippageBps),
	}, nil
}

func (t *transactionJSON) transaction() (*Transaction, error) {
	if t == nil {
		return nil, fmt.Errorf("sushiswap: swap response missing transaction")
	}
	if t.To == (common.Address{}) {
		return nil, fmt.Errorf("sushiswap: transaction router is required")
	}
	if !strings.HasPrefix(t.Data, "0x") {
		return nil, fmt.Errorf("sushiswap: transaction data must have a 0x prefix")
	}
	data, err := hex.DecodeString(t.Data[2:])
	if err != nil || len(data) < 4 {
		return nil, fmt.Errorf("sushiswap: invalid transaction data")
	}
	value := new(big.Int)
	if t.Value != "" {
		value, err = parseAmount("tx.value", t.Value)
		if err != nil {
			return nil, err
		}
	}
	var gas uint64
	if t.Gas != "" {
		gas, err = strconv.ParseUint(t.Gas, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("sushiswap: invalid tx.gas: %w", err)
		}
	}
	return &Transaction{
		From:     t.From,
		To:       t.To,
		Data:     data,
		Value:    value,
		GasPrice: t.GasPrice,
		Gas:      gas,
	}, nil
}

func parseAmount(field, value string) (*big.Int, error) {
	if value == "" || strings.IndexFunc(value, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
		return nil, fmt.Errorf("sushiswap: invalid %s %q", field, value)
	}
	amount, ok := new(big.Int).SetString(value, 10)
	if !ok || amount.BitLen() > 256 {
		return nil, fmt.Errorf("sushiswap: invalid %s %q", field, value)
	}
	return amount, nil
}
