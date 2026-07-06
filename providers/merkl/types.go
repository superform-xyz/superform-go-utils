package merkl

import "errors"

// RootInfo captures Merkl root metadata for one chain.
type RootInfo struct {
	Live string `json:"live"`
}

const (
	// MaxOpportunityItems is the maximum page size accepted by /v4/opportunities.
	MaxOpportunityItems = 100

	// OpportunityStatusLive is Merkl's live opportunity status.
	OpportunityStatusLive = "LIVE"
)

// OpportunityQuery contains filters accepted by /v4/opportunities.
type OpportunityQuery struct {
	ChainID        uint64
	Items          int
	Page           int
	MainProtocolID string
	Status         string
	Campaigns      bool
}

// OpportunityCountQuery contains filters accepted by /v4/opportunities/count.
type OpportunityCountQuery struct {
	ChainID        uint64
	MainProtocolID string
	Status         string
}

// Opportunity models the subset of Merkl opportunity fields used by backend services.
type Opportunity struct {
	ID                    string          `json:"id"`
	Name                  string          `json:"name"`
	Type                  string          `json:"type"`
	ChainID               int             `json:"chainId"`
	Identifier            string          `json:"identifier"`
	Status                string          `json:"status"`
	Action                string          `json:"action"`
	Apr                   float64         `json:"apr"`
	AprRecord             APRRecord       `json:"aprRecord"`
	NativeAPRRecord       NativeAPRRecord `json:"nativeAprRecord"`
	TVL                   float64         `json:"tvl"`
	DailyRewards          float64         `json:"dailyRewards"`
	LiveCampaigns         int             `json:"liveCampaigns"`
	Tags                  []string        `json:"tags"`
	ExplorerAddress       string          `json:"explorerAddress"`
	Tokens                []Token         `json:"tokens"`
	RewardsRecord         RewardsRecord   `json:"rewardsRecord"`
	Campaigns             []Campaign      `json:"campaigns"`
	EarliestCampaignStart int64           `json:"earliestCampaignStart"`
	LatestCampaignEnd     int64           `json:"latestCampaignEnd"`
}

// APRRecord contains the Merkl APR calculation timestamp and breakdowns.
type APRRecord struct {
	Cumulated  float64        `json:"cumulated"`
	Timestamp  string         `json:"timestamp"`
	Breakdowns []APRBreakdown `json:"breakdowns"`
}

// APRBreakdown captures an APR component from Merkl's opportunity payload.
type APRBreakdown struct {
	Identifier       string  `json:"identifier"`
	Type             string  `json:"type"`
	DistributionType string  `json:"distributionType"`
	Value            float64 `json:"value"`
}

// NativeAPRRecord contains optional native APR data in Merkl opportunity payloads.
type NativeAPRRecord struct {
	Title       string  `json:"title"`
	Description string  `json:"description"`
	Value       float64 `json:"value"`
	Timestamp   string  `json:"timestamp"`
}

// RewardsRecord contains the reward breakdowns for an opportunity.
type RewardsRecord struct {
	Total      float64           `json:"total"`
	Breakdowns []RewardBreakdown `json:"breakdowns"`
}

// RewardBreakdown captures an individual reward token.
type RewardBreakdown struct {
	CampaignID        string  `json:"campaignId"`
	OnChainCampaignID string  `json:"onChainCampaignId"`
	DistributionType  string  `json:"distributionType"`
	Amount            string  `json:"amount"`
	Token             Token   `json:"token"`
	Value             float64 `json:"value"`
}

// Token describes a reward token returned by the Merkl API.
type Token struct {
	ChainID  int     `json:"chainId"`
	Address  string  `json:"address"`
	Symbol   string  `json:"symbol"`
	Icon     string  `json:"icon"`
	Decimals int     `json:"decimals"`
	Price    float64 `json:"price"`
}

// ErrMissingTokenDecimals indicates the Merkl API returned a reward token without
// decimals, which are required to interpret reward amounts.
var ErrMissingTokenDecimals = errors.New("merkl: missing reward token decimals")

// RewardDecimals returns the token's decimals, or ErrMissingTokenDecimals when the
// Merkl API omitted them (reported as 0).
func (t Token) RewardDecimals() (int, error) {
	if t.Decimals == 0 {
		return 0, ErrMissingTokenDecimals
	}
	return t.Decimals, nil
}

// Campaign captures campaign-level details when /v4/opportunities is queried with campaigns=true.
type Campaign struct {
	CampaignID          string  `json:"campaignId"`
	OnChainCampaignID   string  `json:"onChainCampaignId"`
	Type                string  `json:"type"`
	ComputeChainID      int     `json:"computeChainId"`
	DistributionChainID int     `json:"distributionChainId"`
	StartTimestamp      int64   `json:"startTimestamp"`
	EndTimestamp        int64   `json:"endTimestamp"`
	Apr                 float64 `json:"apr"`
	DailyRewards        float64 `json:"dailyRewards"`
	RewardToken         Token   `json:"rewardToken"`
}

// UserRewardsChain captures Merkl user rewards grouped by chain.
type UserRewardsChain struct {
	Chain   UserRewardsChainInfo `json:"chain"`
	Rewards []UserReward         `json:"rewards"`
}

// UserRewardsChainInfo captures minimal chain metadata from Merkl.
type UserRewardsChainInfo struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Icon string `json:"icon"`
}

// UserReward describes a single Merkl reward entry.
type UserReward struct {
	Root                string                `json:"root"`
	DistributionChainID int                   `json:"distributionChainId"`
	Recipient           string                `json:"recipient"`
	Amount              string                `json:"amount"`
	Claimed             string                `json:"claimed"`
	Pending             string                `json:"pending"`
	Proofs              []string              `json:"proofs"`
	Token               UserRewardToken       `json:"token"`
	Breakdowns          []UserRewardBreakdown `json:"breakdowns"`
}

// UserRewardToken captures Merkl token metadata.
type UserRewardToken struct {
	Address  string  `json:"address"`
	ChainID  int     `json:"chainId"`
	Symbol   string  `json:"symbol"`
	Decimals uint32  `json:"decimals"`
	Price    float64 `json:"price"`
}

// UserRewardBreakdown captures optional breakdown info for a reward entry.
type UserRewardBreakdown struct {
	Root                string `json:"root"`
	DistributionChainID int    `json:"distributionChainId"`
	Reason              string `json:"reason"`
	Amount              string `json:"amount"`
	Claimed             string `json:"claimed"`
	Pending             string `json:"pending"`
	CampaignID          string `json:"campaignId"`
	SubCampaignID       string `json:"subCampaignId"`
}
