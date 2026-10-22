package merkl

import (
	"context"
	"net/url"
	"strconv"
)

// CampaignLookup reads indexed campaigns independently of unsigned preparation.
type CampaignLookup interface {
	ListCampaigns(context.Context, uint64, string) ([]Campaign, error)
	Close() error
}

func NewCampaignLookup(opts ...Option) (CampaignLookup, error) {
	c, err := New(opts...)
	if err != nil {
		return nil, err
	}
	return c.(*client), nil
}

func (c *client) ListCampaigns(ctx context.Context, chainID uint64, campaignID string) ([]Campaign, error) {
	var campaigns []Campaign
	err := c.getJSON(ctx, "/v4/campaigns", url.Values{
		"chainId": {strconv.FormatUint(chainID, 10)}, "campaignId": {campaignID},
	}, &campaigns)
	return campaigns, err
}
