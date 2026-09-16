package coinbase

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustNew(t *testing.T, opts ...Option) Client {
	t.Helper()
	_, secret := newEd25519Secret(t)
	baseOpts := []Option{
		WithAPIKeyID("key-id"),
		WithAPIKeySecret(secret),
		WithProjectID("project-id"),
	}
	baseOpts = append(baseOpts, opts...)
	c, err := New(baseOpts...)
	require.NoError(t, err)
	return c
}

func readJSONObject(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var body map[string]any
	require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
	return body
}

func TestNewValidation(t *testing.T) {
	t.Parallel()

	_, secret := newEd25519Secret(t)

	t.Run("requires api key id", func(t *testing.T) {
		t.Parallel()
		_, err := New(WithAPIKeySecret(secret))
		require.ErrorContains(t, err, "api key id is required")
	})

	t.Run("requires a usable secret", func(t *testing.T) {
		t.Parallel()
		_, err := New(WithAPIKeyID("key-id"), WithAPIKeySecret("nonsense"))
		require.ErrorIs(t, err, ErrInvalidAPIKeySecret)
	})

	t.Run("rejects a base url with no host", func(t *testing.T) {
		t.Parallel()
		_, err := New(WithAPIKeyID("key-id"), WithAPIKeySecret(secret), WithAPIBaseURL("/relative"))
		require.ErrorContains(t, err, "api base url")
	})
}

func TestCreateSessionToken(t *testing.T) {
	t.Parallel()

	var (
		gotAuthorization string
		gotPath          string
		gotBody          map[string]any
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		gotAuthorization = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		gotBody = readJSONObject(t, r)
		_, _ = w.Write([]byte(`{"token":"session-token","channelId":"channel-1"}`))
	}))
	defer srv.Close()

	c := mustNew(t, WithAPIBaseURL(srv.URL), WithHTTPClient(srv.Client()))
	got, err := c.CreateSessionToken(context.Background(), CreateSessionTokenRequest{
		Addresses: []SessionTokenAddress{{Address: "0xwallet", Blockchains: []string{"base"}}},
		Assets:    []string{"USDC"},
	})
	require.NoError(t, err)

	assert.Equal(t, "session-token", got.Token)
	assert.Equal(t, "channel-1", got.ChannelID)
	assert.Equal(t, createSessionTokenPath, gotPath)
	assert.Equal(t, []any{"USDC"}, gotBody["assets"])
	assert.Equal(t, []any{map[string]any{
		"address":     "0xwallet",
		"blockchains": []any{"base"},
	}}, gotBody["addresses"])

	// The bearer token must be scoped to the exact method+host+path it was
	// sent on, or CDP rejects it.
	require.True(t, strings.HasPrefix(gotAuthorization, "Bearer "))
	_, _, claims, _ := splitJWT(t, strings.TrimPrefix(gotAuthorization, "Bearer "))
	host, err := hostOf(srv.URL)
	require.NoError(t, err)
	assert.Equal(t, []any{"POST " + host + createSessionTokenPath}, claims["uris"])
}

func TestCreateSessionTokenAcceptsSnakeCaseChannelID(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"token":"session-token","channel_id":"channel-2"}`))
	}))
	defer srv.Close()

	c := mustNew(t, WithAPIBaseURL(srv.URL), WithHTTPClient(srv.Client()))
	got, err := c.CreateSessionToken(context.Background(), CreateSessionTokenRequest{
		Addresses: []SessionTokenAddress{{Address: "0xwallet", Blockchains: []string{"base"}}},
	})
	require.NoError(t, err)
	assert.Equal(t, "channel-2", got.ChannelID)
}

func TestCreateSessionTokenValidation(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("no request should reach the server")
	}))
	defer srv.Close()

	c := mustNew(t, WithAPIBaseURL(srv.URL), WithHTTPClient(srv.Client()))

	tests := map[string]struct {
		req     CreateSessionTokenRequest
		wantErr string
	}{
		"no addresses": {
			req:     CreateSessionTokenRequest{},
			wantErr: "at least one address is required",
		},
		"blank address": {
			req:     CreateSessionTokenRequest{Addresses: []SessionTokenAddress{{Address: "  ", Blockchains: []string{"base"}}}},
			wantErr: "address 0 is empty",
		},
		"no blockchains": {
			req:     CreateSessionTokenRequest{Addresses: []SessionTokenAddress{{Address: "0xwallet"}}},
			wantErr: "address 0 has no blockchains",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := c.CreateSessionToken(context.Background(), tc.req)
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

func TestCreateSessionTokenRejectsEmptyToken(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"token":""}`))
	}))
	defer srv.Close()

	c := mustNew(t, WithAPIBaseURL(srv.URL), WithHTTPClient(srv.Client()))
	_, err := c.CreateSessionToken(context.Background(), CreateSessionTokenRequest{
		Addresses: []SessionTokenAddress{{Address: "0xwallet", Blockchains: []string{"base"}}},
	})
	require.ErrorContains(t, err, "missing token")
}

func TestGetBuyTransactions(t *testing.T) {
	t.Parallel()

	var (
		gotPath  string
		gotQuery url.Values
		gotURIs  any
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		gotPath = r.URL.Path
		gotQuery = r.URL.Query()
		_, _, claims, _ := splitJWT(t, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		gotURIs = claims["uris"]
		_, _ = w.Write([]byte(`{
			"transactions":[{
				"status":"ONRAMP_TRANSACTION_STATUS_SUCCESS",
				"transaction_id":"tx-1",
				"tx_hash":"0xhash",
				"purchase_amount":"99.5",
				"purchase_currency":"USDC",
				"purchase_network":"base",
				"payment_total":100,
				"fiat_currency":"USD",
				"wallet_address":"0xwallet"
			}],
			"total_count":"1",
			"next_page_key":"page-2"
		}`))
	}))
	defer srv.Close()

	c := mustNew(t, WithAPIBaseURL(srv.URL), WithHTTPClient(srv.Client()))
	got, err := c.GetBuyTransactions(context.Background(), GetBuyTransactionsRequest{
		PartnerUserRef: "wallet01_abc_1234",
		PageSize:       5,
		PageKey:        "page-1",
	})
	require.NoError(t, err)

	assert.Equal(t, "/onramp/v1/buy/user/wallet01_abc_1234/transactions", gotPath)
	assert.Equal(t, "5", gotQuery.Get("page_size"))
	assert.Equal(t, "page-1", gotQuery.Get("page_key"))

	// The JWT is scoped to the path only — a query string in the `uris` claim
	// would make every distinct page fail verification.
	host, err := hostOf(srv.URL)
	require.NoError(t, err)
	assert.Equal(t, []any{"GET " + host + "/onramp/v1/buy/user/wallet01_abc_1234/transactions"}, gotURIs)

	require.Len(t, got.Transactions, 1)
	tx := got.Transactions[0]
	assert.Equal(t, TransactionStatusSuccess, tx.Status)
	assert.Equal(t, "tx-1", tx.TransactionID)
	assert.Equal(t, "0xhash", tx.TxHash)
	assert.Equal(t, "99.5", tx.PurchaseAmount)
	// A bare JSON number must survive as text, not pick up float formatting.
	assert.Equal(t, "100", tx.PaymentTotal)
	assert.Equal(t, json.Number("1"), got.TotalCount)
	assert.Equal(t, "page-2", got.NextPageKey)
}

func TestGetBuyTransactionsDefaultsAndLimits(t *testing.T) {
	t.Parallel()

	var gotPageSize string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPageSize = r.URL.Query().Get("page_size")
		_, _ = w.Write([]byte(`{"transactions":[],"total_count":0,"next_page_key":null}`))
	}))
	defer srv.Close()

	c := mustNew(t, WithAPIBaseURL(srv.URL), WithHTTPClient(srv.Client()))

	got, err := c.GetBuyTransactions(context.Background(), GetBuyTransactionsRequest{PartnerUserRef: "ref"})
	require.NoError(t, err)
	assert.Equal(t, "1", gotPageSize)
	assert.Empty(t, got.Transactions)
	assert.Equal(t, json.Number("0"), got.TotalCount)

	_, err = c.GetBuyTransactions(context.Background(), GetBuyTransactionsRequest{PartnerUserRef: "ref", PageSize: 51})
	require.ErrorContains(t, err, "exceeds max 50")

	_, err = c.GetBuyTransactions(context.Background(), GetBuyTransactionsRequest{PartnerUserRef: "  "})
	require.ErrorContains(t, err, "partner user ref is required")
}

func TestErrorMapping(t *testing.T) {
	t.Parallel()

	tests := map[int]error{
		http.StatusBadRequest:          ErrBadRequest,
		http.StatusUnauthorized:        ErrUnauthorized,
		http.StatusForbidden:           ErrUnauthorized,
		http.StatusNotFound:            ErrNotFound,
		http.StatusTooManyRequests:     ErrRateLimited,
		http.StatusInternalServerError: ErrUpstream,
	}

	for status, want := range tests {
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":"nope"}`))
			}))
			defer srv.Close()

			c := mustNew(t, WithAPIBaseURL(srv.URL), WithHTTPClient(srv.Client()))
			_, err := c.GetBuyTransactions(context.Background(), GetBuyTransactionsRequest{PartnerUserRef: "ref"})
			require.ErrorIs(t, err, want)

			var apiErr *APIError
			require.ErrorAs(t, err, &apiErr)
			assert.Equal(t, status, apiErr.StatusCode)
			assert.Equal(t, `{"error":"nope"}`, apiErr.Body)
		})
	}
}

func TestBuildBuyURL(t *testing.T) {
	t.Parallel()

	c := mustNew(t)
	got, err := c.BuildBuyURL(BuildBuyURLRequest{
		SessionToken:         "session-token",
		PartnerUserRef:       "wallet01_abc_1234",
		PresetFiatAmount:     json.Number("100.00"),
		DefaultPaymentMethod: "APPLE_PAY",
		RedirectURL:          "superform://onramp-callback",
		Addresses:            map[string][]string{"0xwallet": {"base"}},
		Assets:               []string{"USDC"},
	})
	require.NoError(t, err)

	parsed, err := url.Parse(got)
	require.NoError(t, err)
	assert.Equal(t, "pay.coinbase.com", parsed.Host)
	assert.Equal(t, "/buy", parsed.Path)

	query := parsed.Query()
	assert.Equal(t, "project-id", query.Get("appId"))
	assert.Equal(t, "session-token", query.Get("sessionToken"))
	assert.Equal(t, "wallet01_abc_1234", query.Get("partnerUserRef"))
	// The amount is passed through verbatim — trailing zeros intact.
	assert.Equal(t, "100.00", query.Get("presetFiatAmount"))
	assert.Equal(t, "USD", query.Get("fiatCurrency"))
	assert.Equal(t, "APPLE_PAY", query.Get("defaultPaymentMethod"))
	assert.Equal(t, "superform://onramp-callback", query.Get("redirectUrl"))
	assert.JSONEq(t, `{"0xwallet":["base"]}`, query.Get("addresses"))
	assert.JSONEq(t, `["USDC"]`, query.Get("assets"))
}

func TestBuildBuyURLSandbox(t *testing.T) {
	t.Parallel()

	_, secret := newEd25519Secret(t)
	c, err := New(WithAPIKeyID("key-id"), WithAPIKeySecret(secret), WithSandbox(true))
	require.NoError(t, err)

	got, err := c.BuildBuyURL(BuildBuyURLRequest{
		SessionToken:         "session-token",
		PartnerUserRef:       "ref",
		PresetFiatAmount:     json.Number("50"),
		FiatCurrency:         "EUR",
		DefaultPaymentMethod: "APPLE_PAY",
		RedirectURL:          "superform://onramp-callback",
		Addresses:            map[string][]string{"0xwallet": {"base"}},
		Assets:               []string{"USDC"},
	})
	require.NoError(t, err)

	parsed, err := url.Parse(got)
	require.NoError(t, err)
	assert.Equal(t, "pay-sandbox.coinbase.com", parsed.Host)
	assert.Equal(t, "/buy/select-asset", parsed.Path)

	query := parsed.Query()
	assert.Equal(t, "session-token", query.Get("sessionToken"))
	assert.Equal(t, "EUR", query.Get("fiatCurrency"))
	assert.Equal(t, "50", query.Get("presetFiatAmount"))
	// Sandbox documents none of these, so they are left off rather than sent
	// and silently ignored.
	assert.Empty(t, query.Get("appId"))
	assert.Empty(t, query.Get("addresses"))
	assert.Empty(t, query.Get("assets"))
	assert.Empty(t, query.Get("defaultPaymentMethod"))
}

func TestBuildBuyURLValidation(t *testing.T) {
	t.Parallel()

	t.Run("requires a session token", func(t *testing.T) {
		t.Parallel()
		_, err := mustNew(t).BuildBuyURL(BuildBuyURLRequest{})
		require.ErrorContains(t, err, "session token is required")
	})

	t.Run("requires a project id outside sandbox", func(t *testing.T) {
		t.Parallel()
		_, secret := newEd25519Secret(t)
		c, err := New(WithAPIKeyID("key-id"), WithAPIKeySecret(secret))
		require.NoError(t, err)

		_, err = c.BuildBuyURL(BuildBuyURLRequest{SessionToken: "session-token"})
		require.ErrorContains(t, err, "project id is required outside sandbox")
	})
}

func TestIsTerminalTransactionStatus(t *testing.T) {
	t.Parallel()

	assert.True(t, IsTerminalTransactionStatus(TransactionStatusSuccess))
	assert.True(t, IsTerminalTransactionStatus(TransactionStatusFailed))
	assert.False(t, IsTerminalTransactionStatus(TransactionStatusCreated))
	assert.False(t, IsTerminalTransactionStatus(TransactionStatusInProgress))
	assert.False(t, IsTerminalTransactionStatus("ONRAMP_TRANSACTION_STATUS_SOMETHING_NEW"))
}

func TestNewPartnerUserRef(t *testing.T) {
	t.Parallel()

	got, err := NewPartnerUserRef("0x1234567890abcdefABCDEF1234567890abcdefAB")
	require.NoError(t, err)

	parts := strings.Split(got, "_")
	require.Len(t, parts, 3)
	assert.Equal(t, "abcdefAB", parts[0])
	assert.NotEmpty(t, parts[1])
	assert.Len(t, parts[2], 4)

	other, err := NewPartnerUserRef("0x1234567890abcdefABCDEF1234567890abcdefAB")
	require.NoError(t, err)
	assert.NotEqual(t, got, other, "two refs minted back to back must not collide")

	_, err = NewPartnerUserRef("  ")
	require.ErrorContains(t, err, "wallet address is required")
}

func TestClose(t *testing.T) {
	t.Parallel()
	require.NoError(t, mustNew(t).Close())
}
