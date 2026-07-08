package transak

import (
	"encoding/json"
	"strings"
	"time"
)

// AccessToken contains the partner access token minted from Transak.
type AccessToken struct {
	Token     string
	ExpiresAt time.Time
}

// CreateWidgetSessionRequest contains the widget params sent to Transak's
// create-session endpoint plus request-only metadata.
type CreateWidgetSessionRequest struct {
	WidgetParams WidgetParams
	UserIP       string
}

// WidgetParams is the allow-listed widget parameter set Superform uses for BUY.
type WidgetParams struct {
	APIKey                   string      `json:"apiKey,omitempty"`
	ReferrerDomain           string      `json:"referrerDomain,omitempty"`
	PartnerOrderID           string      `json:"partnerOrderId,omitempty"`
	PartnerCustomerID        string      `json:"partnerCustomerId,omitempty"`
	WalletAddress            string      `json:"walletAddress,omitempty"`
	DisableWalletAddressForm bool        `json:"disableWalletAddressForm"`
	ProductsAvailed          string      `json:"productsAvailed,omitempty"`
	Network                  string      `json:"network,omitempty"`
	DefaultCryptoCurrency    string      `json:"defaultCryptoCurrency,omitempty"`
	PaymentMethod            string      `json:"paymentMethod,omitempty"`
	DefaultFiatCurrency      string      `json:"defaultFiatCurrency,omitempty"`
	CountryCode              string      `json:"countryCode,omitempty"`
	DefaultFiatAmount        json.Number `json:"defaultFiatAmount,omitempty"`
}

// CreateWidgetSessionResponse is the safe subset returned by Transak.
type CreateWidgetSessionResponse struct {
	WidgetURL string
}

// GetOrdersRequest filters partner orders by Superform's partnerOrderId.
type GetOrdersRequest struct {
	PartnerOrderID string
	Limit          int
	Skip           int
}

// GetOrdersResponse contains Transak orders matching a partnerOrderId filter.
type GetOrdersResponse struct {
	Orders []Order
}

// Order models the subset of Transak order fields needed by persephone.
type Order struct {
	ID                string `json:"_id"`
	TransakOrderID    string `json:"transakOrderId"`
	Status            string `json:"status"`
	PartnerOrderID    string `json:"partnerOrderId"`
	PartnerCustomerID string `json:"partnerCustomerId"`
	CryptoCurrency    string `json:"cryptoCurrency"`
	FiatCurrency      string `json:"fiatCurrency"`
	FiatAmount        string `json:"fiatAmount"`
	CryptoAmount      string `json:"cryptoAmount"`
}

func (o *Order) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID                string           `json:"_id"`
		LegacyID          string           `json:"id"`
		TransakOrderID    string           `json:"transakOrderId"`
		Status            string           `json:"status"`
		PartnerOrderID    string           `json:"partnerOrderId"`
		PartnerCustomerID string           `json:"partnerCustomerId"`
		CryptoCurrency    string           `json:"cryptoCurrency"`
		FiatCurrency      string           `json:"fiatCurrency"`
		FiatAmount        jsonAmountString `json:"fiatAmount"`
		CryptoAmount      jsonAmountString `json:"cryptoAmount"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	id := raw.ID
	if id == "" {
		id = raw.LegacyID
	}
	*o = Order{
		ID:                id,
		TransakOrderID:    raw.TransakOrderID,
		Status:            raw.Status,
		PartnerOrderID:    raw.PartnerOrderID,
		PartnerCustomerID: raw.PartnerCustomerID,
		CryptoCurrency:    raw.CryptoCurrency,
		FiatCurrency:      raw.FiatCurrency,
		FiatAmount:        string(raw.FiatAmount),
		CryptoAmount:      string(raw.CryptoAmount),
	}
	return nil
}

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
