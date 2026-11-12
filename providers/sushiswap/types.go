package sushiswap

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"github.com/ethereum/go-ethereum/common"
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
	return &Transaction{From: t.From, To: t.To, Data: data, Value: value, GasPrice: t.GasPrice, Gas: gas}, nil
}
