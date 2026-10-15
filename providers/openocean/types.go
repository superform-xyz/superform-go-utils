package openocean

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"

	"github.com/superform-xyz/superform-go-utils/utils/constants"
)

type swapResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    *Swap  `json:"data"`
}

// SwapRequest contains the query parameters for OpenOcean's swap endpoint.
// Token addresses may use either the zero address or 0xEeee… for the native token.
type SwapRequest struct {
	ChainID          uint64
	InTokenAddress   string
	OutTokenAddress  string
	AmountDecimals   string
	Slippage         string // percent, e.g. "0.5"; takes precedence over SlippageBps
	SlippageBps      int    // used when Slippage is empty
	Account          string
	EnabledDexIDs    string
	GasPriceDecimals string
	Referrer         string
}

// Swap contains parsed transaction data returned by OpenOcean's swap endpoint.
type Swap struct {
	AmountIn         *big.Int
	AmountOut        *big.Int
	MinAmountOut     *big.Int
	TxData           []byte
	RouterAddress    common.Address
	TransactionValue *big.Int
	ChainID          uint64
}

// UnmarshalJSON implements custom JSON unmarshalling for OpenOcean swap data.
func (s *Swap) UnmarshalJSON(data []byte) error {
	type swapAux struct {
		InAmount     string `json:"inAmount"`
		OutAmount    string `json:"outAmount"`
		MinOutAmount string `json:"minOutAmount"`
		To           string `json:"to"`
		Value        string `json:"value"`
		Data         string `json:"data"`
		ChainID      uint64 `json:"chainId"`
	}

	aux := swapAux{}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	amountIn, ok := new(big.Int).SetString(aux.InAmount, 10)
	if !ok || amountIn.Sign() <= 0 {
		return fmt.Errorf("failed to parse inAmount: %s", aux.InAmount)
	}

	amountOut, ok := new(big.Int).SetString(aux.OutAmount, 10)
	if !ok || amountOut.Sign() <= 0 {
		return fmt.Errorf("failed to parse outAmount: %s", aux.OutAmount)
	}

	minAmountOut, ok := new(big.Int).SetString(aux.MinOutAmount, 10)
	if !ok || minAmountOut.Sign() <= 0 {
		return fmt.Errorf("failed to parse minOutAmount: %s", aux.MinOutAmount)
	}

	txData := common.FromHex(aux.Data)
	if len(txData) == 0 {
		return fmt.Errorf("empty tx data in OpenOcean swap response")
	}

	if !common.IsHexAddress(aux.To) || common.HexToAddress(aux.To) == (common.Address{}) {
		return fmt.Errorf("empty router in OpenOcean swap response: %q", aux.To)
	}

	txValue := new(big.Int)
	if aux.Value != "" {
		txValue, ok = parseDecimalOrHexBigInt(aux.Value)
		if !ok {
			return fmt.Errorf("failed to parse value: %s", aux.Value)
		}
	}

	s.AmountIn = amountIn
	s.AmountOut = amountOut
	s.MinAmountOut = minAmountOut
	s.TxData = txData
	s.RouterAddress = common.HexToAddress(aux.To)
	s.TransactionValue = txValue
	s.ChainID = aux.ChainID

	return nil
}

func parseDecimalOrHexBigInt(input string) (*big.Int, bool) {
	if strings.HasPrefix(input, "0x") || strings.HasPrefix(input, "0X") {
		return new(big.Int).SetString(input[2:], 16)
	}
	return new(big.Int).SetString(input, 10)
}

// SlippageBpsToPercent formats basis points as OpenOcean's percent string (50 -> "0.5").
func SlippageBpsToPercent(bps int) string {
	whole, fraction := bps/100, bps%100
	switch {
	case fraction == 0:
		return fmt.Sprintf("%d", whole)
	case fraction%10 == 0:
		return fmt.Sprintf("%d.%d", whole, fraction/10)
	default:
		return fmt.Sprintf("%d.%02d", whole, fraction)
	}
}

// openOceanTokenAddress maps 0xEeee… to the zero address OpenOcean uses for the native token.
func openOceanTokenAddress(token string) string {
	token = strings.TrimSpace(token)
	if common.IsHexAddress(token) && constants.IsNativeToken(token) {
		return constants.GetNullAddress().Hex()
	}
	return token
}
