package oneinch

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"github.com/ethereum/go-ethereum/common"
)

// QuoteRequest contains the inputs for 1inch's GET /swap/v6.0/{chainID}/quote endpoint.
type QuoteRequest struct {
	ChainID   uint64
	FromToken common.Address
	ToToken   common.Address
	Amount    *big.Int
	Slippage  float64
}

// Quote contains parsed quote data returned by 1inch's quote endpoint.
type Quote struct {
	DstAmount   *big.Int
	ToAmountMin *big.Int
	SrcToken    Token
	DstToken    Token
	Gas         int
	Router      common.Address
	ChainID     uint64
	FromToken   common.Address
	ToToken     common.Address
	FromAmount  *big.Int
	Slippage    float64
}

// SwapRequest contains the inputs for 1inch's GET /swap/v6.0/{chainID}/swap endpoint.
type SwapRequest struct {
	ChainID     uint64
	FromToken   common.Address
	ToToken     common.Address
	Amount      *big.Int
	FromAddress common.Address
	ToAddress   common.Address
	Slippage    float64
}

// Token contains 1inch token metadata returned in quote responses.
type Token struct {
	Address  string `json:"address"`
	Symbol   string `json:"symbol"`
	Name     string `json:"name"`
	Decimals int    `json:"decimals"`
	LogoURI  string `json:"logoURI"`
}

// Swap contains parsed executable transaction data returned by 1inch's swap endpoint.
type Swap struct {
	DstAmount *big.Int
	Tx        Transaction
	Protocols []string
}

type Transaction struct {
	From     string
	To       common.Address
	Data     []byte
	Value    *big.Int
	Gas      int
	GasPrice string
}

func (q *Quote) UnmarshalJSON(data []byte) error {
	type quoteAux struct {
		DstAmount string `json:"dstAmount"`
		SrcToken  Token  `json:"srcToken"`
		DstToken  Token  `json:"dstToken"`
		Gas       int    `json:"gas"`
	}

	aux := quoteAux{}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	dstAmount, err := parsePositiveDecimalBigInt("dstAmount", aux.DstAmount)
	if err != nil {
		return err
	}

	q.DstAmount = dstAmount
	q.SrcToken = aux.SrcToken
	q.DstToken = aux.DstToken
	q.Gas = aux.Gas
	return nil
}

func (s *Swap) UnmarshalJSON(data []byte) error {
	type txAux struct {
		From     string `json:"from"`
		To       string `json:"to"`
		Data     string `json:"data"`
		Value    string `json:"value"`
		Gas      int    `json:"gas"`
		GasPrice string `json:"gasPrice"`
	}
	type swapAux struct {
		DstAmount string `json:"dstAmount"`
		Tx        txAux  `json:"tx"`
		Protocols [][][]struct {
			Name string `json:"name"`
		} `json:"protocols"`
	}

	aux := swapAux{}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	dstAmount, err := parsePositiveDecimalBigInt("dstAmount", aux.DstAmount)
	if err != nil {
		return err
	}

	value, ok := parseDecimalOrHexBigInt(aux.Tx.Value)
	if !ok {
		return fmt.Errorf("failed to parse value: %s", aux.Tx.Value)
	}

	s.DstAmount = dstAmount
	s.Tx = Transaction{
		From:     aux.Tx.From,
		To:       common.HexToAddress(aux.Tx.To),
		Data:     common.FromHex(aux.Tx.Data),
		Value:    value,
		Gas:      aux.Tx.Gas,
		GasPrice: aux.Tx.GasPrice,
	}

	s.Protocols = make([]string, 0)
	for _, chainProtocols := range aux.Protocols {
		for _, protocols := range chainProtocols {
			for _, subProtocol := range protocols {
				s.Protocols = append(s.Protocols, subProtocol.Name)
			}
		}
	}

	return nil
}

func parsePositiveDecimalBigInt(field, value string) (*big.Int, error) {
	parsed, ok := new(big.Int).SetString(value, 10)
	if !ok || parsed.Sign() <= 0 {
		return nil, fmt.Errorf("failed to parse %s: %s", field, value)
	}
	return parsed, nil
}

func parseDecimalOrHexBigInt(input string) (*big.Int, bool) {
	if strings.HasPrefix(input, "0x") || strings.HasPrefix(input, "0X") {
		return new(big.Int).SetString(input[2:], 16)
	}
	return new(big.Int).SetString(input, 10)
}

func scaleBigInt(amount *big.Int, scale float64) *big.Int {
	if amount == nil {
		return big.NewInt(0)
	}

	scaleRat := new(big.Rat)
	if _, ok := scaleRat.SetString(strconv.FormatFloat(scale, 'f', -1, 64)); !ok {
		return big.NewInt(0)
	}
	scaleRat.Quo(scaleRat, big.NewRat(100, 1))

	factor := new(big.Rat).Add(big.NewRat(1, 1), scaleRat)
	scaled := new(big.Rat).Mul(new(big.Rat).SetInt(amount), factor)
	return new(big.Int).Quo(scaled.Num(), scaled.Denom())
}
