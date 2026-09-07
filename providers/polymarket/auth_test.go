package polymarket

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/signer/core/apitypes"
	"github.com/stretchr/testify/require"

	protocol "github.com/superform-xyz/superform-go-utils/utils/polymarket"
)

func testL1Authorization(t *testing.T, at time.Time) L1Authorization {
	t.Helper()
	key, err := crypto.HexToECDSA(strings.Repeat("01", 32))
	require.NoError(t, err)
	address := strings.ToLower(crypto.PubkeyToAddress(key.PublicKey).Hex())
	data, err := protocol.AuthTypedData(address, at.Unix(), 0)
	require.NoError(t, err)
	digest, _, err := apitypes.TypedDataAndHash(apitypes.TypedData(data))
	require.NoError(t, err)
	signature, err := crypto.Sign(digest, key)
	require.NoError(t, err)
	signature[64] += 27
	return L1Authorization{Address: address, Timestamp: at.Unix(), Signature: hexutil.Encode(signature)}
}

func TestCredentialExchangeCreatesOnceAndDerivesBoundIdentity(t *testing.T) {
	now := time.Unix(1800000000, 0)
	auth := testL1Authorization(t, now)
	for _, initial := range []int{200, 400, 409, 500, 401, 403, 429} {
		t.Run(fmt.Sprint(initial), func(t *testing.T) {
			var paths []string
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paths = append(paths, r.Method+" "+r.URL.Path)
				require.Equal(t, auth.Address, r.Header.Get("POLY_ADDRESS"))
				require.Equal(t, auth.Signature, r.Header.Get("POLY_SIGNATURE"))
				require.Equal(t, "1800000000", r.Header.Get("POLY_TIMESTAMP"))
				require.Equal(t, "0", r.Header.Get("POLY_NONCE"))
				if r.Method == "POST" && initial != 200 {
					w.WriteHeader(initial)
					_, _ = io.WriteString(w, auth.Signature+" private-secret")
					return
				}
				_, _ = io.WriteString(w, `{"apiKey":"api-key","secret":"c2VjcmV0","passphrase":"private-phrase"}`)
			}))
			defer server.Close()
			client, err := NewCredentialClient(WithBaseURL(server.URL), WithHTTPClient(server.Client()), withClock(func() time.Time { return now }))
			require.NoError(t, err)
			creds, err := client.CreateOrDeriveCredentials(context.Background(), auth)
			if initial == 401 || initial == 403 || initial == 429 {
				require.Error(t, err)
				require.NotContains(t, err.Error(), "private-secret")
				require.NotContains(t, err.Error(), auth.Signature)
				require.Equal(t, []string{"POST /auth/api-key"}, paths)
			} else {
				require.NoError(t, err)
				require.Equal(t, auth.Address, creds.Address)
				require.Equal(t, "api-key", creds.APIKey)
				if initial == 200 {
					require.Equal(t, []string{"POST /auth/api-key"}, paths)
				} else {
					require.Equal(t, []string{"POST /auth/api-key", "GET /auth/derive-api-key"}, paths)
				}
			}
		})
	}
	for _, value := range []any{auth} {
		encoded, err := json.Marshal(value)
		require.NoError(t, err)
		require.NotContains(t, string(encoded), auth.Signature)
		require.NotContains(t, fmt.Sprintf("%v %#v", value, value), auth.Signature)
	}
}

func TestCredentialExchangeBoundsProofResponsesAndCancellation(t *testing.T) {
	now := time.Unix(1800000000, 0)
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, strings.Repeat("sensitive", 1024))
	}))
	defer server.Close()
	client, err := NewCredentialClient(WithBaseURL(server.URL), WithHTTPClient(server.Client()), withClock(func() time.Time { return now }))
	require.NoError(t, err)
	auth := testL1Authorization(t, now)
	for _, bad := range []L1Authorization{testL1Authorization(t, now.Add(-6*time.Minute)), testL1Authorization(t, now.Add(time.Minute)), {Address: auth.Address, Timestamp: now.Unix(), Signature: "0x12"}} {
		_, err := client.CreateOrDeriveCredentials(context.Background(), bad)
		require.Error(t, err)
	}
	require.Zero(t, calls.Load())
	_, err = client.CreateOrDeriveCredentials(context.Background(), auth)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "sensitive")
	require.EqualValues(t, 2, calls.Load())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = client.CreateOrDeriveCredentials(ctx, auth)
	require.ErrorIs(t, err, context.Canceled)
	require.EqualValues(t, 2, calls.Load())
}

func TestL2SignerDiffersFromFundingNexus(t *testing.T) {
	credentials := testCredentials()
	credentials.Address = "0x2222222222222222222222222222222222222222"
	var postCalls int
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, credentials.Address, r.Header.Get("POLY_ADDRESS"))
		switch r.URL.Path {
		case "/order":
			postCalls++
			var body createOrderPayload
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			require.Equal(t, testMaker, body.Order.Maker)
			require.Equal(t, testMaker, body.Order.Signer)
			require.Equal(t, credentials.APIKey, body.Owner)
			_, _ = io.WriteString(w, `{"success":true,"orderID":"`+testOrderID+`","status":"LIVE"}`)
		case "/data/orders":
			_, _ = io.WriteString(w, `{"data":[`+validOpenOrderJSON()+`],"next_cursor":"LTE="}`)
		default:
			_, _ = io.WriteString(w, validOpenOrderJSON())
		}
	}))
	defer server.Close()
	client := testClient(t, server, true)
	_, err := client.PostOrder(context.Background(), credentials, testSignedOrder())
	require.NoError(t, err)
	require.Equal(t, 1, postCalls)
	page, err := client.ListOpenOrders(context.Background(), credentials, OpenOrderFilter{})
	require.NoError(t, err)
	require.Equal(t, testMaker, page.Data[0].MakerAddress)
	order, err := client.GetOrder(context.Background(), credentials, testOrderID)
	require.NoError(t, err)
	require.NoError(t, order.Validate(testMaker))
	require.Error(t, order.Validate(credentials.Address))
}
