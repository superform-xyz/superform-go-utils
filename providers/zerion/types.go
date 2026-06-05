package zerion

import (
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strings"

	"github.com/ethereum/go-ethereum/common"

	"github.com/superform-xyz/superform-go-utils/utils/constants"
)

const (
	WalletPositionsNoFilter     = "no_filter"
	WalletPositionsCurrencyUSD  = "usd"
	WalletPositionsOnlyNonTrash = "only_non_trash"
	WalletPositionsSortValue    = "value"
)

var chainSlugByChainID = map[uint64]string{
	constants.MainnetChainID:   "ethereum",
	constants.ArbitrumChainID:  "arbitrum",
	constants.BaseChainID:      "base",
	constants.BscChainID:       "binance-smart-chain",
	constants.OptimismChainID:  "optimism",
	constants.MaticChainID:     "polygon",
	constants.AvalancheChainID: "avalanche",
	constants.UnichainChainID:  "unichain",
	constants.HyperEvmChainID:  "hyperevm",
}

var chainIDBySlug = map[string]uint64{
	"ethereum":            constants.MainnetChainID,
	"arbitrum":            constants.ArbitrumChainID,
	"base":                constants.BaseChainID,
	"binance-smart-chain": constants.BscChainID,
	"optimism":            constants.OptimismChainID,
	"polygon":             constants.MaticChainID,
	"avalanche":           constants.AvalancheChainID,
	"unichain":            constants.UnichainChainID,
	"hyperevm":            constants.HyperEvmChainID,
}

type WalletPositionsRequest struct {
	ChainIDs        []uint64
	ChainSlugs      []string
	PositionsFilter string
	Currency        string
	TrashFilter     string
	Sort            string
}

func DefaultWalletPositionsRequest(chainIDs []uint64) WalletPositionsRequest {
	return WalletPositionsRequest{
		ChainIDs:        chainIDs,
		PositionsFilter: WalletPositionsNoFilter,
		Currency:        WalletPositionsCurrencyUSD,
		TrashFilter:     WalletPositionsOnlyNonTrash,
		Sort:            WalletPositionsSortValue,
	}
}

func ChainSlug(chainID uint64) (string, bool) {
	slug, ok := chainSlugByChainID[chainID]
	return slug, ok
}

func ChainID(slug string) (uint64, bool) {
	chainID, ok := chainIDBySlug[strings.ToLower(strings.TrimSpace(slug))]
	return chainID, ok
}

func SupportedChains() []uint64 {
	out := make([]uint64, 0, len(chainSlugByChainID))
	for chainID := range chainSlugByChainID {
		out = append(out, chainID)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i] < out[j]
	})
	return out
}

func ChainFilterFromChainIDs(chainIDs []uint64) string {
	if len(chainIDs) == 0 {
		return ""
	}

	slugs := make([]string, 0, len(chainIDs))
	seen := make(map[string]struct{}, len(chainIDs))
	for _, chainID := range chainIDs {
		slug, ok := ChainSlug(chainID)
		if !ok || strings.TrimSpace(slug) == "" {
			continue
		}
		if _, ok := seen[slug]; ok {
			continue
		}
		seen[slug] = struct{}{}
		slugs = append(slugs, slug)
	}

	sort.Strings(slugs)
	return strings.Join(slugs, ",")
}

func ChainFilterFromSlugs(slugs []string) string {
	if len(slugs) == 0 {
		return ""
	}

	out := make([]string, 0, len(slugs))
	seen := make(map[string]struct{}, len(slugs))
	for _, slug := range slugs {
		slug = strings.ToLower(strings.TrimSpace(slug))
		if slug == "" {
			continue
		}
		if _, ok := seen[slug]; ok {
			continue
		}
		seen[slug] = struct{}{}
		out = append(out, slug)
	}

	sort.Strings(out)
	return strings.Join(out, ",")
}

type WalletPositionsResponse struct {
	Data []Position `json:"data"`
}

type Position struct {
	ChainID    uint64             `json:"-"`
	Attributes PositionAttributes `json:"attributes"`
}

func (p *Position) UnmarshalJSON(data []byte) error {
	type auxPosition struct {
		Attributes    PositionAttributes    `json:"attributes"`
		Relationships PositionRelationships `json:"relationships"`
	}

	aux := &auxPosition{}
	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}

	*p = Position{
		ChainID:    aux.Relationships.Chain.Data.ChainID,
		Attributes: aux.Attributes,
	}

	return nil
}

type PositionAttributes struct {
	Protocol     string         `json:"protocol"`
	PoolAddress  common.Address `json:"-"`
	PositionType string         `json:"position_type"`
	Quantity     Quantity       `json:"quantity"`
	Value        float64        `json:"value"`
	Price        float64        `json:"price"`
	FungibleInfo *FungibleInfo  `json:"fungible_info"`
	Flags        *PositionFlags `json:"flags"`
	UpdatedAt    string         `json:"updated_at"`
}

func (a *PositionAttributes) UnmarshalJSON(data []byte) error {
	type auxAttributes struct {
		Protocol     string         `json:"protocol"`
		PoolAddress  string         `json:"pool_address"`
		PositionType string         `json:"position_type"`
		Quantity     Quantity       `json:"quantity"`
		Value        float64        `json:"value"`
		Price        float64        `json:"price"`
		FungibleInfo *FungibleInfo  `json:"fungible_info"`
		Flags        *PositionFlags `json:"flags"`
		UpdatedAt    string         `json:"updated_at"`
	}

	aux := &auxAttributes{}
	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}

	*a = PositionAttributes{
		Protocol:     aux.Protocol,
		PositionType: aux.PositionType,
		Quantity:     aux.Quantity,
		Value:        aux.Value,
		Price:        aux.Price,
		FungibleInfo: aux.FungibleInfo,
		Flags:        aux.Flags,
		UpdatedAt:    aux.UpdatedAt,
	}
	if strings.TrimSpace(aux.PoolAddress) != "" {
		a.PoolAddress = common.HexToAddress(aux.PoolAddress)
	}

	return nil
}

type PositionFlags struct {
	Displayable bool `json:"displayable"`
	IsTrash     bool `json:"is_trash"`
}

type Quantity struct {
	RawAmount *big.Int `json:"-"`
	Decimals  int32    `json:"decimals"`
}

func (q *Quantity) UnmarshalJSON(data []byte) error {
	type auxQuantity struct {
		Int      string `json:"int"`
		Decimals int32  `json:"decimals"`
	}

	aux := &auxQuantity{}
	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}

	rawAmount := big.NewInt(0)
	if raw := strings.TrimSpace(aux.Int); raw != "" {
		parsed, ok := new(big.Int).SetString(raw, 10)
		if !ok {
			return fmt.Errorf("invalid zerion quantity int %q", raw)
		}
		rawAmount = parsed
	}

	*q = Quantity{
		RawAmount: rawAmount,
		Decimals:  aux.Decimals,
	}

	return nil
}

type FungibleInfo struct {
	Name            string           `json:"name"`
	Symbol          string           `json:"symbol"`
	Icon            Icon             `json:"icon"`
	Implementations []Implementation `json:"implementations"`
}

type Icon struct {
	URL string `json:"url"`
}

type Implementation struct {
	ChainID  uint64         `json:"-"`
	Address  common.Address `json:"-"`
	Decimals int32          `json:"decimals"`
	IsNative bool           `json:"-"`
}

func (i *Implementation) UnmarshalJSON(data []byte) error {
	type auxImplementation struct {
		ChainID  string  `json:"chain_id"`
		Address  *string `json:"address"`
		Decimals int32   `json:"decimals"`
	}

	aux := &auxImplementation{}
	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}

	*i = Implementation{
		Decimals: aux.Decimals,
	}
	if chainID, ok := ChainID(aux.ChainID); ok {
		i.ChainID = chainID
	}
	if aux.Address == nil || strings.TrimSpace(*aux.Address) == "" {
		i.Address = constants.GetNativeToken()
		i.IsNative = true
	} else {
		i.Address = common.HexToAddress(*aux.Address)
	}

	return nil
}

type PositionRelationships struct {
	Chain RelationshipChain `json:"chain"`
}

type RelationshipChain struct {
	Data RelationshipChainData `json:"data"`
}

type RelationshipChainData struct {
	ChainID uint64 `json:"-"`
}

func (d *RelationshipChainData) UnmarshalJSON(data []byte) error {
	type auxRelationshipChainData struct {
		ID string `json:"id"`
	}

	aux := &auxRelationshipChainData{}
	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}

	if chainID, ok := ChainID(aux.ID); ok {
		d.ChainID = chainID
	}

	return nil
}
