package snapshotd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
		{name: "missing base URL", want: "base URL is required"},
		{name: "invalid base URL", opts: []Option{WithBaseURL("not a URL")}, want: "invalid base URL"},
		{name: "base URL with userinfo", opts: []Option{WithBaseURL("https://user:pass@snapshot.example")}, want: "invalid base URL"},
		{name: "base URL with query", opts: []Option{WithBaseURL("https://snapshot.example?tenant=a")}, want: "invalid base URL"},
		{name: "base URL with empty query", opts: []Option{WithBaseURL("https://snapshot.example?")}, want: "invalid base URL"},
		{name: "base URL with fragment", opts: []Option{WithBaseURL("https://snapshot.example#fragment")}, want: "invalid base URL"},
		{name: "invalid timeout", opts: []Option{WithBaseURL("https://snapshot.example"), WithTimeout(0)}, want: "timeout must be positive"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := New(tt.opts...)
			require.ErrorContains(t, err, tt.want)
			require.Nil(t, client)
		})
	}
}

func TestNewTrimsBaseURL(t *testing.T) {
	got, err := New(WithBaseURL(" https://snapshot.example/ "))
	require.NoError(t, err)
	concrete := got.(*client)
	assert.Equal(t, "https://snapshot.example", concrete.baseURL)
}

func TestEndpointPreservesBaseURLPathPrefix(t *testing.T) {
	block := uint64(12345)
	got := mustNew(t, WithBaseURL("https://snapshot.example/internal/snapshot/"))

	endpoint := got.(*client).endpoint("pps", Query{
		ChainID:     8453,
		Strategy:    testStrategy,
		BlockNumber: &block,
	})
	assert.Equal(t, "https://snapshot.example/internal/snapshot/v1/pps/8453/0x1111111111111111111111111111111111111111?block=12345", endpoint)
}

func TestGetPPSBuildsPublicRequest(t *testing.T) {
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
		WithHTTPClient(server.Client()),
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
	assert.Empty(t, request.authorization)
}

func TestGetPPSIncludesPinnedBlock(t *testing.T) {
	block := uint64(12345)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "12345", r.URL.Query().Get("block"))
		_, _ = fmt.Fprint(w, `{"pps":"1"}`)
	}))
	defer server.Close()

	client := mustNew(t, WithBaseURL(server.URL), WithHTTPClient(server.Client()))
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

	client := mustNew(t, WithBaseURL(server.URL), WithHTTPClient(server.Client()))
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

func TestHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = fmt.Fprint(w, `{"error":{"code":"UPSTREAM_ERROR","message":"rpc unavailable","request_id":"req-1"}}`)
	}))
	defer server.Close()

	client := mustNew(t, WithBaseURL(server.URL), WithHTTPClient(server.Client()))
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

	client := mustNew(t, WithBaseURL(server.URL), WithHTTPClient(server.Client()))
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

		client := mustNew(t, WithBaseURL(server.URL), WithHTTPClient(server.Client()))
		_, err := client.GetPPS(context.Background(), Query{ChainID: 1, Strategy: testStrategy})
		assert.ErrorIs(t, err, ErrInvalidResponse)
	})

	t.Run("mismatched allocation identity", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprintf(w, `{"strategy":%q,"chainId":2,"asset":%q,"totalAssets":"1","totalSupply":"1","idleBalance":"0","sources":[]}`, testStrategy.Hex(), testAsset.Hex())
		}))
		defer server.Close()

		client := mustNew(t, WithBaseURL(server.URL), WithHTTPClient(server.Client()))
		_, err := client.GetAllocation(context.Background(), Query{ChainID: 1, Strategy: testStrategy})
		assert.ErrorIs(t, err, ErrInvalidResponse)
	})

	t.Run("malformed JSON", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprint(w, `{"pps":`)
		}))
		defer server.Close()

		client := mustNew(t, WithBaseURL(server.URL), WithHTTPClient(server.Client()))
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

		client := mustNew(t, WithBaseURL(server.URL), WithHTTPClient(server.Client()))
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

	client := mustNew(t, WithBaseURL(server.URL), WithHTTPClient(server.Client()))
	allocation, err := client.GetAllocation(context.Background(), Query{ChainID: 1, Strategy: testStrategy})
	require.NoError(t, err)
	require.Len(t, allocation.Sources, 1)
	assert.Equal(t, "0", allocation.Sources[0].AssetTVL)
}

func TestQueryValidationAndContextCancellation(t *testing.T) {
	client := mustNew(t, WithBaseURL("https://snapshot.example"))
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

	client := mustNew(t, WithBaseURL(server.URL))
	_, err := client.GetPPS(context.Background(), Query{ChainID: 1, Strategy: testStrategy})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUpstream)
	assert.Equal(t, int64(1), hits.Load())
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
