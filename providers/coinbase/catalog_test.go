package coinbase

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCatalogRequests(t *testing.T) {
	public, secret := newEd25519Secret(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		signingInput, _, claims, signature := splitJWT(t, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		require.True(t, ed25519.Verify(public, []byte(signingInput), signature))
		require.Equal(t, []any{"GET " + r.Host + r.URL.Path}, claims["uris"])
		switch r.URL.Path {
		case buyConfigPath:
			require.Empty(t, r.URL.RawQuery)
			_, _ = fmt.Fprint(w, `{"countries":[{"id":"US","payment_methods":[{"id":"CARD"},{"id":"APPLE_PAY"}],"subdivisions":["CA","NY"]}]}`)
		case buyOptionsPath:
			switch r.URL.Query().Get("country") {
			case "US":
				require.Equal(t, "CA", r.URL.Query().Get("subdivision"))
			case "DE":
				require.False(t, r.URL.Query().Has("subdivision"))
			default:
				t.Error("country was not normalized")
			}
			_, _ = fmt.Fprint(w, `{"payment_currencies":[{"id":"USD","limits":[{"id":"CARD","min":"2.01","max":"7500.00"}]}],"purchase_currencies":[{"symbol":"USDC","networks":[{"name":"base","chain_id":"8453","contract_address":"0x833589fcd6edb6e08f4c7c32d4f71b54bda02913"}]},{"symbol":"BTC","networks":[{"name":"bitcoin","chain_id":"","contract_address":""}]}]}`)
		default:
			t.Error("unexpected endpoint")
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := mustNew(t, WithAPIKeySecret(secret), WithAPIBaseURL(srv.URL), WithHTTPClient(srv.Client()))
	defer func() { _ = c.Close() }()
	config, err := c.GetBuyConfig(context.Background())
	require.NoError(t, err)
	require.Len(t, config.Countries, 1)
	require.Equal(t, []string{"CA", "NY"}, config.Countries[0].Subdivisions)
	require.Equal(t, []BuyMethod{{ID: "CARD"}, {ID: "APPLE_PAY"}}, config.Countries[0].PaymentMethods)
	for _, req := range []GetBuyOptionsRequest{{Country: " us ", Subdivision: " ca "}, {Country: "de"}} {
		options, err := c.GetBuyOptions(context.Background(), req)
		require.NoError(t, err)
		require.Len(t, options.PaymentCurrencies, 1)
		require.Equal(t, []BuyLimit{{ID: "CARD", Min: "2.01", Max: "7500.00"}}, options.PaymentCurrencies[0].Limits)
		require.Len(t, options.PurchaseCurrencies, 2)
		require.Equal(t, "8453", options.PurchaseCurrencies[0].Networks[0].ChainID)
		require.Empty(t, options.PurchaseCurrencies[1].Networks[0].ChainID)
	}
}

func TestCatalogValidatesLocationBeforeRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("invalid location reached the provider")
	}))
	defer srv.Close()
	c := mustNew(t, WithAPIBaseURL(srv.URL), WithHTTPClient(srv.Client()))
	for _, req := range []GetBuyOptionsRequest{{}, {Country: "USA"}, {Country: " us ", Subdivision: " "}} {
		_, err := c.GetBuyOptions(context.Background(), req)
		require.ErrorContains(t, err, "country and US subdivision are required")
	}
}

func TestCatalogRequiresExplicitLists(t *testing.T) {
	for _, tc := range []struct {
		name, config, options string
		valid                 bool
	}{
		{name: "missing", config: `{}`, options: `{}`},
		{name: "null", config: `{"countries":null}`, options: `{"payment_currencies":null,"purchase_currencies":null}`},
		{name: "missing assets", config: `{}`, options: `{"payment_currencies":[]}`},
		{name: "missing fiat", config: `{}`, options: `{"purchase_currencies":[]}`},
		{name: "explicit empty", config: `{"countries":[]}`, options: `{"payment_currencies":[],"purchase_currencies":[]}`, valid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == buyConfigPath {
					_, _ = fmt.Fprint(w, tc.config)
				} else {
					_, _ = fmt.Fprint(w, tc.options)
				}
			}))
			defer srv.Close()
			c := mustNew(t, WithAPIBaseURL(srv.URL), WithHTTPClient(srv.Client()))
			config, configErr := c.GetBuyConfig(context.Background())
			options, optionsErr := c.GetBuyOptions(context.Background(), GetBuyOptionsRequest{Country: "DE"})
			if tc.valid {
				require.NoError(t, configErr)
				require.NoError(t, optionsErr)
				require.NotNil(t, config.Countries)
				require.NotNil(t, options.PaymentCurrencies)
				require.NotNil(t, options.PurchaseCurrencies)
			} else {
				require.Error(t, configErr)
				require.Error(t, optionsErr)
			}
		})
	}
}

func TestCatalogPreservesProviderErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprint(w, `{"error":"unauthorized"}`)
	}))
	defer srv.Close()
	c := mustNew(t, WithAPIBaseURL(srv.URL), WithHTTPClient(srv.Client()))
	_, err := c.GetBuyConfig(context.Background())
	require.ErrorIs(t, err, ErrUnauthorized)
	_, err = c.GetBuyOptions(context.Background(), GetBuyOptionsRequest{Country: "DE"})
	require.ErrorIs(t, err, ErrUnauthorized)
}
