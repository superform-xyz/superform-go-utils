package hyperliquid

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	gethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/vmihailenco/msgpack/v5"
)

// SigningSource is the venue's EIP-712 source, independent of an EVM chain ID.
// Applications decide which sources and networks they permit.
type SigningSource string

const (
	SigningSourceMainnet SigningSource = "a"
	SigningSourceTestnet SigningSource = "b"
	SignatureLength                    = 65

	agentChainID       = 1337
	agentPrimaryType   = "Agent(string source,bytes32 connectionId)"
	eip712DomainType   = "EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)"
	agentDomainName    = "Exchange"
	agentDomainVersion = "1"
)

var (
	ErrMalformedSignature = errors.New("hyperliquid: malformed action signature")
	ErrActionTooLarge     = errors.New("hyperliquid: encoded action is too large")
)

type LimitOrderType struct {
	Tif string `msgpack:"tif" json:"tif"`
}

type OrderType struct {
	Limit   *LimitOrderType   `msgpack:"limit,omitempty" json:"limit,omitempty"`
	Trigger *TriggerOrderType `msgpack:"trigger,omitempty" json:"trigger,omitempty"`
}

// Field declaration order is part of the signed msgpack preimage.
type TriggerOrderType struct {
	IsMarket     bool   `msgpack:"isMarket" json:"isMarket"`
	TriggerPrice string `msgpack:"triggerPx" json:"triggerPx"`
	TPSL         string `msgpack:"tpsl" json:"tpsl"`
}

// OrderWire uses the venue's field order and abbreviations. Build typed actions
// rather than unordered maps when reproducing an existing signature.
type OrderWire struct {
	Asset      uint32    `msgpack:"a" json:"a"`
	IsBuy      bool      `msgpack:"b" json:"b"`
	LimitPrice string    `msgpack:"p" json:"p"`
	Size       string    `msgpack:"s" json:"s"`
	ReduceOnly bool      `msgpack:"r" json:"r"`
	OrderType  OrderType `msgpack:"t" json:"t"`
	Cloid      string    `msgpack:"c" json:"c"`
}

type OrderAction struct {
	Type     string      `msgpack:"type" json:"type"`
	Orders   []OrderWire `msgpack:"orders" json:"orders"`
	Grouping string      `msgpack:"grouping" json:"grouping"`
}

type CancelByCloidWire struct {
	Asset uint32 `msgpack:"asset" json:"asset"`
	Cloid string `msgpack:"cloid" json:"cloid"`
}

type CancelByCloidAction struct {
	Type    string              `msgpack:"type" json:"type"`
	Cancels []CancelByCloidWire `msgpack:"cancels" json:"cancels"`
}

func NewOrderAction(order OrderWire) OrderAction {
	return OrderAction{Type: "order", Orders: []OrderWire{order}, Grouping: "na"}
}

func NewCancelByCloidAction(cancel CancelByCloidWire) CancelByCloidAction {
	return CancelByCloidAction{Type: "cancelByCloid", Cancels: []CancelByCloidWire{cancel}}
}

type actionEncodingConfig struct{ maxBytes int }
type ActionEncodingOption func(*actionEncodingConfig)

// WithActionEncodingLimit applies the caller's maximum encoded action size.
// Without this option the codec imposes no application-specific size limit.
func WithActionEncodingLimit(maxBytes int) ActionEncodingOption {
	return func(c *actionEncodingConfig) {
		if maxBytes > 0 {
			c.maxBytes = maxBytes
		}
	}
}

// EncodeAction preserves typed field order and uses compact numeric encoding,
// matching the reference SDK's msgpack packb settings.
func EncodeAction(action any, opts ...ActionEncodingOption) ([]byte, error) {
	var cfg actionEncodingConfig
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	var buffer bytes.Buffer
	encoder := msgpack.NewEncoder(&buffer)
	encoder.SetCustomStructTag("msgpack")
	encoder.UseCompactInts(true)
	encoder.UseCompactFloats(true)
	if err := encoder.Encode(action); err != nil {
		return nil, fmt.Errorf("hyperliquid: encoding action: %w", err)
	}
	encoded := buffer.Bytes()
	if cfg.maxBytes > 0 && len(encoded) > cfg.maxBytes {
		return nil, ErrActionTooLarge
	}
	return encoded, nil
}

// ConnectionID binds action bytes, nonce, optional vault and optional expiry.
// An omitted expiry and a signed expiry have different preimages.
func ConnectionID(action any, nonce int64, vaultAddress string, expiresAfter *int64, opts ...ActionEncodingOption) ([32]byte, error) {
	encoded, err := EncodeAction(action, opts...)
	if err != nil {
		return [32]byte{}, err
	}
	if nonce < 0 {
		return [32]byte{}, errors.New("hyperliquid: action nonce cannot be negative")
	}
	preimage := make([]byte, 0, len(encoded)+32)
	preimage = append(preimage, encoded...)
	preimage = binary.BigEndian.AppendUint64(preimage, uint64(nonce))
	vault := strings.TrimSpace(vaultAddress)
	if vault == "" {
		preimage = append(preimage, 0x00)
	} else {
		if _, err := NormalizeAddress(vault); err != nil {
			return [32]byte{}, ErrInvalidAddress
		}
		preimage = append(preimage, 0x01)
		preimage = append(preimage, common.HexToAddress(vault).Bytes()...)
	}
	if expiresAfter != nil {
		if *expiresAfter <= 0 {
			return [32]byte{}, errors.New("hyperliquid: expiresAfter must be positive")
		}
		preimage = append(preimage, 0x00)
		preimage = binary.BigEndian.AppendUint64(preimage, uint64(*expiresAfter))
	}
	return [32]byte(gethcrypto.Keccak256(preimage)), nil
}

// AgentDigest hashes the fixed L1 action domain and the explicit signing source.
func AgentDigest(connectionID [32]byte, source SigningSource) ([32]byte, error) {
	if source != SigningSourceMainnet && source != SigningSourceTestnet {
		return [32]byte{}, fmt.Errorf("hyperliquid: unsupported signing source %q", source)
	}
	domainSeparator := gethcrypto.Keccak256(
		gethcrypto.Keccak256([]byte(eip712DomainType)),
		gethcrypto.Keccak256([]byte(agentDomainName)),
		gethcrypto.Keccak256([]byte(agentDomainVersion)),
		common.LeftPadBytes(big.NewInt(agentChainID).Bytes(), 32),
		common.LeftPadBytes(common.Address{}.Bytes(), 32),
	)
	structHash := gethcrypto.Keccak256(
		gethcrypto.Keccak256([]byte(agentPrimaryType)),
		gethcrypto.Keccak256([]byte(source)),
		connectionID[:],
	)
	return [32]byte(gethcrypto.Keccak256([]byte{0x19, 0x01}, domainSeparator, structHash)), nil
}

// RecoverActionSigner returns the lowercase signer without deciding whether it
// is authorized for an application account.
func RecoverActionSigner(digest [32]byte, signature []byte) (string, error) {
	if len(signature) != SignatureLength {
		return "", ErrMalformedSignature
	}
	normalized := append([]byte(nil), signature...)
	switch normalized[64] {
	case 27, 28:
		normalized[64] -= 27
	case 0, 1:
	default:
		return "", ErrMalformedSignature
	}
	publicKey, err := gethcrypto.SigToPub(digest[:], normalized)
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrMalformedSignature, err)
	}
	return strings.ToLower(gethcrypto.PubkeyToAddress(*publicKey).Hex()), nil
}

func VerifyActionSignature(action any, nonce int64, vaultAddress string, expiresAfter *int64, source SigningSource, signature []byte, opts ...ActionEncodingOption) (string, error) {
	connectionID, err := ConnectionID(action, nonce, vaultAddress, expiresAfter, opts...)
	if err != nil {
		return "", err
	}
	digest, err := AgentDigest(connectionID, source)
	if err != nil {
		return "", err
	}
	return RecoverActionSigner(digest, signature)
}
