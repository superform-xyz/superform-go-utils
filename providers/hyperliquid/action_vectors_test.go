package hyperliquid

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"
)

// Independent published vectors from test_l1_action_signing_order_with_cloid_matches:
// https://github.com/hyperliquid-dex/hyperliquid-python-sdk/blob/master/tests/signing_test.py
func TestActionMatchesPublishedSDKSignatures(t *testing.T) {
	action := NewOrderAction(OrderWire{Asset: 1, IsBuy: true, LimitPrice: "100", Size: "100", OrderType: OrderType{Limit: &LimitOrderType{Tif: "Gtc"}}, Cloid: "0x00000000000000000000000000000001"})
	key, err := crypto.HexToECDSA("0123456789012345678901234567890123456789012345678901234567890123")
	require.NoError(t, err)
	want := strings.ToLower(crypto.PubkeyToAddress(key.PublicKey).Hex())
	for _, test := range []struct {
		source    SigningSource
		signature string
	}{
		{SigningSourceMainnet, "041ae18e8239a56cacbc5dad94d45d0b747e5da11ad564077fcac71277a946e33c61f667e747404fe7eea8f90ab0e76cc12ce60270438b2058324681a00116da1b"},
		{SigningSourceTestnet, "eba0664bed2676fc4e5a743bf89e5c7501aa6d870bdb9446e122c9466c5cd16d7f3e74825c9114bc59086f1eebea2928c190fdfbfde144827cb02b85bbe909881c"},
	} {
		t.Run(string(test.source), func(t *testing.T) {
			signature, err := hex.DecodeString(test.signature)
			require.NoError(t, err)
			signer, err := VerifyActionSignature(action, 0, "", nil, test.source, signature)
			require.NoError(t, err)
			require.Equal(t, want, signer)
			otherSource := SigningSourceMainnet
			if test.source == SigningSourceMainnet {
				otherSource = SigningSourceTestnet
			}
			other, err := VerifyActionSignature(action, 0, "", nil, otherSource, signature)
			require.NoError(t, err)
			require.NotEqual(t, want, other, "the source is signed identity")
		})
	}
}
