package hyperliquid

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type exchangeRoundTripper func(*http.Request) (*http.Response, error)

func (f exchangeRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestEnvelopeCodecPreservesProtocolFieldsWithoutAdmissionPolicy(t *testing.T) {
	raw := []byte(`{"action":{"type":"order"},"nonce":0,"signature":{"r":"0x1","s":"0x2","v":27},"vaultAddress":"0x1111111111111111111111111111111111111111"}`)
	envelope, err := DecodeEnvelope(raw)
	require.NoError(t, err)
	require.Zero(t, envelope.Nonce)
	require.Nil(t, envelope.ExpiresAfter)
	require.Len(t, envelope.Signature, SignatureLength)
	request, err := NewExchangeRequest(envelope)
	require.NoError(t, err)
	require.Equal(t, uint64(27), request.Signature.V)
	require.Equal(t, "0x"+strings.Repeat("0", 63)+"1", request.Signature.R)
	encoded, err := json.Marshal(request)
	require.NoError(t, err)
	roundTrip, err := DecodeEnvelope(encoded)
	require.NoError(t, err)
	require.Equal(t, envelope, roundTrip)

	for _, invalid := range []string{
		string(raw) + `{}`, strings.Replace(string(raw), `"nonce":0`, `"nonce":-1`, 1),
		strings.Replace(string(raw), `"v":27`, `"v":3`, 1),
		strings.Replace(string(raw), `"r":"0x1"`, `"r":"0xgg"`, 1),
		strings.Replace(string(raw), `"nonce":0`, `"nonce":0,"authorization":"secret"`, 1),
	} {
		_, err := DecodeEnvelope([]byte(invalid))
		require.ErrorIs(t, err, ErrMalformedEnvelope)
		require.NotContains(t, err.Error(), "secret")
	}
}

func TestExchangePreDispatchFailuresSendNothing(t *testing.T) {
	refusal := errors.New("durable attempt refused")
	for _, test := range []struct {
		name, endpoint string
		payload        any
		refuse         bool
		kind           ErrorKind
	}{
		{name: "encoding", payload: make(chan int), kind: ErrorEncode},
		{name: "request", endpoint: "://credential-secret", payload: struct{}{}, kind: ErrorRequest},
		{name: "dispatch refusal", payload: struct{}{}, refuse: true, kind: ErrorDispatch},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls, callbacks := 0, 0
			transport := exchangeRoundTripper(func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New("unexpected") })
			client := NewExchangeClient(test.endpoint, &http.Client{Transport: transport})
			response, err := client.Post(t.Context(), test.payload, new(any), func(context.Context) error { callbacks++; return refusal })
			require.Error(t, err)
			var requestError *RequestError
			require.ErrorAs(t, err, &requestError)
			require.Equal(t, test.kind, requestError.Kind)
			require.False(t, response.Dispatched)
			require.Zero(t, calls)
			if test.refuse {
				require.Equal(t, 1, callbacks)
				require.ErrorIs(t, err, refusal)
			} else {
				require.Zero(t, callbacks)
			}
			require.NotContains(t, err.Error(), "credential-secret")
		})
	}
}

func TestExchangeAttemptFactsRetainUncertaintyAndExcludeSecrets(t *testing.T) {
	const secret = "signed-payload-or-credential-secret"
	for _, test := range []struct {
		name      string
		status    int
		body      string
		transport bool
		wantError bool
	}{
		{name: "transport", transport: true, wantError: true},
		{name: "rate limit", status: http.StatusTooManyRequests, body: secret, wantError: true},
		{name: "http error", status: http.StatusBadGateway, body: secret, wantError: true},
		{name: "decode error", status: http.StatusOK, body: secret, wantError: true},
		{name: "response limit", status: http.StatusOK, body: `"` + strings.Repeat("x", 65) + `"`, wantError: true},
		{name: "success", status: http.StatusOK, body: `{"status":"ok","response":{"type":"order"}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls, callbacks := 0, 0
			date := time.Now().UTC().Format(http.TimeFormat)
			transport := exchangeRoundTripper(func(*http.Request) (*http.Response, error) {
				calls++
				if test.transport {
					return nil, errors.New("https://user:" + secret + "@example.com/exchange")
				}
				return &http.Response{StatusCode: test.status, Header: http.Header{"Date": []string{date}}, Body: io.NopCloser(strings.NewReader(test.body))}, nil
			})
			client := NewExchangeClient("https://user:"+secret+"@example.com/exchange", &http.Client{Transport: transport}, WithExchangeResponseLimit(64))
			var out ExchangeResponse
			response, err := client.Post(t.Context(), map[string]string{"signed": secret}, &out, func(context.Context) error { callbacks++; return nil })
			require.True(t, response.Dispatched)
			require.Equal(t, 1, calls, "an attempted signed request is never retried")
			require.Equal(t, 1, callbacks)
			if test.wantError {
				require.Error(t, err)
				require.NotContains(t, err.Error(), secret)
			} else {
				require.NoError(t, err)
				require.Equal(t, "ok", out.Status)
			}
			if !test.transport {
				require.Equal(t, date, response.Date)
				require.Equal(t, test.status, response.StatusCode)
			}
			if test.status == http.StatusTooManyRequests {
				require.ErrorIs(t, err, ErrRateLimited)
			}
			if test.status == http.StatusBadGateway {
				var httpError *HTTPError
				require.ErrorAs(t, err, &httpError)
				require.Empty(t, httpError.Body)
			}
		})
	}
}

func TestExchangeNeverFollowsRedirects(t *testing.T) {
	for _, status := range []int{http.StatusMovedPermanently, http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			http.Redirect(w, r, "/replay", status)
		}))
		client := NewExchangeClient(server.URL, server.Client())
		response, err := client.Post(t.Context(), struct{}{}, new(any), nil)
		server.Close()
		require.Error(t, err)
		require.True(t, response.Dispatched)
		require.Equal(t, 1, calls)
		require.Equal(t, status, response.StatusCode)
	}
}

func TestExchangeCancellationAfterDurableCallbackRemainsAttempted(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	callbacks := 0
	transport := exchangeRoundTripper(func(*http.Request) (*http.Response, error) { return nil, context.Canceled })
	client := NewExchangeClient("https://example.com/exchange", &http.Client{Transport: transport})
	response, err := client.Post(ctx, struct{}{}, new(any), func(context.Context) error { callbacks++; cancel(); return nil })
	require.Equal(t, 1, callbacks)
	require.True(t, response.Dispatched, "callback success is retained through the Do invocation")
	require.ErrorIs(t, err, context.Canceled)
	var requestError *RequestError
	require.ErrorAs(t, err, &requestError)
	require.Equal(t, ErrorTransport, requestError.Kind)
}

func TestDecodeEnvelopeAcceptsSDKIntegerHexAndRejectsMalformedScalars(t *testing.T) {
	for _, scalar := range []string{"0x1", "0x" + strings.Repeat("a", 63), "0x" + strings.Repeat("b", 64)} {
		raw := `{"action":{"type":"order"},"nonce":1,"expiresAfter":2,"signature":{"r":"` + scalar + `","s":"0x2","v":27}}`
		envelope, err := DecodeEnvelope([]byte(raw))
		require.NoError(t, err)
		require.Len(t, envelope.Signature, SignatureLength)
	}
	for _, scalar := range []string{"", "1", "0x", "0xgg", "0x" + strings.Repeat("1", 65)} {
		_, err := DecodeSignatureScalar(scalar)
		require.Error(t, err)
	}
}
