package coinbase

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// Coinbase Developer Platform authenticates every REST call with a short-lived
// bearer JWT signed by the API key secret, scoped to the exact method+host+path
// it is used on. This mirrors `generateJwt` from @coinbase/cdp-sdk/auth, which
// apps/v2's `src/lib/server/coinbase-cdp.ts` calls today, and is implemented on
// the standard library so the module gains no new dependency.
const (
	jwtIssuer     = "cdp"
	jwtAudience   = "cdp_service"
	jwtExpiry     = 2 * time.Minute
	jwtNonceBytes = 16

	algEdDSA = "EdDSA"
	algES256 = "ES256"
)

// ErrInvalidAPIKeySecret marks a secret that is neither a base64 Ed25519 key
// nor a PEM-encoded EC private key. It is reported separately from transport
// failures because it is a deployment fault, never a retryable upstream one.
var ErrInvalidAPIKeySecret = errors.New("coinbase invalid api key secret")

// apiKeySigner signs the JWS signing input with whichever key type the CDP
// portal issued. Ed25519 keys are handed out as base64; the older ECDSA keys
// as PEM.
type apiKeySigner interface {
	alg() string
	sign(signingInput []byte) ([]byte, error)
}

type ed25519Signer struct {
	key ed25519.PrivateKey
}

func (s ed25519Signer) alg() string { return algEdDSA }

func (s ed25519Signer) sign(signingInput []byte) ([]byte, error) {
	return ed25519.Sign(s.key, signingInput), nil
}

type ecdsaSigner struct {
	key *ecdsa.PrivateKey
}

func (s ecdsaSigner) alg() string { return algES256 }

func (s ecdsaSigner) sign(signingInput []byte) ([]byte, error) {
	digest := sha256.Sum256(signingInput)
	r, sig, err := ecdsa.Sign(rand.Reader, s.key, digest[:])
	if err != nil {
		return nil, fmt.Errorf("sign: %w", err)
	}
	// JWS wants the fixed-width r||s pair, not the ASN.1 envelope
	// ecdsa.SignASN1 produces.
	size := (s.key.Curve.Params().N.BitLen() + 7) / 8
	out := make([]byte, 2*size)
	padInto(out[:size], r)
	padInto(out[size:], sig)
	return out, nil
}

func padInto(dst []byte, value *big.Int) {
	b := value.Bytes()
	copy(dst[len(dst)-len(b):], b)
}

// parseAPIKeySecret detects the key format and returns a signer for it.
func parseAPIKeySecret(secret string) (apiKeySigner, error) {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return nil, fmt.Errorf("%w: empty", ErrInvalidAPIKeySecret)
	}

	// Env vars and secret managers routinely carry PEM newlines escaped; the
	// CDP SDK unescapes them too, so a key copied verbatim from the portal
	// keeps working here.
	if unescaped := strings.ReplaceAll(secret, `\n`, "\n"); strings.Contains(unescaped, "-----BEGIN") {
		return parsePEMSecret(unescaped)
	}
	return parseBase64Secret(secret)
}

func parsePEMSecret(secret string) (apiKeySigner, error) {
	block, _ := pem.Decode([]byte(secret))
	if block == nil {
		return nil, fmt.Errorf("%w: undecodable PEM block", ErrInvalidAPIKeySecret)
	}

	var (
		key *ecdsa.PrivateKey
		err error
	)
	switch block.Type {
	case "EC PRIVATE KEY":
		key, err = x509.ParseECPrivateKey(block.Bytes)
	case "PRIVATE KEY":
		var parsed any
		if parsed, err = x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
			var ok bool
			if key, ok = parsed.(*ecdsa.PrivateKey); !ok {
				if edKey, isEd := parsed.(ed25519.PrivateKey); isEd {
					return ed25519Signer{key: edKey}, nil
				}
				return nil, fmt.Errorf("%w: unsupported PKCS8 key type %T", ErrInvalidAPIKeySecret, parsed)
			}
		}
	default:
		return nil, fmt.Errorf("%w: unsupported PEM block %q", ErrInvalidAPIKeySecret, block.Type)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalidAPIKeySecret, err)
	}
	// ES256 is defined over P-256 only; any other curve would produce a token
	// the CDP gateway rejects with an opaque 401.
	if key.Curve != elliptic.P256() {
		return nil, fmt.Errorf("%w: expected P-256 curve, got %s", ErrInvalidAPIKeySecret, key.Curve.Params().Name)
	}
	return ecdsaSigner{key: key}, nil
}

func parseBase64Secret(secret string) (apiKeySigner, error) {
	var (
		raw []byte
		err error
	)
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	} {
		if raw, err = enc.DecodeString(secret); err == nil {
			break
		}
	}
	if err != nil {
		return nil, fmt.Errorf("%w: not base64 or PEM", ErrInvalidAPIKeySecret)
	}

	switch len(raw) {
	case ed25519.PrivateKeySize: // seed || public key, what the CDP portal emits
		return ed25519Signer{key: ed25519.PrivateKey(raw)}, nil
	case ed25519.SeedSize:
		return ed25519Signer{key: ed25519.NewKeyFromSeed(raw)}, nil
	default:
		return nil, fmt.Errorf("%w: expected %d or %d key bytes, got %d",
			ErrInvalidAPIKeySecret, ed25519.SeedSize, ed25519.PrivateKeySize, len(raw))
	}
}

// mintJWT builds the bearer token for one request. `uris` binds the token to a
// single method/host/path, so a token minted for the session-token endpoint
// cannot be replayed against the transactions endpoint.
func mintJWT(signer apiKeySigner, apiKeyID, method, host, path string) (string, error) {
	nonce := make([]byte, jwtNonceBytes)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}

	header := map[string]any{
		"alg":   signer.alg(),
		"kid":   apiKeyID,
		"typ":   "JWT",
		"nonce": hex.EncodeToString(nonce),
	}
	now := time.Now().UTC()
	claims := map[string]any{
		"sub":  apiKeyID,
		"iss":  jwtIssuer,
		"aud":  []string{jwtAudience},
		"nbf":  now.Unix(),
		"exp":  now.Add(jwtExpiry).Unix(),
		"uris": []string{fmt.Sprintf("%s %s%s", method, host, path)},
	}

	encodedHeader, err := encodeJWTSegment(header)
	if err != nil {
		return "", fmt.Errorf("encode header: %w", err)
	}
	encodedClaims, err := encodeJWTSegment(claims)
	if err != nil {
		return "", fmt.Errorf("encode claims: %w", err)
	}

	signingInput := encodedHeader + "." + encodedClaims
	signature, err := signer.sign([]byte(signingInput))
	if err != nil {
		return "", err
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func encodeJWTSegment(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}
