// Package confighooks decodes Ethereum configuration values with mapstructure.
package confighooks

import (
	"context"
	"crypto/ecdsa"
	"reflect"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/mitchellh/mapstructure"
	"github.com/pkg/errors"
)

var (
	ErrInvalidAddr     = errors.New("invalid address")
	ErrInvalidDataType = errors.New("invalid data type")
)

// ClientConfig controls how RPC clients are created during decoding.
// A nil Context uses context.Background and a nil Dial uses ethclient.DialContext.
// Verify is optional; callers choose which RPC capabilities to check.
type ClientConfig struct {
	Context context.Context
	Dial    func(context.Context, string) (*ethclient.Client, error)
	Verify  func(context.Context, *ethclient.Client, string) error
}

// NewEthClientHook decodes RPC URLs without making application-specific probes.
// The decoded client belongs to the caller; failed verification closes it.
func NewEthClientHook(config ClientConfig) mapstructure.DecodeHookFuncType {
	ctx := config.Context
	if ctx == nil {
		ctx = context.Background()
	}
	dial := config.Dial
	if dial == nil {
		dial = ethclient.DialContext
	}
	return func(from, to reflect.Type, data interface{}) (interface{}, error) {
		if to != reflect.TypeOf(ethclient.Client{}) {
			return data, nil
		}
		if from.Kind() != reflect.String {
			return data, ErrInvalidDataType
		}
		endpoint := data.(string)
		if endpoint == "" {
			return nil, nil
		}
		client, err := dial(ctx, endpoint)
		if err != nil {
			return nil, errors.Wrap(err, "failed to dial connection")
		}
		if config.Verify != nil {
			if err := config.Verify(ctx, client, endpoint); err != nil {
				client.Close()
				return nil, err
			}
		}
		return client, nil
	}
}

// PrivateKeyHook creates a mapstructure decode hook for converting hex-encoded private keys
// into ecdsa.PrivateKey objects. The input should be a hex string without the 0x prefix.
func PrivateKeyHook(f reflect.Type, t reflect.Type, data interface{}) (interface{}, error) {
	if t != reflect.TypeOf(ecdsa.PrivateKey{}) {
		return data, nil
	}

	if f.Kind() != reflect.String {
		return data, errors.Wrapf(ErrInvalidDataType, "expected=%s, acutal=%s", reflect.String.String(), f.Kind().String())
	}

	pk, err := crypto.HexToECDSA(data.(string))
	if err != nil {
		return data, errors.Wrap(err, "failed to init keypair")
	}

	return pk, nil
}

// AddressHook creates a mapstructure decode hook for converting string representations
// of Ethereum addresses into common.Address objects. Validates the hex address format.
func AddressHook(f reflect.Type, t reflect.Type, data interface{}) (interface{}, error) {
	if t != reflect.TypeOf(common.Address{}) || f != reflect.TypeOf("") {
		return data, nil
	}

	addrStr := data.(string)
	if !common.IsHexAddress(addrStr) {
		return data, errors.Wrapf(ErrInvalidAddr, "invalid address string '%s' for target common.Address", addrStr)
	}

	return common.HexToAddress(addrStr), nil
}

// AddressSliceHook creates a mapstructure decode hook for converting a slice of strings
// into a slice of common.Address. Each element is validated as a proper Ethereum address.
// Handles both direct string slices and slices of interface{} containing strings.
func AddressSliceHook(f reflect.Type, t reflect.Type, data interface{}) (interface{}, error) {
	// Check if the target type is indeed a slice of common.Address
	if t != reflect.TypeOf([]common.Address{}) {
		return data, nil
	}

	// Check if input is a slice
	if reflect.ValueOf(data).Kind() != reflect.Slice {
		return data, errors.Wrapf(ErrInvalidDataType, "expected slice input, got %T", data)
	}

	// Create a new slice of common.Address with the same capacity
	resultSlice := []common.Address{}
	dataVal := reflect.ValueOf(data)

	// Iterate over the input slice
	for i := range dataVal.Len() {
		elem := dataVal.Index(i)
		var addrStr string

		// Handle elements that might be interface{} wrapping a string, or direct strings
		switch elem.Kind() {
		case reflect.Interface:
			if elem.Elem().Kind() != reflect.String {
				return data, errors.Wrapf(ErrInvalidDataType, "element %d in slice is an interface but not a string (kind: %s)", i, elem.Elem().Kind())
			}

			addrStr = elem.Elem().String()

		case reflect.String:
			addrStr = elem.String()

		default:
			return data, errors.Wrapf(ErrInvalidDataType, "element %d in slice is not a string or interface{} (kind: %s)", i, elem.Kind())
		}

		if !common.IsHexAddress(addrStr) {
			return data, errors.Wrapf(ErrInvalidAddr, "element %d ('%s') is not a valid hex address", i, addrStr)
		}

		resultSlice = append(resultSlice, common.HexToAddress(addrStr))
	}

	return resultSlice, nil
}

// AddressMapHook creates a mapstructure decode hook for converting a map[string]string
// into a map[string]common.Address. Validates all values as proper Ethereum addresses.
func AddressMapHook(f reflect.Type, t reflect.Type, data interface{}) (interface{}, error) {
	// Check if the target type is map[string]common.Address
	if t != reflect.TypeOf(map[string]common.Address{}) {
		return data, nil
	}

	// Check if input is a map
	dataVal := reflect.ValueOf(data)
	if dataVal.Kind() != reflect.Map {
		return data, errors.Wrapf(ErrInvalidDataType, "expected map input, got %T", data)
	}

	// Check if the map key type is string
	if dataVal.Type().Key().Kind() != reflect.String {
		return data, errors.New("map keys must be strings")
	}

	// Convert the source map to map[string]string
	sourceMap := make(map[string]string, dataVal.Len())
	iter := dataVal.MapRange()
	for iter.Next() {
		key := iter.Key()
		value := iter.Value()

		// Convert value to string
		var strValue string
		switch v := value.Interface().(type) {
		case string:
			strValue = v
		default:
			return data, errors.Errorf("value for key %s is not a string (type: %T)", key.String(), value.Interface())
		}

		sourceMap[key.String()] = strValue
	}

	// Convert map[string]interface{} to map[string]common.Address
	result := make(map[string]common.Address)
	for k, v := range sourceMap {
		if !common.IsHexAddress(v) {
			return data, errors.Errorf("invalid address for key %s: %s", k, v)
		}

		result[k] = common.HexToAddress(v)
	}

	return result, nil
}

// DecodeTimeDurationHook creates a mapstructure decode hook for parsing time duration strings
// into time.Duration values. Supports standard Go duration strings (e.g., "5s", "1m30s").
func DecodeTimeDurationHook(from reflect.Type, to reflect.Type, data interface{}) (interface{}, error) {
	if from.Kind() == reflect.String && to == reflect.TypeOf(time.Duration(0)) {
		return time.ParseDuration(data.(string))
	}
	return data, nil
}

// CfgHooks combines the reusable hooks. RPC verification is selected by config.
func CfgHooks(config ClientConfig) mapstructure.DecodeHookFunc {
	return mapstructure.ComposeDecodeHookFunc(
		AddressHook,
		AddressSliceHook,
		NewEthClientHook(config),
		PrivateKeyHook,
		DecodeTimeDurationHook,
		AddressMapHook,
	)
}
