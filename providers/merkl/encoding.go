package merkl

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
)

// CampaignClient prepares unsigned campaigns; it never signs or broadcasts.
type CampaignClient interface {
	EncodeCampaign(context.Context, JSONAirdropConfig) (EncodedCampaign, error)
	RewardTokens(context.Context, uint64) ([]CampaignRewardToken, error)
	Close() error
}

type AirdropAllocation struct {
	RewardToken string                       `json:"rewardToken"`
	Rewards     map[string]map[string]string `json:"rewards"`
}

type JSONAirdropConfig struct {
	CampaignType        uint32 `json:"campaignType"`
	DistributionChainID uint64 `json:"distributionChainId"`
	ComputeChainID      uint64 `json:"computeChainId"`
	Creator             string `json:"creator"`
	RewardToken         string `json:"rewardToken"`
	Amount              string `json:"amount"`
	StartTimestamp      uint32 `json:"startTimestamp"`
	JSONURL             string `json:"jsonUrl"`
	Name                string `json:"url"`
}

type CampaignArguments struct {
	CampaignID     string `json:"campaignId"`
	Creator        string `json:"creator"`
	RewardToken    string `json:"rewardToken"`
	Amount         string `json:"amount"`
	CampaignType   uint32 `json:"campaignType"`
	StartTimestamp uint32 `json:"startTimestamp"`
	Duration       uint32 `json:"duration"`
	CampaignData   string `json:"campaignData"`
}

type EncodedCampaign struct {
	Args    CampaignArguments `json:"args"`
	Payload string            `json:"payload"`
}

type CampaignRewardToken struct {
	Address              string `json:"address"`
	ChainID              uint64 `json:"chainId"`
	Decimals             int    `json:"decimals"`
	MinimumAmountPerHour string `json:"minimumAmountPerHour"`
}

// NewCampaignClient shares the transport, credentials and limits of New without
// extending the read-only Client interface implemented by existing consumers.
func NewCampaignClient(opts ...Option) (CampaignClient, error) {
	c, err := New(opts...)
	if err != nil {
		return nil, err
	}
	return c.(*client), nil
}

func (c *client) RewardTokens(ctx context.Context, chainID uint64) ([]CampaignRewardToken, error) {
	var out []CampaignRewardToken
	if chainID == 0 {
		return nil, fmt.Errorf("reward tokens requires a chain ID")
	}
	err := c.getJSON(ctx, "/v4/tokens/reward/"+strconv.FormatUint(chainID, 10), nil, &out)
	return out, err
}

func (c *client) EncodeCampaign(ctx context.Context, config JSONAirdropConfig) (EncodedCampaign, error) {
	var out EncodedCampaign
	body, err := json.Marshal(config)
	if err != nil {
		return out, err
	}
	if c.limiter != nil {
		if err := c.limiter.Wait(ctx); err != nil {
			return out, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v4/config/encode", bytes.NewReader(body))
	if err != nil {
		return out, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.apiKey != "" {
		req.Header.Set(apiKeyHeader, c.apiKey)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return out, fmt.Errorf("encode campaign request: %w", err)
	}
	defer resp.Body.Close()
	// Do not log provider error bodies: they can echo full allocations or keys.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return out, fmt.Errorf("encode campaign HTTP status %d", resp.StatusCode)
	}
	body, err = io.ReadAll(io.LimitReader(resp.Body, maxResponseBody+1))
	if err != nil {
		return out, err
	}
	if len(body) > maxResponseBody {
		return out, fmt.Errorf("encoded campaign response exceeds size limit")
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return out, fmt.Errorf("decode campaign response: %w", err)
	}
	if out.Payload == "" || out.Args.Amount == "" {
		return out, fmt.Errorf("encoded campaign response is incomplete")
	}
	return out, nil
}
