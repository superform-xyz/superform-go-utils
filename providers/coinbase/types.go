package coinbase

import (
	"encoding/json"
	"strings"
)

// CreateSessionTokenRequest asks CDP for a single-use token binding the
// destination addresses, and optionally the assets, to one hosted onramp
// session.
type CreateSessionTokenRequest struct {
	Addresses []SessionTokenAddress
	Assets    []string
}

// SessionTokenAddress pairs one destination address with the CDP blockchain
// names it may receive on (e.g. "base", "ethereum").
type SessionTokenAddress struct {
	Address     string   `json:"address"`
	Blockchains []string `json:"blockchains"`
}

// CreateSessionTokenResponse is the safe subset returned by CDP.
type CreateSessionTokenResponse struct {
	Token     string
	ChannelID string
}

// BuildBuyURLRequest describes the hosted Coinbase Onramp URL to hand back to a
// client. Every field but SessionToken is optional and omitted when unset.
type BuildBuyURLRequest struct {
	SessionToken string
	// PartnerUserRef is the handle GetBuyTransactions reads the order back by.
	PartnerUserRef string
	// PresetFiatAmount is sent verbatim, so "100.00" stays "100.00".
	PresetFiatAmount json.Number
	FiatCurrency     string
	// DefaultPaymentMethod preselects a rail, e.g. "APPLE_PAY".
	DefaultPaymentMethod string
	RedirectURL          string
	// Addresses maps a destination address to its blockchain names.
	Addresses map[string][]string
	// Assets restricts the buyable assets, e.g. ["USDC"].
	Assets []string
}

// GetBuyTransactionsRequest reads one partner user's buy history. PageSize and
// PageKey are omitted when unset.
type GetBuyTransactionsRequest struct {
	PartnerUserRef string
	PageSize       int
	// PageKey continues a previous page, from NextPageKey.
	PageKey string
}

// GetBuyTransactionsResponse contains one page of buy transactions.
type GetBuyTransactionsResponse struct {
	Transactions []BuyTransaction
	// TotalCount is kept as a json.Number: CDP returns it as a JSON string on
	// some responses and a number on others.
	TotalCount  json.Number
	NextPageKey string
}

// Buy transaction statuses reported by CDP, for comparison by the caller. An
// unrecognised status is passed through untouched.
const (
	TransactionStatusCreated    = "ONRAMP_TRANSACTION_STATUS_CREATED"
	TransactionStatusInProgress = "ONRAMP_TRANSACTION_STATUS_IN_PROGRESS"
	TransactionStatusSuccess    = "ONRAMP_TRANSACTION_STATUS_SUCCESS"
	TransactionStatusFailed     = "ONRAMP_TRANSACTION_STATUS_FAILED"
)

// BuyTransaction models the subset of a CDP buy transaction downstream services
// need. Amounts stay strings; this package never does arithmetic on them.
type BuyTransaction struct {
	Status           string `json:"status"`
	TransactionID    string `json:"transaction_id"`
	TxHash           string `json:"tx_hash"`
	PurchaseAmount   string `json:"purchase_amount"`
	PurchaseCurrency string `json:"purchase_currency"`
	PurchaseNetwork  string `json:"purchase_network"`
	PaymentTotal     string `json:"payment_total"`
	FiatCurrency     string `json:"fiat_currency"`
	ExchangeRate     string `json:"exchange_rate"`
	CoinbaseFee      string `json:"coinbase_fee"`
	NetworkFee       string `json:"network_fee"`
	WalletAddress    string `json:"wallet_address"`
	CreatedAt        string `json:"created_at"`
}

func (t *BuyTransaction) UnmarshalJSON(data []byte) error {
	var raw struct {
		Status           string           `json:"status"`
		TransactionID    string           `json:"transaction_id"`
		TxHash           string           `json:"tx_hash"`
		PurchaseAmount   jsonAmountString `json:"purchase_amount"`
		PurchaseCurrency string           `json:"purchase_currency"`
		PurchaseNetwork  string           `json:"purchase_network"`
		PaymentTotal     jsonAmountString `json:"payment_total"`
		FiatCurrency     string           `json:"fiat_currency"`
		ExchangeRate     jsonAmountString `json:"exchange_rate"`
		CoinbaseFee      jsonAmountString `json:"coinbase_fee"`
		NetworkFee       jsonAmountString `json:"network_fee"`
		WalletAddress    string           `json:"wallet_address"`
		CreatedAt        string           `json:"created_at"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*t = BuyTransaction{
		Status:           raw.Status,
		TransactionID:    raw.TransactionID,
		TxHash:           raw.TxHash,
		PurchaseAmount:   string(raw.PurchaseAmount),
		PurchaseCurrency: raw.PurchaseCurrency,
		PurchaseNetwork:  raw.PurchaseNetwork,
		PaymentTotal:     string(raw.PaymentTotal),
		FiatCurrency:     raw.FiatCurrency,
		ExchangeRate:     string(raw.ExchangeRate),
		CoinbaseFee:      string(raw.CoinbaseFee),
		NetworkFee:       string(raw.NetworkFee),
		WalletAddress:    raw.WalletAddress,
		CreatedAt:        raw.CreatedAt,
	}
	return nil
}

// jsonAmountString keeps an amount as text whether CDP encodes it as a JSON
// string or a bare number.
type jsonAmountString string

func (a *jsonAmountString) UnmarshalJSON(data []byte) error {
	raw := strings.TrimSpace(string(data))
	if raw == "" || raw == "null" {
		*a = ""
		return nil
	}
	if strings.HasPrefix(raw, `"`) {
		var amount string
		if err := json.Unmarshal(data, &amount); err != nil {
			return err
		}
		*a = jsonAmountString(amount)
		return nil
	}
	var amount json.Number
	if err := json.Unmarshal(data, &amount); err != nil {
		return err
	}
	*a = jsonAmountString(amount.String())
	return nil
}

// jsonCountNumber accepts a count encoded as either a JSON string or a number.
type jsonCountNumber json.Number

func (n *jsonCountNumber) UnmarshalJSON(data []byte) error {
	var amount jsonAmountString
	if err := amount.UnmarshalJSON(data); err != nil {
		return err
	}
	*n = jsonCountNumber(amount)
	return nil
}
