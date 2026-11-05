package merkl

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

const claimsPath = "/v4/claims/"

// MaxUnscopedClaims is how many of the newest rows Merkl returns for a query
// without a recipient. Recipient-scoped queries return every matching row.
const MaxUnscopedClaims = 100

// ClaimLookup reads Merkl's per-campaign claim attribution without extending
// the read-only Client interface.
type ClaimLookup interface {
	// ListClaims returns the latest row per (recipient, token, campaign, reason).
	// Every new claim of a (recipient, token) leaf re-stamps all of its rows.
	ListClaims(context.Context, ClaimsQuery) ([]Claim, error)
	Close() error
}

func NewClaimLookup(opts ...Option) (ClaimLookup, error) {
	c, err := New(opts...)
	if err != nil {
		return nil, err
	}
	return c.(*client), nil
}

// ClaimsQuery filters GET /v4/claims/. Empty fields are omitted, Reason matches
// exactly, and From/Through are inclusive bounds.
type ClaimsQuery struct {
	ChainIDs   []uint64
	Recipient  string
	Token      string
	CampaignID string
	Reason     string
	Root       string
	From       time.Time
	Through    time.Time
}

type Claim struct {
	ID          string  `json:"id"`
	ChainID     ChainID `json:"chainId"`
	Recipient   string  `json:"recipient"`
	BlockNumber uint64  `json:"blockNumber"`
	Timestamp   int64   `json:"timestamp"`
	Token       string  `json:"token"`
	CampaignID  string  `json:"campaignId"`
	Reason      string  `json:"reason"`
	Root        string  `json:"root"`
	// Amount is cumulative for the campaign and reason, not the transaction delta.
	Amount   string        `json:"amount"`
	IsLatest bool          `json:"isLatest"`
	Metadata ClaimMetadata `json:"metadata"`
}

// ClaimMetadata links a row to its claim transaction. Migrated rows may carry
// neither field.
type ClaimMetadata struct {
	TransactionHash string `json:"transactionHash"`
	// EventAmount is the transaction's token-level delta, repeated on every row
	// of that claim.
	EventAmount string `json:"eventAmount"`
}

// ChainID accepts a positive chain ID encoded as a JSON number or numeric string.
type ChainID uint64

func (id *ChainID) UnmarshalJSON(data []byte) error {
	raw := strings.TrimSpace(string(data))
	if len(raw) >= 2 && raw[0] == '"' && raw[len(raw)-1] == '"' {
		raw = raw[1 : len(raw)-1]
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || value == 0 {
		return fmt.Errorf("invalid merkl chain id %s", data)
	}
	*id = ChainID(value)
	return nil
}

func (c *client) ListClaims(ctx context.Context, query ClaimsQuery) ([]Claim, error) {
	values, err := claimsQueryValues(query)
	if err != nil {
		return nil, err
	}
	var out []Claim
	if err := c.getJSON(ctx, claimsPath, values, &out); err != nil {
		return nil, fmt.Errorf("merkl claims: %w", err)
	}
	return out, nil
}

func claimsQueryValues(query ClaimsQuery) (url.Values, error) {
	if !query.From.IsZero() && !query.Through.IsZero() && query.From.After(query.Through) {
		return nil, fmt.Errorf("merkl claims time range is inverted")
	}
	values := url.Values{}
	chains := slices.Clone(query.ChainIDs)
	slices.Sort(chains)
	for _, chainID := range slices.Compact(chains) {
		if chainID == 0 {
			return nil, fmt.Errorf("merkl claims requires non-zero chain ids")
		}
		values.Add("chainIds", strconv.FormatUint(chainID, 10))
	}
	for key, value := range map[string]string{
		"recipient": query.Recipient, "token": query.Token, "campaignId": query.CampaignID, "root": query.Root,
	} {
		if value = strings.TrimSpace(value); value != "" {
			values.Set(key, value)
		}
	}
	if query.Reason != "" {
		values.Set("reason", query.Reason)
	}
	if !query.From.IsZero() {
		values.Set("fromTimestamp", strconv.FormatInt(query.From.Unix(), 10))
	}
	if !query.Through.IsZero() {
		values.Set("toTimestamp", strconv.FormatInt(query.Through.Unix(), 10))
	}
	return values, nil
}
