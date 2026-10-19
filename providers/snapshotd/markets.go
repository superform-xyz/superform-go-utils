package snapshotd

import (
	"fmt"

	"github.com/ethereum/go-ethereum/common"
)

const morphoBlueMarketKind = 12

type marketIdentity struct {
	protocol common.Address
	id       common.Hash
}

type marketCoverage struct {
	key          common.Address
	loanToken    common.Address
	loanDecimals uint8
	sides        map[string]struct{}
}

// validateMarketSources checks the combined-market wire contract. Valuation and
// protocol position discovery remain the snapshot service's responsibility.
func validateMarketSources(allocation *Allocation) error {
	markets := make(map[marketIdentity]*marketCoverage)
	accountingKeys := make(map[common.Address]struct{}, len(allocation.Sources))
	for i, source := range allocation.Sources {
		if source.Kind != morphoBlueMarketKind {
			continue
		}
		if source.MarketKey == (common.Address{}) || source.MarketID == (common.Hash{}) ||
			source.ProtocolAddress == (common.Address{}) || source.UnderlyingAsset == (common.Address{}) || source.UnderlyingDecimals == nil {
			return fmt.Errorf("snapshotd: sources[%d] has incomplete market metadata: %w", i, ErrInvalidResponse)
		}
		for _, field := range []struct{ name, value string }{
			{"underlyingTvl", source.UnderlyingTVL}, {"rawShares", source.RawShares},
		} {
			if err := validateDecimal(fmt.Sprintf("sources[%d].%s", i, field.name), field.value); err != nil {
				return err
			}
		}
		role := "asset"
		switch source.PositionSide {
		case "lending", "collateral":
		case "debt":
			role = "liability"
		default:
			return fmt.Errorf("snapshotd: sources[%d] has invalid market position side: %w", i, ErrInvalidResponse)
		}
		if source.PositionType != role {
			return fmt.Errorf("snapshotd: sources[%d] market side conflicts with position type: %w", i, ErrInvalidResponse)
		}
		if _, exists := accountingKeys[source.Source]; exists {
			return fmt.Errorf("snapshotd: duplicate market accounting key %s: %w", source.Source.Hex(), ErrInvalidResponse)
		}
		accountingKeys[source.Source] = struct{}{}
		identity := marketIdentity{protocol: source.ProtocolAddress, id: source.MarketID}
		market := markets[identity]
		if market == nil {
			market = &marketCoverage{key: source.MarketKey, sides: make(map[string]struct{}, 3)}
			markets[identity] = market
		}
		_, duplicateSide := market.sides[source.PositionSide]
		if market.key != source.MarketKey || duplicateSide {
			return fmt.Errorf("snapshotd: conflicting or duplicate market position %s: %w", source.MarketID.Hex(), ErrInvalidResponse)
		}
		market.sides[source.PositionSide] = struct{}{}
		if source.PositionSide != "collateral" {
			if market.loanToken != (common.Address{}) &&
				(market.loanToken != source.UnderlyingAsset || market.loanDecimals != *source.UnderlyingDecimals) {
				return fmt.Errorf("snapshotd: inconsistent market loan token %s: %w", source.MarketID.Hex(), ErrInvalidResponse)
			}
			market.loanToken, market.loanDecimals = source.UnderlyingAsset, *source.UnderlyingDecimals
		}
	}
	if len(markets) == 0 {
		return nil
	}
	if allocation.BlockNumber == nil {
		return fmt.Errorf("snapshotd: market allocation blockNumber is required: %w", ErrInvalidResponse)
	}
	for _, field := range []struct{ name, value string }{
		{"grossAssets", allocation.GrossAssets}, {"totalLiabilities", allocation.TotalLiabilities},
	} {
		if err := validateDecimal(field.name, field.value); err != nil {
			return err
		}
	}
	for identity, market := range markets {
		// A market without a collateral token legitimately has only these two rows.
		_, hasLending := market.sides["lending"]
		_, hasDebt := market.sides["debt"]
		if !hasLending || !hasDebt {
			return fmt.Errorf("snapshotd: market %s is missing lending or debt positions: %w", identity.id.Hex(), ErrInvalidResponse)
		}
	}
	return nil
}
