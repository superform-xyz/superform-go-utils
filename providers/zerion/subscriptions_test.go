package zerion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/superform-xyz/superform-go-utils/utils/constants"
)

func newSubscriptionTestClient(t *testing.T, handler http.HandlerFunc) (TxSubscriptionClient, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c, err := NewTxSubscriptionClient("test-key", WithTxSubscriptionBaseURL(srv.URL+"/v1/"), WithTxSubscriptionTimeout(5*time.Second))
	require.NoError(t, err)
	return c, srv
}

func TestNewTxSubscriptionClient(t *testing.T) {
	_, err := NewTxSubscriptionClient(" ")
	require.Error(t, err)
	_, err = NewTxSubscriptionClient("k", WithTxSubscriptionBaseURL("not a url"))
	require.Error(t, err)
	c, err := NewTxSubscriptionClient("k", WithTxSubscriptionHTTPClient(&http.Client{}))
	require.NoError(t, err)
	assert.Equal(t, "https://api.zerion.io/v1", c.(*txSubscriptionClient).baseURL.String())
}

func TestTxSubscriptionCreateAndGet(t *testing.T) {
	c, _ := newSubscriptionTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Basic test-key", r.Header.Get("Authorization"))
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tx-subscriptions/":
			var body map[string]json.RawMessage
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			assert.JSONEq(t, `"https://cb.example/webhooks/zerion"`, string(body["callback_url"]))
			assert.JSONEq(t, `[]`, string(body["addresses"]))
			assert.JSONEq(t, `["base","ethereum"]`, string(body["chain_ids"]))
			assert.JSONEq(t, `{"exclude":{"is_spam":true}}`, string(body["filters"]))
			assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"type":"transactions_subscription","id":"sub-1","attributes":{"callback_url":"https://cb.example/webhooks/zerion","created_at":"x","updated_at":"y","filters":{"exclude":{"is_spam":true}}},
			  "relationships":{"chains":[{"links":{"related":"r"},"data":{"type":"chains","id":"base"}},{"links":{"related":"r"},"data":{"type":"chains","id":"Ethereum"}}]}}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tx-subscriptions/sub-1":
			_, _ = w.Write([]byte(`{"data":{"id":"sub-1","attributes":{"callback_url":"https://cb","status":"enabled","chain_ids":["base"]},"relationships":{"chains":{"data":[{"id":"ethereum"}]}}}}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})

	sub, meta, err := c.CreateSubscription(context.Background(), CreateTxSubscriptionRequest{
		CallbackURL: "https://cb.example/webhooks/zerion",
		ChainIDs:    []string{"base", "ethereum"},
		Filters:     json.RawMessage(`{"exclude":{"is_spam":true}}`),
	})
	require.NoError(t, err)
	assert.Equal(t, http.StatusCreated, meta.StatusCode)
	assert.Equal(t, "sub-1", sub.ID)
	assert.Equal(t, []string{"base", "ethereum"}, sub.ChainIDs)
	assert.JSONEq(t, `{"exclude":{"is_spam":true}}`, string(sub.Filters))
	assert.Empty(t, sub.Status)

	sub, _, err = c.GetSubscription(context.Background(), "sub-1")
	require.NoError(t, err)
	assert.Equal(t, "enabled", sub.Status)
	assert.Equal(t, []string{"base"}, sub.ChainIDs, "attributes.chain_ids wins when present")
	assert.Nil(t, sub.Filters)

	_, _, err = c.CreateSubscription(context.Background(), CreateTxSubscriptionRequest{})
	assert.Error(t, err)
	_, _, err = c.GetSubscription(context.Background(), "../x")
	assert.Error(t, err)
}

func TestTxSubscriptionPatchWallets(t *testing.T) {
	var calls atomic.Int64
	var lastBody atomic.Value
	c, _ := newSubscriptionTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		assert.Equal(t, http.MethodPatch, r.Method)
		assert.Equal(t, "/v1/tx-subscriptions/sub-1/wallets", r.URL.Path)
		assert.Equal(t, "Basic test-key", r.Header.Get("Authorization"))
		raw, _ := io.ReadAll(r.Body)
		lastBody.Store(string(raw))
		w.Header().Set("RateLimit-Org-Second-Remaining", "9")
		w.Header().Set("RateLimit-Org-Second-Reset", "1")
		w.Header().Set("RateLimit-Org-Day-Remaining", "1000")
		w.Header().Set("RateLimit-Org-Month-Remaining", "200000")
		w.WriteHeader(http.StatusOK)
	})

	for _, tc := range []struct {
		name        string
		add, remove []string
		want        string
	}{
		{"add only", []string{"0xa"}, nil, `{"add":["0xa"]}`},
		{"remove only", nil, []string{"0xb"}, `{"remove":["0xb"]}`},
		{"both", []string{"0xa"}, []string{"0xb"}, `{"add":["0xa"],"remove":["0xb"]}`},
		{"empty remove omitted", []string{"0xa"}, []string{}, `{"add":["0xa"]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			meta, err := c.PatchWallets(context.Background(), "sub-1", tc.add, tc.remove)
			require.NoError(t, err)
			assert.JSONEq(t, tc.want, lastBody.Load().(string))
			assert.Equal(t, http.StatusOK, meta.StatusCode)
			require.NotNil(t, meta.RateLimit.SecondRemaining)
			assert.EqualValues(t, 9, *meta.RateLimit.SecondRemaining)
			assert.EqualValues(t, 1000, *meta.RateLimit.DayRemaining)
			assert.EqualValues(t, 200000, *meta.RateLimit.MonthRemaining)
			assert.Equal(t, time.Second, meta.RateLimit.SecondReset)
		})
	}

	addresses := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("0x%040x", i)
		}
		return out
	}
	before := calls.Load()
	_, err := c.PatchWallets(context.Background(), "sub-1", nil, nil)
	require.Error(t, err)
	_, err = c.PatchWallets(context.Background(), "sub-1", addresses(60), addresses(41))
	require.Error(t, err)
	_, err = c.PatchWallets(context.Background(), "sub-1", []string{" "}, nil)
	require.Error(t, err)
	assert.Equal(t, before, calls.Load(), "invalid batches must not reach the API")

	_, err = c.PatchWallets(context.Background(), "sub-1", addresses(50), addresses(50))
	require.NoError(t, err)
}

func TestTxSubscriptionErrorsAndInvalidWallet(t *testing.T) {
	batch := []string{
		"0xAAAAaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"0xcccccccccccccccccccccccccccccccccccccccc",
	}
	var status atomic.Int64
	var body atomic.Value
	c, _ := newSubscriptionTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("RateLimit-Org-Second-Remaining", "0")
		w.Header().Set("RateLimit-Org-Second-Reset", "0.5")
		w.Header().Set("RateLimit-Org-Day-Remaining", "12")
		w.WriteHeader(int(status.Load()))
		_, _ = w.Write([]byte(body.Load().(string)))
	})
	call := func(code int, payload string) error {
		status.Store(int64(code))
		body.Store(payload)
		_, err := c.PatchWallets(context.Background(), "sub-1", batch, nil)
		return err
	}

	for _, tc := range []struct {
		name    string
		code    int
		payload string
		want    string
		ok      bool
	}{
		{"named in detail", 400, `{"errors":[{"title":"Malformed parameters","detail":"wallet 0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa is not trackable"}]}`, batch[0], true},
		{"named in title upper case", 400, `{"errors":[{"title":"Invalid address 0xBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB","detail":""}]}`, batch[1], true},
		{"named only in raw body", 400, `{"message":"bad wallet: 0xcccccccccccccccccccccccccccccccccccccccc"}`, batch[2], true},
		{"no batch address named", 400, `{"errors":[{"title":"Malformed parameters","detail":"chain foo does not support transactions"}]}`, "", false},
		{"address outside batch", 400, `{"errors":[{"detail":"0xdddddddddddddddddddddddddddddddddddddddd invalid"}]}`, "", false},
		{"multiple batch addresses", 400, `{"errors":[{"detail":"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa and 0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb invalid"}]}`, "", false},
		{"prefix of longer hex is not a match", 400, `{"errors":[{"detail":"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa00 invalid"}]}`, "", false},
		{"not a 400", 429, `{"errors":[{"title":"Too many requests","detail":"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}`, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := call(tc.code, tc.payload)
			var apiErr *APIError
			require.ErrorAs(t, err, &apiErr)
			assert.Equal(t, tc.code, apiErr.StatusCode)
			got, ok := InvalidWalletFromError(fmt.Errorf("wrapped: %w", err), batch)
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.want, got)
		})
	}

	t.Run("429 rate limit headers on APIError", func(t *testing.T) {
		err := call(http.StatusTooManyRequests, `{"errors":[{"title":"Too many requests","detail":"Your request had been throttled"}]}`)
		var apiErr *APIError
		require.ErrorAs(t, err, &apiErr)
		assert.Equal(t, []APIErrorObject{{Title: "Too many requests", Detail: "Your request had been throttled"}}, apiErr.Errors)
		require.NotNil(t, apiErr.RateLimit.SecondRemaining)
		assert.Zero(t, *apiErr.RateLimit.SecondRemaining)
		assert.EqualValues(t, 12, *apiErr.RateLimit.DayRemaining)
		assert.Nil(t, apiErr.RateLimit.MonthRemaining)
		assert.Equal(t, 500*time.Millisecond, apiErr.RateLimit.SecondReset)
		assert.Contains(t, err.Error(), "429")
	})

	t.Run("body truncated", func(t *testing.T) {
		err := call(http.StatusBadGateway, strings.Repeat("x", 10<<10))
		var apiErr *APIError
		require.ErrorAs(t, err, &apiErr)
		assert.Len(t, apiErr.Body, 4<<10)
		assert.Empty(t, apiErr.Errors)
	})

	_, ok := InvalidWalletFromError(errors.New("plain"), batch)
	assert.False(t, ok)
	_, ok = InvalidWalletFromError(nil, batch)
	assert.False(t, ok)
}

func TestTxSubscriptionListWalletsPagination(t *testing.T) {
	var next atomic.Value
	var mu sync.Mutex
	var seenQueries []string
	c, srv := newSubscriptionTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/tx-subscriptions/sub-1/wallets", r.URL.Path)
		mu.Lock()
		seenQueries = append(seenQueries, r.URL.RawQuery)
		mu.Unlock()
		if r.URL.Query().Get("page[after]") == "" {
			_, _ = fmt.Fprintf(w, `{"links":{"self":"s","next":%q},"data":{"type":"transactions_subscription_wallets","id":"sub-1-wallets","attributes":{"addresses":["0xAA","0xbb"," "]}}}`, next.Load().(string))
			return
		}
		_, _ = w.Write([]byte(`{"links":{"self":"s"},"data":[{"id":"0xCC"},{"attributes":{"address":"0xDD"}},"0xEE"]}`))
	})

	next.Store(srv.URL + "/v1/tx-subscriptions/sub-1/wallets?page%5Bsize%5D=2000&page%5Bafter%5D=cur1")
	page, meta, err := c.ListWallets(context.Background(), "sub-1", 2000, "")
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, meta.StatusCode)
	assert.Equal(t, []string{"0xaa", "0xbb"}, page.Addresses)
	require.NotEmpty(t, page.NextCursor)

	page, _, err = c.ListWallets(context.Background(), "sub-1", 2000, page.NextCursor)
	require.NoError(t, err)
	assert.Equal(t, []string{"0xcc", "0xdd", "0xee"}, page.Addresses)
	assert.Empty(t, page.NextCursor)
	mu.Lock()
	assert.Equal(t, "page%5Bafter%5D=cur1&page%5Bsize%5D=2000", seenQueries[1])
	mu.Unlock()

	next.Store("/v1/tx-subscriptions/sub-1/wallets?page%5Bafter%5D=rel")
	page, _, err = c.ListWallets(context.Background(), "sub-1", 0, "")
	require.NoError(t, err, "relative next link on the same host is fine")
	assert.Equal(t, "page%5Bafter%5D=rel", page.NextCursor)

	for name, bad := range map[string]string{
		"other host":      "https://evil.example/v1/tx-subscriptions/sub-1/wallets?page%5Bafter%5D=x",
		"other scheme":    strings.Replace(srv.URL, "http://", "https://", 1) + "/v1/tx-subscriptions/sub-1/wallets?page%5Bafter%5D=x",
		"other path":      srv.URL + "/v1/tx-subscriptions/sub-2/wallets?page%5Bafter%5D=x",
		"no query":        srv.URL + "/v1/tx-subscriptions/sub-1/wallets",
		"protocol-rel":    "//evil.example/v1/tx-subscriptions/sub-1/wallets?page%5Bafter%5D=x",
		"userinfo":        strings.Replace(srv.URL, "http://", "http://u:p@", 1) + "/v1/tx-subscriptions/sub-1/wallets?page%5Bafter%5D=x",
		"did not advance": srv.URL + "/v1/tx-subscriptions/sub-1/wallets?page%5Bsize%5D=10",
	} {
		t.Run(name, func(t *testing.T) {
			next.Store(bad)
			_, _, err := c.ListWallets(context.Background(), "sub-1", 10, "")
			require.ErrorIs(t, err, errUntrustedPaginationLink)
		})
	}

	_, _, err = c.ListWallets(context.Background(), "sub-1", 2001, "")
	require.Error(t, err)
	_, _, err = c.ListWallets(context.Background(), "sub-1", 10, "%zz")
	require.Error(t, err)
}

func TestTxSubscriptionCountEnableDisableChains(t *testing.T) {
	c, _ := newSubscriptionTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Basic test-key", r.Header.Get("Authorization"))
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/tx-subscriptions/sub-1/wallets/count":
			_, _ = w.Write([]byte(`{"links":{"self":"s"},"data":{"type":"transactions_subscription_wallets_count","id":"sub-1-wallets-count","attributes":{"count":123456}}}`))
		case "GET /v1/tx-subscriptions/sub-2/wallets/count":
			_, _ = w.Write([]byte(`{"data":{"attributes":{}}}`))
		case "PATCH /v1/tx-subscriptions/sub-1/disable", "PATCH /v1/tx-subscriptions/sub-1/enable":
			w.WriteHeader(http.StatusNoContent)
		case "GET /v1/chains/":
			_, _ = w.Write([]byte(`{"links":{"self":"s"},"data":[
			  {"type":"chains","id":"base","attributes":{"external_id":"0x2105","name":"Base","flags":{"supports_transactions":true,"supports_positions":true}}},
			  {"type":"chains","id":"bob","attributes":{"external_id":"0xed88","name":"BOB","flags":{"supports_transactions":true,"supports_positions":false}}},
			  {"type":"chains","id":"solana","attributes":{"external_id":"solana","name":"Solana","flags":{"supports_transactions":true,"supports_positions":true}}},
			  {"type":"chains","id":"Arbitrum","attributes":{"name":"Arbitrum"}}
			]}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	ctx := context.Background()

	n, _, err := c.CountWallets(ctx, "sub-1")
	require.NoError(t, err)
	assert.EqualValues(t, 123456, n)
	_, _, err = c.CountWallets(ctx, "sub-2")
	require.Error(t, err)

	meta, err := c.DisableSubscription(ctx, "sub-1")
	require.NoError(t, err)
	assert.Equal(t, http.StatusNoContent, meta.StatusCode)
	meta, err = c.EnableSubscription(ctx, "sub-1")
	require.NoError(t, err)
	assert.Equal(t, http.StatusNoContent, meta.StatusCode)

	chains, _, err := c.ListChains(ctx)
	require.NoError(t, err)
	assert.Equal(t, []ChainInfo{
		{ID: "base", ChainID: constants.BaseChainID, SupportsTransactions: true, SupportsPositions: true},
		{ID: "bob", ChainID: 60808, SupportsTransactions: true},
		{ID: "solana", SupportsTransactions: true, SupportsPositions: true},
		{ID: "arbitrum", ChainID: constants.ArbitrumChainID},
	}, chains)
}

func TestTxSubscriptionTransportErrorHasNoRetry(t *testing.T) {
	var calls atomic.Int64
	c, _ := newSubscriptionTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	meta, err := c.DisableSubscription(context.Background(), "sub-1")
	var apiErr *APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusServiceUnavailable, meta.StatusCode)
	assert.EqualValues(t, 1, calls.Load())
}
