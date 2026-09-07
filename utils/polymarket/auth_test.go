package polymarket

import (
	"encoding/json"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common/hexutil"
	ethmath "github.com/ethereum/go-ethereum/common/math"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/signer/core/apitypes"
	"github.com/stretchr/testify/require"
)

// The upstream public test vector uses Amoy; production verification must
// reject that signature even though its other ClobAuth fields match.
// https://github.com/Polymarket/clob-client-v2/blob/main/tests/signing/eip712.test.ts
func TestAuthTypedDataMatchesOfficialVectorAndBindsPolygon(t *testing.T) {
	key, err := crypto.HexToECDSA("ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80")
	require.NoError(t, err)
	address := crypto.PubkeyToAddress(key.PublicKey).Hex()
	data, err := AuthTypedData(address, 10000000, 23)
	require.NoError(t, err)
	require.Equal(t, "137", (*big.Int)(data.Domain.ChainId).String())
	require.Equal(t, "ClobAuth", data.PrimaryType)
	payload, err := json.Marshal(data)
	require.NoError(t, err)
	var wire struct {
		Domain map[string]any `json:"domain"`
	}
	require.NoError(t, json.Unmarshal(payload, &wire))
	require.Equal(t, map[string]any{"name": "ClobAuthDomain", "version": "1", "chainId": "0x89"}, wire.Domain)
	data.Domain.ChainId = (*ethmath.HexOrDecimal256)(big.NewInt(80002))
	digest, _, err := apitypes.TypedDataAndHash(apitypes.TypedData(data))
	require.NoError(t, err)
	signature, err := crypto.Sign(digest, key)
	require.NoError(t, err)
	signature[64] += 27
	require.Equal(t, "0xf62319a987514da40e57e2f4d7529f7bac38f0355bd88bb5adbb3768d80de6c1682518e0af677d5260366425f4361e7b70c25ae232aff0ab2331e2b164a1aedc1b", hexutil.Encode(signature))
	require.Error(t, VerifyAuthSignature(address, 10000000, 23, hexutil.Encode(signature)))

	data.Domain.ChainId = (*ethmath.HexOrDecimal256)(big.NewInt(137))
	digest, _, err = apitypes.TypedDataAndHash(apitypes.TypedData(data))
	require.NoError(t, err)
	signature, err = crypto.Sign(digest, key)
	require.NoError(t, err)
	require.NoError(t, VerifyAuthSignature(address, 10000000, 23, hexutil.Encode(signature)))
	signature[64] += 27
	require.NoError(t, VerifyAuthSignature(strings.ToLower(address), 10000000, 23, hexutil.Encode(signature)))
	require.Error(t, VerifyAuthSignature(address, 10000001, 23, hexutil.Encode(signature)))
	require.Error(t, VerifyAuthSignature(address, 10000000, 24, hexutil.Encode(signature)))
	require.Error(t, VerifyAuthSignature("0x1111111111111111111111111111111111111111", 10000000, 23, hexutil.Encode(signature)))
	personal, err := crypto.Sign(accounts.TextHash(digest), key)
	require.NoError(t, err)
	require.Error(t, VerifyAuthSignature(address, 10000000, 23, hexutil.Encode(personal)))
	highS := new(big.Int).Sub(crypto.S256().Params().N, new(big.Int).SetBytes(signature[32:64]))
	highS.FillBytes(signature[32:64])
	signature[64] = 27 + (signature[64] - 27) ^ 1
	require.Error(t, VerifyAuthSignature(address, 10000000, 23, hexutil.Encode(signature)))
	for _, sig := range []string{"", "0x12", "0x" + strings.Repeat("ff", 65)} {
		require.Error(t, VerifyAuthSignature(address, 10000000, 23, sig))
	}
}
