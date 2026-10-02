package coinbase

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

const (
	buyConfigPath  = "/onramp/v1/buy/config"
	buyOptionsPath = "/onramp/v1/buy/options"
)

// GetBuyConfig discovers supported countries, subdivisions and payment methods
// without creating a checkout session.
func (c *client) GetBuyConfig(ctx context.Context) (*GetBuyConfigResponse, error) {
	var out GetBuyConfigResponse
	if err := c.doJSON(ctx, http.MethodGet, buyConfigPath, "", nil, &out); err != nil {
		return nil, fmt.Errorf("coinbase get buy config: %w", err)
	}
	if out.Countries == nil {
		return nil, errors.New("coinbase get buy config: missing countries")
	}
	return &out, nil
}

// GetBuyOptions discovers fiat limits and purchase assets for one geography.
// Amounts retain their provider precision and are in the payment currency.
func (c *client) GetBuyOptions(ctx context.Context, req GetBuyOptionsRequest) (*GetBuyOptionsResponse, error) {
	country := strings.ToUpper(strings.TrimSpace(req.Country))
	subdivision := strings.ToUpper(strings.TrimSpace(req.Subdivision))
	if len(country) != 2 || country == "US" && subdivision == "" {
		return nil, errors.New("coinbase get buy options: country and US subdivision are required")
	}
	query := url.Values{"country": {country}}
	if subdivision != "" {
		query.Set("subdivision", subdivision)
	}
	var out GetBuyOptionsResponse
	if err := c.doJSON(ctx, http.MethodGet, buyOptionsPath, query.Encode(), nil, &out); err != nil {
		return nil, fmt.Errorf("coinbase get buy options: %w", err)
	}
	if out.PaymentCurrencies == nil || out.PurchaseCurrencies == nil {
		return nil, errors.New("coinbase get buy options: missing currency lists")
	}
	return &out, nil
}

// GetBuyConfigResponse contains the provider's current geography capabilities.
type GetBuyConfigResponse struct {
	Countries []BuyCountry `json:"countries"`
}

type BuyCountry struct {
	ID             string      `json:"id"`
	PaymentMethods []BuyMethod `json:"payment_methods"`
	Subdivisions   []string    `json:"subdivisions"`
}

type BuyMethod struct {
	ID string `json:"id"`
}

// GetBuyOptionsRequest identifies the user's country and, for the US, state.
type GetBuyOptionsRequest struct {
	Country     string
	Subdivision string
}

type GetBuyOptionsResponse struct {
	PaymentCurrencies  []BuyFiat  `json:"payment_currencies"`
	PurchaseCurrencies []BuyAsset `json:"purchase_currencies"`
}

type BuyFiat struct {
	ID     string     `json:"id"`
	Limits []BuyLimit `json:"limits"`
}

// BuyLimit is one payment method's bounds, denominated in its BuyFiat.ID.
type BuyLimit struct {
	ID  string `json:"id"`
	Min string `json:"min"`
	Max string `json:"max"`
}

type BuyAsset struct {
	ID       string       `json:"id"`
	Symbol   string       `json:"symbol"`
	Networks []BuyNetwork `json:"networks"`
}

type BuyNetwork struct {
	Name string `json:"name"`
	// CDP encodes chain IDs as strings, including empty/non-EVM identifiers.
	ChainID         string `json:"chain_id"`
	ContractAddress string `json:"contract_address"`
}
