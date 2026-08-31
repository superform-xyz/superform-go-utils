// Package polymarket contains dependency-light Polymarket V2 protocol facts
// and signed-order primitives shared by backend services.
package polymarket

import (
	"fmt"

	"github.com/ethereum/go-ethereum/common"
)

const (
	// PolygonChainID is the only chain supported by Polymarket V2.
	PolygonChainID uint64 = 137

	// USDCEAddress is the canonical Polygon USDC.e collateral token.
	USDCEAddress = "0x2791bca1f2de4661ed88a30c99a7a9449aa84174"
	// PUSDAddress is the canonical Polymarket USD token.
	PUSDAddress = "0xc011a7e12a19f7b1f670d46f03b03f3342e82dfb"
	// ConditionalTokensAddress is the canonical Polymarket conditional-tokens contract.
	ConditionalTokensAddress = "0x4d97dcd97ec945f40cf65f87097ace5ea0476045"
	// StandardExchangeAddress is the canonical standard-market exchange.
	StandardExchangeAddress = "0xe111180000d2663c0091e4f400237545b87b996b"
	// NegRiskExchangeAddress is the canonical Neg Risk exchange.
	NegRiskExchangeAddress = "0xe2222d279d744050d28e00520010520000310f59"
)

// MarketType identifies one of the independently deployed Polymarket V2
// exchange families.
type MarketType string

const (
	// MarketTypeStandard identifies a standard Polymarket market.
	MarketTypeStandard MarketType = "STANDARD"
	// MarketTypeNegRisk identifies a Neg Risk Polymarket market.
	MarketTypeNegRisk MarketType = "NEG_RISK"
)

var (
	usdceContract             = common.HexToAddress(USDCEAddress)
	pusdContract              = common.HexToAddress(PUSDAddress)
	conditionalTokensContract = common.HexToAddress(ConditionalTokensAddress)
	standardExchangeContract  = common.HexToAddress(StandardExchangeAddress)
	negRiskExchangeContract   = common.HexToAddress(NegRiskExchangeAddress)
)

// ParseMarketType parses an exact market-family discriminant.
func ParseMarketType(value string) (MarketType, error) {
	marketType := MarketType(value)
	switch marketType {
	case MarketTypeStandard, MarketTypeNegRisk:
		return marketType, nil
	default:
		return "", fmt.Errorf("polymarket: unsupported market type %q", value)
	}
}

// ExchangeAddress returns the immutable lowercase exchange address for a
// market family.
func ExchangeAddress(marketType MarketType) (string, error) {
	switch marketType {
	case MarketTypeStandard:
		return StandardExchangeAddress, nil
	case MarketTypeNegRisk:
		return NegRiskExchangeAddress, nil
	default:
		return "", fmt.Errorf("polymarket: unsupported market type %q", marketType)
	}
}

// Exchange returns the exchange that verifies and settles this market family.
func (m MarketType) Exchange() (common.Address, error) {
	switch m {
	case MarketTypeStandard:
		return standardExchangeContract, nil
	case MarketTypeNegRisk:
		return negRiskExchangeContract, nil
	default:
		return common.Address{}, fmt.Errorf("polymarket: unsupported market type %q", m)
	}
}

// IsNegRisk reports whether this market family uses the Neg Risk exchange.
func (m MarketType) IsNegRisk() (bool, error) {
	switch m {
	case MarketTypeStandard:
		return false, nil
	case MarketTypeNegRisk:
		return true, nil
	default:
		return false, fmt.Errorf("polymarket: unsupported market type %q", m)
	}
}

// USDCEContract returns the canonical Polygon USDC.e token by value.
func USDCEContract() common.Address { return usdceContract }

// PUSDContract returns the canonical pUSD token by value.
func PUSDContract() common.Address { return pusdContract }

// ConditionalTokensContract returns the canonical Conditional Tokens contract by value.
func ConditionalTokensContract() common.Address {
	return conditionalTokensContract
}

// StandardExchangeContract returns the canonical standard-market exchange by value.
func StandardExchangeContract() common.Address {
	return standardExchangeContract
}

// NegRiskExchangeContract returns the canonical Neg Risk exchange by value.
func NegRiskExchangeContract() common.Address {
	return negRiskExchangeContract
}
