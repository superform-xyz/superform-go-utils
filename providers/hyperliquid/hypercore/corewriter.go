package hypercore

import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/superform-xyz/superform-go-utils/utils/units"
)

const (
	// CoreWriterEncodingVersion is the CoreWriter payload version.
	CoreWriterEncodingVersion = byte(0x01)

	// ActionUsdClassTransfer is the usdClassTransfer action ID.
	ActionUsdClassTransfer = uint32(7)
	// ActionAddApiWallet is the addApiWallet action ID.
	ActionAddApiWallet = uint32(9)
	// ActionApproveBuilderFee is the approveBuilderFee action ID.
	ActionApproveBuilderFee = uint32(12)
	// ActionSendAsset is the sendAsset action ID.
	ActionSendAsset = uint32(13)

	// MaxBuilderFeeRatePerps is the maximum perp builder fee rate.
	MaxBuilderFeeRatePerps = uint64(100)
	// MaxBuilderFeeRateSpot is the maximum spot builder fee rate.
	MaxBuilderFeeRateSpot = uint64(1000)
)

// DexSpotSentinel identifies the spot DEX.
const DexSpotSentinel = uint32(0xFFFFFFFF)

var ntlScale = units.Pow10(NtlDecimals)

// CoreWriterAddress is the venue contract that emits RawAction evidence.
var CoreWriterAddress = common.HexToAddress("0x3333333333333333333333333333333333333333")

// HypeTokenIndex is the HYPE token index.
const HypeTokenIndex = uint64(150)

// HypeSystemAddress is the HYPE system address.
var HypeSystemAddress = common.HexToAddress("0x2222222222222222222222222222222222222222")

// TokenSystemAddress returns the system address of a spot token.
func TokenSystemAddress(tokenIndex uint64) common.Address {
	if tokenIndex == HypeTokenIndex {
		return HypeSystemAddress
	}
	base := new(big.Int).Lsh(big.NewInt(0x20), 152)
	return common.BigToAddress(base.Add(base, new(big.Int).SetUint64(tokenIndex)))
}

// UsdToNtl converts a USD amount to CoreWriter ntl units.
func UsdToNtl(usd *big.Rat) (uint64, error) {
	if usd == nil || usd.Sign() <= 0 {
		return 0, fmt.Errorf("usd amount must be positive, got %v", usd)
	}
	scaled := new(big.Rat).Mul(usd, new(big.Rat).SetInt(ntlScale))
	if !scaled.IsInt() {
		return 0, fmt.Errorf("usd amount %s does not convert exactly to ntl (1e%d) units", usd.FloatString(8), NtlDecimals)
	}
	num := scaled.Num()
	if !num.IsUint64() {
		return 0, fmt.Errorf("ntl amount %s exceeds the uint64 bound", num)
	}
	return num.Uint64(), nil
}

// NtlToUsd converts CoreWriter ntl units to a USD amount.
func NtlToUsd(ntl uint64) *big.Rat {
	return new(big.Rat).SetFrac(new(big.Int).SetUint64(ntl), new(big.Int).Set(ntlScale))
}

// UsdFromScaled converts a scaled HyperCore USD value to a USD amount.
func UsdFromScaled(scaled int64) *big.Rat {
	return new(big.Rat).SetFrac(big.NewInt(scaled), new(big.Int).Set(ntlScale))
}

// CoreWeiToAmount scales token wei without assigning the token a USD price.
func CoreWeiToAmount(coreWei uint64, weiDecimals uint8) *big.Rat {
	return new(big.Rat).SetFrac(
		new(big.Int).SetUint64(coreWei),
		units.Pow10(uint(weiDecimals)),
	)
}
