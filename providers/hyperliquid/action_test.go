package hyperliquid

import (
	"encoding/hex"
	"strings"
	"testing"

	gethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"
)

func testOrderAction() OrderAction {
	return NewOrderAction(OrderWire{
		Asset:      159,
		IsBuy:      true,
		LimitPrice: "44.12",
		Size:       "1.25",
		ReduceOnly: false,
		OrderType:  OrderType{Limit: &LimitOrderType{Tif: "Gtc"}},
		Cloid:      "0x00112233445566778899aabbccddeeff",
	})
}

func mustSign(t *testing.T, digest [32]byte, keyHex string) []byte {
	t.Helper()
	key, err := gethcrypto.HexToECDSA(keyHex)
	if err != nil {
		t.Fatalf("HexToECDSA() error = %v", err)
	}
	signature, err := gethcrypto.Sign(digest[:], key)
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	return signature
}

func signerAddress(t *testing.T, keyHex string) string {
	t.Helper()
	key, err := gethcrypto.HexToECDSA(keyHex)
	if err != nil {
		t.Fatalf("HexToECDSA() error = %v", err)
	}
	return strings.ToLower(gethcrypto.PubkeyToAddress(key.PublicKey).Hex())
}

const testAgentKey = "0101010101010101010101010101010101010101010101010101010101010101"

func TestVerifyActionSignatureRoundTripsToTheSigner(t *testing.T) {
	action := testOrderAction()
	expiresAfter := int64(1_789_030_479_123)
	connectionID, err := ConnectionID(action, 1_789_030_419_123, "", &expiresAfter)
	if err != nil {
		t.Fatalf("ConnectionID() error = %v", err)
	}
	digest, err := AgentDigest(connectionID, SigningSourceMainnet)
	if err != nil {
		t.Fatalf("AgentDigest() error = %v", err)
	}
	signature := mustSign(t, digest, testAgentKey)

	signer, err := VerifyActionSignature(
		action, 1_789_030_419_123, "", &expiresAfter, SigningSourceMainnet, signature)
	if err != nil {
		t.Fatalf("VerifyActionSignature() error = %v", err)
	}
	if signer != signerAddress(t, testAgentKey) {
		t.Fatalf("signer = %q, want %q", signer, signerAddress(t, testAgentKey))
	}
}

// Every field the venue acts on must be inside the signed preimage, so altering
// any of them makes the signature recover to somebody else.
func TestVerifyActionSignatureBindsEveryActedOnField(t *testing.T) {
	action := testOrderAction()
	nonce := int64(1_789_030_419_123)
	expiresAfter := nonce + 60_000
	connectionID, err := ConnectionID(action, nonce, "", &expiresAfter)
	if err != nil {
		t.Fatalf("ConnectionID() error = %v", err)
	}
	digest, err := AgentDigest(connectionID, SigningSourceMainnet)
	if err != nil {
		t.Fatalf("AgentDigest() error = %v", err)
	}
	signature := mustSign(t, digest, testAgentKey)
	expected := signerAddress(t, testAgentKey)

	tampered := map[string]func(*OrderAction, *int64, *int64, *SigningSource){
		"size":          func(a *OrderAction, _, _ *int64, _ *SigningSource) { a.Orders[0].Size = "125" },
		"limit price":   func(a *OrderAction, _, _ *int64, _ *SigningSource) { a.Orders[0].LimitPrice = "4.412" },
		"side":          func(a *OrderAction, _, _ *int64, _ *SigningSource) { a.Orders[0].IsBuy = false },
		"asset":         func(a *OrderAction, _, _ *int64, _ *SigningSource) { a.Orders[0].Asset = 1 },
		"reduce only":   func(a *OrderAction, _, _ *int64, _ *SigningSource) { a.Orders[0].ReduceOnly = true },
		"time in force": func(a *OrderAction, _, _ *int64, _ *SigningSource) { a.Orders[0].OrderType.Limit.Tif = "Ioc" },
		"cloid": func(a *OrderAction, _, _ *int64, _ *SigningSource) {
			a.Orders[0].Cloid = "0xffffffffffffffffffffffffffffffff"
		},
		"nonce":   func(_ *OrderAction, n, _ *int64, _ *SigningSource) { *n += 1 },
		"expiry":  func(_ *OrderAction, _, e *int64, _ *SigningSource) { *e += 1 },
		"network": func(_ *OrderAction, _, _ *int64, n *SigningSource) { *n = SigningSourceTestnet },
	}
	for name, mutate := range tampered {
		t.Run(name, func(t *testing.T) {
			mutatedAction := testOrderAction()
			mutatedNonce, mutatedExpiry, mutatedNetwork := nonce, expiresAfter, SigningSourceMainnet
			mutate(&mutatedAction, &mutatedNonce, &mutatedExpiry, &mutatedNetwork)

			signer, err := VerifyActionSignature(
				mutatedAction, mutatedNonce, "", &mutatedExpiry, mutatedNetwork, signature)
			if err != nil {
				return // A recovery failure is also a rejection.
			}
			if signer == expected {
				t.Fatalf("altering the %s still recovered the approved signer", name)
			}
		})
	}
}

// An action signed with an expiry must not be usable as one without: the marker
// is part of the preimage precisely so an expiry cannot be stripped.
func TestConnectionIDSeparatesExpiringFromNonExpiringActions(t *testing.T) {
	action := testOrderAction()
	nonce := int64(1_789_030_419_123)
	expiresAfter := nonce + 60_000

	withExpiry, err := ConnectionID(action, nonce, "", &expiresAfter)
	if err != nil {
		t.Fatalf("ConnectionID() error = %v", err)
	}
	withoutExpiry, err := ConnectionID(action, nonce, "", nil)
	if err != nil {
		t.Fatalf("ConnectionID() error = %v", err)
	}
	if withExpiry == withoutExpiry {
		t.Fatal("the expiry marker does not change the signed preimage")
	}

	vaulted, err := ConnectionID(action, nonce, "0x3333333333333333333333333333333333333333", &expiresAfter)
	if err != nil {
		t.Fatalf("ConnectionID() error = %v", err)
	}
	if vaulted == withExpiry {
		t.Fatal("the vault marker does not change the signed preimage")
	}
}

// This pins our own encoding so a refactor cannot silently change what a stored
// signature means. It is NOT proof of agreement with Hyperliquid: replace or
// supplement it with an action signed by the reference SDK before routing real
// orders.
func TestEncodeActionByteLayoutIsPinned(t *testing.T) {
	encoded, err := EncodeAction(testOrderAction())
	if err != nil {
		t.Fatalf("EncodeAction() error = %v", err)
	}
	// Decoded: {"type":"order","orders":[{"a":159,"b":true,"p":"44.12","s":"1.25",
	// "r":false,"t":{"limit":{"tif":"Gtc"}},"c":"0x0011..eeff"}],"grouping":"na"}
	// with 159 packed compactly as uint8 (cc 9f), matching the reference SDK.
	const want = "83a474797065a56f72646572a66f726465727391" +
		"87a161cc9fa162c3a170a534342e3132a173a4312e3235a172c2" +
		"a17481a56c696d697481a3746966a3477463" +
		"a163d92230783030313132323333343435353636373738383939616162626363646465656666" +
		"a867726f7570696e67a26e61"
	if got := hex.EncodeToString(encoded); got != want {
		t.Fatalf("encoded action = %s\nwant           = %s", got, want)
	}
}

func TestEncodeActionIsDeterministic(t *testing.T) {
	first, err := EncodeAction(testOrderAction())
	if err != nil {
		t.Fatalf("EncodeAction() error = %v", err)
	}
	for range 8 {
		next, err := EncodeAction(testOrderAction())
		if err != nil {
			t.Fatalf("EncodeAction() error = %v", err)
		}
		if hex.EncodeToString(next) != hex.EncodeToString(first) {
			t.Fatal("action encoding is not deterministic")
		}
	}
}

func TestRecoverActionSignerRejectsMalformedSignatures(t *testing.T) {
	digest := [32]byte{1}
	for name, signature := range map[string][]byte{
		"empty":        {},
		"short":        make([]byte, 64),
		"long":         make([]byte, 66),
		"bad recovery": append(make([]byte, 64), 7),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := RecoverActionSigner(digest, signature); err == nil {
				t.Fatalf("%s signature was accepted", name)
			}
		})
	}
}

// Both encodings of the recovery byte are produced in the wild, and rejecting
// one of them would reject half of all valid signatures.
func TestRecoverActionSignerAcceptsBothRecoveryEncodings(t *testing.T) {
	digest := [32]byte{9}
	signature := mustSign(t, digest, testAgentKey)
	expected := signerAddress(t, testAgentKey)

	canonical, err := RecoverActionSigner(digest, signature)
	if err != nil {
		t.Fatalf("RecoverActionSigner() error = %v", err)
	}
	if canonical != expected {
		t.Fatalf("signer = %q, want %q", canonical, expected)
	}

	shifted := make([]byte, SignatureLength)
	copy(shifted, signature)
	shifted[64] += 27
	legacy, err := RecoverActionSigner(digest, shifted)
	if err != nil {
		t.Fatalf("RecoverActionSigner() with 27/28 recovery byte error = %v", err)
	}
	if legacy != expected {
		t.Fatalf("signer = %q, want %q", legacy, expected)
	}
}

func TestSigningSourcesAndCallerEncodingLimit(t *testing.T) {
	mainnet, err := AgentDigest([32]byte{1}, SigningSourceMainnet)
	require.NoError(t, err)
	testnet, err := AgentDigest([32]byte{1}, SigningSourceTestnet)
	require.NoError(t, err)
	require.NotEqual(t, mainnet, testnet)
	_, err = AgentDigest([32]byte{}, "")
	require.Error(t, err)

	action := strings.Repeat("x", 20*1024)
	encoded, err := EncodeAction(action)
	require.NoError(t, err, "the codec must not inherit Persephone's 16 KiB admission policy")
	require.Greater(t, len(encoded), 16*1024)
	_, err = EncodeAction(action, WithActionEncodingLimit(16*1024))
	require.ErrorIs(t, err, ErrActionTooLarge)
	_, err = ConnectionID(action, 0, "", nil, WithActionEncodingLimit(16*1024))
	require.ErrorIs(t, err, ErrActionTooLarge)
	_, err = ConnectionID(testOrderAction(), 0, "", nil)
	require.NoError(t, err, "zero nonce and omitted expiry occur in reference protocol vectors")
}
