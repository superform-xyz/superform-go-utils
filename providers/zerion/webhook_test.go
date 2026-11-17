package zerion

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testCertPath = "/certificates/webhooks.pem"

var (
	testKeysOnce sync.Once
	testRSAKeyA  *rsa.PrivateKey
	testRSAKeyB  *rsa.PrivateKey
	testRSAWeak  *rsa.PrivateKey
)

func testKeys(t *testing.T) (*rsa.PrivateKey, *rsa.PrivateKey, *rsa.PrivateKey) {
	t.Helper()
	testKeysOnce.Do(func() {
		var err error
		testRSAKeyA, err = rsa.GenerateKey(rand.Reader, 2048)
		require.NoError(t, err)
		testRSAKeyB, err = rsa.GenerateKey(rand.Reader, 2048)
		require.NoError(t, err)
		testRSAWeak, err = rsa.GenerateKey(rand.Reader, 1024)
		require.NoError(t, err)
	})
	return testRSAKeyA, testRSAKeyB, testRSAWeak
}

func selfSignedPEM(t *testing.T, pub, priv any) []byte {
	t.Helper()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "zerion webhook signing"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func signCallback(t *testing.T, key *rsa.PrivateKey, ts string, body []byte) string {
	t.Helper()
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, signingDigest(ts, body))
	require.NoError(t, err)
	return base64.StdEncoding.EncodeToString(sig)
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// certServer impersonates webhooks.zerion.io over TLS with a test CA.
type certServer struct {
	srv     *httptest.Server
	pool    *x509.CertPool
	fetches atomic.Int64
	mu      sync.Mutex
	routes  map[string]func(w http.ResponseWriter, r *http.Request)
	gate    chan struct{}
	entered chan struct{}
}

func newCertServer(t *testing.T) *certServer {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	require.NoError(t, err)
	caCert, err := x509.ParseCertificate(caDER)
	require.NoError(t, err)
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: TrustedCertificateHost},
		DNSNames:     []string{TrustedCertificateHost},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, caCert, &leafKey.PublicKey, caKey)
	require.NoError(t, err)

	cs := &certServer{pool: x509.NewCertPool(), routes: map[string]func(http.ResponseWriter, *http.Request){}}
	cs.pool.AddCert(caCert)
	cs.srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cs.fetches.Add(1)
		cs.mu.Lock()
		h, gate, entered := cs.routes[r.URL.Path], cs.gate, cs.entered
		cs.mu.Unlock()
		if entered != nil {
			select {
			case entered <- struct{}{}:
			default:
			}
		}
		if gate != nil {
			<-gate
		}
		if h == nil {
			http.NotFound(w, r)
			return
		}
		h(w, r)
	}))
	cs.srv.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{leafDER}, PrivateKey: leafKey}}}
	cs.srv.StartTLS()
	t.Cleanup(cs.srv.Close)
	return cs
}

func (cs *certServer) serve(path string, body []byte) {
	cs.handle(path, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) })
}

func (cs *certServer) handle(path string, h func(http.ResponseWriter, *http.Request)) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.routes[path] = h
}

func (cs *certServer) fetcherOptions() []FetcherOption {
	addr := cs.srv.Listener.Addr().String()
	return []FetcherOption{
		WithFetcherRootCAs(cs.pool),
		WithInsecureDialContextForTesting(func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		}),
	}
}

type verifierEnv struct {
	server   *certServer
	clock    *fakeClock
	fetcher  *CertificateFetcher
	verifier *CallbackVerifier
}

func newVerifierEnv(t *testing.T, opts ...FetcherOption) *verifierEnv {
	t.Helper()
	cs := newCertServer(t)
	clock := &fakeClock{now: time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)}
	fetcherOpts := append(cs.fetcherOptions(), WithFetcherClock(clock.Now))
	fetcherOpts = append(fetcherOpts, opts...)
	fetcher := NewCertificateFetcher(fetcherOpts...)
	return &verifierEnv{
		server:   cs,
		clock:    clock,
		fetcher:  fetcher,
		verifier: NewCallbackVerifier(fetcher, WithVerifierClock(clock.Now)),
	}
}

func (e *verifierEnv) headers(t *testing.T, key *rsa.PrivateKey, body []byte) CallbackHeaders {
	t.Helper()
	ts := e.clock.Now().Format(time.RFC3339)
	return CallbackHeaders{Timestamp: ts, Signature: signCallback(t, key, ts, body), CertificateURL: "https://" + TrustedCertificateHost + testCertPath}
}

func TestCallbackHeadersFromHTTP(t *testing.T) {
	full := func() http.Header {
		h := http.Header{}
		h.Set(TimestampHeader, "2026-11-11T12:00:00Z")
		h.Set(SignatureHeader, "c2ln")
		h.Set(CertificateURLHeader, "https://webhooks.zerion.io/cert.pem")
		return h
	}
	got, err := CallbackHeadersFromHTTP(full())
	require.NoError(t, err)
	assert.Equal(t, CallbackHeaders{Timestamp: "2026-11-11T12:00:00Z", Signature: "c2ln", CertificateURL: "https://webhooks.zerion.io/cert.pem"}, got)

	for _, name := range []string{TimestampHeader, SignatureHeader, CertificateURLHeader} {
		t.Run("missing "+name, func(t *testing.T) {
			h := full()
			h.Del(name)
			_, err := CallbackHeadersFromHTTP(h)
			assert.ErrorIs(t, err, ErrMissingSignatureHeaders)
		})
		t.Run("duplicate "+name, func(t *testing.T) {
			h := full()
			h.Add(name, h.Get(name))
			_, err := CallbackHeadersFromHTTP(h)
			assert.ErrorIs(t, err, ErrMissingSignatureHeaders)
		})
		t.Run("empty "+name, func(t *testing.T) {
			h := full()
			h.Set(name, " ")
			_, err := CallbackHeadersFromHTTP(h)
			assert.ErrorIs(t, err, ErrMissingSignatureHeaders)
		})
	}
}

func TestParseCallbackTimestamp(t *testing.T) {
	want := time.Date(2024, 7, 31, 0, 17, 36, 0, time.UTC)
	for _, tc := range []struct {
		in   string
		want time.Time
	}{
		{"2024-07-31T00:17:36Z", want},
		{"2024-07-31T00:17:36.123456Z", want.Add(123456 * time.Microsecond)},
		{"2024-07-31T02:17:36+02:00", want},
		{"2024-07-31T02:17:36+0200", want},
		{"2024-07-31T00:17:36", want},
		{"2024-07-31T00:17:36.5", want.Add(500 * time.Millisecond)},
	} {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseCallbackTimestamp(tc.in)
			require.NoError(t, err)
			assert.True(t, tc.want.Equal(got), "got %s", got)
			assert.Equal(t, time.UTC, got.Location())
		})
	}
	for _, in := range []string{"", "yesterday", "1722385056", "2024-07-31", "2024-13-31T00:17:36Z"} {
		t.Run("invalid "+in, func(t *testing.T) {
			_, err := ParseCallbackTimestamp(in)
			assert.ErrorIs(t, err, ErrInvalidTimestamp)
		})
	}
}

func TestValidateCertificateURL(t *testing.T) {
	allowed := map[string]string{
		"https://webhooks.zerion.io/cert.pem":              "https://webhooks.zerion.io/cert.pem",
		"https://WEBHOOKS.Zerion.IO/cert.pem":              "https://webhooks.zerion.io/cert.pem",
		"https://webhooks.zerion.io:443/cert.pem":          "https://webhooks.zerion.io/cert.pem",
		"HTTPS://webhooks.zerion.io/a/b/public.crt?v=2":    "https://webhooks.zerion.io/a/b/public.crt?v=2",
		"https://webhooks.zerion.io/":                      "https://webhooks.zerion.io/",
		"https://webhooks.zerion.io/certificates/2026.pem": "https://webhooks.zerion.io/certificates/2026.pem",
	}
	for raw, want := range allowed {
		t.Run("allow "+raw, func(t *testing.T) {
			u, err := ValidateCertificateURL(raw)
			require.NoError(t, err)
			assert.Equal(t, want, u.String())
		})
	}
	denied := []string{
		"",
		"http://webhooks.zerion.io/cert.pem",
		"ftp://webhooks.zerion.io/cert.pem",
		"https://webhooks.zerion.io",
		"https://webhooks.zerion.io:8443/cert.pem",
		"https://webhooks.zerion.io:80/cert.pem",
		"https://webhooks.zerion.io:/cert.pem",
		"https://user@webhooks.zerion.io/cert.pem",
		"https://user:pass@webhooks.zerion.io/cert.pem",
		"https://@webhooks.zerion.io/cert.pem",
		"https://webhooks.zerion.io/cert.pem#frag",
		"https://webhooks.zerion.io/cert.pem#",
		"https://a.webhooks.zerion.io/cert.pem",
		"https://webhooks.zerion.io.evil.com/cert.pem",
		"https://evilwebhooks.zerion.io/cert.pem",
		"https://webhooks-zerion.io/cert.pem",
		"https://zerion.io/cert.pem",
		"https://api.zerion.io/cert.pem",
		"https://webhooks.zerion.io./cert.pem",
		"https://evil.com/webhooks.zerion.io/cert.pem",
		"https://evil.com?u=https://webhooks.zerion.io/cert.pem",
		"https://webhooks.zerion.io@evil.com/cert.pem",
		"https://evil.com\\@webhooks.zerion.io/cert.pem",
		"https://127.0.0.1/cert.pem",
		"https://[::1]/cert.pem",
		"https:webhooks.zerion.io/cert.pem",
		"//webhooks.zerion.io/cert.pem",
		"https://webhooks.zerion.io/cert.pem\n",
		" https://webhooks.zerion.io/cert.pem",
		"https://webhooks.zerion.io/" + strings.Repeat("a", 2100),
	}
	for _, raw := range denied {
		t.Run("deny "+raw, func(t *testing.T) {
			_, err := ValidateCertificateURL(raw)
			assert.ErrorIs(t, err, ErrUntrustedCertificateURL)
		})
	}
}

func TestVerifyValidSignatureCachesKey(t *testing.T) {
	keyA, _, _ := testKeys(t)
	env := newVerifierEnv(t)
	certPEM := selfSignedPEM(t, &keyA.PublicKey, keyA)
	env.server.serve(testCertPath, certPEM)
	body := []byte(`{"data":{"type":"callback"}}`)

	res, err := env.verifier.Verify(context.Background(), env.headers(t, keyA, body), body)
	require.NoError(t, err)
	assert.True(t, res.Key.Fresh)
	assert.False(t, res.Refreshed)
	assert.Equal(t, env.clock.Now(), res.Timestamp)
	block, _ := pem.Decode(certPEM)
	cert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	assert.Equal(t, hex.EncodeToString(sum[:]), res.Key.SPKISHA256)

	res, err = env.verifier.Verify(context.Background(), env.headers(t, keyA, body), body)
	require.NoError(t, err)
	assert.False(t, res.Key.Fresh)
	assert.EqualValues(t, 1, env.server.fetches.Load())
}

func TestVerifyRejections(t *testing.T) {
	keyA, keyB, _ := testKeys(t)
	body := []byte(`{"data":{}}`)
	certURL := "https://" + TrustedCertificateHost + testCertPath
	for _, tc := range []struct {
		name        string
		headers     func(env *verifierEnv) CallbackHeaders
		body        []byte
		wantErr     error
		wantFetches int64
	}{
		{
			name:        "tampered body",
			headers:     func(env *verifierEnv) CallbackHeaders { return env.headers(t, keyA, body) },
			body:        []byte(`{"data":{"x":1}}`),
			wantErr:     ErrInvalidSignature,
			wantFetches: 1,
		},
		{
			name: "tampered timestamp",
			headers: func(env *verifierEnv) CallbackHeaders {
				h := env.headers(t, keyA, body)
				h.Timestamp = env.clock.Now().Add(time.Second).Format(time.RFC3339)
				return h
			},
			wantErr:     ErrInvalidSignature,
			wantFetches: 1,
		},
		{
			name:        "wrong key",
			headers:     func(env *verifierEnv) CallbackHeaders { return env.headers(t, keyB, body) },
			wantErr:     ErrInvalidSignature,
			wantFetches: 1,
		},
		{
			name: "malformed base64 never fetches",
			headers: func(env *verifierEnv) CallbackHeaders {
				h := env.headers(t, keyA, body)
				h.Signature = "!!!not-base64!!!"
				return h
			},
			wantErr: ErrInvalidSignature,
		},
		{
			name: "stale timestamp",
			headers: func(env *verifierEnv) CallbackHeaders {
				ts := env.clock.Now().Add(-6 * time.Minute).Format(time.RFC3339)
				return CallbackHeaders{Timestamp: ts, Signature: signCallback(t, keyA, ts, body), CertificateURL: certURL}
			},
			wantErr: ErrStaleTimestamp,
		},
		{
			name: "future timestamp",
			headers: func(env *verifierEnv) CallbackHeaders {
				ts := env.clock.Now().Add(6 * time.Minute).Format(time.RFC3339)
				return CallbackHeaders{Timestamp: ts, Signature: signCallback(t, keyA, ts, body), CertificateURL: certURL}
			},
			wantErr: ErrStaleTimestamp,
		},
		{
			name: "unparseable timestamp",
			headers: func(env *verifierEnv) CallbackHeaders {
				h := env.headers(t, keyA, body)
				h.Timestamp = "not-a-time"
				return h
			},
			wantErr: ErrInvalidTimestamp,
		},
		{
			name: "untrusted certificate url",
			headers: func(env *verifierEnv) CallbackHeaders {
				h := env.headers(t, keyA, body)
				h.CertificateURL = "https://webhooks.zerion.io.evil.com" + testCertPath
				return h
			},
			wantErr: ErrUntrustedCertificateURL,
		},
		{
			name: "missing header",
			headers: func(env *verifierEnv) CallbackHeaders {
				h := env.headers(t, keyA, body)
				h.Signature = ""
				return h
			},
			wantErr: ErrMissingSignatureHeaders,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newVerifierEnv(t)
			env.server.serve(testCertPath, selfSignedPEM(t, &keyA.PublicKey, keyA))
			reqBody := body
			if tc.body != nil {
				reqBody = tc.body
			}
			_, err := env.verifier.Verify(context.Background(), tc.headers(env), reqBody)
			require.ErrorIs(t, err, tc.wantErr)
			assert.Equal(t, tc.wantFetches, env.server.fetches.Load())
		})
	}
}

func TestVerifyCachedKeyMismatch(t *testing.T) {
	keyA, keyB, _ := testKeys(t)
	body := []byte(`{"data":{}}`)

	warm := func(t *testing.T, env *verifierEnv) {
		t.Helper()
		env.server.serve(testCertPath, selfSignedPEM(t, &keyA.PublicKey, keyA))
		_, err := env.verifier.Verify(context.Background(), env.headers(t, keyA, body), body)
		require.NoError(t, err)
	}

	t.Run("rotation refreshes and succeeds", func(t *testing.T) {
		env := newVerifierEnv(t)
		warm(t, env)
		env.server.serve(testCertPath, selfSignedPEM(t, &keyB.PublicKey, keyB))
		env.clock.Advance(6 * time.Minute)
		res, err := env.verifier.Verify(context.Background(), env.headers(t, keyB, body), body)
		require.NoError(t, err)
		assert.True(t, res.Refreshed)
		assert.True(t, res.Key.Fresh)
		assert.EqualValues(t, 2, env.server.fetches.Load())

		res, err = env.verifier.Verify(context.Background(), env.headers(t, keyB, body), body)
		require.NoError(t, err)
		assert.False(t, res.Refreshed)
		assert.EqualValues(t, 2, env.server.fetches.Load())
	})

	t.Run("forged after refresh window is invalid", func(t *testing.T) {
		env := newVerifierEnv(t)
		warm(t, env)
		env.clock.Advance(6 * time.Minute)
		_, err := env.verifier.Verify(context.Background(), env.headers(t, keyB, body), body)
		require.ErrorIs(t, err, ErrInvalidSignature)
		assert.EqualValues(t, 2, env.server.fetches.Load())
	})

	t.Run("refresh throttled", func(t *testing.T) {
		env := newVerifierEnv(t)
		warm(t, env)
		env.server.serve(testCertPath, selfSignedPEM(t, &keyB.PublicKey, keyB))
		env.clock.Advance(4 * time.Minute)
		_, err := env.verifier.Verify(context.Background(), env.headers(t, keyB, body), body)
		require.ErrorIs(t, err, ErrSignatureMismatchThrottled)
		assert.False(t, errors.Is(err, ErrInvalidSignature))
		assert.EqualValues(t, 1, env.server.fetches.Load())
	})

	t.Run("refresh fetch failure", func(t *testing.T) {
		env := newVerifierEnv(t)
		warm(t, env)
		env.server.handle(testCertPath, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) })
		env.clock.Advance(6 * time.Minute)
		_, err := env.verifier.Verify(context.Background(), env.headers(t, keyB, body), body)
		require.ErrorIs(t, err, ErrCertificateUnavailable)
		assert.EqualValues(t, 2, env.server.fetches.Load())

		// A failed attempt still consumes the per-URL refresh window.
		_, err = env.verifier.Verify(context.Background(), env.headers(t, keyB, body), body)
		require.ErrorIs(t, err, ErrSignatureMismatchThrottled)
		assert.EqualValues(t, 2, env.server.fetches.Load())
	})

	t.Run("concurrent refreshes collapse", func(t *testing.T) {
		env := newVerifierEnv(t)
		warm(t, env)
		env.server.serve(testCertPath, selfSignedPEM(t, &keyB.PublicKey, keyB))
		env.clock.Advance(6 * time.Minute)
		gate, entered := make(chan struct{}), make(chan struct{}, 1)
		env.server.mu.Lock()
		env.server.gate, env.server.entered = gate, entered
		env.server.mu.Unlock()

		const n = 16
		errs := make(chan error, n)
		var wg sync.WaitGroup
		for range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := env.verifier.Verify(context.Background(), env.headers(t, keyB, body), body)
				errs <- err
			}()
		}
		<-entered
		time.Sleep(50 * time.Millisecond)
		close(gate)
		wg.Wait()
		close(errs)
		for err := range errs {
			require.NoError(t, err)
		}
		assert.EqualValues(t, 2, env.server.fetches.Load())
	})
}

func TestVerifyConcurrentColdFetchesCollapse(t *testing.T) {
	keyA, _, _ := testKeys(t)
	env := newVerifierEnv(t)
	env.server.serve(testCertPath, selfSignedPEM(t, &keyA.PublicKey, keyA))
	body := []byte(`{}`)
	const n = 16
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := env.verifier.Verify(context.Background(), env.headers(t, keyA, body), body)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	assert.EqualValues(t, 1, env.server.fetches.Load())
}

func TestVerifyColdFetchBudget(t *testing.T) {
	keyA, _, _ := testKeys(t)
	var outcomes []FetchOutcome
	var mu sync.Mutex
	env := newVerifierEnv(t, WithFetchBudget(time.Hour, 1), WithFetchObserver(func(o FetchOutcome) {
		mu.Lock()
		outcomes = append(outcomes, o)
		mu.Unlock()
	}))
	pemA := selfSignedPEM(t, &keyA.PublicKey, keyA)
	env.server.serve("/a.pem", pemA)
	env.server.serve("/b.pem", pemA)
	body := []byte(`{}`)

	h := env.headers(t, keyA, body)
	h.CertificateURL = "https://webhooks.zerion.io/a.pem"
	_, err := env.verifier.Verify(context.Background(), h, body)
	require.NoError(t, err)

	h.CertificateURL = "https://webhooks.zerion.io/b.pem"
	_, err = env.verifier.Verify(context.Background(), h, body)
	require.ErrorIs(t, err, ErrCertificateUnavailable)
	assert.EqualValues(t, 1, env.server.fetches.Load())
	assert.Equal(t, []FetchOutcome{FetchOutcomeOK, FetchOutcomeBudgetExhausted}, outcomes)

	// Cached URLs keep working while the cold budget is exhausted.
	h.CertificateURL = "https://webhooks.zerion.io/a.pem"
	_, err = env.verifier.Verify(context.Background(), h, body)
	require.NoError(t, err)
}

func TestVerifyCertificateFetchFailures(t *testing.T) {
	keyA, _, keyWeak := testKeys(t)
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	body := []byte(`{}`)

	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, cs *certServer)
	}{
		{"redirect refused", func(t *testing.T, cs *certServer) {
			cs.serve("/real.pem", selfSignedPEM(t, &keyA.PublicKey, keyA))
			cs.handle(testCertPath, func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, "/real.pem", http.StatusFound)
			})
		}},
		{"oversize response", func(t *testing.T, cs *certServer) {
			cs.serve(testCertPath, append([]byte(strings.Repeat("#", maxCertificateBytes)), selfSignedPEM(t, &keyA.PublicKey, keyA)...))
		}},
		{"oversize response without content length", func(t *testing.T, cs *certServer) {
			pemA := selfSignedPEM(t, &keyA.PublicKey, keyA)
			cs.handle(testCertPath, func(w http.ResponseWriter, _ *http.Request) {
				for range maxCertificateBytes / 1024 {
					_, _ = w.Write([]byte(strings.Repeat("#", 1024)))
					w.(http.Flusher).Flush()
				}
				_, _ = w.Write(pemA)
			})
		}},
		{"not pem", func(t *testing.T, cs *certServer) {
			cs.serve(testCertPath, []byte("hello"))
		}},
		{"pem without certificate", func(t *testing.T, cs *certServer) {
			der, err := x509.MarshalPKIXPublicKey(&keyA.PublicKey)
			require.NoError(t, err)
			cs.serve(testCertPath, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
		}},
		{"ecdsa key", func(t *testing.T, cs *certServer) {
			cs.serve(testCertPath, selfSignedPEM(t, &ecKey.PublicKey, ecKey))
		}},
		{"1024-bit rsa key", func(t *testing.T, cs *certServer) {
			cs.serve(testCertPath, selfSignedPEM(t, &keyWeak.PublicKey, keyWeak))
		}},
		{"non-200", func(t *testing.T, cs *certServer) {
			cs.handle(testCertPath, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newVerifierEnv(t)
			tc.setup(t, env.server)
			_, err := env.verifier.Verify(context.Background(), env.headers(t, keyA, body), body)
			require.ErrorIs(t, err, ErrCertificateUnavailable)
			assert.False(t, errors.Is(err, ErrInvalidSignature))
			assert.EqualValues(t, 1, env.server.fetches.Load())
		})
	}
}

func TestVerifyIgnoresProxyEnvironment(t *testing.T) {
	keyA, _, _ := testKeys(t)
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	env := newVerifierEnv(t)
	env.server.serve(testCertPath, selfSignedPEM(t, &keyA.PublicKey, keyA))
	body := []byte(`{}`)
	_, err := env.verifier.Verify(context.Background(), env.headers(t, keyA, body), body)
	require.NoError(t, err)
}

func TestVerifyCanceledContextIsUnavailable(t *testing.T) {
	keyA, _, _ := testKeys(t)
	env := newVerifierEnv(t)
	env.server.serve(testCertPath, selfSignedPEM(t, &keyA.PublicKey, keyA))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	body := []byte(`{}`)
	_, err := env.verifier.Verify(ctx, env.headers(t, keyA, body), body)
	require.ErrorIs(t, err, ErrCertificateUnavailable)
	require.ErrorIs(t, err, context.Canceled)
}

func TestCertificateCacheIsBoundedAndKeepsVerifiedEntries(t *testing.T) {
	keyA, _, _ := testKeys(t)
	env := newVerifierEnv(t, WithCertificateCacheSize(2))
	pemA := selfSignedPEM(t, &keyA.PublicKey, keyA)
	body := []byte(`{}`)
	for _, p := range []string{"/legit.pem", "/x.pem", "/y.pem", "/z.pem"} {
		env.server.serve(p, pemA)
	}
	h := env.headers(t, keyA, body)
	h.CertificateURL = "https://webhooks.zerion.io/legit.pem"
	_, err := env.verifier.Verify(context.Background(), h, body)
	require.NoError(t, err)

	forged := env.headers(t, keyA, []byte(`other`))
	for _, p := range []string{"/x.pem", "/y.pem", "/z.pem"} {
		forged.CertificateURL = "https://webhooks.zerion.io" + p
		_, err := env.verifier.Verify(context.Background(), forged, body)
		require.ErrorIs(t, err, ErrInvalidSignature)
	}
	env.fetcher.mu.Lock()
	size := len(env.fetcher.cache)
	_, kept := env.fetcher.cache["https://webhooks.zerion.io/legit.pem"]
	env.fetcher.mu.Unlock()
	assert.Equal(t, 2, size)
	assert.True(t, kept, "verified entry must survive eviction")
}

func TestProductionDialerRejectsNonPublicAddresses(t *testing.T) {
	cs := newCertServer(t)
	f := NewCertificateFetcher(WithFetcherRootCAs(cs.pool))
	transport := f.client.Transport.(*http.Transport)

	_, err := transport.DialContext(context.Background(), "tcp", cs.srv.Listener.Addr().String())
	require.ErrorIs(t, err, errDialRefused)
	_, err = transport.DialContext(context.Background(), "tcp", "127.0.0.1:443")
	require.ErrorIs(t, err, errDialRefused)

	u := *mustParseURL(t, cs.srv.URL+testCertPath)
	_, err = f.fetch(context.Background(), &u)
	require.ErrorIs(t, err, errDialRefused)
	assert.Zero(t, cs.fetches.Load())
}

func TestPublicDialControl(t *testing.T) {
	for _, tc := range []struct {
		network, address string
		ok               bool
	}{
		{"tcp4", "93.184.216.34:443", true},
		{"tcp6", "[2606:4700::6810:84e5]:443", true},
		{"tcp6", "[64:ff9b::5db8:d822]:443", true},
		{"tcp4", "93.184.216.34:80", false},
		{"udp4", "93.184.216.34:443", false},
		{"tcp4", "127.0.0.1:443", false},
		{"tcp4", "10.1.2.3:443", false},
		{"tcp4", "172.16.0.1:443", false},
		{"tcp4", "192.168.1.1:443", false},
		{"tcp4", "169.254.169.254:443", false},
		{"tcp4", "100.64.0.1:443", false},
		{"tcp4", "100.127.255.254:443", false},
		{"tcp4", "0.0.0.0:443", false},
		{"tcp4", "224.0.0.1:443", false},
		{"tcp4", "255.255.255.255:443", false},
		{"tcp4", "198.18.0.1:443", false},
		{"tcp6", "[::1]:443", false},
		{"tcp6", "[::]:443", false},
		{"tcp6", "[fd00:ec2::254]:443", false},
		{"tcp6", "[fc00::1]:443", false},
		{"tcp6", "[fe80::1]:443", false},
		{"tcp6", "[fe80::1%en0]:443", false},
		{"tcp6", "[ff02::1]:443", false},
		{"tcp6", "[::ffff:127.0.0.1]:443", false},
		{"tcp6", "[::ffff:169.254.169.254]:443", false},
		{"tcp6", "[::ffff:10.0.0.1]:443", false},
		{"tcp6", "[64:ff9b::a9fe:a9fe]:443", false},
		{"tcp6", "[64:ff9b::7f00:1]:443", false},
		{"tcp6", "[2002:7f00:1::]:443", false},
		{"tcp6", "[2001:db8::1]:443", false},
		{"tcp6", "[::7f00:1]:443", false},
	} {
		t.Run(tc.network+" "+tc.address, func(t *testing.T) {
			err := publicDialControl(tc.network, tc.address, nil)
			if tc.ok {
				assert.NoError(t, err)
			} else {
				assert.ErrorIs(t, err, errDialRefused)
			}
		})
	}
	assert.False(t, isPublicAddr(netip.Addr{}))
}

// Junk certificate URLs must not starve the cold-fetch budget that a fresh
// process needs for the real URL: with a pin they never reach the fetcher, and
// the pinned certificate is prefetched outside the budget.
func TestPinnedCertificateURLResistsBudgetStarvation(t *testing.T) {
	keyA, _, _ := testKeys(t)
	env := newVerifierEnv(t, WithFetchBudget(time.Hour, 1))
	pinned := "https://" + TrustedCertificateHost + testCertPath
	env.verifier = NewCallbackVerifier(env.fetcher, WithVerifierClock(env.clock.Now), WithPinnedCertificateURL(pinned))
	env.server.serve(testCertPath, selfSignedPEM(t, &keyA.PublicKey, keyA))
	body := []byte(`{"data":{}}`)

	for _, junk := range []string{
		"https://webhooks.zerion.io/x/1", "https://webhooks.zerion.io/x/2",
		pinned + "?n=1", "https://webhooks.zerion.io/certificates/other.pem",
	} {
		h := env.headers(t, keyA, body)
		h.CertificateURL = junk
		_, err := env.verifier.Verify(context.Background(), h, body)
		require.ErrorIs(t, err, ErrUntrustedCertificateURL, junk)
	}
	assert.Zero(t, env.server.fetches.Load(), "rejected before any fetch")

	// Exhaust the cold budget anyway; prefetch must not depend on it.
	require.True(t, env.fetcher.budget.AllowN(env.clock.Now(), 1))
	require.NoError(t, env.verifier.Prefetch(context.Background()))
	_, err := env.verifier.Verify(context.Background(), env.headers(t, keyA, body), body)
	require.NoError(t, err)
	assert.EqualValues(t, 1, env.server.fetches.Load())
}

func TestPinnedCertificateURLFailsClosed(t *testing.T) {
	keyA, _, _ := testKeys(t)
	env := newVerifierEnv(t)
	env.verifier = NewCallbackVerifier(env.fetcher, WithVerifierClock(env.clock.Now),
		WithPinnedCertificateURL("https://evil.example/cert.pem"))
	body := []byte(`{}`)
	_, err := env.verifier.Verify(context.Background(), env.headers(t, keyA, body), body)
	require.ErrorIs(t, err, ErrUntrustedCertificateURL)
	require.ErrorIs(t, env.verifier.Prefetch(context.Background()), ErrUntrustedCertificateURL)
	unpinned := NewCallbackVerifier(env.fetcher)
	require.ErrorIs(t, unpinned.Prefetch(context.Background()), ErrUntrustedCertificateURL)
}
