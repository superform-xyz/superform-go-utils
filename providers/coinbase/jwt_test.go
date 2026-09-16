package coinbase

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newEd25519Secret(t *testing.T) (ed25519.PublicKey, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return pub, base64.StdEncoding.EncodeToString(priv)
}

func newECDSASecret(t *testing.T) (*ecdsa.PublicKey, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	return &key.PublicKey, string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}))
}

// splitJWT returns the signing input, header, claims and signature of a JWS.
func splitJWT(t *testing.T, token string) (string, map[string]any, map[string]any, []byte) {
	t.Helper()
	parts := strings.Split(token, ".")
	require.Len(t, parts, 3)

	decode := func(segment string) map[string]any {
		raw, err := base64.RawURLEncoding.DecodeString(segment)
		require.NoError(t, err)
		var out map[string]any
		require.NoError(t, json.Unmarshal(raw, &out))
		return out
	}

	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	require.NoError(t, err)
	return parts[0] + "." + parts[1], decode(parts[0]), decode(parts[1]), signature
}

func TestMintJWTEd25519(t *testing.T) {
	t.Parallel()

	pub, secret := newEd25519Secret(t)
	signer, err := parseAPIKeySecret(secret)
	require.NoError(t, err)

	token, err := mintJWT(signer, "key-id", "POST", "api.developer.coinbase.com", createSessionTokenPath)
	require.NoError(t, err)

	signingInput, header, claims, signature := splitJWT(t, token)
	assert.True(t, ed25519.Verify(pub, []byte(signingInput), signature))

	assert.Equal(t, algEdDSA, header["alg"])
	assert.Equal(t, "key-id", header["kid"])
	assert.Equal(t, "JWT", header["typ"])
	assert.Len(t, header["nonce"], 2*jwtNonceBytes)

	assert.Equal(t, "key-id", claims["sub"])
	assert.Equal(t, jwtIssuer, claims["iss"])
	assert.Equal(t, []any{jwtAudience}, claims["aud"])
	assert.Equal(t, []any{"POST api.developer.coinbase.com" + createSessionTokenPath}, claims["uris"])
	assert.Greater(t, claims["exp"], claims["nbf"])
}

func TestMintJWTEd25519AcceptsSeedOnlySecret(t *testing.T) {
	t.Parallel()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	signer, err := parseAPIKeySecret(base64.StdEncoding.EncodeToString(priv.Seed()))
	require.NoError(t, err)

	token, err := mintJWT(signer, "key-id", "GET", "example.test", "/onramp/v1/token")
	require.NoError(t, err)

	signingInput, _, _, signature := splitJWT(t, token)
	assert.True(t, ed25519.Verify(pub, []byte(signingInput), signature))
}

func TestMintJWTECDSA(t *testing.T) {
	t.Parallel()

	pub, secret := newECDSASecret(t)
	signer, err := parseAPIKeySecret(secret)
	require.NoError(t, err)

	token, err := mintJWT(signer, "key-id", "GET", "api.developer.coinbase.com", "/onramp/v1/buy/user/ref/transactions")
	require.NoError(t, err)

	signingInput, header, claims, signature := splitJWT(t, token)
	assert.Equal(t, algES256, header["alg"])
	assert.Equal(t, []any{"GET api.developer.coinbase.com/onramp/v1/buy/user/ref/transactions"}, claims["uris"])

	// ES256 carries a fixed-width r||s pair, not an ASN.1 envelope.
	require.Len(t, signature, 64)
	digest := sha256.Sum256([]byte(signingInput))
	r := new(big.Int).SetBytes(signature[:32])
	s := new(big.Int).SetBytes(signature[32:])
	assert.True(t, ecdsa.Verify(pub, digest[:], r, s))
}

func TestMintJWTNonceIsPerToken(t *testing.T) {
	t.Parallel()

	_, secret := newEd25519Secret(t)
	signer, err := parseAPIKeySecret(secret)
	require.NoError(t, err)

	first, err := mintJWT(signer, "key-id", "POST", "example.test", "/path")
	require.NoError(t, err)
	second, err := mintJWT(signer, "key-id", "POST", "example.test", "/path")
	require.NoError(t, err)

	_, firstHeader, _, _ := splitJWT(t, first)
	_, secondHeader, _, _ := splitJWT(t, second)
	assert.NotEqual(t, firstHeader["nonce"], secondHeader["nonce"])
}

func TestParseAPIKeySecretUnescapesPEMNewlines(t *testing.T) {
	t.Parallel()

	_, secret := newECDSASecret(t)
	escaped := strings.ReplaceAll(secret, "\n", `\n`)

	signer, err := parseAPIKeySecret(escaped)
	require.NoError(t, err)
	assert.Equal(t, algES256, signer.alg())
}

func TestParseAPIKeySecretRejectsNonP256Curve(t *testing.T) {
	t.Parallel()

	key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	secret := string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}))

	_, err = parseAPIKeySecret(secret)
	require.ErrorIs(t, err, ErrInvalidAPIKeySecret)
	assert.ErrorContains(t, err, "P-256")
}

func TestParseAPIKeySecretErrors(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"empty":            "   ",
		"not base64":       "!!!not-base64!!!",
		"wrong key length": base64.StdEncoding.EncodeToString([]byte("too-short")),
		"undecodable pem":  "-----BEGIN EC PRIVATE KEY-----\nnot-base64\n-----END EC PRIVATE KEY-----",
		"unsupported pem":  string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: []byte{1, 2, 3}})),
	}

	for name, secret := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := parseAPIKeySecret(secret)
			require.ErrorIs(t, err, ErrInvalidAPIKeySecret)
		})
	}
}
