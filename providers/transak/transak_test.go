package transak

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustNew(t *testing.T, opts ...Option) Client {
	t.Helper()
	baseOpts := []Option{
		WithAPIKey("api-key"),
		WithAPISecret("api-secret"),
	}
	baseOpts = append(baseOpts, opts...)
	c, err := New(baseOpts...)
	require.NoError(t, err)
	return c
}

func TestRefreshToken(t *testing.T) {
	t.Parallel()

	expiresAt := time.Now().UTC().Add(7 * 24 * time.Hour).Unix()
	var gotSecret string
	var gotAPIKeyHeader string
	var gotAPIKeyBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, refreshTokenPath, r.URL.Path)
		gotAPIKeyHeader = r.Header.Get(apiKeyHeader)
		gotSecret = r.Header.Get(apiSecretHeader)
		gotAPIKeyBody = readJSONField(t, r, "apiKey")
		_, _ = fmt.Fprintf(w, `{"data":{"accessToken":"partner-token","expiresAt":%d}}`, expiresAt)
	}))
	defer srv.Close()

	c := mustNew(t, WithAPIBaseURL(srv.URL), WithHTTPClient(srv.Client()))
	got, err := c.RefreshToken(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "api-key", gotAPIKeyHeader)
	assert.Equal(t, "api-secret", gotSecret)
	assert.Equal(t, "api-key", gotAPIKeyBody)
	assert.Equal(t, "partner-token", got.Token)
	assert.Equal(t, time.Unix(expiresAt, 0).UTC(), got.ExpiresAt)
}

func TestCreateWidgetSession(t *testing.T) {
	t.Parallel()

	var gotAPIKeyHeader string
	var gotAccessToken string
	var gotUserIP string
	var gotWidgetParams map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, createWidgetSessionPath, r.URL.Path)
		gotAPIKeyHeader = r.Header.Get(apiKeyHeader)
		gotAccessToken = r.Header.Get(accessTokenHeader)
		gotUserIP = r.Header.Get(userIPHeader)
		body := readJSONObject(t, r)
		var ok bool
		gotWidgetParams, ok = body["widgetParams"].(map[string]any)
		require.True(t, ok)
		_, _ = w.Write([]byte(`{"data":{"widgetUrl":"https://global.transak.com?apiKey=api-key&sessionId=session"}}`))
	}))
	defer srv.Close()

	c := mustNew(t, WithGatewayBaseURL(srv.URL), WithHTTPClient(srv.Client()))
	got, err := c.CreateWidgetSession(context.Background(), "partner-token", CreateWidgetSessionRequest{
		UserIP: "203.0.113.10",
		WidgetParams: WidgetParams{
			ReferrerDomain:           "com.superform.app",
			PartnerOrderID:           "11111111-1111-4111-8111-111111111111",
			PartnerCustomerID:        "dynamic-user",
			WalletAddress:            "0xwallet",
			DisableWalletAddressForm: true,
			ProductsAvailed:          "BUY",
			Network:                  "base",
			DefaultCryptoCurrency:    "USDC",
			PaymentMethod:            "credit_debit_card",
			DefaultFiatCurrency:      "USD",
			CountryCode:              "US",
			DefaultFiatAmount:        json.Number("100.00"),
		},
	})
	require.NoError(t, err)

	assert.Equal(t, "api-key", gotAPIKeyHeader)
	assert.Equal(t, "partner-token", gotAccessToken)
	assert.Equal(t, "203.0.113.10", gotUserIP)
	assert.Equal(t, "api-key", gotWidgetParams["apiKey"])
	assert.Equal(t, "BUY", gotWidgetParams["productsAvailed"])
	assert.Equal(t, "USDC", gotWidgetParams["defaultCryptoCurrency"])
	assert.Equal(t, "US", gotWidgetParams["countryCode"])
	amount, ok := gotWidgetParams["defaultFiatAmount"].(float64)
	require.True(t, ok)
	assert.Equal(t, 100.00, amount)
	assert.Equal(t, "https://global.transak.com?apiKey=api-key&sessionId=session", got.WidgetURL)
}

func TestCreateWidgetSessionOmitsEmptyDefaultFiatAmount(t *testing.T) {
	t.Parallel()

	var gotWidgetParams map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		body := readJSONObject(t, r)
		var ok bool
		gotWidgetParams, ok = body["widgetParams"].(map[string]any)
		require.True(t, ok)
		_, _ = w.Write([]byte(`{"data":{"widgetUrl":"https://global.transak.com?apiKey=api-key&sessionId=session"}}`))
	}))
	defer srv.Close()

	c := mustNew(t, WithGatewayBaseURL(srv.URL), WithHTTPClient(srv.Client()))
	_, err := c.CreateWidgetSession(context.Background(), "partner-token", CreateWidgetSessionRequest{
		WidgetParams: WidgetParams{ProductsAvailed: "BUY"},
	})
	require.NoError(t, err)

	assert.NotContains(t, gotWidgetParams, "defaultFiatAmount")
}

func TestGetOrdersUsesPartnerOrderIDFilter(t *testing.T) {
	t.Parallel()

	var gotAPIKeyHeader string
	var gotAccessToken string
	var gotPartnerOrderID string
	var gotStatusFilter string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, getOrdersPath, r.URL.Path)
		gotAPIKeyHeader = r.Header.Get(apiKeyHeader)
		gotAccessToken = r.Header.Get(accessTokenHeader)
		gotPartnerOrderID = r.URL.Query().Get("filter[partnerOrderId]")
		gotStatusFilter = r.URL.Query().Get("filter[status]")
		_, _ = w.Write([]byte(`{"data":[{"id":"transak-id","status":"COMPLETED","partnerOrderId":"order-id","partnerCustomerId":"dynamic-user","cryptoCurrency":"USDC","fiatCurrency":"USD","fiatAmount":"100","cryptoAmount":"99.5"}]}`))
	}))
	defer srv.Close()

	c := mustNew(t, WithAPIBaseURL(srv.URL), WithHTTPClient(srv.Client()))
	got, err := c.GetOrders(context.Background(), "partner-token", GetOrdersRequest{PartnerOrderID: "order-id"})
	require.NoError(t, err)
	require.Len(t, got.Orders, 1)

	assert.Equal(t, "api-key", gotAPIKeyHeader)
	assert.Equal(t, "partner-token", gotAccessToken)
	assert.Equal(t, "order-id", gotPartnerOrderID)
	assert.Empty(t, gotStatusFilter)
	assert.Equal(t, "transak-id", got.Orders[0].ID)
	assert.Equal(t, "COMPLETED", got.Orders[0].Status)
	assert.Equal(t, "dynamic-user", got.Orders[0].PartnerCustomerID)
	assert.Equal(t, "100", got.Orders[0].FiatAmount)
	assert.Equal(t, "99.5", got.Orders[0].CryptoAmount)
}

func TestGetOrdersDecodesDocumentedOrderShape(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"_id":"7d8e0655-728e-458c-9c15-b3e9f2aa66ed","status":"COMPLETED","partnerOrderId":"order-id","partnerCustomerId":"dynamic-user","cryptoCurrency":"ETH","fiatCurrency":"GBP","fiatAmount":50,"cryptoAmount":0.01596979}]}`))
	}))
	defer srv.Close()

	c := mustNew(t, WithAPIBaseURL(srv.URL), WithHTTPClient(srv.Client()))
	got, err := c.GetOrders(context.Background(), "partner-token", GetOrdersRequest{PartnerOrderID: "order-id"})
	require.NoError(t, err)
	require.Len(t, got.Orders, 1)

	assert.Equal(t, "7d8e0655-728e-458c-9c15-b3e9f2aa66ed", got.Orders[0].ID)
	assert.Equal(t, "50", got.Orders[0].FiatAmount)
	assert.Equal(t, "0.01596979", got.Orders[0].CryptoAmount)
}

func TestGetOrdersSupportsObjectEnvelope(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"orders":[{"id":"order-a","partnerOrderId":"order-id"}]}}`))
	}))
	defer srv.Close()

	c := mustNew(t, WithAPIBaseURL(srv.URL), WithHTTPClient(srv.Client()))
	got, err := c.GetOrders(context.Background(), "partner-token", GetOrdersRequest{PartnerOrderID: "order-id"})
	require.NoError(t, err)
	require.Len(t, got.Orders, 1)
	assert.Equal(t, "order-a", got.Orders[0].ID)
}

func TestUnauthorizedIsTyped(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid token"}`))
	}))
	defer srv.Close()

	c := mustNew(t, WithGatewayBaseURL(srv.URL), WithHTTPClient(srv.Client()))
	_, err := c.CreateWidgetSession(context.Background(), "bad-token", CreateWidgetSessionRequest{})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrUnauthorized))
	var apiErr *APIError
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, http.StatusUnauthorized, apiErr.StatusCode)
}

func TestNewRequiresCredentials(t *testing.T) {
	t.Parallel()

	_, err := New(WithAPISecret("secret"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "api key")

	_, err = New(WithAPIKey("key"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "api secret")
}

// TestNewClientSurfacesTypedError exercises the real client built by New
// (no WithHTTPClient), so the typed-error path is validated end-to-end rather
// than only against an injected client.
func TestNewClientSurfacesTypedError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid api secret"}`))
	}))
	defer srv.Close()

	c := mustNew(t, WithAPIBaseURL(srv.URL))
	_, err := c.RefreshToken(context.Background())
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrUnauthorized))
	var apiErr *APIError
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, http.StatusUnauthorized, apiErr.StatusCode)
}

func TestGetOrdersEncodesLimitAndSkip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		req       GetOrdersRequest
		wantLimit string
		wantSkip  string
	}{
		{
			name:      "defaults page size and omits skip",
			req:       GetOrdersRequest{PartnerOrderID: "order-id"},
			wantLimit: fmt.Sprintf("%d", defaultGetOrdersPageSize),
			wantSkip:  "",
		},
		{
			name:      "passes explicit limit and skip",
			req:       GetOrdersRequest{PartnerOrderID: "order-id", Limit: 5, Skip: 20},
			wantLimit: "5",
			wantSkip:  "20",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var gotLimit, gotSkip string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotLimit = r.URL.Query().Get("limit")
				gotSkip = r.URL.Query().Get("skip")
				_, _ = w.Write([]byte(`{"data":[]}`))
			}))
			defer srv.Close()

			c := mustNew(t, WithAPIBaseURL(srv.URL), WithHTTPClient(srv.Client()))
			_, err := c.GetOrders(context.Background(), "partner-token", tt.req)
			require.NoError(t, err)
			assert.Equal(t, tt.wantLimit, gotLimit)
			assert.Equal(t, tt.wantSkip, gotSkip)
		})
	}
}

func TestRefreshTokenParsesRFC3339Expiry(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"accessToken":"partner-token","expiresAt":"2027-01-02T15:04:05Z"}}`))
	}))
	defer srv.Close()

	c := mustNew(t, WithAPIBaseURL(srv.URL), WithHTTPClient(srv.Client()))
	got, err := c.RefreshToken(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "partner-token", got.Token)
	assert.Equal(t, time.Date(2027, 1, 2, 15, 4, 5, 0, time.UTC), got.ExpiresAt)
}

func TestRequiredFieldGuards(t *testing.T) {
	t.Parallel()

	c := mustNew(t)

	_, err := c.GetOrders(context.Background(), "  ", GetOrdersRequest{PartnerOrderID: "order-id"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "access token")

	_, err = c.GetOrders(context.Background(), "partner-token", GetOrdersRequest{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "partner order id")

	_, err = c.CreateWidgetSession(context.Background(), " ", CreateWidgetSessionRequest{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "access token")
}

func readJSONField(t *testing.T, r *http.Request, field string) string {
	t.Helper()
	body := readJSONObject(t, r)
	value, _ := body[field].(string)
	return value
}

func readJSONObject(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	defer func() { _ = r.Body.Close() }()
	var body map[string]any
	require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
	return body
}
