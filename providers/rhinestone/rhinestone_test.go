package rhinestone

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func testClient(t *testing.T, handler http.HandlerFunc) Client {
	t.Helper()
	s := httptest.NewServer(handler)
	t.Cleanup(s.Close)
	c, err := New(WithAPIKey("project-secret"), WithAPIBaseURL(s.URL+"/deposit-processor/"), WithHTTPClient(s.Client()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestDiscoveryRequestsPreserveProviderEvidence(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "project-secret", r.Header.Get("x-api-key"))
		switch r.URL.Path {
		case "/deposit-processor/onramp/options":
			require.Equal(t, "swapped", r.URL.Query().Get("provider"))
			require.Equal(t, "fiat", r.URL.Query().Get("kind"))
			require.Equal(t, "DE", r.Header.Get("x-user-country"))
			_, _ = fmt.Fprint(w, `{"provider":"swapped","kind":"fiat","country":"DE","methods":[{"method":"bank-transfer","currencies":["EUR"],"minAmount":7.2500,"maxAmount":1000000},{"method":"google-pay","currencies":["USD"],"minAmount":0,"maxAmount":null},{"method":"future-method","currencies":[]}]}`)
		case "/deposit-processor/chains":
			require.Empty(t, r.Header.Get("x-user-country"))
			require.Empty(t, r.URL.RawQuery)
			_, _ = fmt.Fprint(w, `{"eip155:8453":{"destination":true,"supportedTokens":"all"},"1":{"destination":false,"supportedTokens":[{"address":"0x123"}]}}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	options, err := c.GetOnrampOptions(context.Background(), GetOnrampOptionsRequest{Provider: "swapped", Kind: "fiat", Country: "DE"})
	require.NoError(t, err)
	require.Equal(t, "DE", *options.Country)
	require.Len(t, options.Methods, 3)
	require.Equal(t, "bank-transfer", options.Methods[0].Method)
	require.Equal(t, []string{"EUR"}, options.Methods[0].Currencies)
	require.Equal(t, "7.2500", options.Methods[0].MinAmount.String())
	require.Equal(t, "1000000", options.Methods[0].MaxAmount.String())
	require.Equal(t, "0", options.Methods[1].MinAmount.String())
	require.Nil(t, options.Methods[1].MaxAmount)
	require.Equal(t, "future-method", options.Methods[2].Method)
	require.Equal(t, []string{}, options.Methods[2].Currencies)

	// Cache serialization must preserve the previous wire field names and nulls.
	encoded, err := json.Marshal(options)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"minAmount":7.2500`)
	require.Contains(t, string(encoded), `"maxAmount":null`)
	chains, err := c.GetChains(context.Background())
	require.NoError(t, err)
	require.True(t, chains["eip155:8453"].Destination)
	require.JSONEq(t, `"all"`, string(chains["eip155:8453"].SupportedTokens))
	require.JSONEq(t, `[{"address":"0x123"}]`, string(chains["1"].SupportedTokens))
}

func TestDiscoveryPreservesUnknownAndEmpty(t *testing.T) {
	for _, body := range []string{
		`{"country":null,"methods":null}`,
		`{}`,
		`{"methods":[]}`,
		`{"methods":[{"method":"bank-transfer","currencies":null}]}`,
	} {
		t.Run(body, func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				require.Empty(t, r.Header.Get("x-user-country"))
				_, _ = fmt.Fprint(w, body)
			})
			out, err := c.GetOnrampOptions(context.Background(), GetOnrampOptionsRequest{Provider: "swapped", Kind: "fiat"})
			require.NoError(t, err)
			require.Nil(t, out.Country)
			switch body {
			case `{"methods":[]}`:
				require.NotNil(t, out.Methods)
				require.Empty(t, out.Methods)
			case `{"methods":[{"method":"bank-transfer","currencies":null}]}`:
				require.Len(t, out.Methods, 1)
				require.Nil(t, out.Methods[0].Currencies)
			default:
				require.Nil(t, out.Methods)
			}
		})
	}
}

func TestHTTPFailuresAreRedactedAndNotRetried(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests, http.StatusBadGateway} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(status)
				_, _ = fmt.Fprint(w, "private vendor detail project-secret")
			})
			_, err := c.GetChains(context.Background())
			var apiErr *APIError
			require.ErrorAs(t, err, &apiErr)
			require.Equal(t, status, apiErr.StatusCode)
			require.NotContains(t, err.Error(), "private")
			require.NotContains(t, err.Error(), "project-secret")
			require.Equal(t, 1, calls)
		})
	}
}

func TestRejectsInvalidAndOversizedResponses(t *testing.T) {
	for _, body := range []string{`{`, `{} {}`, strings.Repeat(" ", maxResponseBody+1) + `{}`} {
		c := testClient(t, func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, body) })
		_, err := c.GetChains(context.Background())
		require.Error(t, err)
	}
}

func TestRejectsRedirectsWithInjectedClient(t *testing.T) {
	calls := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	t.Cleanup(target.Close)
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) })
	_, err := c.GetChains(context.Background())
	var apiErr *APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, http.StatusFound, apiErr.StatusCode)
	require.Zero(t, calls, "must not forward the project key or country on redirects")
}

func TestConfigurationAndCancellation(t *testing.T) {
	_, err := New()
	require.Error(t, err)
	for _, base := range []string{"", ":invalid", "ftp://example.com", "https://secret@example.com", "https://example.com?secret=value", "https://example.com#fragment"} {
		_, err := New(WithAPIKey("project-secret"), WithAPIBaseURL(base))
		require.EqualError(t, err, "rhinestone: invalid api base url")
	}
	injected := &http.Client{}
	c, err := New(WithAPIKey("project-secret"), WithHTTPClient(injected))
	require.NoError(t, err)
	require.Nil(t, injected.CheckRedirect, "constructor must not mutate the supplied client")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = c.GetChains(ctx)
	require.ErrorIs(t, err, context.Canceled)
	for _, input := range []GetOnrampOptionsRequest{{}, {Provider: "swapped"}, {Provider: "swapped", Kind: "fiat", Country: "DE\r\nx-secret: value"}} {
		_, err := c.GetOnrampOptions(context.Background(), input)
		require.Error(t, err)
	}
}

type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("private transport detail project-secret")
}

func TestTransportErrorsAreRedacted(t *testing.T) {
	c, err := New(WithAPIKey("project-secret"), WithHTTPClient(&http.Client{Transport: failingTransport{}}))
	require.NoError(t, err)
	_, err = c.GetChains(context.Background())
	require.EqualError(t, err, "rhinestone: request failed")
}
