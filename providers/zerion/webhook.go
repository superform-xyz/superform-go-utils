package zerion

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	TimestampHeader        = "X-Timestamp"
	SignatureHeader        = "X-Signature"
	CertificateURLHeader   = "X-Certificate-URL"
	TrustedCertificateHost = "webhooks.zerion.io"

	defaultMaxClockSkew  = 5 * time.Minute
	maxCertificateURLLen = 2048
)

var (
	ErrMissingSignatureHeaders = errors.New("zerion webhook: missing or duplicate signature headers")
	ErrInvalidTimestamp        = errors.New("zerion webhook: invalid timestamp")
	ErrStaleTimestamp          = errors.New("zerion webhook: timestamp outside allowed skew")
	ErrUntrustedCertificateURL = errors.New("zerion webhook: untrusted certificate url")
	// ErrInvalidSignature means the signature is malformed or fails against a key fetched in this call.
	ErrInvalidSignature = errors.New("zerion webhook: invalid signature")
	// ErrSignatureMismatchThrottled means the cached key failed and a refresh is not allowed yet.
	ErrSignatureMismatchThrottled = errors.New("zerion webhook: signature mismatch with refresh throttled")
	ErrCertificateUnavailable     = errors.New("zerion webhook: certificate unavailable")
)

type CallbackHeaders struct {
	Timestamp      string
	Signature      string
	CertificateURL string
}

// CallbackHeadersFromHTTP requires exactly one non-empty value for each signature header.
func CallbackHeadersFromHTTP(h http.Header) (CallbackHeaders, error) {
	one := func(name string) (string, bool) {
		values := h.Values(name)
		if len(values) != 1 || strings.TrimSpace(values[0]) == "" {
			return "", false
		}
		return values[0], true
	}
	ts, okTS := one(TimestampHeader)
	sig, okSig := one(SignatureHeader)
	certURL, okURL := one(CertificateURLHeader)
	if !okTS || !okSig || !okURL {
		return CallbackHeaders{}, ErrMissingSignatureHeaders
	}
	return CallbackHeaders{Timestamp: ts, Signature: sig, CertificateURL: certURL}, nil
}

var callbackTimestampLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02T15:04:05.999999999Z0700",
	"2006-01-02T15:04:05.999999999", // zone-less ISO-8601 is read as UTC
}

func ParseCallbackTimestamp(raw string) (time.Time, error) {
	s := strings.TrimSpace(raw)
	if s == "" || len(s) > 64 {
		return time.Time{}, ErrInvalidTimestamp
	}
	for _, layout := range callbackTimestampLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("%w: %q", ErrInvalidTimestamp, s)
}

// ValidateCertificateURL enforces https on the exact host webhooks.zerion.io without DNS lookups.
func ValidateCertificateURL(raw string) (*url.URL, error) {
	reject := func(reason string) (*url.URL, error) {
		return nil, fmt.Errorf("%w: %s", ErrUntrustedCertificateURL, reason)
	}
	if raw == "" || len(raw) > maxCertificateURLLen {
		return reject("empty or oversized")
	}
	if strings.ContainsAny(raw, "#\\") || strings.IndexFunc(raw, func(r rune) bool { return r <= ' ' || r == 0x7f }) >= 0 {
		return reject("fragment, backslash or control characters")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return reject("unparseable")
	}
	switch {
	case u.Scheme != "https":
		return reject("scheme must be https")
	case u.Opaque != "":
		return reject("opaque url")
	case u.User != nil:
		return reject("userinfo present")
	case u.Fragment != "" || u.RawFragment != "":
		return reject("fragment present")
	case !strings.EqualFold(u.Hostname(), TrustedCertificateHost):
		return reject("host must be " + TrustedCertificateHost)
	case strings.HasSuffix(u.Host, ":"):
		return reject("empty port")
	case u.Port() != "" && u.Port() != "443":
		return reject("port must be 443")
	case u.Path == "":
		return reject("empty path")
	}
	normalized := *u
	normalized.Host = TrustedCertificateHost
	return &normalized, nil
}

type VerifierOption func(*CallbackVerifier)

func WithMaxClockSkew(d time.Duration) VerifierOption {
	return func(v *CallbackVerifier) {
		if d > 0 {
			v.maxSkew = d
		}
	}
}

func WithVerifierClock(now func() time.Time) VerifierOption {
	return func(v *CallbackVerifier) {
		if now != nil {
			v.now = now
		}
	}
}

// WithPinnedCertificateURL accepts only this URL (Zerion's never changes); an invalid pin rejects every callback.
func WithPinnedCertificateURL(raw string) VerifierOption {
	return func(v *CallbackVerifier) {
		v.pinned = true
		if u, err := ValidateCertificateURL(raw); err == nil {
			v.pinnedURL = u.String()
		}
	}
}

type CallbackVerifier struct {
	fetcher   *CertificateFetcher
	maxSkew   time.Duration
	now       func() time.Time
	pinned    bool
	pinnedURL string
}

type VerifyResult struct {
	Timestamp time.Time
	Key       CertificateKey
	Refreshed bool
}

func NewCallbackVerifier(fetcher *CertificateFetcher, opts ...VerifierOption) *CallbackVerifier {
	if fetcher == nil {
		fetcher = NewCertificateFetcher()
	}
	v := &CallbackVerifier{fetcher: fetcher, maxSkew: defaultMaxClockSkew, now: time.Now}
	for _, opt := range opts {
		if opt != nil {
			opt(v)
		}
	}
	return v
}

// ValidateCertificateURL applies the host policy and, when configured, the pin.
func (v *CallbackVerifier) ValidateCertificateURL(raw string) (*url.URL, error) {
	certURL, err := ValidateCertificateURL(raw)
	if err != nil {
		return nil, err
	}
	if v.pinned && certURL.String() != v.pinnedURL {
		return nil, fmt.Errorf("%w: not the pinned certificate URL", ErrUntrustedCertificateURL)
	}
	return certURL, nil
}

// Prefetch warms the pinned certificate outside the attacker-facing cold-fetch
// budget, so a freshly started process never needs a budgeted fetch.
func (v *CallbackVerifier) Prefetch(ctx context.Context) error {
	if !v.pinned || v.pinnedURL == "" {
		return fmt.Errorf("%w: no valid pinned certificate URL", ErrUntrustedCertificateURL)
	}
	certURL, err := ValidateCertificateURL(v.pinnedURL)
	if err != nil {
		return err
	}
	return v.fetcher.prefetch(ctx, certURL)
}

// Verify authenticates the raw body; see the Err* values for the 401 (forged) vs 503 (retryable) split.
func (v *CallbackVerifier) Verify(ctx context.Context, h CallbackHeaders, body []byte) (VerifyResult, error) {
	var res VerifyResult
	if strings.TrimSpace(h.Timestamp) == "" || strings.TrimSpace(h.Signature) == "" || strings.TrimSpace(h.CertificateURL) == "" {
		return res, ErrMissingSignatureHeaders
	}
	ts, err := ParseCallbackTimestamp(h.Timestamp)
	if err != nil {
		return res, err
	}
	res.Timestamp = ts
	now := v.now()
	if ts.Before(now.Add(-v.maxSkew)) || ts.After(now.Add(v.maxSkew)) {
		return res, fmt.Errorf("%w: skew %s", ErrStaleTimestamp, now.Sub(ts).Round(time.Second))
	}
	certURL, err := v.ValidateCertificateURL(h.CertificateURL)
	if err != nil {
		return res, err
	}
	sig, ok := decodeSignature(h.Signature)
	if !ok {
		return res, fmt.Errorf("%w: malformed base64", ErrInvalidSignature)
	}
	digest := signingDigest(h.Timestamp, body)

	key, err := v.fetcher.get(ctx, certURL)
	if err != nil {
		return res, fmt.Errorf("%w: %w", ErrCertificateUnavailable, err)
	}
	res.Key = key
	if verifyDigest(key.PublicKey, digest, sig) {
		v.fetcher.markVerified(certURL, key)
		return res, nil
	}
	if key.Fresh {
		return res, ErrInvalidSignature
	}

	refreshed, err := v.fetcher.refresh(ctx, certURL, key)
	if errors.Is(err, errRefreshThrottled) {
		return res, ErrSignatureMismatchThrottled
	}
	if err != nil {
		return res, fmt.Errorf("%w: %w", ErrCertificateUnavailable, err)
	}
	res.Key = refreshed
	if verifyDigest(refreshed.PublicKey, digest, sig) {
		v.fetcher.markVerified(certURL, refreshed)
		res.Refreshed = true
		return res, nil
	}
	if refreshed.Fresh {
		return res, ErrInvalidSignature
	}
	// The replacement key came from another request's refresh, not this one.
	return res, ErrSignatureMismatchThrottled
}

func signingDigest(timestamp string, body []byte) []byte {
	h := sha256.New()
	h.Write([]byte(timestamp))
	h.Write([]byte{'\n'})
	h.Write(body)
	h.Write([]byte{'\n'})
	return h.Sum(nil)
}

func verifyDigest(pub *rsa.PublicKey, digest, sig []byte) bool {
	return pub != nil && rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest, sig) == nil
}

func decodeSignature(raw string) ([]byte, bool) {
	raw = strings.TrimSpace(raw)
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if sig, err := enc.DecodeString(raw); err == nil && len(sig) > 0 {
			return sig, true
		}
	}
	return nil, false
}
