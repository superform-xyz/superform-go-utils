package polymarket

import (
	"encoding/json"
	"errors"
	"math/big"
	"strconv"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	ethmath "github.com/ethereum/go-ethereum/common/math"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/signer/core/apitypes"
)

const AuthDomainName = "ClobAuthDomain"
const AuthDomainVersion = "1"
const AuthMessage = "This message attests that I control the given wallet"

// AuthPayload preserves the exact ClobAuth domain on the wire. The generic
// Ethereum domain serializer also emits empty salt/verifyingContract fields,
// which wallet libraries can incorrectly treat as additional domain members.
type AuthPayload apitypes.TypedData

func (p AuthPayload) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Types       apitypes.Types            `json:"types"`
		PrimaryType string                    `json:"primaryType"`
		Domain      map[string]any            `json:"domain"`
		Message     apitypes.TypedDataMessage `json:"message"`
	}{
		Types: p.Types, PrimaryType: p.PrimaryType,
		Domain:  map[string]any{"name": p.Domain.Name, "version": p.Domain.Version, "chainId": p.Domain.ChainId},
		Message: p.Message,
	})
}

// AuthTypedData is the provider's exact L1 authentication payload. The address
// is the owner/API signer, not the POLY_1271 funding wallet.
func AuthTypedData(address string, timestamp int64, nonce uint64) (AuthPayload, error) {
	if !common.IsHexAddress(address) || common.HexToAddress(address) == (common.Address{}) || timestamp <= 0 {
		return AuthPayload{}, errors.New("polymarket: invalid authentication identity or timestamp")
	}
	return AuthPayload{
		Types: apitypes.Types{
			"EIP712Domain": {{Name: "name", Type: "string"}, {Name: "version", Type: "string"}, {Name: "chainId", Type: "uint256"}},
			"ClobAuth":     {{Name: "address", Type: "address"}, {Name: "timestamp", Type: "string"}, {Name: "nonce", Type: "uint256"}, {Name: "message", Type: "string"}},
		},
		PrimaryType: "ClobAuth",
		Domain:      apitypes.TypedDataDomain{Name: AuthDomainName, Version: AuthDomainVersion, ChainId: (*ethmath.HexOrDecimal256)(big.NewInt(137))},
		Message:     apitypes.TypedDataMessage{"address": strings.ToLower(address), "timestamp": strconv.FormatInt(timestamp, 10), "nonce": strconv.FormatUint(nonce, 10), "message": AuthMessage},
	}, nil
}

// VerifyAuthSignature verifies the exact EIP-712 payload without accepting a
// personal_sign signature or exporting/requiring an owner's private key.
func VerifyAuthSignature(address string, timestamp int64, nonce uint64, signature string) error {
	data, err := AuthTypedData(address, timestamp, nonce)
	if err != nil {
		return err
	}
	digest, _, err := apitypes.TypedDataAndHash(apitypes.TypedData(data))
	if err != nil {
		return errors.New("polymarket: invalid authentication payload")
	}
	sig, err := hexutil.Decode(signature)
	if err != nil || len(sig) != crypto.SignatureLength {
		return errors.New("polymarket: invalid authentication signature")
	}
	defer clear(sig)
	if sig[64] >= 27 {
		sig[64] -= 27
	}
	if !crypto.ValidateSignatureValues(sig[64], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:64]), true) {
		return errors.New("polymarket: invalid authentication signature")
	}
	key, err := crypto.SigToPub(digest, sig)
	if err != nil || crypto.PubkeyToAddress(*key) != common.HexToAddress(address) {
		return errors.New("polymarket: authentication signer mismatch")
	}
	return nil
}
