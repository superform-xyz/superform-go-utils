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
	// ReferenceID is the partner-side handle for this attempt.
	ReferenceID string
}

// CreateConnectIDResponse carries the minted connect id.
type CreateConnectIDResponse struct {
	ConnectID string
}

// BuildConnectURLRequest describes the Robinhood Connect handoff URL. Every
// field but ConnectID and WalletAddress is optional and omitted when unset.
type BuildConnectURLRequest struct {
	ConnectID     string
	WalletAddress string
	// RedirectURL is where Robinhood returns the user, typically a deep link.
	RedirectURL string
	// SupportedNetworks and SupportedAssets are joined with commas.
	SupportedNetworks []string
	SupportedAssets   []string
	// FiatAmount is sent verbatim, so "100.00" stays "100.00".
	FiatAmount json.Number
	FiatCode   string
	AssetCode  string
	// LockAmount pins the amount in the Robinhood UI. It is set independently
	// of FiatAmount rather than inferred from it.
	LockAmount bool
}

// Order statuses reported by the order-details API, for comparison by the
// caller. An unrecognised status is passed through untouched.
const (
	OrderStatusInProgress = "ORDER_STATUS_IN_PROGRESS"
	OrderStatusSucceeded  = "ORDER_STATUS_SUCCEEDED"
	OrderStatusFailed     = "ORDER_STATUS_FAILED"
	OrderStatusCancelled  = "ORDER_STATUS_CANCELLED"
)

// Order models the subset of a Robinhood order downstream services need.
// CryptoAmount stays a string; this package never does arithmetic on it.
type Order struct {
	// ID is Robinhood's connectId as reported. Robinhood omits it on some
	// responses, where it is left empty rather than backfilled.
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

// jsonAmountString keeps an amount as text whether Robinhood encodes it as a
// JSON string or a bare number.
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
