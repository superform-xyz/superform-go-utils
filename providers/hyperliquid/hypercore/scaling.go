package hypercore

import (
	"fmt"
	"math"
	"math/big"

	"github.com/superform-xyz/superform-go-utils/utils/units"
)

var maxCoreWei = new(big.Int).SetUint64(math.MaxUint64)

// EvmToCoreWei converts an EVM token amount to HyperCore wei.
func EvmToCoreWei(evmAmount *big.Int, evmExtraWeiDecimals int8) (uint64, error) {
	coreWei, remainder, err := evmToCoreWei(evmAmount, evmExtraWeiDecimals)
	if err != nil {
		return 0, err
	}
	if remainder.Sign() != 0 {
		return 0, fmt.Errorf("evm amount %s does not convert exactly to core wei with extra decimals %d: remainder %s",
			evmAmount, evmExtraWeiDecimals, remainder)
	}
	return coreWei, nil
}

// EvmToCoreWeiFloor converts an EVM token amount to HyperCore wei by flooring.
func EvmToCoreWeiFloor(evmAmount *big.Int, evmExtraWeiDecimals int8) (uint64, *big.Int, error) {
	coreWei, remainder, err := evmToCoreWei(evmAmount, evmExtraWeiDecimals)
	if err != nil {
		return 0, nil, err
	}
	return coreWei, remainder, nil
}

func evmToCoreWei(evmAmount *big.Int, evmExtraWeiDecimals int8) (uint64, *big.Int, error) {
	if evmAmount == nil || evmAmount.Sign() < 0 {
		return 0, nil, fmt.Errorf("evm amount must be a non-negative integer, got %s", evmAmount)
	}

	core := new(big.Int)
	remainder := new(big.Int)
	if evmExtraWeiDecimals >= 0 {
		core.QuoRem(evmAmount, units.Pow10(uint(evmExtraWeiDecimals)), remainder)
	} else {
		core.Mul(evmAmount, units.Pow10(uint(-int(evmExtraWeiDecimals))))
	}

	if core.Cmp(maxCoreWei) > 0 {
		return 0, nil, fmt.Errorf("core wei amount %s exceeds the uint64 bound HyperCore enforces", core)
	}
	return core.Uint64(), remainder, nil
}

// CoreWeiToEvm converts HyperCore wei to EVM token units.
func CoreWeiToEvm(coreWei uint64, evmExtraWeiDecimals int8) (*big.Int, error) {
	evm, remainder := coreWeiToEvm(coreWei, evmExtraWeiDecimals)
	if remainder != 0 {
		return nil, fmt.Errorf("core wei amount %d does not convert exactly to evm units with extra decimals %d: remainder %d",
			coreWei, evmExtraWeiDecimals, remainder)
	}
	return evm, nil
}

// CoreWeiToEvmFloor converts HyperCore wei to EVM token units by flooring.
func CoreWeiToEvmFloor(coreWei uint64, evmExtraWeiDecimals int8) (*big.Int, uint64) {
	return coreWeiToEvm(coreWei, evmExtraWeiDecimals)
}

func coreWeiToEvm(coreWei uint64, evmExtraWeiDecimals int8) (*big.Int, uint64) {
	core := new(big.Int).SetUint64(coreWei)
	if evmExtraWeiDecimals >= 0 {
		return core.Mul(core, units.Pow10(uint(evmExtraWeiDecimals))), 0
	}

	remainder := new(big.Int)
	evm, _ := new(big.Int).QuoRem(core, units.Pow10(uint(-int(evmExtraWeiDecimals))), remainder)
	return evm, remainder.Uint64()
}
