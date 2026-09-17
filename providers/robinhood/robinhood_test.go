package robinhood

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustNew(t *testing.T, opts ...Option) Client {
	t.Helper()
	baseOpts := []Option{
		WithApplicationID("application-id"),
		WithAPIKey("api-key"),
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

	_, err := New(WithAPIKey("api-key"))
	require.ErrorContains(t, err, "application id is required")

	_, err = New(WithApplicationID("application-id"))
	require.ErrorContains(t, err, "api key is required")
}

func TestCreateConnectID(t *testing.T) {
	t.Parallel()

	var (
		gotPath          string
		gotApplicationID string
		gotAPIKey        string
		gotBody          map[string]any
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		gotPath = r.URL.Path
		gotApplicationID = r.Header.Get(applicationIDHeader)
		gotAPIKey = r.Header.Get(apiKeyHeader)
		gotBody = readJSONObject(t, r)
		_, _ = w.Write([]byte(`{"connectId":"connect-1"}`))
	}))
	defer srv.Close()

	c := mustNew(t, WithAPIBaseURL(srv.URL), WithHTTPClient(srv.Client()))
	got, err := c.CreateConnectID(context.Background(), CreateConnectIDRequest{
		WalletAddress: "0xwallet",
		ReferenceID:   "wallet01_abc_1234",
	})
	require.NoError(t, err)

	assert.Equal(t, "connect-1", got.ConnectID)
	assert.Equal(t, createConnectIDPath, gotPath)
	assert.Equal(t, "application-id", gotApplicationID)
	assert.Equal(t, "api-key", gotAPIKey)
	// Snake_case on the address, camelCase on the reference.
	assert.Equal(t, "0xwallet", gotBody["withdrawal_address"])
	assert.Equal(t, "wallet01_abc_1234", gotBody["referenceId"])
}

func TestCreateConnectIDValidation(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("no request should reach the server")
	}))
	defer srv.Close()

	c := mustNew(t, WithAPIBaseURL(srv.URL), WithHTTPClient(srv.Client()))

	_, err := c.CreateConnectID(context.Background(), CreateConnectIDRequest{ReferenceID: "ref"})
	require.ErrorContains(t, err, "wallet address is required")

	_, err = c.CreateConnectID(context.Background(), CreateConnectIDRequest{WalletAddress: "0xwallet"})
	require.ErrorContains(t, err, "reference id is required")
}

func TestCreateConnectIDRejectsEmptyID(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"connectId":""}`))
	}))
	defer srv.Close()

	c := mustNew(t, WithAPIBaseURL(srv.URL), WithHTTPClient(srv.Client()))
	_, err := c.CreateConnectID(context.Background(), CreateConnectIDRequest{
		WalletAddress: "0xwallet",
		ReferenceID:   "ref",
	})
	require.ErrorContains(t, err, "missing connectId")
}

func TestGetOrder(t *testing.T) {
	t.Parallel()

	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{
			"connectId":"connect-1",
			"status":"ORDER_STATUS_SUCCEEDED",
			"assetCode":"USDC",
			"networkCode":"ETHEREUM",
			"cryptoAmount":"99.5",
			"blockchainTransactionId":"0xhash",
			"destinationAddress":"0xwallet",
			"referenceID":"wallet01_abc_1234"
		}`))
	}))
	defer srv.Close()

	c := mustNew(t, WithAPIBaseURL(srv.URL), WithHTTPClient(srv.Client()))
	got, err := c.GetOrder(context.Background(), "connect-1")
	require.NoError(t, err)

	assert.Equal(t, "/catpay/v1/external/order/connect-1", gotPath)
	assert.Equal(t, Order{
		ID:                      "connect-1",
		Status:                  OrderStatusSucceeded,
		AssetCode:               "USDC",
		NetworkCode:             "ETHEREUM",
		CryptoAmount:            "99.5",
		BlockchainTransactionID: "0xhash",
		DestinationAddress:      "0xwallet",
		ReferenceID:             "wallet01_abc_1234",
	}, *got)
}

func TestGetOrderNumericCryptoAmount(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"connectId":"connect-1","status":"ORDER_STATUS_IN_PROGRESS","cryptoAmount":100}`))
	}))
	defer srv.Close()

	c := mustNew(t, WithAPIBaseURL(srv.URL), WithHTTPClient(srv.Client()))
	got, err := c.GetOrder(context.Background(), "connect-1")
	require.NoError(t, err)
	assert.Equal(t, "100", got.CryptoAmount)
}

func TestGetOrderReportsWhatTheProviderSaid(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ORDER_STATUS_SOMETHING_NEW"}`))
	}))
	defer srv.Close()

	c := mustNew(t, WithAPIBaseURL(srv.URL), WithHTTPClient(srv.Client()))
	got, err := c.GetOrder(context.Background(), "connect-1")
	require.NoError(t, err)

	assert.Equal(t, "ORDER_STATUS_SOMETHING_NEW", got.Status)
	assert.Empty(t, got.ID)
}

func TestGetOrderRequiresConnectID(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("no request should reach the server")
	}))
	defer srv.Close()

	c := mustNew(t, WithAPIBaseURL(srv.URL), WithHTTPClient(srv.Client()))
	_, err := c.GetOrder(context.Background(), "  ")
	require.ErrorContains(t, err, "connect id is required")
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
			_, err := c.GetOrder(context.Background(), "connect-1")
			require.ErrorIs(t, err, want)

			var apiErr *APIError
			require.ErrorAs(t, err, &apiErr)
			assert.Equal(t, status, apiErr.StatusCode)
			assert.Equal(t, `{"error":"nope"}`, apiErr.Body)
		})
	}
}

func TestBuildConnectURL(t *testing.T) {
	t.Parallel()

	got, err := mustNew(t).BuildConnectURL(BuildConnectURLRequest{
		ConnectID:         "connect-1",
		WalletAddress:     "0xwallet",
		RedirectURL:       "superform://onramp/robinhood/callback",
		SupportedNetworks: []string{"ETHEREUM", "BASE"},
		SupportedAssets:   []string{"USDC", "ETH"},
		FiatAmount:        json.Number("100.00"),
		FiatCode:          "USD",
		AssetCode:         "USDC",
		LockAmount:        true,
	})
	require.NoError(t, err)

	parsed, err := url.Parse(got)
	require.NoError(t, err)
	assert.Equal(t, "applink.robinhood.com", parsed.Host)
	assert.Equal(t, "/u/connect", parsed.Path)

	query := parsed.Query()
	assert.Equal(t, "application-id", query.Get("applicationId"))
	assert.Equal(t, "connect-1", query.Get("connectId"))
	assert.Equal(t, "0xwallet", query.Get("walletAddress"))
	assert.Equal(t, "ETHEREUM,BASE", query.Get("supportedNetworks"))
	assert.Equal(t, "USDC,ETH", query.Get("supportedAssets"))
	assert.Equal(t, "superform://onramp/robinhood/callback", query.Get("redirectUrl"))
	assert.Equal(t, "100.00", query.Get("fiatAmount"))
	assert.Equal(t, "USD", query.Get("fiatCode"))
	assert.Equal(t, "USDC", query.Get("assetCode"))
	assert.Equal(t, "true", query.Get("lockAmount"))
}

func TestBuildConnectURLOmitsUnsetFields(t *testing.T) {
	t.Parallel()

	got, err := mustNew(t).BuildConnectURL(BuildConnectURLRequest{
		ConnectID:     "connect-1",
		WalletAddress: "0xwallet",
	})
	require.NoError(t, err)

	parsed, err := url.Parse(got)
	require.NoError(t, err)
	assert.Equal(t, url.Values{
		"applicationId": {"application-id"},
		"connectId":     {"connect-1"},
		"walletAddress": {"0xwallet"},
	}, parsed.Query())
}

func TestBuildConnectURLLocksAmountIndependently(t *testing.T) {
	t.Parallel()

	got, err := mustNew(t).BuildConnectURL(BuildConnectURLRequest{
		ConnectID:     "connect-1",
		WalletAddress: "0xwallet",
		FiatAmount:    json.Number("100.00"),
	})
	require.NoError(t, err)

	parsed, err := url.Parse(got)
	require.NoError(t, err)
	query := parsed.Query()
	assert.Equal(t, "100.00", query.Get("fiatAmount"))
	assert.False(t, query.Has("lockAmount"))
}

func TestBuildConnectURLValidation(t *testing.T) {
	t.Parallel()

	c := mustNew(t)

	_, err := c.BuildConnectURL(BuildConnectURLRequest{WalletAddress: "0xwallet"})
	require.ErrorContains(t, err, "connect id is required")

	_, err = c.BuildConnectURL(BuildConnectURLRequest{ConnectID: "connect-1"})
	require.ErrorContains(t, err, "wallet address is required")
}

func TestClose(t *testing.T) {
	t.Parallel()
	require.NoError(t, mustNew(t).Close())
}
