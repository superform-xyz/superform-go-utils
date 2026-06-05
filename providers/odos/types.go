package odos

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
)

// QuoteRequest contains the inputs for Odos's POST /sor/quote/v2 endpoint.
type QuoteRequest struct {
	ChainID              uint64        `json:"chainId"`
	Compact              bool          `json:"compact"`
	Simple               bool          `json:"simple"`
	InputTokens          []InputToken  `json:"inputTokens"`
	OutputTokens         []OutputToken `json:"outputTokens"`
	SlippageLimitPercent float32       `json:"slippageLimitPercent"`
	UserAddr             string        `json:"userAddr"`
	SourceBlacklist      []string      `json:"sourceBlacklist"`
}

type InputToken struct {
	Amount       string `json:"amount"`
	TokenAddress string `json:"tokenAddress"`
}

type OutputToken struct {
	Proportion   int    `json:"proportion"`
	TokenAddress string `json:"tokenAddress"`
}

// Quote contains the selected path returned by Odos's quote endpoint.
type Quote struct {
	TraceID           string    `json:"traceId"`
	InTokens          []string  `json:"inTokens"`
	OutTokens         []string  `json:"outTokens"`
	InAmounts         []string  `json:"inAmounts"`
	OutAmounts        []string  `json:"outAmounts"`
	GasEstimate       float64   `json:"gasEstimate"`
	DataGasEstimate   int       `json:"dataGasEstimate"`
	GweiPerGas        float64   `json:"gweiPerGas"`
	GasEstimateValue  float64   `json:"gasEstimateValue"`
	InValues          []float64 `json:"inValues"`
	OutValues         []float64 `json:"outValues"`
	NetOutValue       float64   `json:"netOutValue"`
	PriceImpact       float64   `json:"priceImpact"`
	PercentDiff       float64   `json:"percentDiff"`
	PartnerFeePercent float64   `json:"partnerFeePercent"`
	PathID            string    `json:"pathId"`
}

// AssembleRequest contains the inputs for Odos's POST /sor/assemble endpoint.
type AssembleRequest struct {
	PathID   string `json:"pathId"`
	Simulate bool   `json:"simulate"`
	UserAddr string `json:"userAddr"`
}

// Assemble contains parsed transaction data returned by Odos's assemble endpoint.
type Assemble struct {
	TraceID          string                `json:"traceId"`
	BlockNumber      int                   `json:"blockNumber"`
	GasEstimate      int                   `json:"gasEstimate"`
	GasEstimateValue float64               `json:"gasEstimateValue"`
	OutputTokens     []AssembleOutputToken `json:"outputTokens"`
	Transaction      Transaction           `json:"transaction"`
}

type AssembleOutputToken struct {
	TokenAddress common.Address `json:"tokenAddress"`
	Amount       *big.Int       `json:"amount"`
}

type Transaction struct {
	Gas      int            `json:"gas"`
	GasPrice int64          `json:"gasPrice"`
	Value    *big.Int       `json:"value"`
	To       common.Address `json:"to"`
	From     common.Address `json:"from"`
	Data     []byte         `json:"data"`
	Nonce    int            `json:"nonce"`
	ChainID  int            `json:"chainId"`
}

// UnmarshalJSON implements custom JSON unmarshalling for Odos assemble data.
func (a *Assemble) UnmarshalJSON(data []byte) error {
	type outputTokenAux struct {
		TokenAddress string `json:"tokenAddress"`
		Amount       string `json:"amount"`
	}
	type transactionAux struct {
		Gas      int    `json:"gas"`
		GasPrice int64  `json:"gasPrice"`
		Value    string `json:"value"`
		To       string `json:"to"`
		From     string `json:"from"`
		Data     string `json:"data"`
		Nonce    int    `json:"nonce"`
		ChainID  int    `json:"chainId"`
	}
	type assembleAux struct {
		TraceID          string           `json:"traceId"`
		BlockNumber      int              `json:"blockNumber"`
		GasEstimate      int              `json:"gasEstimate"`
		GasEstimateValue float64          `json:"gasEstimateValue"`
		OutputTokens     []outputTokenAux `json:"outputTokens"`
		Transaction      transactionAux   `json:"transaction"`
	}

	aux := assembleAux{}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	outputTokens := make([]AssembleOutputToken, 0, len(aux.OutputTokens))
	for _, token := range aux.OutputTokens {
		amount, ok := new(big.Int).SetString(token.Amount, 10)
		if !ok {
			return fmt.Errorf("failed to parse output amount: %s", token.Amount)
		}
		outputTokens = append(outputTokens, AssembleOutputToken{
			TokenAddress: common.HexToAddress(token.TokenAddress),
			Amount:       amount,
		})
	}

	txValue := big.NewInt(0)
	if strings.TrimSpace(aux.Transaction.Value) != "" {
		var ok bool
		txValue, ok = new(big.Int).SetString(aux.Transaction.Value, 10)
		if !ok {
			return fmt.Errorf("failed to parse transaction value: %s", aux.Transaction.Value)
		}
	}

	var txData []byte
	if strings.TrimSpace(aux.Transaction.Data) != "" {
		rawData := strings.TrimPrefix(aux.Transaction.Data, "0x")
		var err error
		txData, err = hex.DecodeString(rawData)
		if err != nil {
			return fmt.Errorf("failed to decode transaction data: %w", err)
		}
	}

	*a = Assemble{
		TraceID:          aux.TraceID,
		BlockNumber:      aux.BlockNumber,
		GasEstimate:      aux.GasEstimate,
		GasEstimateValue: aux.GasEstimateValue,
		OutputTokens:     outputTokens,
		Transaction: Transaction{
			Gas:      aux.Transaction.Gas,
			GasPrice: aux.Transaction.GasPrice,
			Value:    txValue,
			To:       common.HexToAddress(aux.Transaction.To),
			From:     common.HexToAddress(aux.Transaction.From),
			Data:     txData,
			Nonce:    aux.Transaction.Nonce,
			ChainID:  aux.Transaction.ChainID,
		},
	}

	return nil
}
