package snapshotd

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testSecretHex = "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"

var (
	testStrategy = common.HexToAddress("0x1111111111111111111111111111111111111111")
	testAsset    = common.HexToAddress("0x2222222222222222222222222222222222222222")
	testSource   = common.HexToAddress("0x3333333333333333333333333333333333333333")
	testOracle   = common.HexToAddress("0x4444444444444444444444444444444444444444")
)

type capturedHTTPRequest struct {
	method        string
	path          string
	rawQuery      string
	accept        string
	authorization string
}

func TestNewValidatesConfiguration(t *testing.T) {
	tests := []struct {
		name string
		opts []Option
		want string
	}{
		{name: "missing base URL", opts: []Option{WithJWTSecretHex(testSecretHex)}, want: "base URL is required"},
		{name: "invalid base URL", opts: []Option{WithBaseURL("not a URL"), WithJWTSecretHex(testSecretHex)}, want: "invalid base URL"},
		{name: "base URL with userinfo", opts: []Option{WithBaseURL("https://user:pass@snapshot.example"), WithJWTSecretHex(testSecretHex)}, want: "invalid base URL"},
		{name: "base URL with query", opts: []Option{WithBaseURL("https://snapshot.example?tenant=a"), WithJWTSecretHex(testSecretHex)}, want: "invalid base URL"},
		{name: "base URL with empty query", opts: []Option{WithBaseURL("https://snapshot.example?"), WithJWTSecretHex(testSecretHex)}, want: "invalid base URL"},
		{name: "base URL with fragment", opts: []Option{WithBaseURL("https://snapshot.example#fragment"), WithJWTSecretHex(testSecretHex)}, want: "invalid base URL"},
		{name: "missing secret", opts: []Option{WithBaseURL("https://snapshot.example")}, want: "JWT secret is required"},
		{name: "malformed secret", opts: []Option{WithBaseURL("https://snapshot.example"), WithJWTSecretHex("xyz")}, want: "decode JWT secret"},
		{name: "short secret", opts: []Option{WithBaseURL("https://snapshot.example"), WithJWTSecretHex("01")}, want: "want at least 32"},
		{name: "zero secret", opts: []Option{WithBaseURL("https://snapshot.example"), WithJWTSecretHex(strings.Repeat("00", 32))}, want: "cannot be all zeros"},
		{name: "invalid timeout", opts: []Option{WithBaseURL("https://snapshot.example"), WithJWTSecretHex(testSecretHex), WithTimeout(0)}, want: "timeout must be positive"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := New(tt.opts...)
			require.ErrorContains(t, err, tt.want)
			require.Nil(t, client)
		})
	}
}

func TestNewAcceptsPrefixedSecretAndTrimsBaseURL(t *testing.T) {
	got, err := New(
		WithBaseURL(" https://snapshot.example/ "),
		WithJWTSecretHex("0x"+testSecretHex),
	)
	require.NoError(t, err)
	concrete := got.(*client)
	assert.Equal(t, "https://snapshot.example", concrete.baseURL)
	assert.Len(t, concrete.jwtSecret, 32)
	assert.Empty(t, concrete.jwtSecretHex)
}

func TestEndpointPreservesBaseURLPathPrefix(t *testing.T) {
	block := uint64(12345)
	got := mustNew(t,
		WithBaseURL("https://snapshot.example/internal/snapshot/"),
		WithJWTSecretHex(testSecretHex),
	)

	endpoint := got.(*client).endpoint("pps", Query{
		ChainID:     8453,
		Strategy:    testStrategy,
		BlockNumber: &block,
	})
	assert.Equal(t, "https://snapshot.example/internal/snapshot/v1/pps/8453/0x1111111111111111111111111111111111111111?block=12345", endpoint)
}

func TestGetPPSBuildsRequestAndMintsJWT(t *testing.T) {
	fixedNow := time.Unix(1_800_000_000, 0)
	secret, err := hex.DecodeString(testSecretHex)
	require.NoError(t, err)
	requests := make(chan capturedHTTPRequest, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- capturedHTTPRequest{
			method:        r.Method,
			path:          r.URL.Path,
			rawQuery:      r.URL.RawQuery,
			accept:        r.Header.Get("Accept"),
			authorization: r.Header.Get("Authorization"),
		}
		_, _ = fmt.Fprint(w, `{"pps":"1003942771999791660"}`)
	}))
	defer server.Close()

	client := mustNew(t,
		WithBaseURL(server.URL),
		WithJWTSecretHex(testSecretHex),
		WithHTTPClient(server.Client()),
		withClock(func() time.Time { return fixedNow }),
	)
	result, err := client.GetPPS(context.Background(), Query{ChainID: 8453, Strategy: testStrategy})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "1003942771999791660", result.PPS)
	request := <-requests
	assert.Equal(t, http.MethodGet, request.method)
	assert.Equal(t, "/v1/pps/8453/0x1111111111111111111111111111111111111111", request.path)
	assert.Empty(t, request.rawQuery)
	assert.Equal(t, "application/json", request.accept)
	assertJWT(t, request.authorization, secret, fixedNow)
}

func TestGetPPSIncludesPinnedBlock(t *testing.T) {
	block := uint64(12345)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "12345", r.URL.Query().Get("block"))
		_, _ = fmt.Fprint(w, `{"pps":"1"}`)
	}))
	defer server.Close()

	client := mustNew(t, WithBaseURL(server.URL), WithJWTSecretHex(testSecretHex), WithHTTPClient(server.Client()))
	_, err := client.GetPPS(context.Background(), Query{ChainID: 1, Strategy: testStrategy, BlockNumber: &block})
	require.NoError(t, err)
}

func TestGetAllocationDecodesResponse(t *testing.T) {
	block := uint64(98765)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/allocation/1/0x1111111111111111111111111111111111111111", r.URL.Path)
		assert.Equal(t, "98765", r.URL.Query().Get("block"))
		_, _ = fmt.Fprintf(w, `{
			"strategy":%q,
			"chainId":1,
			"blockNumber":98765,
			"asset":%q,
			"assetSymbol":"USDC",
			"assetDecimals":6,
			"totalAssets":"1200",
			"totalSupply":"1000",
			"idleBalance":"200",
			"sources":[{
				"source":%q,
				"oracle":%q,
				"kind":1,
				"rawShares":"50",
				"assetTvl":"1000",
				"assetSymbol":"USDC",
				"active":true
			}]
		}`, testStrategy.Hex(), testAsset.Hex(), testSource.Hex(), testOracle.Hex())
	}))
	defer server.Close()

	client := mustNew(t, WithBaseURL(server.URL), WithJWTSecretHex(testSecretHex), WithHTTPClient(server.Client()))
	allocation, err := client.GetAllocation(context.Background(), Query{ChainID: 1, Strategy: testStrategy, BlockNumber: &block})
	require.NoError(t, err)
	require.NotNil(t, allocation)
	assert.Equal(t, testAsset, allocation.Asset)
	assert.Equal(t, "USDC", allocation.AssetSymbol)
	assert.Equal(t, uint8(6), allocation.AssetDecimals)
	assert.Equal(t, "200", allocation.IdleBalance)
	require.Len(t, allocation.Sources, 1)
	assert.Equal(t, testSource, allocation.Sources[0].Source)
	assert.Equal(t, "1000", allocation.Sources[0].AssetTVL)
	assert.True(t, allocation.Sources[0].Active)
}

func TestEachRequestMintsCurrentJWT(t *testing.T) {
	var calls atomic.Int64
	times := []time.Time{time.Unix(1_800_000_000, 0), time.Unix(1_800_000_001, 0)}
	secret, err := hex.DecodeString(testSecretHex)
	require.NoError(t, err)
	authorizations := make(chan string, len(times))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		authorizations <- r.Header.Get("Authorization")
		_, _ = fmt.Fprint(w, `{"pps":"1"}`)
	}))
	defer server.Close()

	var clockCalls atomic.Int64
	client := mustNew(t,
		WithBaseURL(server.URL),
		WithJWTSecretHex(testSecretHex),
		WithHTTPClient(server.Client()),
		withClock(func() time.Time {
			return times[int(clockCalls.Add(1)-1)]
		}),
	)
	query := Query{ChainID: 1, Strategy: testStrategy}
	for _, issuedAt := range times {
		_, err = client.GetPPS(context.Background(), query)
		require.NoError(t, err)
		assertJWT(t, <-authorizations, secret, issuedAt)
	}
	assert.Equal(t, int64(2), calls.Load())
}

func TestHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = fmt.Fprint(w, `{"error":{"code":"UPSTREAM_ERROR","message":"rpc unavailable","request_id":"req-1"}}`)
	}))
	defer server.Close()

	client := mustNew(t, WithBaseURL(server.URL), WithJWTSecretHex(testSecretHex), WithHTTPClient(server.Client()))
	_, err := client.GetPPS(context.Background(), Query{ChainID: 1, Strategy: testStrategy})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUpstream)
	var httpErr *HTTPError
	require.ErrorAs(t, err, &httpErr)
	assert.Equal(t, http.StatusBadGateway, httpErr.StatusCode)
	assert.Equal(t, "UPSTREAM_ERROR", httpErr.Code)
	assert.Equal(t, "rpc unavailable", httpErr.Message)
	assert.Equal(t, "req-1", httpErr.RequestID)
}

func TestUnauthorizedPlainTextResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprint(w, "unauthorized\n")
	}))
	defer server.Close()

	client := mustNew(t, WithBaseURL(server.URL), WithJWTSecretHex(testSecretHex), WithHTTPClient(server.Client()))
	_, err := client.GetPPS(context.Background(), Query{ChainID: 1, Strategy: testStrategy})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnauthorized)
	assert.ErrorContains(t, err, "unauthorized")
}

func TestResponseValidation(t *testing.T) {
	t.Run("invalid PPS", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprint(w, `{"pps":"1.5"}`)
		}))
		defer server.Close()

		client := mustNew(t, WithBaseURL(server.URL), WithJWTSecretHex(testSecretHex), WithHTTPClient(server.Client()))
		_, err := client.GetPPS(context.Background(), Query{ChainID: 1, Strategy: testStrategy})
		assert.ErrorIs(t, err, ErrInvalidResponse)
	})

	t.Run("mismatched allocation identity", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprintf(w, `{"strategy":%q,"chainId":2,"asset":%q,"totalAssets":"1","totalSupply":"1","idleBalance":"0","sources":[]}`, testStrategy.Hex(), testAsset.Hex())
		}))
		defer server.Close()

		client := mustNew(t, WithBaseURL(server.URL), WithJWTSecretHex(testSecretHex), WithHTTPClient(server.Client()))
		_, err := client.GetAllocation(context.Background(), Query{ChainID: 1, Strategy: testStrategy})
		assert.ErrorIs(t, err, ErrInvalidResponse)
	})

	t.Run("malformed JSON", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprint(w, `{"pps":`)
		}))
		defer server.Close()

		client := mustNew(t, WithBaseURL(server.URL), WithJWTSecretHex(testSecretHex), WithHTTPClient(server.Client()))
		_, err := client.GetPPS(context.Background(), Query{ChainID: 1, Strategy: testStrategy})
		assert.ErrorIs(t, err, ErrInvalidResponse)
		assert.ErrorContains(t, err, "decode response")
		var syntaxErr *json.SyntaxError
		assert.ErrorAs(t, err, &syntaxErr)
	})

	t.Run("oversized body", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprint(w, strings.Repeat("x", maxResponseBody+1))
		}))
		defer server.Close()

		client := mustNew(t, WithBaseURL(server.URL), WithJWTSecretHex(testSecretHex), WithHTTPClient(server.Client()))
		_, err := client.GetPPS(context.Background(), Query{ChainID: 1, Strategy: testStrategy})
		assert.ErrorIs(t, err, ErrInvalidResponse)
		assert.ErrorContains(t, err, "response body exceeds")
	})
}

func TestResponseReadErrorPreservesCauses(t *testing.T) {
	readErr := errors.New("read response failed")
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body:       errorReadCloser{err: readErr},
			Request:    request,
		}, nil
	})}
	client := mustNew(t,
		WithBaseURL("https://snapshot.example"),
		WithJWTSecretHex(testSecretHex),
		WithHTTPClient(httpClient),
	)

	_, err := client.GetPPS(context.Background(), Query{ChainID: 1, Strategy: testStrategy})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidResponse)
	assert.ErrorIs(t, err, readErr)
}

func TestHTTPErrorReadErrorPreservesCauses(t *testing.T) {
	readErr := errors.New("read error response failed")
	err := decodeHTTPError(&http.Response{
		StatusCode: http.StatusBadGateway,
		Body:       errorReadCloser{err: readErr},
	})

	assert.ErrorIs(t, err, ErrUpstream)
	assert.ErrorIs(t, err, readErr)
	var httpErr *HTTPError
	assert.ErrorAs(t, err, &httpErr)
}

func TestValidateAndNormalizeAllocationReportsFirstFieldDeterministically(t *testing.T) {
	query := Query{ChainID: 1, Strategy: testStrategy}
	allocation := &Allocation{
		Strategy: testStrategy,
		ChainID:  1,
		Asset:    testAsset,
	}

	for i := 0; i < 100; i++ {
		err := validateAndNormalizeAllocation(query, allocation)
		assert.EqualError(t, err, "snapshotd: totalAssets is required: snapshotd invalid response")
	}
}

func TestGetAllocationNormalizesInactiveSourceWithoutAmounts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `{
			"strategy":%q,
			"chainId":1,
			"asset":%q,
			"totalAssets":"1",
			"totalSupply":"1",
			"idleBalance":"1",
			"sources":[{"source":%q,"oracle":%q,"kind":1}]
		}`, testStrategy.Hex(), testAsset.Hex(), testSource.Hex(), testOracle.Hex())
	}))
	defer server.Close()

	client := mustNew(t, WithBaseURL(server.URL), WithJWTSecretHex(testSecretHex), WithHTTPClient(server.Client()))
	allocation, err := client.GetAllocation(context.Background(), Query{ChainID: 1, Strategy: testStrategy})
	require.NoError(t, err)
	require.Len(t, allocation.Sources, 1)
	assert.Equal(t, "0", allocation.Sources[0].AssetTVL)
}

func TestQueryValidationAndContextCancellation(t *testing.T) {
	client := mustNew(t, WithBaseURL("https://snapshot.example"), WithJWTSecretHex(testSecretHex))
	_, err := client.GetPPS(context.Background(), Query{Strategy: testStrategy})
	assert.ErrorContains(t, err, "chain ID must be positive")
	_, err = client.GetPPS(context.Background(), Query{ChainID: 1})
	assert.ErrorContains(t, err, "strategy is required")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = client.GetPPS(ctx, Query{ChainID: 1, Strategy: testStrategy})
	assert.ErrorIs(t, err, context.Canceled)
}

func TestDefaultClientDoesNotRetry(t *testing.T) {
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = fmt.Fprint(w, `{"error":{"code":"UPSTREAM_ERROR","message":"unavailable"}}`)
	}))
	defer server.Close()

	client := mustNew(t, WithBaseURL(server.URL), WithJWTSecretHex(testSecretHex))
	_, err := client.GetPPS(context.Background(), Query{ChainID: 1, Strategy: testStrategy})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUpstream)
	assert.Equal(t, int64(1), hits.Load())
}

func assertJWT(t *testing.T, authorization string, secret []byte, issuedAt time.Time) {
	t.Helper()
	require.True(t, strings.HasPrefix(authorization, "Bearer "))
	raw := strings.TrimPrefix(authorization, "Bearer ")
	token, err := jwt.Parse(raw, func(token *jwt.Token) (any, error) {
		require.Equal(t, jwt.SigningMethodHS256, token.Method)
		return secret, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}), jwt.WithoutClaimsValidation())
	require.NoError(t, err)
	require.True(t, token.Valid)
	claims, ok := token.Claims.(jwt.MapClaims)
	require.True(t, ok)
	assert.Equal(t, float64(issuedAt.Unix()), claims["iat"])
}

func mustNew(t *testing.T, opts ...Option) Client {
	t.Helper()
	client, err := New(opts...)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	return client
}

func TestHTTPErrorUnwrapsSentinel(t *testing.T) {
	err := &HTTPError{StatusCode: http.StatusNotFound, Err: ErrNotFound}
	assert.True(t, errors.Is(err, ErrNotFound))
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

type errorReadCloser struct {
	err error
}

func (r errorReadCloser) Read([]byte) (int, error) {
	return 0, r.err
}

func (errorReadCloser) Close() error {
	return nil
}
