package merkl

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"time"
)

// HistoryClient keeps optional archive APIs separate from the live client contract.
type HistoryClient interface {
	Client
	ListHistoricalRoots(context.Context, uint64, time.Time, time.Time, int) ([]HistoricalRoot, error)
	GetHistoricalTree(context.Context, uint64, string, time.Time) (HistoricalTree, error)
	GetHistoricalRoot(context.Context, uint64, string, string) (HistoricalTree, error)
	GetClaimHistory(context.Context, string) ([]HistoricalClaim, error)
	GetPriceHistory(context.Context, uint64, string, int64, int64) ([]HistoricalPrice, error)
}

type HistoricalRoot struct {
	Root      string `json:"root"`
	ChainID   uint64 `json:"chainId"`
	Timestamp string `json:"timestamp"`
}

func (c *client) ListHistoricalRoots(ctx context.Context, chain uint64, from, through time.Time, page int) ([]HistoricalRoot, error) {
	if chain == 0 || !from.Before(through) || page < 0 {
		return nil, fmt.Errorf("valid chain, range and page are required")
	}
	var out []HistoricalRoot
	err := c.getJSON(ctx, "/v4/roots/", url.Values{"chainId": {strconv.FormatUint(chain, 10)}, "fromTimestamp": {strconv.FormatInt(from.Unix(), 10)}, "toTimestamp": {strconv.FormatInt(through.Unix(), 10)}, "items": {"100"}, "page": {strconv.Itoa(page)}}, &out)
	return out, err
}

type HistoricalTree struct {
	Root       string             `json:"root"`
	Timestamp  string             `json:"timestamp"`
	Breakdowns []HistoricalReward `json:"breakdowns"`
}

type HistoricalReward struct {
	CampaignID  string `json:"campaignId"`
	Recipient   string `json:"recipient"`
	Reason      string `json:"reason"`
	RewardToken string `json:"rewardToken"`
	Amount      string `json:"amount"`
}

type HistoricalClaim struct {
	ID        string `json:"id"`
	ChainID   uint64 `json:"chainId"`
	Recipient string `json:"recipient"`
	Timestamp int64  `json:"timestamp"`
	Token     string `json:"token"`
	Amount    string `json:"amount"`
	Metadata  struct {
		TransactionHash string `json:"transactionHash"`
	} `json:"metadata"`
}

type HistoricalPrice struct {
	Day     int64       `json:"day"`
	Close   json.Number `json:"close"`
	Samples int64       `json:"samples"`
}

func NewHistory(opts ...Option) (HistoryClient, error) {
	c, err := New(opts...)
	if err != nil {
		return nil, err
	}
	return c.(*client), nil
}

func (c *client) GetHistoricalTree(ctx context.Context, chain uint64, recipient string, at time.Time) (HistoricalTree, error) {
	var out HistoricalTree
	if chain == 0 || recipient == "" || at.IsZero() {
		return out, fmt.Errorf("chain, recipient and historical time are required")
	}
	err := c.getJSON(ctx, "/v4/historical/tree", url.Values{"chainId": {strconv.FormatUint(chain, 10)}, "recipient": {recipient}, "date": {at.UTC().Format(time.RFC3339)}}, &out)
	return out, err
}

func (c *client) GetHistoricalRoot(ctx context.Context, chain uint64, recipient, root string) (HistoricalTree, error) {
	var out HistoricalTree
	if chain == 0 || recipient == "" || root == "" {
		return out, fmt.Errorf("chain, recipient and root are required")
	}
	err := c.getJSON(ctx, "/v4/historical/tree", url.Values{"chainId": {strconv.FormatUint(chain, 10)}, "recipient": {recipient}, "root": {root}}, &out)
	return out, err
}

func (c *client) GetClaimHistory(ctx context.Context, recipient string) ([]HistoricalClaim, error) {
	if recipient == "" {
		return nil, fmt.Errorf("recipient is required")
	}
	var out []HistoricalClaim
	err := c.getJSON(ctx, "/v4/claims/history/"+url.PathEscape(recipient), nil, &out)
	return out, err
}

func (c *client) GetPriceHistory(ctx context.Context, chain uint64, token string, from, through int64) ([]HistoricalPrice, error) {
	if chain == 0 || token == "" || through < from {
		return nil, fmt.Errorf("valid chain, token and day range are required")
	}
	var out []HistoricalPrice
	err := c.getJSON(ctx, "/v4/tokens/prices/history", url.Values{"chainId": {strconv.FormatUint(chain, 10)}, "address": {token}, "from": {strconv.FormatInt(from, 10)}, "to": {strconv.FormatInt(through, 10)}, "fill": {"carry-forward"}}, &out)
	return out, err
}
