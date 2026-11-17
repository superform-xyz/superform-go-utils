package zerion

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/superform-xyz/superform-go-utils/utils/constants"
)

var nativeTokenAddress = strings.ToLower(constants.GetNativeToken().Hex())

type Callback struct {
	Data     CallbackData          `json:"data"`
	Included []CallbackTransaction `json:"included"`
}

type CallbackData struct {
	ID            string                    `json:"id"`
	Type          string                    `json:"type"`
	Attributes    CallbackDataAttributes    `json:"attributes"`
	Relationships CallbackDataRelationships `json:"relationships"`
}

type CallbackDataAttributes struct {
	Address     string `json:"address"`
	CallbackURL string `json:"callback_url"`
	Timestamp   string `json:"timestamp"`
}

type CallbackDataRelationships struct {
	Subscription CallbackRelationship `json:"subscription"`
}

// CallbackRelationship accepts both {"id":..} and {"data":{"id":..}}; other shapes decode as empty.
type CallbackRelationship struct {
	Type string              `json:"type"`
	ID   string              `json:"id"`
	Data *ResourceIdentifier `json:"data"`
}

func (r *CallbackRelationship) UnmarshalJSON(data []byte) error {
	type plain CallbackRelationship
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		*r = CallbackRelationship{}
		return nil
	}
	*r = CallbackRelationship(decoded)
	return nil
}

func (r CallbackRelationship) ResourceID() string {
	if r.Data != nil {
		if id := strings.TrimSpace(r.Data.ID); id != "" {
			return id
		}
	}
	return strings.TrimSpace(r.ID)
}

type CallbackTransaction struct {
	Type          string                           `json:"type"`
	ID            string                           `json:"id"`
	Attributes    CallbackTransactionAttributes    `json:"attributes"`
	Relationships CallbackTransactionRelationships `json:"relationships"`
}

type CallbackTransactionRelationships struct {
	Chain CallbackRelationship `json:"chain"`
	Dapp  CallbackRelationship `json:"dapp"`
}

type CallbackTransactionAttributes struct {
	Hash          string            `json:"hash"`
	MinedAt       *time.Time        `json:"-"`
	MinedAtBlock  *uint64           `json:"-"`
	Status        string            `json:"status"`
	Deleted       bool              `json:"deleted"`
	OperationType string            `json:"operation_type"`
	SentFrom      string            `json:"sent_from"`
	SentTo        string            `json:"sent_to"`
	Flags         *TransactionFlags `json:"flags"`
	Transfers     json.RawMessage   `json:"transfers"` // decoded only by DecodeTransfers
}

type TransactionFlags struct {
	IsTrash bool `json:"is_trash"`
}

// UnmarshalJSON leaves MinedAt/MinedAtBlock nil when absent or unparseable.
func (a *CallbackTransactionAttributes) UnmarshalJSON(data []byte) error {
	type attributesJSON CallbackTransactionAttributes
	var decoded attributesJSON
	aux := struct {
		*attributesJSON
		MinedAt      json.RawMessage `json:"mined_at"`
		MinedAtBlock json.RawMessage `json:"mined_at_block"`
	}{attributesJSON: &decoded}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	*a = CallbackTransactionAttributes(decoded)
	var minedAt string
	if json.Unmarshal(aux.MinedAt, &minedAt) == nil {
		if t, err := ParseCallbackTimestamp(minedAt); err == nil {
			a.MinedAt = &t
		}
	}
	if block, ok := parseBlockNumber(aux.MinedAtBlock); ok {
		a.MinedAtBlock = &block
	}
	return nil
}

func parseBlockNumber(raw json.RawMessage) (uint64, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return 0, false
	}
	s := string(raw)
	if raw[0] == '"' {
		if err := json.Unmarshal(raw, &s); err != nil {
			return 0, false
		}
	}
	n, err := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
	return n, err == nil
}

func ParseCallback(body []byte) (Callback, error) {
	var cb Callback
	if err := json.Unmarshal(body, &cb); err != nil {
		return Callback{}, fmt.Errorf("zerion: decode callback: %w", err)
	}
	return cb, nil
}

func (c Callback) WalletAddress() string {
	return strings.ToLower(strings.TrimSpace(c.Data.Attributes.Address))
}

func (c Callback) SubscriptionID() string {
	return c.Data.Relationships.Subscription.ResourceID()
}

func (t CallbackTransaction) ChainSlug() string {
	return strings.ToLower(t.Relationships.Chain.ResourceID())
}

// ChainID maps ChainSlug through the package slug map.
func (t CallbackTransaction) ChainID() (uint64, bool) {
	return ChainID(t.ChainSlug())
}

func (t CallbackTransaction) IsTrash() bool {
	return t.Attributes.Flags != nil && t.Attributes.Flags.IsTrash
}

func (t CallbackTransaction) DecodeTransfers() ([]CallbackTransfer, error) {
	raw := bytes.TrimSpace(t.Attributes.Transfers)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}
	var transfers []CallbackTransfer
	if err := json.Unmarshal(raw, &transfers); err != nil {
		return nil, fmt.Errorf("zerion: decode transfers: %w", err)
	}
	return transfers, nil
}

type CallbackTransfer struct {
	Direction    string        `json:"direction"`
	Quantity     Quantity      `json:"quantity"` // int + decimals; float is never read
	Sender       string        `json:"sender"`
	Recipient    string        `json:"recipient"`
	ActID        string        `json:"act_id"`
	FungibleInfo *FungibleInfo `json:"fungible_info"`
}

// TokenAddress returns the lowercase token address and decimals for chainID; native maps to 0xeeee…eeee.
func (t CallbackTransfer) TokenAddress(chainID uint64) (string, int, bool) {
	if t.FungibleInfo == nil || chainID == 0 {
		return "", 0, false
	}
	for _, impl := range t.FungibleInfo.Implementations {
		if impl.ChainID != chainID {
			continue
		}
		address := strings.ToLower(impl.Address.Hex())
		if impl.IsNative {
			address = nativeTokenAddress
		}
		decimals := int(impl.Decimals)
		if decimals <= 0 {
			decimals = int(t.Quantity.Decimals)
		}
		return address, decimals, true
	}
	return "", 0, false
}
