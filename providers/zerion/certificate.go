package zerion

import (
	"context"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sync/singleflight"
	"golang.org/x/time/rate"
)

const (
	defaultCertFetchTimeout    = 5 * time.Second
	defaultCertCacheSize       = 16
	defaultCertRefreshInterval = 5 * time.Minute
	defaultCertFetchEvery      = 15 * time.Second
	defaultCertFetchBurst      = 4
	maxCertificateBytes        = 64 << 10
	minCertificateRSABits      = 2048
)

var (
	errFetchBudgetExhausted = errors.New("certificate fetch budget exhausted")
	errRefreshThrottled     = errors.New("certificate refresh throttled")
	errRedirectRefused      = errors.New("certificate redirect refused")
	errDialRefused          = errors.New("certificate dial refused")
)

// CertificateKey is a verified-shape RSA key from a Zerion signing certificate.
type CertificateKey struct {
	PublicKey  *rsa.PublicKey
	SPKISHA256 string // hex SHA-256 of SubjectPublicKeyInfo
	NotAfter   time.Time
	FetchedAt  time.Time
	Fresh      bool // fetched from the network during this call
}

type FetchOutcome string

const (
	FetchOutcomeOK              FetchOutcome = "ok"
	FetchOutcomeError           FetchOutcome = "error"
	FetchOutcomeBudgetExhausted FetchOutcome = "budget_exhausted"
	FetchOutcomeThrottled       FetchOutcome = "throttled"
)

type FetcherOption func(*CertificateFetcher)

func WithFetcherClock(now func() time.Time) FetcherOption {
	return func(f *CertificateFetcher) {
		if now != nil {
			f.now = now
		}
	}
}

func WithFetchTimeout(d time.Duration) FetcherOption {
	return func(f *CertificateFetcher) {
		if d > 0 {
			f.timeout = d
		}
	}
}

// WithFetchBudget bounds cold (uncached) fetches host-wide: one token per every, up to burst.
func WithFetchBudget(every time.Duration, burst int) FetcherOption {
	return func(f *CertificateFetcher) {
		if every > 0 && burst > 0 {
			f.fetchEvery, f.fetchBurst = every, burst
		}
	}
}

// WithRefreshInterval sets the minimum gap between network fetches of one URL.
func WithRefreshInterval(d time.Duration) FetcherOption {
	return func(f *CertificateFetcher) {
		if d > 0 {
			f.refreshInterval = d
		}
	}
}

func WithCertificateCacheSize(n int) FetcherOption {
	return func(f *CertificateFetcher) {
		if n > 0 {
			f.cacheSize = n
		}
	}
}

func WithFetchObserver(observe func(FetchOutcome)) FetcherOption {
	return func(f *CertificateFetcher) {
		if observe != nil {
			f.observe = observe
		}
	}
}

// WithFetcherRootCAs replaces the system roots used to verify the certificate host.
func WithFetcherRootCAs(pool *x509.CertPool) FetcherOption {
	return func(f *CertificateFetcher) { f.rootCAs = pool }
}

// WithInsecureDialContextForTesting replaces the public-IP-only dialer. Test use only.
func WithInsecureDialContextForTesting(dial func(ctx context.Context, network, addr string) (net.Conn, error)) FetcherOption {
	return func(f *CertificateFetcher) { f.dialContext = dial }
}

// CertificateFetcher fetches and caches Zerion signing keys with SSRF and abuse controls.
type CertificateFetcher struct {
	client          *http.Client
	now             func() time.Time
	timeout         time.Duration
	cacheSize       int
	refreshInterval time.Duration
	fetchEvery      time.Duration
	fetchBurst      int
	budget          *rate.Limiter
	observe         func(FetchOutcome)
	rootCAs         *x509.CertPool
	dialContext     func(ctx context.Context, network, addr string) (net.Conn, error)

	group singleflight.Group
	mu    sync.Mutex
	cache map[string]*certEntry
	tick  uint64
}

type certEntry struct {
	key       CertificateKey
	lastFetch time.Time
	lastUsed  uint64
	verified  bool
}

func NewCertificateFetcher(opts ...FetcherOption) *CertificateFetcher {
	f := &CertificateFetcher{
		now:             time.Now,
		timeout:         defaultCertFetchTimeout,
		cacheSize:       defaultCertCacheSize,
		refreshInterval: defaultCertRefreshInterval,
		fetchEvery:      defaultCertFetchEvery,
		fetchBurst:      defaultCertFetchBurst,
		observe:         func(FetchOutcome) {},
		cache:           make(map[string]*certEntry),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(f)
		}
	}
	f.budget = rate.NewLimiter(rate.Every(f.fetchEvery), f.fetchBurst)
	dial := f.dialContext
	if dial == nil {
		dial = (&net.Dialer{Timeout: f.timeout, Control: publicDialControl}).DialContext
	}
	f.client = &http.Client{
		Timeout: f.timeout,
		Transport: &http.Transport{
			Proxy:                  nil,
			DialContext:            dial,
			TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: f.rootCAs},
			TLSHandshakeTimeout:    f.timeout,
			ResponseHeaderTimeout:  f.timeout,
			MaxResponseHeaderBytes: 16 << 10,
			DisableCompression:     true,
			MaxIdleConns:           2,
			IdleConnTimeout:        90 * time.Second,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return errRedirectRefused },
	}
	return f
}

// get returns the cached key or performs a budgeted cold fetch.
func (f *CertificateFetcher) get(ctx context.Context, certURL *url.URL) (CertificateKey, error) {
	key := certURL.String()
	if k, ok := f.cached(key); ok {
		return k, nil
	}
	return f.shared(ctx, "get|"+key, func(fctx context.Context) (CertificateKey, error) {
		// Filled by a flight that finished after this call began.
		if k, ok := f.cached(key); ok {
			k.Fresh = true
			return k, nil
		}
		return f.coldFetch(fctx, certURL)
	})
}

func (f *CertificateFetcher) cached(key string) (CertificateKey, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.cache[key]
	if !ok {
		return CertificateKey{}, false
	}
	f.touch(e)
	return e.key, true
}

// refresh re-fetches after stale failed verification, at most once per refreshInterval per URL.
func (f *CertificateFetcher) refresh(ctx context.Context, certURL *url.URL, stale CertificateKey) (CertificateKey, error) {
	key := certURL.String()
	return f.shared(ctx, "refresh|"+key, func(fctx context.Context) (CertificateKey, error) {
		now := f.now()
		f.mu.Lock()
		e, ok := f.cache[key]
		if !ok {
			f.mu.Unlock()
			return f.coldFetch(fctx, certURL)
		}
		if e.key.SPKISHA256 != stale.SPKISHA256 {
			// Another refresh already replaced the key.
			f.touch(e)
			k := e.key
			f.mu.Unlock()
			return k, nil
		}
		if now.Sub(e.lastFetch) < f.refreshInterval {
			f.mu.Unlock()
			f.observe(FetchOutcomeThrottled)
			return CertificateKey{}, errRefreshThrottled
		}
		e.lastFetch = now
		f.mu.Unlock()
		return f.freshFetch(fctx, certURL)
	})
}

// prefetch fetches an operator-configured URL without spending cold budget.
func (f *CertificateFetcher) prefetch(ctx context.Context, certURL *url.URL) error {
	key := certURL.String()
	_, err := f.shared(ctx, "get|"+key, func(fctx context.Context) (CertificateKey, error) {
		return f.fetchAndStore(fctx, certURL)
	})
	return err
}

func (f *CertificateFetcher) coldFetch(ctx context.Context, certURL *url.URL) (CertificateKey, error) {
	if !f.budget.AllowN(f.now(), 1) {
		f.observe(FetchOutcomeBudgetExhausted)
		return CertificateKey{}, errFetchBudgetExhausted
	}
	return f.freshFetch(ctx, certURL)
}

// freshFetch is fetchAndStore for a key fetched on this call's behalf.
func (f *CertificateFetcher) freshFetch(ctx context.Context, certURL *url.URL) (CertificateKey, error) {
	k, err := f.fetchAndStore(ctx, certURL)
	if err != nil {
		return CertificateKey{}, err
	}
	k.Fresh = true
	return k, nil
}

func (f *CertificateFetcher) fetchAndStore(ctx context.Context, certURL *url.URL) (CertificateKey, error) {
	k, err := f.fetch(ctx, certURL)
	if err != nil {
		f.observe(FetchOutcomeError)
		return CertificateKey{}, err
	}
	f.observe(FetchOutcomeOK)
	f.store(certURL.String(), k)
	return k, nil
}

// shared collapses concurrent work per key; the flight is detached from any one caller's cancellation.
func (f *CertificateFetcher) shared(ctx context.Context, key string, fn func(context.Context) (CertificateKey, error)) (CertificateKey, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return CertificateKey{}, err
	}
	ch := f.group.DoChan(key, func() (any, error) {
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), f.timeout)
		defer cancel()
		return fn(fctx)
	})
	select {
	case <-ctx.Done():
		return CertificateKey{}, ctx.Err()
	case r := <-ch:
		if r.Err != nil {
			return CertificateKey{}, r.Err
		}
		return r.Val.(CertificateKey), nil
	}
}

func (f *CertificateFetcher) store(key string, k CertificateKey) {
	k.Fresh = false
	f.mu.Lock()
	defer f.mu.Unlock()
	if e, ok := f.cache[key]; ok {
		if e.key.SPKISHA256 != k.SPKISHA256 {
			e.verified = false
		}
		e.key = k
		e.lastFetch = k.FetchedAt
		f.touch(e)
		return
	}
	if len(f.cache) >= f.cacheSize {
		f.evictLocked()
	}
	e := &certEntry{key: k, lastFetch: k.FetchedAt}
	f.touch(e)
	f.cache[key] = e
}

// evictLocked drops the least recently used entry, preferring entries that never verified a signature.
func (f *CertificateFetcher) evictLocked() {
	var victim string
	var best *certEntry
	for k, e := range f.cache {
		if best == nil || (best.verified && !e.verified) || (best.verified == e.verified && e.lastUsed < best.lastUsed) {
			victim, best = k, e
		}
	}
	if best != nil {
		delete(f.cache, victim)
	}
}

func (f *CertificateFetcher) markVerified(certURL *url.URL, k CertificateKey) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e, ok := f.cache[certURL.String()]; ok && e.key.SPKISHA256 == k.SPKISHA256 {
		e.verified = true
	}
}

func (f *CertificateFetcher) touch(e *certEntry) {
	f.tick++
	e.lastUsed = f.tick
}

func (f *CertificateFetcher) fetch(ctx context.Context, certURL *url.URL) (CertificateKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, certURL.String(), nil)
	if err != nil {
		return CertificateKey{}, err
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return CertificateKey{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return CertificateKey{}, fmt.Errorf("certificate fetch status %d", resp.StatusCode)
	}
	if resp.ContentLength > maxCertificateBytes {
		return CertificateKey{}, fmt.Errorf("certificate exceeds %d bytes", maxCertificateBytes)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxCertificateBytes+1))
	if err != nil {
		return CertificateKey{}, err
	}
	if len(raw) > maxCertificateBytes {
		return CertificateKey{}, fmt.Errorf("certificate exceeds %d bytes", maxCertificateBytes)
	}
	k, err := parseCertificateKey(raw)
	if err != nil {
		return CertificateKey{}, err
	}
	k.FetchedAt = f.now()
	return k, nil
}

func parseCertificateKey(raw []byte) (CertificateKey, error) {
	var cert *x509.Certificate
	for rest := raw; ; {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			return CertificateKey{}, errors.New("no PEM CERTIFICATE block")
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		parsed, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return CertificateKey{}, fmt.Errorf("parse certificate: %w", err)
		}
		cert = parsed
		break
	}
	pub, ok := cert.PublicKey.(*rsa.PublicKey)
	if !ok {
		return CertificateKey{}, fmt.Errorf("certificate key is %T, want RSA", cert.PublicKey)
	}
	if pub.N.BitLen() < minCertificateRSABits {
		return CertificateKey{}, fmt.Errorf("certificate RSA key is %d bits, want >= %d", pub.N.BitLen(), minCertificateRSABits)
	}
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return CertificateKey{PublicKey: pub, SPKISHA256: hex.EncodeToString(sum[:]), NotAfter: cert.NotAfter}, nil
}

// publicDialControl runs after DNS resolution, so it also defeats DNS rebinding.
func publicDialControl(network, address string, _ syscall.RawConn) error {
	if network != "tcp4" && network != "tcp6" {
		return fmt.Errorf("%w: network %q", errDialRefused, network)
	}
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("%w: %v", errDialRefused, err)
	}
	if ap.Port() != 443 {
		return fmt.Errorf("%w: port %d", errDialRefused, ap.Port())
	}
	if !isPublicAddr(ap.Addr()) {
		return fmt.Errorf("%w: non-public address %s", errDialRefused, ap.Addr())
	}
	return nil
}

var (
	nat64Prefix  = netip.MustParsePrefix("64:ff9b::/96")
	sixToFour    = netip.MustParsePrefix("2002::/16")
	nonPublicNet = []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/8"),
		netip.MustParsePrefix("100.64.0.0/10"),
		netip.MustParsePrefix("192.0.0.0/24"),
		netip.MustParsePrefix("192.0.2.0/24"),
		netip.MustParsePrefix("198.18.0.0/15"),
		netip.MustParsePrefix("198.51.100.0/24"),
		netip.MustParsePrefix("203.0.113.0/24"),
		netip.MustParsePrefix("240.0.0.0/4"),
		netip.MustParsePrefix("::/96"),
		netip.MustParsePrefix("64:ff9b:1::/48"),
		netip.MustParsePrefix("100::/64"),
		netip.MustParsePrefix("2001::/32"),
		netip.MustParsePrefix("2001:2::/48"),
		netip.MustParsePrefix("2001:db8::/32"),
		netip.MustParsePrefix("3fff::/20"),
		netip.MustParsePrefix("fec0::/10"),
	}
)

func isPublicAddr(ip netip.Addr) bool {
	if !ip.IsValid() || ip.Zone() != "" {
		return false
	}
	ip = ip.Unmap()
	if ip.IsUnspecified() || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || !ip.IsGlobalUnicast() {
		return false
	}
	if ip.Is6() {
		b := ip.As16()
		if nat64Prefix.Contains(ip) {
			return isPublicAddr(netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}))
		}
		if sixToFour.Contains(ip) {
			return isPublicAddr(netip.AddrFrom4([4]byte{b[2], b[3], b[4], b[5]}))
		}
	}
	for _, p := range nonPublicNet {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}
