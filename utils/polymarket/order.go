package polymarket

import (
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

const (
	// OrderSignatureTypePoly1271 is Polymarket's ERC-1271 smart-account signature type.
	OrderSignatureTypePoly1271 uint8 = 3
	// OrderDomainName is the official Polymarket V2 EIP-712 domain name.
	OrderDomainName = "Polymarket CTF Exchange"
	// OrderDomainVersion is the official Polymarket V2 EIP-712 domain version.
	OrderDomainVersion = "2"
	// OrderPriceScaleE6 is the fixed-point price scale used by the V2 builder.
	OrderPriceScaleE6 uint64 = 1_000_000
	// OrderSizeGridE6 restricts size inputs to at most two decimal places.
	OrderSizeGridE6 uint64 = 10_000
	// MaxSafeOrderSalt keeps the JSON number exactly representable by JavaScript clients.
	MaxSafeOrderSalt uint64 = 1<<53 - 1

	maxUint256DecimalDigits = 78
)

// OrderSide is the signed V2 order side.
type OrderSide uint8

const (
	// OrderSideBuy spends collateral for outcome tokens.
	OrderSideBuy OrderSide = 0
	// OrderSideSell spends outcome tokens for collateral.
	OrderSideSell OrderSide = 1
)

var (
	orderTypeHash = crypto.Keccak256Hash([]byte(
		"Order(uint256 salt,address maker,address signer,uint256 tokenId,uint256 makerAmount,uint256 takerAmount,uint8 side,uint8 signatureType,uint256 timestamp,bytes32 metadata,bytes32 builder)",
	))
	domainTypeHash = crypto.Keccak256Hash([]byte(
		"EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)",
	))
	orderProtocolName = crypto.Keccak256Hash([]byte(OrderDomainName))
	orderProtocolV2   = crypto.Keccak256Hash([]byte(OrderDomainVersion))
)

// Order contains exactly the fields signed by a Polymarket V2 order. Wire-only
// submission fields such as expiration, order type, and post-only are
// deliberately absent and therefore cannot affect Hash.
type Order struct {
	Salt          uint64
	Maker         common.Address
	Signer        common.Address
	TokenID       string
	MakerAmount   string
	TakerAmount   string
	Side          OrderSide
	SignatureType uint8
	Timestamp     uint64
	Metadata      common.Hash
	Builder       common.Hash
}

// BuildOrderInput supplies every signed value, plus price/size inputs used to
// derive the signed maker and taker amounts.
type BuildOrderInput struct {
	Salt          uint64
	Maker         common.Address
	Signer        common.Address
	TokenID       *big.Int
	PriceE6       uint64
	SizeE6        *big.Int
	TickSizeE6    uint64
	Side          OrderSide
	SignatureType uint8
	Timestamp     uint64
	Metadata      common.Hash
	Builder       common.Hash
}

// BuildOrder validates the V2 price and size grids, normalizes maker/taker
// amounts, and returns only the signed order fields.
func BuildOrder(input BuildOrderInput) (Order, error) {
	if input.Maker == (common.Address{}) || input.Signer == (common.Address{}) {
		return Order{}, errors.New("polymarket: maker and signer are required")
	}
	if input.TokenID == nil || input.TokenID.Sign() <= 0 || input.TokenID.BitLen() > 256 {
		return Order{}, errors.New("polymarket: token_id must be a positive uint256")
	}
	if input.PriceE6 == 0 || input.PriceE6 >= OrderPriceScaleE6 {
		return Order{}, errors.New("polymarket: price_e6 must be between 1 and 999999")
	}
	if input.SizeE6 == nil || input.SizeE6.Sign() <= 0 || input.SizeE6.BitLen() > 256 {
		return Order{}, errors.New("polymarket: size_e6 must be a positive uint256")
	}
	if new(big.Int).Mod(new(big.Int).Set(input.SizeE6), new(big.Int).SetUint64(OrderSizeGridE6)).Sign() != 0 {
		return Order{}, errors.New("polymarket: size_e6 must use at most two decimal places")
	}
	if !SupportedTickSizeE6(input.TickSizeE6) || input.PriceE6%input.TickSizeE6 != 0 {
		return Order{}, fmt.Errorf(
			"polymarket: price_e6 %d is not on supported tick-size grid %d",
			input.PriceE6,
			input.TickSizeE6,
		)
	}
	if input.Side != OrderSideBuy && input.Side != OrderSideSell {
		return Order{}, errors.New("polymarket: side must be BUY or SELL")
	}
	if input.SignatureType != OrderSignatureTypePoly1271 {
		return Order{}, errors.New("polymarket: signature type must be POLY_1271")
	}
	if input.Salt > MaxSafeOrderSalt {
		return Order{}, errors.New("polymarket: salt exceeds Number.MAX_SAFE_INTEGER")
	}
	if input.Timestamp == 0 || input.Timestamp > 1<<63-1 {
		return Order{}, errors.New("polymarket: timestamp is invalid")
	}

	makerAmount, takerAmount, err := NormalizeOrderAmounts(input.Side, input.PriceE6, input.SizeE6)
	if err != nil {
		return Order{}, err
	}
	order := Order{
		Salt:          input.Salt,
		Maker:         input.Maker,
		Signer:        input.Signer,
		TokenID:       input.TokenID.String(),
		MakerAmount:   makerAmount.String(),
		TakerAmount:   takerAmount.String(),
		Side:          input.Side,
		SignatureType: input.SignatureType,
		Timestamp:     input.Timestamp,
		Metadata:      input.Metadata,
		Builder:       input.Builder,
	}
	if err := order.Validate(); err != nil {
		return Order{}, err
	}
	return order, nil
}

// Validate checks the exact structural bounds of the supported signed order.
func (o Order) Validate() error {
	switch {
	case o.Maker == (common.Address{}) || o.Signer == (common.Address{}):
		return errors.New("polymarket: maker and signer are required")
	case o.Salt > MaxSafeOrderSalt:
		return errors.New("polymarket: salt exceeds Number.MAX_SAFE_INTEGER")
	case !IsCanonicalUint256(o.TokenID, false),
		!IsCanonicalUint256(o.MakerAmount, false),
		!IsCanonicalUint256(o.TakerAmount, false):
		return errors.New("polymarket: order amount field is invalid")
	case o.Side != OrderSideBuy && o.Side != OrderSideSell:
		return errors.New("polymarket: side is invalid")
	case o.SignatureType != OrderSignatureTypePoly1271:
		return errors.New("polymarket: signature type must be POLY_1271")
	case o.Timestamp == 0 || o.Timestamp > 1<<63-1:
		return errors.New("polymarket: timestamp is invalid")
	}
	return nil
}

// Hash returns the official Polymarket V2 EIP-712 digest for the selected
// Standard or Neg Risk exchange domain.
func (o Order) Hash(marketType MarketType) (common.Hash, error) {
	if err := o.Validate(); err != nil {
		return common.Hash{}, err
	}
	exchange, err := marketType.Exchange()
	if err != nil {
		return common.Hash{}, err
	}
	domainEncoded, err := abi.Arguments{
		{Type: mustABIType("bytes32")},
		{Type: mustABIType("bytes32")},
		{Type: mustABIType("bytes32")},
		{Type: mustABIType("uint256")},
		{Type: mustABIType("address")},
	}.Pack(
		domainTypeHash,
		orderProtocolName,
		orderProtocolV2,
		new(big.Int).SetUint64(PolygonChainID),
		exchange,
	)
	if err != nil {
		return common.Hash{}, fmt.Errorf("polymarket: encoding EIP-712 domain: %w", err)
	}

	tokenID, err := parseUint256(o.TokenID)
	if err != nil {
		return common.Hash{}, err
	}
	makerAmount, err := parseUint256(o.MakerAmount)
	if err != nil {
		return common.Hash{}, err
	}
	takerAmount, err := parseUint256(o.TakerAmount)
	if err != nil {
		return common.Hash{}, err
	}
	orderEncoded, err := abi.Arguments{
		{Type: mustABIType("bytes32")},
		{Type: mustABIType("uint256")},
		{Type: mustABIType("address")},
		{Type: mustABIType("address")},
		{Type: mustABIType("uint256")},
		{Type: mustABIType("uint256")},
		{Type: mustABIType("uint256")},
		{Type: mustABIType("uint8")},
		{Type: mustABIType("uint8")},
		{Type: mustABIType("uint256")},
		{Type: mustABIType("bytes32")},
		{Type: mustABIType("bytes32")},
	}.Pack(
		orderTypeHash,
		new(big.Int).SetUint64(o.Salt),
		o.Maker,
		o.Signer,
		tokenID,
		makerAmount,
		takerAmount,
		uint8(o.Side),
		o.SignatureType,
		new(big.Int).SetUint64(o.Timestamp),
		o.Metadata,
		o.Builder,
	)
	if err != nil {
		return common.Hash{}, fmt.Errorf("polymarket: encoding EIP-712 order: %w", err)
	}
	domainSeparator := crypto.Keccak256Hash(domainEncoded)
	structHash := crypto.Keccak256Hash(orderEncoded)
	return crypto.Keccak256Hash([]byte{0x19, 0x01}, domainSeparator.Bytes(), structHash.Bytes()), nil
}

// NormalizeOrderAmounts derives V2 maker/taker amounts using BUY ceiling and
// SELL floor semantics.
func NormalizeOrderAmounts(side OrderSide, priceE6 uint64, sizeE6 *big.Int) (*big.Int, *big.Int, error) {
	if side != OrderSideBuy && side != OrderSideSell {
		return nil, nil, errors.New("polymarket: side must be BUY or SELL")
	}
	if priceE6 == 0 || priceE6 >= OrderPriceScaleE6 || sizeE6 == nil || sizeE6.Sign() <= 0 || sizeE6.BitLen() > 256 {
		return nil, nil, errors.New("polymarket: price and size must be positive and bounded")
	}
	product := new(big.Int).Mul(new(big.Int).SetUint64(priceE6), sizeE6)
	scale := new(big.Int).SetUint64(OrderPriceScaleE6)
	if side == OrderSideBuy {
		maker := new(big.Int).Add(product, new(big.Int).Sub(new(big.Int).Set(scale), big.NewInt(1)))
		maker.Div(maker, scale)
		if maker.BitLen() > 256 {
			return nil, nil, errors.New("polymarket: normalized amounts exceed uint256")
		}
		return maker, new(big.Int).Set(sizeE6), nil
	}
	taker := new(big.Int).Div(product, scale)
	if taker.Sign() <= 0 || taker.BitLen() > 256 {
		return nil, nil, errors.New("polymarket: normalized amounts exceed uint256")
	}
	return new(big.Int).Set(sizeE6), taker, nil
}

// SupportedTickSizeE6 reports whether a tick is one of the official V2 price grids.
func SupportedTickSizeE6(tickSizeE6 uint64) bool {
	switch tickSizeE6 {
	case 100_000, 10_000, 5_000, 2_500, 1_000, 100:
		return true
	default:
		return false
	}
}

// IsCanonicalUint256 reports whether raw is a canonical base-10 uint256.
func IsCanonicalUint256(raw string, allowZero bool) bool {
	if raw == "" || len(raw) > maxUint256DecimalDigits || strings.TrimSpace(raw) != raw ||
		(len(raw) > 1 && raw[0] == '0') {
		return false
	}
	value, ok := new(big.Int).SetString(raw, 10)
	if !ok || value.Sign() < 0 || value.BitLen() > 256 {
		return false
	}
	return allowZero || value.Sign() > 0
}

// ParseFixedPoint parses a canonical unsigned decimal at the requested scale.
func ParseFixedPoint(raw string, decimals int) (*big.Int, error) {
	if decimals < 0 || decimals > 256 || raw == "" || len(raw) > maxUint256DecimalDigits+decimals+1 ||
		strings.TrimSpace(raw) != raw || strings.ContainsAny(raw, "eE+") {
		return nil, errors.New("decimal must be canonical fixed point")
	}
	parts := strings.Split(raw, ".")
	if len(parts) > 2 || parts[0] == "" {
		return nil, errors.New("decimal is malformed")
	}
	if len(parts[0]) > 1 && parts[0][0] == '0' {
		return nil, errors.New("decimal has leading zeroes")
	}
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
		if fraction == "" || len(fraction) > decimals {
			return nil, errors.New("decimal precision exceeds scale")
		}
	}
	for _, character := range parts[0] + fraction {
		if character < '0' || character > '9' {
			return nil, errors.New("decimal contains non-digits")
		}
	}
	combined := parts[0] + fraction + strings.Repeat("0", decimals-len(fraction))
	value, ok := new(big.Int).SetString(combined, 10)
	if !ok {
		return nil, errors.New("decimal is invalid")
	}
	if value.BitLen() > 256 {
		return nil, errors.New("decimal exceeds uint256 bounds")
	}
	return value, nil
}

func parseUint256(raw string) (*big.Int, error) {
	value, ok := new(big.Int).SetString(raw, 10)
	if !ok || value.Sign() <= 0 || value.BitLen() > 256 {
		return nil, errors.New("polymarket: order amount field is invalid")
	}
	return value, nil
}

// DecimalToE6 parses a canonical fixed-point decimal into uint64 e6 units.
func DecimalToE6(raw string) (uint64, error) {
	value, err := ParseFixedPoint(raw, 6)
	if err != nil || value.Sign() < 0 || !value.IsUint64() {
		return 0, errors.New("decimal is outside uint64 e6 bounds")
	}
	return value.Uint64(), nil
}

func mustABIType(name string) abi.Type {
	typeValue, err := abi.NewType(name, "", nil)
	if err != nil {
		panic(err)
	}
	return typeValue
}
