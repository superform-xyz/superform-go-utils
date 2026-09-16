package robinhood

import (
	"encoding/json"
	"strings"
)

// CreateConnectIDRequest asks Robinhood for the connect id that identifies one
// transfer session.
type CreateConnectIDRequest struct {
	// WalletAddress is the destination the user's crypto is withdrawn to.
	WalletAddress string
	// ReferenceID is the partner-side handle for this attempt. Use
	// NewReferenceID unless the caller already has one.
	ReferenceID string
}

// CreateConnectIDResponse carries the minted connect id.
type CreateConnectIDResponse struct {
	ConnectID string
}

// BuildConnectURLRequest describes the Robinhood Connect handoff URL. The URL
// is a universal link: it opens the Robinhood app when installed and falls back
// to the web flow otherwise.
type BuildConnectURLRequest struct {
	ConnectID     string
	WalletAddress string
	// RedirectURL is where Robinhood returns the user, typically the caller's
	// own deep link. Required: without it the user is stranded in Robinhood at
	// the end of the flow.
	RedirectURL string
	// SupportedNetworks defaults to ETHEREUM, SupportedAssets to USDC.
	SupportedNetworks []string
	SupportedAssets   []string
	// FiatAmount is sent verbatim when set, and locks the amount in the
	// Robinhood UI so the user cannot buy a different size than the one the
	// caller already quoted.
	FiatAmount json.Number
	// FiatCode and AssetCode default to USD and USDC, and apply only when
	// FiatAmount is set.
	FiatCode  string
	AssetCode string
}

// Order statuses reported by the Robinhood order-details API.
const (
	OrderStatusInProgress = "ORDER_STATUS_IN_PROGRESS"
	OrderStatusSucceeded  = "ORDER_STATUS_SUCCEEDED"
	OrderStatusFailed     = "ORDER_STATUS_FAILED"
	OrderStatusCancelled  = "ORDER_STATUS_CANCELLED"
)

// IsTerminalOrderStatus reports whether a status will not change again, so a
// poller can stop.
func IsTerminalOrderStatus(status string) bool {
	switch status {
	case OrderStatusSucceeded, OrderStatusFailed, OrderStatusCancelled:
		return true
	default:
		return false
	}
}

func isKnownOrderStatus(status string) bool {
	return status == OrderStatusInProgress || IsTerminalOrderStatus(status)
}

// Order models the subset of a Robinhood order downstream services need.
// CryptoAmount stays a string: it is a settlement value, never an operand here.
type Order struct {
	// ID is Robinhood's connectId for the order.
	ID                      string
	Status                  string
	AssetCode               string
	NetworkCode             string
	CryptoAmount            string
	BlockchainTransactionID string
	DestinationAddress      string
	ReferenceID             string
}

// orderResponse is the wire shape of the order-details API, which is camelCase
// except for referenceID.
type orderResponse struct {
	ConnectID               string           `json:"connectId"`
	Status                  string           `json:"status"`
	AssetCode               string           `json:"assetCode"`
	NetworkCode             string           `json:"networkCode"`
	CryptoAmount            jsonAmountString `json:"cryptoAmount"`
	BlockchainTransactionID string           `json:"blockchainTransactionId"`
	DestinationAddress      string           `json:"destinationAddress"`
	ReferenceID             string           `json:"referenceID"`
}

func (r orderResponse) toOrder() Order {
	return Order{
		ID:                      r.ConnectID,
		Status:                  r.Status,
		AssetCode:               r.AssetCode,
		NetworkCode:             r.NetworkCode,
		CryptoAmount:            string(r.CryptoAmount),
		BlockchainTransactionID: r.BlockchainTransactionID,
		DestinationAddress:      r.DestinationAddress,
		ReferenceID:             r.ReferenceID,
	}
}

// jsonAmountString accepts an amount whether Robinhood encodes it as a JSON
// string or a bare number, and keeps it as text either way.
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
