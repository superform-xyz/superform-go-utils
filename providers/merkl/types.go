package merkl

// RootInfo captures Merkl root metadata for one chain.
type RootInfo struct {
	Live string `json:"live"`
}

// Opportunity models the subset of Merkl opportunity fields used by backend services.
type Opportunity struct {
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	Type            string          `json:"type"`
	ChainID         int             `json:"chainId"`
	Identifier      string          `json:"identifier"`
	Status          string          `json:"status"`
	Action          string          `json:"action"`
	Apr             float64         `json:"apr"`
	AprRecord       APRRecord       `json:"aprRecord"`
	NativeAPRRecord NativeAPRRecord `json:"nativeAprRecord"`
	TVL             float64         `json:"tvl"`
	Tags            []string        `json:"tags"`
	ExplorerAddress string          `json:"explorerAddress"`
	Tokens          []Token         `json:"tokens"`
	RewardsRecord   RewardsRecord   `json:"rewardsRecord"`
}

// APRRecord contains the Merkl APR calculation timestamp and breakdowns.
type APRRecord struct {
	Cumulated  float64        `json:"cumulated"`
	Timestamp  string         `json:"timestamp"`
	Breakdowns []APRBreakdown `json:"breakdowns"`
}

// APRBreakdown captures an APR component from Merkl's opportunity payload.
type APRBreakdown struct {
	Type  string  `json:"type"`
	Value float64 `json:"value"`
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
	Breakdowns []RewardBreakdown `json:"breakdowns"`
}

// RewardBreakdown captures an individual reward token.
type RewardBreakdown struct {
	Token             Token   `json:"token"`
	Value             float64 `json:"value"`
	OnChainCampaignID string  `json:"onChainCampaignId"`
}

// Token describes a reward token returned by the Merkl API.
type Token struct {
	ChainID int    `json:"chainId"`
	Address string `json:"address"`
	Symbol  string `json:"symbol"`
	Icon    string `json:"icon"`
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
