package polymarket

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"

	protocol "github.com/superform-xyz/superform-go-utils/utils/polymarket"
)

const (
	testMaker   = "0x1111111111111111111111111111111111111111"
	testOrderID = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testMarket  = "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestNewRequiresHTTPSOriginAndDefaultsSubmissionOff(t *testing.T) {
	provider, err := New()
	require.NoError(t, err)
	require.NotNil(t, provider)
	require.NoError(t, provider.Close())

	for _, rawURL := range []string{
		"http://clob.example",
		"https://user@clob.example",
		"https://clob.example/base",
		"https://clob.example?tenant=one",
		"https://clob.example#fragment",
		"clob.example",
	} {
		t.Run(rawURL, func(t *testing.T) {
			_, err := New(WithBaseURL(rawURL))
			require.ErrorContains(t, err, "absolute HTTPS origin")
		})
	}
	_, err = New(WithTimeout(0))
	require.ErrorContains(t, err, "timeout must be positive")

	_, err = provider.PostOrder(context.Background(), Credentials{}, SignedOrder{})
	require.ErrorIs(t, err, ErrTradingDisabled)
}

func TestCredentialsAreStrictMakerBoundAndRedacted(t *testing.T) {
	credentials := testCredentials()
	require.NoError(t, credentials.Validate())
	encoded, err := json.Marshal(credentials)
	require.NoError(t, err)
	require.JSONEq(t, `"[redacted polymarket credentials]"`, string(encoded))
	require.NotContains(t, fmt.Sprintf("%v", credentials), credentials.APISecret)
	require.NotContains(t, fmt.Sprintf("%#v", credentials), credentials.Passphrase)

	invalid := []Credentials{
		{},
		{Address: testMaker, APIKey: "key", APISecret: "not base64!", Passphrase: "phrase"},
		{Address: common.Address{}.Hex(), APIKey: "key", APISecret: "c2VjcmV0", Passphrase: "phrase"},
	}
	for _, credential := range invalid {
		require.Error(t, credential.Validate())
	}
}

func TestSetL2HeadersMatchesOfficialGoldenVector(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "https://clob.example/order", bytes.NewBufferString("{\"x\":1}"))
	err := setL2Headers(request, Credentials{
		Address: testMaker, APIKey: "key", APISecret: "c2VjcmV0", Passphrase: "phrase",
	}, "/order", []byte("{\"x\":1}"), time.Unix(1_700_000_000, 0))
	require.NoError(t, err)
	require.Equal(t, "1700000000", request.Header.Get("POLY_TIMESTAMP"))
	require.Equal(t, "Uc3z_vcj4K83dnLn8zBFPPSLPInoPi4jixQmfdQDv8s=", request.Header.Get("POLY_SIGNATURE"))
	require.Equal(t, testMaker, request.Header.Get("POLY_ADDRESS"))
	require.Equal(t, "key", request.Header.Get("POLY_API_KEY"))
	require.Equal(t, "phrase", request.Header.Get("POLY_PASSPHRASE"))
}

func TestPostOrderSendsExactV2BodyOnceAndSignsThoseBytes(t *testing.T) {
	fixedNow := time.Unix(1_700_000_000, 0).UTC()
	credentials := testCredentials()
	attempts := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		attempts++
		require.Equal(t, http.MethodPost, request.Method)
		require.Equal(t, "/order", request.URL.Path)
		payload, err := io.ReadAll(request.Body)
		require.NoError(t, err)
		expectedBody := `{"order":{"salt":1,"maker":"` + testMaker + `","signer":"` + testMaker + `","tokenId":"1","makerAmount":"500000","takerAmount":"1000000","side":"BUY","expiration":"1700000840","signatureType":3,"timestamp":"1700000000000","metadata":"0x` + strings.Repeat("0", 64) + `","builder":"0x` + strings.Repeat("0", 64) + `","signature":"0x12"},"owner":"key","orderType":"GTD","deferExec":false,"postOnly":true}`
		require.Equal(t, expectedBody, string(payload))

		secret, err := base64.RawURLEncoding.DecodeString(credentials.APISecret)
		require.NoError(t, err)
		mac := hmac.New(sha256.New, secret)
		_, err = mac.Write(append([]byte("1700000000POST/order"), payload...))
		require.NoError(t, err)
		require.Equal(t, base64.URLEncoding.EncodeToString(mac.Sum(nil)), request.Header.Get("POLY_SIGNATURE"))
		_, err = io.WriteString(writer, `{"success":true,"errorMsg":"","orderID":"`+testOrderID+`","status":"LIVE","transactionsHashes":[],"tradeIDs":[],"makingAmount":"500000","takingAmount":"1000000"}`)
		require.NoError(t, err)
	}))
	defer server.Close()

	provider, err := New(
		WithBaseURL(server.URL),
		WithHTTPClient(server.Client()),
		WithOrderSubmissionEnabled(true),
		withClock(func() time.Time { return fixedNow }),
	)
	require.NoError(t, err)
	placement, err := provider.PostOrder(context.Background(), credentials, testSignedOrder())
	require.NoError(t, err)
	require.Equal(t, testOrderID, placement.OrderID)
	require.Equal(t, 1, attempts)
}

func TestPublicMarketMethodsUseExactPathsAndValidateBooks(t *testing.T) {
	marketJSON := validMarketJSON()
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/sampling-markets":
			require.Equal(t, "cursor-1", request.URL.Query().Get("next_cursor"))
			_, _ = io.WriteString(writer, `{"data":[`+marketJSON+`],"next_cursor":"cursor-2"}`)
		case "/markets/" + testMarket:
			_, _ = io.WriteString(writer, marketJSON)
		case "/book":
			require.Equal(t, "1", request.URL.Query().Get("token_id"))
			_, _ = io.WriteString(writer, `{"market":"`+testMarket+`","asset_id":"1","timestamp":"1","bids":[],"asks":[],"min_order_size":"1","neg_risk":false,"tick_size":"0.01","last_trade_price":"0.5","hash":"hash"}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	provider := testClient(t, server, false)

	page, err := provider.ListMarkets(context.Background(), "cursor-1")
	require.NoError(t, err)
	require.Equal(t, "cursor-2", page.NextCursor)
	market, err := provider.GetMarket(context.Background(), testMarket, true)
	require.NoError(t, err)
	require.NotNil(t, market.Tokens[0].Book)
}

func TestAuthenticatedReadUpdateListAndCancelMappings(t *testing.T) {
	credentials := testCredentials()
	seen := map[string]int{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		seen[request.Method+" "+request.URL.Path]++
		require.Equal(t, testMaker, request.Header.Get("POLY_ADDRESS"))
		require.NotEmpty(t, request.Header.Get("POLY_SIGNATURE"))
		switch request.Method + " " + request.URL.Path {
		case "GET /data/order/" + testOrderID:
			_, _ = io.WriteString(writer, validOpenOrderJSON())
		case "GET /data/orders":
			require.Equal(t, testMarket, request.URL.Query().Get("market"))
			require.Equal(t, "1", request.URL.Query().Get("asset_id"))
			require.Equal(t, testOrderID, request.URL.Query().Get("id"))
			require.Equal(t, "next", request.URL.Query().Get("next_cursor"))
			_, _ = io.WriteString(writer, `{"data":[`+validOpenOrderJSON()+`],"next_cursor":""}`)
		case "GET /balance-allowance/update":
			require.Equal(t, "CONDITIONAL", request.URL.Query().Get("asset_type"))
			require.Equal(t, "1", request.URL.Query().Get("token_id"))
			require.Equal(t, "3", request.URL.Query().Get("signature_type"))
			writer.WriteHeader(http.StatusOK)
		case "DELETE /order":
			payload, err := io.ReadAll(request.Body)
			require.NoError(t, err)
			require.JSONEq(t, `{"orderID":"`+testOrderID+`"}`, string(payload))
			_, _ = io.WriteString(writer, `{"canceled":["`+testOrderID+`"],"not_canceled":{}}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	provider := testClient(t, server, false)

	_, err := provider.GetOrder(context.Background(), credentials, testOrderID)
	require.NoError(t, err)
	_, err = provider.ListOpenOrders(context.Background(), credentials, OpenOrderFilter{
		ConditionID: testMarket, TokenID: "1", OrderID: testOrderID, NextCursor: "next",
	})
	require.NoError(t, err)
	require.NoError(t, provider.UpdateBalanceAllowance(context.Background(), credentials, BalanceAllowanceUpdate{
		AssetType: BalanceAllowanceConditional, TokenID: "1",
	}))
	_, err = provider.CancelOrder(context.Background(), credentials, testOrderID)
	require.NoError(t, err)
	require.Equal(t, 1, seen["GET /data/order/"+testOrderID])
	require.Equal(t, 1, seen["GET /data/orders"])
	require.Equal(t, 1, seen["GET /balance-allowance/update"])
	require.Equal(t, 1, seen["DELETE /order"])
}

func TestOnlyGETRetriesTransientResponses(t *testing.T) {
	getAttempts := 0
	deleteAttempts := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet:
			getAttempts++
			if getAttempts == 1 {
				http.Error(writer, "temporary", http.StatusServiceUnavailable)
				return
			}
			_, _ = io.WriteString(writer, `{"ok":true}`)
		case http.MethodDelete:
			deleteAttempts++
			http.Error(writer, "temporary", http.StatusServiceUnavailable)
		}
	}))
	defer server.Close()
	provider := testClient(t, server, false).(*client)
	var result map[string]bool
	require.NoError(t, provider.do(context.Background(), http.MethodGet, "/retry", nil, nil, nil, 1024, &result))
	require.Equal(t, 2, getAttempts)
	require.True(t, result["ok"])

	_, err := provider.CancelOrder(context.Background(), testCredentials(), testOrderID)
	var providerError *HTTPError
	require.ErrorAs(t, err, &providerError)
	require.Equal(t, 1, deleteAttempts)
}

func TestPOSTNeverRetriesAmbiguousOrMalformedSuccess(t *testing.T) {
	for _, test := range []struct {
		name       string
		statusCode int
		body       string
	}{
		{name: "server failure", statusCode: http.StatusServiceUnavailable, body: "temporary"},
		{name: "malformed success", statusCode: http.StatusOK, body: `{"success":true`},
		{name: "invalid success", statusCode: http.StatusOK, body: `{"success":false,"orderID":""}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			attempts := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				attempts++
				writer.WriteHeader(test.statusCode)
				_, _ = io.WriteString(writer, test.body)
			}))
			defer server.Close()
			provider := testClient(t, server, true)
			_, err := provider.PostOrder(context.Background(), testCredentials(), testSignedOrder())
			require.Error(t, err)
			require.False(t, IsDefinitiveSubmissionRejection(err))
			require.Equal(t, 1, attempts)
		})
	}
}

func TestRedirectOversizeAndTrailingJSONFailClosed(t *testing.T) {
	targetCalls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/redirect":
			http.Redirect(writer, request, "/target", http.StatusFound)
		case "/target":
			targetCalls++
			_, _ = io.WriteString(writer, `{}`)
		case "/trailing":
			_, _ = io.WriteString(writer, `{} {}`)
		}
	}))
	defer server.Close()
	provider := testClient(t, server, false).(*client)
	var target map[string]any
	err := provider.do(context.Background(), http.MethodGet, "/redirect", nil, nil, nil, 1024, &target)
	var providerError *HTTPError
	require.ErrorAs(t, err, &providerError)
	require.Equal(t, http.StatusFound, providerError.StatusCode)
	require.Zero(t, targetCalls)
	err = provider.do(context.Background(), http.MethodGet, "/trailing", nil, nil, nil, 1024, &target)
	require.ErrorContains(t, err, "multiple JSON values")
	_, err = readBounded(strings.NewReader("12345"), 4)
	require.ErrorContains(t, err, "response exceeds")
}

func TestDefinitiveSubmissionClassification(t *testing.T) {
	require.True(t, IsDefinitiveSubmissionRejection(&HTTPError{StatusCode: http.StatusBadRequest}))
	require.True(t, IsDefinitiveSubmissionRejection(&HTTPError{StatusCode: http.StatusTooManyRequests}))
	require.False(t, IsDefinitiveSubmissionRejection(&HTTPError{StatusCode: http.StatusRequestTimeout}))
	require.False(t, IsDefinitiveSubmissionRejection(&HTTPError{StatusCode: http.StatusInternalServerError}))
	require.False(t, IsDefinitiveSubmissionRejection(errors.New("network")))
	require.ErrorIs(t, &HTTPError{StatusCode: http.StatusTooManyRequests}, ErrRateLimited)
}

func TestGetOrderMapsNotFound(t *testing.T) {
	server := httptest.NewTLSServer(http.NotFoundHandler())
	defer server.Close()
	provider := testClient(t, server, false)

	_, err := provider.GetOrder(context.Background(), testCredentials(), testOrderID)
	require.ErrorIs(t, err, ErrOrderNotFound)
}

func TestNetworkFailuresAreNeverRetried(t *testing.T) {
	attempts := 0
	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		attempts++
		return nil, errors.New("network down")
	})}
	provider, err := New(WithHTTPClient(httpClient))
	require.NoError(t, err)

	_, err = provider.ListMarkets(context.Background(), "")
	require.ErrorContains(t, err, "request failed")
	require.Equal(t, 1, attempts)
}

func TestRequestAndErrorBodiesAreBounded(t *testing.T) {
	provider, err := New()
	require.NoError(t, err)
	concrete := provider.(*client)
	var target map[string]any
	err = concrete.do(
		context.Background(),
		http.MethodPost,
		"/order",
		nil,
		strings.Repeat("x", maxRequestBodyBytes+1),
		nil,
		1024,
		&target,
	)
	require.ErrorContains(t, err, "request body is too large")

	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(writer, strings.Repeat("x", maxErrorBodyBytes+1))
	}))
	defer server.Close()
	concrete = testClient(t, server, false).(*client)
	err = concrete.do(context.Background(), http.MethodGet, "/error", nil, nil, nil, 1024, &target)
	require.ErrorContains(t, err, "response exceeds")
}

func TestRetryAfterParsing(t *testing.T) {
	response := &http.Response{Header: http.Header{"Retry-After": []string{"3"}}}
	require.Equal(t, 3*time.Second, retryAfter(response))

	response.Header.Set("Retry-After", "not-a-date")
	require.Zero(t, retryAfter(response))
}

func TestClientSupportsConcurrentReads(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, `{"data":[],"next_cursor":""}`)
	}))
	defer server.Close()
	provider := testClient(t, server, false)

	results := make(chan error, 16)
	for range 16 {
		go func() {
			page, err := provider.ListMarkets(context.Background(), "")
			if err == nil && page == nil {
				err = errors.New("expected market page")
			} else if err == nil && len(page.Data) != 0 {
				err = errors.New("expected empty market page")
			}
			results <- err
		}()
	}
	for range 16 {
		require.NoError(t, <-results)
	}
}

func TestWireOnlyOrderFieldsDoNotChangeTheDigest(t *testing.T) {
	first := testSignedOrder()
	second := first
	second.Expiration = 0
	second.OrderType = OrderTypeGTC
	second.PostOnly = false

	require.NoError(t, first.Validate())
	require.NoError(t, second.Validate())
	firstDigest, err := first.Order.Hash(protocol.MarketTypeStandard)
	require.NoError(t, err)
	secondDigest, err := second.Order.Hash(protocol.MarketTypeStandard)
	require.NoError(t, err)
	require.Equal(t, firstDigest, secondDigest)
}

func testClient(t *testing.T, server *httptest.Server, submissionEnabled bool) Client {
	t.Helper()
	provider, err := New(
		WithBaseURL(server.URL),
		WithHTTPClient(server.Client()),
		WithOrderSubmissionEnabled(submissionEnabled),
		withClock(func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, provider.Close()) })
	return provider
}

func testCredentials() Credentials {
	return Credentials{Address: testMaker, APIKey: "key", APISecret: "c2VjcmV0", Passphrase: "phrase"}
}

func testSignedOrder() SignedOrder {
	return SignedOrder{
		Order: protocol.Order{
			Salt: 1, Maker: common.HexToAddress(testMaker), Signer: common.HexToAddress(testMaker), TokenID: "1",
			MakerAmount: "500000", TakerAmount: "1000000", Side: protocol.OrderSideBuy,
			SignatureType: protocol.OrderSignatureTypePoly1271, Timestamp: 1_700_000_000_000,
		},
		Signature: "0x12", Expiration: 1_700_000_840, OrderType: OrderTypeGTD, PostOnly: true,
	}
}

func validMarketJSON() string {
	return `{"enable_order_book":true,"active":true,"closed":false,"archived":false,"accepting_orders":true,"accepting_order_timestamp":null,"minimum_order_size":1,"minimum_tick_size":0.01,"condition_id":"` + testMarket + `","question_id":"q","question":"Question?","description":"","market_slug":"market","end_date_iso":"","neg_risk":false,"neg_risk_market_id":"","neg_risk_request_id":"","icon":"","image":"","tokens":[{"token_id":"1","outcome":"Yes","price":0.5,"winner":false}],"tags":[]}`
}

func validOpenOrderJSON() string {
	return `{"id":"` + testOrderID + `","status":"LIVE","owner":"key","maker_address":"` + testMaker + `","market":"` + testMarket + `","asset_id":"1","side":"BUY","original_size":"1","size_matched":"0","price":"0.5","associate_trades":[],"created_at":1,"expiration":"0","order_type":"GTC"}`
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
