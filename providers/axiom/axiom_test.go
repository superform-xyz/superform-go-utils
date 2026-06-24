package axiom

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/superform-xyz/superform-go-utils/utils/constants"
)

func TestNew(t *testing.T) {
	p := New("https://axiom.example.com/")
	require.NotNil(t, p)

	concrete, ok := p.(*axiom)
	require.True(t, ok)
	assert.Equal(t, "https://axiom.example.com", concrete.baseURL)
	assert.NotNil(t, concrete.client)
}

func TestSupportsChain(t *testing.T) {
	p := New("http://unused")

	assert.Contains(t, p.SupportedChains(), constants.MainnetChainID)
	assert.Contains(t, p.SupportedChains(), constants.BaseChainID)
	assert.Contains(t, p.SupportedChains(), constants.HyperEvmChainID)
	assert.Contains(t, p.SupportedChains(), constants.FlareChainID)
	assert.NotContains(t, p.SupportedChains(), uint64(99999))
}

func TestGetTokenPrice_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/price/8453/0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48", r.URL.Path,
			"path must use lowercased token address")
		_, _ = fmt.Fprint(w, `{"value":"1850.500000000000000000"}`)
	}))
	defer srv.Close()

	p := New(srv.URL, WithHTTPClient(srv.Client()))
	got, err := p.GetTokenPrice(
		context.Background(),
		constants.BaseChainID,
		common.HexToAddress("0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
	)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, constants.BaseChainID, got.ChainID)
	assert.Equal(t, common.HexToAddress("0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"), got.Address)
	assert.InDelta(t, 1850.5, got.Price, 1e-9)
	assert.False(t, got.UpdatedAt.IsZero())
}

func TestGetTokenPrice_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	p := New(srv.URL, WithHTTPClient(srv.Client()))
	_, err := p.GetTokenPrice(context.Background(), constants.MainnetChainID, common.HexToAddress("0xdeadbeef"))
	assert.ErrorIs(t, err, ErrTokenNotFound)
}

func TestGetTokenPrice_AuthRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	p := New(srv.URL, WithHTTPClient(srv.Client()))
	_, err := p.GetTokenPrice(
		context.Background(),
		constants.MainnetChainID,
		common.HexToAddress("0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
	)
	assert.ErrorIs(t, err, ErrUnauthorized)
}

func TestGetTokenPrice_ServerErrorIsUpstream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	p := New(srv.URL, WithHTTPClient(srv.Client()))
	_, err := p.GetTokenPrice(context.Background(), constants.MainnetChainID, common.HexToAddress("0x1"))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUpstream)
	assert.NotErrorIs(t, err, ErrTokenNotFound)
	assert.NotErrorIs(t, err, ErrUnauthorized)
}

func TestGetTokenPrice_TooManyRequestsIsRateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	p := New(srv.URL, WithHTTPClient(srv.Client()))
	_, err := p.GetTokenPrice(context.Background(), constants.MainnetChainID, common.HexToAddress("0x1"))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRateLimited)
	assert.NotErrorIs(t, err, ErrUpstream)
}

func TestGetTokenPrice_ParseError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"value":"not-a-number"}`)
	}))
	defer srv.Close()

	p := New(srv.URL, WithHTTPClient(srv.Client()))
	_, err := p.GetTokenPrice(context.Background(), constants.MainnetChainID, common.HexToAddress("0x1"))
	require.Error(t, err)
}

func TestGetTokenPrice_ZeroValueIsNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"value":"0"}`)
	}))
	defer srv.Close()

	p := New(srv.URL, WithHTTPClient(srv.Client()))
	_, err := p.GetTokenPrice(context.Background(), constants.MainnetChainID, common.HexToAddress("0x1"))
	assert.ErrorIs(t, err, ErrTokenNotFound)
}

func TestGetTokenPrice_UnsupportedChain(t *testing.T) {
	p := New("http://unused")
	_, err := p.GetTokenPrice(context.Background(), 99999, common.HexToAddress("0x1"))
	assert.ErrorIs(t, err, ErrUnsupportedChain)
}

func TestHealthCheck_TokenNotFoundIsHealthy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	p := New(srv.URL, WithHTTPClient(srv.Client()))
	assert.NoError(t, p.HealthCheck(context.Background()))
}

func TestGetTokenPrice_ContextCanceled(t *testing.T) {
	p := New("http://unused")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := p.GetTokenPrice(ctx, constants.MainnetChainID, common.HexToAddress("0x1"))
	assert.True(t, errors.Is(err, context.Canceled))
}
