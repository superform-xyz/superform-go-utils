package debank

import (
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/superform-xyz/superform-go-utils/pkg/http_client"
	"github.com/superform-xyz/superform-go-utils/utils/constants"
)

// newTestClient creates an httptest server and returns a debank client wired to it.
func newTestClient(t *testing.T, handler http.HandlerFunc) *debank {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &debank{accessKey: "test-key", baseUrl: server.URL, client: newTestHTTPClient()}
}

func newTestHTTPClient() *http_client.Client {
	return http_client.NewClientBuilder().SetRetry(0, time.Millisecond).BuildClient()
}

// debankTokenJSON is the raw JSON shape returned by Debank API (with "id" and "chain" fields).
type debankTokenJSON struct {
	ID              string   `json:"id"`
	Chain           string   `json:"chain"`
	Name            string   `json:"name"`
	Symbol          string   `json:"symbol"`
	DisplaySymbol   string   `json:"display_symbol"`
	OptimizedSymbol string   `json:"optimized_symbol"`
	Decimals        uint32   `json:"decimals"`
	LogoURL         string   `json:"logo_url"`
	ProtocolID      string   `json:"protocol_id"`
	Price           float64  `json:"price"`
	TimeAt          float64  `json:"time_at"`
	IsScam          bool     `json:"is_scam"`
	IsSuspicious    bool     `json:"is_suspicious"`
	LowCreditScore  bool     `json:"low_credit_score"`
	IsVerified      bool     `json:"is_verified"`
	IsCore          bool     `json:"is_core"`
	IsWallet        bool     `json:"is_wallet"`
	Amount          float64  `json:"amount"`
	RawAmount       *big.Int `json:"raw_amount"`
	CreditScore     float64  `json:"credit_score"`
}

func jsonOK(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	require.NoError(t, json.NewEncoder(w).Encode(v))
}

var (
	daiAddr    = common.HexToAddress("0x6B175474E89094C44Da98b954EedeAC495271d0F")
	nativeAddr = constants.GetNativeToken()
	nullAddr   = constants.GetNullAddress()
)

// ── New ─────────────────────────────────────────────────────────────────

func TestNew(t *testing.T) {
	d := New("my-key")
	require.NotNil(t, d)

	concrete, ok := d.(*debank)
	require.True(t, ok)
	assert.Equal(t, debankBaseURL, concrete.baseUrl)
	assert.Equal(t, "my-key", concrete.accessKey)
	assert.NotNil(t, concrete.client)
}

func TestNew_WithOptions(t *testing.T) {
	d := New("my-key", WithBaseURL("https://example.com/v1/"), WithRetry(2, time.Second))
	require.NotNil(t, d)

	concrete, ok := d.(*debank)
	require.True(t, ok)
	assert.Equal(t, "https://example.com/v1", concrete.baseUrl)
	assert.NotNil(t, concrete.client)
	require.NotNil(t, concrete.maxRetries)
	assert.Equal(t, uint(2), *concrete.maxRetries)
	require.NotNil(t, concrete.retryDelay)
	assert.Equal(t, time.Second, *concrete.retryDelay)
}

// ── HealthCheck ─────────────────────────────────────────────────────────

func TestHealthCheck(t *testing.T) {
	d := &debank{}
	assert.NoError(t, d.HealthCheck(context.Background()))
}

func TestHealthCheck_ReturnsContextError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	d := &debank{}
	assert.ErrorIs(t, d.HealthCheck(ctx), context.Canceled)
}

// ── GetToken ────────────────────────────────────────────────────────────

func TestGetToken(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		d := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "/token", r.URL.Path)
			assert.Equal(t, "eth", r.URL.Query().Get("chain_id"))
			assert.Equal(t, daiAddr.String(), r.URL.Query().Get("id"))
			jsonOK(t, w, debankTokenJSON{
				ID: daiAddr.Hex(), Chain: "eth", Name: "Dai", Symbol: "DAI",
				Decimals: 18, Price: 1.0, IsVerified: true, RawAmount: big.NewInt(1000),
			})
		})

		token, err := d.GetToken(context.Background(), constants.MainnetChainID, daiAddr)
		require.NoError(t, err)
		require.NotNil(t, token)
		assert.Equal(t, constants.MainnetChainID, token.ChainID)
		assert.Equal(t, daiAddr, token.Address)
		assert.Equal(t, "DAI", token.Symbol)
		assert.Equal(t, uint32(18), token.Decimals)
		assert.Equal(t, 1.0, token.Price)
	})

	t.Run("native token uses chain name as id", func(t *testing.T) {
		d := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			// Native token should send chain name as id param
			assert.Equal(t, "eth", r.URL.Query().Get("id"))
			jsonOK(t, w, debankTokenJSON{
				ID: "eth", Chain: "eth", Name: "Ether", Symbol: "ETH",
				Decimals: 18, Price: 3500.0, RawAmount: big.NewInt(1),
			})
		})

		token, err := d.GetToken(context.Background(), constants.MainnetChainID, nativeAddr)
		require.NoError(t, err)
		require.NotNil(t, token)
		// native token: ID == Chain, so Address should be set to native token
		assert.Equal(t, nativeAddr, token.Address)
	})

	t.Run("null address uses chain name as id", func(t *testing.T) {
		d := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "eth", r.URL.Query().Get("id"))
			jsonOK(t, w, debankTokenJSON{
				ID: "eth", Chain: "eth", Name: "Ether", Symbol: "ETH",
				Decimals: 18, Price: 3500.0, RawAmount: big.NewInt(1),
			})
		})

		token, err := d.GetToken(context.Background(), constants.MainnetChainID, nullAddr)
		require.NoError(t, err)
		require.NotNil(t, token)
	})

	t.Run("token not found - chain_id 0", func(t *testing.T) {
		d := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			// Debank returns a response with chain_id=0 when token doesn't exist
			jsonOK(t, w, debankTokenJSON{ID: "0x0000000000000000000000000000000000000001"})
		})

		token, err := d.GetToken(context.Background(), constants.MainnetChainID, common.HexToAddress("0x01"))
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "token not found")
		assert.Nil(t, token)
	})

	t.Run("chain not found", func(t *testing.T) {
		d := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("server should not be called for unknown chain")
		})

		token, err := d.GetToken(context.Background(), 999999999, daiAddr)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
		assert.Nil(t, token)
	})

	t.Run("http error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		server.Close()
		d := &debank{baseUrl: server.URL, client: newTestHTTPClient()}

		token, err := d.GetToken(context.Background(), constants.MainnetChainID, daiAddr)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to get token")
		assert.Nil(t, token)
	})

	t.Run("json decode error", func(t *testing.T) {
		d := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("bad json"))
		})

		token, err := d.GetToken(context.Background(), constants.MainnetChainID, daiAddr)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to decode debank token response")
		assert.Nil(t, token)
	})

	t.Run("request creation error", func(t *testing.T) {
		d := &debank{baseUrl: "http://invalid\nurl", client: newTestHTTPClient()}

		token, err := d.GetToken(context.Background(), constants.MainnetChainID, daiAddr)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to create debank request")
		assert.Nil(t, token)
	})
}

// ── GetHistoryTokenPrice ────────────────────────────────────────────────

func TestGetHistoryTokenPrice(t *testing.T) {
	ts := time.Date(2025, 1, 15, 0, 0, 0, 0, time.UTC)

	t.Run("success", func(t *testing.T) {
		d := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "/token/history_price", r.URL.Path)
			assert.Equal(t, "eth", r.URL.Query().Get("chain_id"))
			assert.Equal(t, daiAddr.Hex(), r.URL.Query().Get("id"))
			assert.Equal(t, "2025-01-15", r.URL.Query().Get("date_at"))
			jsonOK(t, w, debankTokenJSON{
				ID: daiAddr.Hex(), Chain: "eth", Price: 0.9999, RawAmount: big.NewInt(0),
			})
		})

		price, err := d.GetHistoryTokenPrice(context.Background(), constants.MainnetChainID, daiAddr, ts)
		require.NoError(t, err)
		require.NotNil(t, price)
		assert.Equal(t, 0.9999, *price)
	})

	t.Run("native token uses chain name as id", func(t *testing.T) {
		d := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "eth", r.URL.Query().Get("id"))
			jsonOK(t, w, debankTokenJSON{
				ID: "eth", Chain: "eth", Price: 3500.0, RawAmount: big.NewInt(0),
			})
		})

		price, err := d.GetHistoryTokenPrice(context.Background(), constants.MainnetChainID, nativeAddr, ts)
		require.NoError(t, err)
		require.NotNil(t, price)
		assert.Equal(t, 3500.0, *price)
	})

	t.Run("null address uses chain name as id", func(t *testing.T) {
		d := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "eth", r.URL.Query().Get("id"))
			jsonOK(t, w, debankTokenJSON{
				ID: "eth", Chain: "eth", Price: 3500.0, RawAmount: big.NewInt(0),
			})
		})

		price, err := d.GetHistoryTokenPrice(context.Background(), constants.MainnetChainID, nullAddr, ts)
		require.NoError(t, err)
		require.NotNil(t, price)
	})

	t.Run("chain not found", func(t *testing.T) {
		d := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("server should not be called for unknown chain")
		})

		price, err := d.GetHistoryTokenPrice(context.Background(), 999999999, daiAddr, ts)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
		assert.Nil(t, price)
	})

	t.Run("http error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		server.Close()
		d := &debank{baseUrl: server.URL, client: newTestHTTPClient()}

		price, err := d.GetHistoryTokenPrice(context.Background(), constants.MainnetChainID, daiAddr, ts)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to get token")
		assert.Nil(t, price)
	})

	t.Run("json decode error", func(t *testing.T) {
		d := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("bad json"))
		})

		price, err := d.GetHistoryTokenPrice(context.Background(), constants.MainnetChainID, daiAddr, ts)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to decode debank token history response")
		assert.Nil(t, price)
	})

	t.Run("request creation error", func(t *testing.T) {
		d := &debank{baseUrl: "http://invalid\nurl", client: newTestHTTPClient()}

		price, err := d.GetHistoryTokenPrice(context.Background(), constants.MainnetChainID, daiAddr, ts)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to create debank request")
		assert.Nil(t, price)
	})
}

// ── GetTokenBalances ────────────────────────────────────────────────────

func TestGetTokenBalances(t *testing.T) {
	t.Run("success without filter", func(t *testing.T) {
		d := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "/user/all_token_list", r.URL.Path)
			assert.Equal(t, "true", r.URL.Query().Get("is_all"))
			jsonOK(t, w, []debankTokenJSON{
				{ID: daiAddr.Hex(), Chain: "eth", Name: "Dai", Symbol: "DAI", Decimals: 18, Price: 1.0, IsVerified: true, IsCore: true, RawAmount: big.NewInt(1000)},
				{ID: "0x0000000000000000000000000000000000000abc", Chain: "eth", Name: "Scam", Symbol: "SCM", Decimals: 18, Price: 0.001, IsVerified: false, IsCore: false, RawAmount: big.NewInt(999)},
			})
		})

		tokens, err := d.GetTokenBalances(context.Background(), "0xabcdef1234567890abcdef1234567890abcdef12", false)
		require.NoError(t, err)
		require.Len(t, tokens, 2)
		assert.Equal(t, "DAI", tokens[0].Symbol)
		assert.Equal(t, "SCM", tokens[1].Symbol)
	})

	t.Run("success with filter", func(t *testing.T) {
		d := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			jsonOK(t, w, []debankTokenJSON{
				{ID: daiAddr.Hex(), Chain: "eth", Name: "Dai", Symbol: "DAI", Decimals: 18, IsVerified: true, IsCore: true, RawAmount: big.NewInt(1000)},
				{ID: "0x0000000000000000000000000000000000000abc", Chain: "eth", Name: "Scam", Symbol: "SCM", Decimals: 18, IsVerified: false, IsCore: false, RawAmount: big.NewInt(999)},
				{ID: "0x0000000000000000000000000000000000000def", Chain: "eth", Name: "Core", Symbol: "CORE", Decimals: 18, IsVerified: false, IsCore: true, RawAmount: big.NewInt(500)},
			})
		})

		tokens, err := d.GetTokenBalances(context.Background(), "0xabcdef1234567890abcdef1234567890abcdef12", true)
		require.NoError(t, err)
		require.Len(t, tokens, 2)
		// Scam token (not verified, not core) should be filtered out
		for _, token := range tokens {
			assert.True(t, token.IsVerified || token.IsCore)
		}
	})

	t.Run("http error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		server.Close()
		d := &debank{baseUrl: server.URL, client: newTestHTTPClient()}

		tokens, err := d.GetTokenBalances(context.Background(), "0xabc", false)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to get token balances for user")
		assert.Nil(t, tokens)
	})

	t.Run("json decode error", func(t *testing.T) {
		d := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("bad json"))
		})

		tokens, err := d.GetTokenBalances(context.Background(), "0xabc", false)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to decode debank token list response")
		assert.Nil(t, tokens)
	})

	t.Run("request creation error", func(t *testing.T) {
		d := &debank{baseUrl: "http://invalid\nurl", client: newTestHTTPClient()}

		tokens, err := d.GetTokenBalances(context.Background(), "0xabc", false)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to create debank request")
		assert.Nil(t, tokens)
	})
}

func TestGetTokens(t *testing.T) {
	d := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/token/list_by_ids", r.URL.Path)
		assert.Equal(t, "test-key", r.Header.Get("AccessKey"))
		assert.Equal(t, "op", r.URL.Query().Get("chain_id"))
		assert.Equal(t, "0x1111111111111111111111111111111111111111,0x2222222222222222222222222222222222222222", r.URL.Query().Get("ids"))
		jsonOK(t, w, []debankTokenJSON{
			{
				ID:             "0x1111111111111111111111111111111111111111",
				Chain:          "op",
				Name:           "Token One",
				Symbol:         "ONE",
				ProtocolID:     "proto_one",
				IsVerified:     true,
				TimeAt:         1713400735.0,
				RawAmount:      big.NewInt(0),
				LowCreditScore: false,
				IsScam:         false,
				IsSuspicious:   false,
			},
			{
				ID:             "0x2222222222222222222222222222222222222222",
				Chain:          "op",
				Name:           "Token Two",
				Symbol:         "TWO",
				IsScam:         true,
				IsSuspicious:   true,
				LowCreditScore: true,
				TimeAt:         1713400736.0,
				RawAmount:      big.NewInt(0),
			},
		})
	})

	tokens, err := d.GetTokens(context.Background(), constants.OptimismChainID, []common.Address{
		common.HexToAddress("0x2222222222222222222222222222222222222222"),
		common.HexToAddress("0x1111111111111111111111111111111111111111"),
		common.HexToAddress("0x1111111111111111111111111111111111111111"),
	})
	require.NoError(t, err)
	require.Len(t, tokens, 2)
	assert.Equal(t, common.HexToAddress("0x1111111111111111111111111111111111111111"), tokens[0].Address)
	assert.Equal(t, "proto_one", tokens[0].ProtocolID)
	assert.Equal(t, int64(1713400735), tokens[0].TimeAt)
	assert.Equal(t, common.HexToAddress("0x2222222222222222222222222222222222222222"), tokens[1].Address)
	assert.True(t, tokens[1].IsScam)
	assert.True(t, tokens[1].IsSuspicious)
	assert.True(t, tokens[1].LowCreditScore)
}

func TestGetTokensRejectsOversizedBatch(t *testing.T) {
	d := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("server should not be called for oversized batch")
	})

	addresses := make([]common.Address, maxTokenListByIDs+1)
	for i := range addresses {
		addresses[i] = common.BigToAddress(big.NewInt(int64(i + 1)))
	}

	tokens, err := d.GetTokens(context.Background(), constants.MainnetChainID, addresses)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "supports at most 100 ids")
	assert.Nil(t, tokens)
}

func TestGetAccountCredits(t *testing.T) {
	d := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/account/units", r.URL.Path)
		assert.Equal(t, "test-key", r.Header.Get("AccessKey"))
		jsonOK(t, w, AccountCredits{
			Balance: 2155032,
			Stats: []AccountCreditStat{
				{Usage: 111369, Remains: 2159335, Date: "2026-01-29"},
			},
		})
	})

	units, err := d.GetAccountCredits(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(2155032), units.Balance)
	require.Len(t, units.Stats, 1)
	assert.Equal(t, int64(111369), units.Stats[0].Usage)
	assert.Equal(t, int64(2159335), units.Stats[0].Remains)
	assert.Equal(t, "2026-01-29", units.Stats[0].Date)
}

func TestGetProtocolPortfolios(t *testing.T) {
	d := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/user/all_complex_protocol_list", r.URL.Path)
		assert.Equal(t, "0xabc", r.URL.Query().Get("id"))
		jsonOK(t, w, []ProtocolPortfolio{
			{
				ID:    "aave",
				Chain: "eth",
				Name:  "Aave",
				PortfolioItemList: []PortfolioItem{
					{
						Name:  "Deposit",
						Stats: Stats{AssetUSDValue: 42, NetUSDValue: 40},
						AssetTokenList: []AssetToken{
							{ID: daiAddr.Hex(), Chain: "eth", Symbol: "DAI", ProtocolID: "aave"},
						},
					},
				},
			},
		})
	})

	protocols, err := d.GetProtocolPortfolios(context.Background(), "0xabc")
	require.NoError(t, err)
	require.Len(t, protocols, 1)
	assert.Equal(t, "aave", protocols[0].ID)
	require.Len(t, protocols[0].PortfolioItemList, 1)
	assert.Equal(t, float64(42), protocols[0].PortfolioItemList[0].Stats.AssetUSDValue)
	require.Len(t, protocols[0].PortfolioItemList[0].AssetTokenList, 1)
	assert.Equal(t, "aave", protocols[0].PortfolioItemList[0].AssetTokenList[0].ProtocolID)
}

// ── UnmarshalJSON ───────────────────────────────────────────────────────

func TestToken_UnmarshalJSON(t *testing.T) {
	t.Run("standard token", func(t *testing.T) {
		data := `{"id":"0x6B175474E89094C44Da98b954EedeAC495271d0F","chain":"eth","name":"Dai","symbol":"DAI","decimals":18,"price":1.0,"is_verified":true,"is_core":true,"raw_amount":12345}`
		var token Token
		require.NoError(t, json.Unmarshal([]byte(data), &token))

		assert.Equal(t, daiAddr, token.Address)
		assert.Equal(t, constants.MainnetChainID, token.ChainID)
		assert.Equal(t, "Dai", token.Name)
		assert.Equal(t, "DAI", token.Symbol)
		assert.Equal(t, uint32(18), token.Decimals)
		assert.Equal(t, 1.0, token.Price)
		assert.True(t, token.IsVerified)
		assert.True(t, token.IsCore)
		assert.Equal(t, big.NewInt(12345), token.RawAmount)
	})

	t.Run("native token - id equals chain", func(t *testing.T) {
		data := `{"id":"eth","chain":"eth","name":"Ether","symbol":"ETH","decimals":18,"price":3500.0,"raw_amount":1000000000000000000}`
		var token Token
		require.NoError(t, json.Unmarshal([]byte(data), &token))

		// When ID == Chain, address should be native token address
		assert.Equal(t, nativeAddr, token.Address)
		assert.Equal(t, constants.MainnetChainID, token.ChainID)
	})

	t.Run("nil raw_amount defaults to zero", func(t *testing.T) {
		data := `{"id":"0x6B175474E89094C44Da98b954EedeAC495271d0F","chain":"eth","name":"Dai","symbol":"DAI","decimals":18}`
		var token Token
		require.NoError(t, json.Unmarshal([]byte(data), &token))

		assert.NotNil(t, token.RawAmount)
		assert.Equal(t, big.NewInt(0), token.RawAmount)
	})

	t.Run("empty chain - chainID is 0", func(t *testing.T) {
		data := `{"id":"0x6B175474E89094C44Da98b954EedeAC495271d0F","chain":"","name":"Unknown","symbol":"UNK","decimals":18}`
		var token Token
		require.NoError(t, json.Unmarshal([]byte(data), &token))

		assert.Equal(t, uint64(0), token.ChainID)
	})

	t.Run("unknown chain name - chainID is 0", func(t *testing.T) {
		data := `{"id":"0x6B175474E89094C44Da98b954EedeAC495271d0F","chain":"unknown_chain","name":"Unknown","symbol":"UNK","decimals":18}`
		var token Token
		require.NoError(t, json.Unmarshal([]byte(data), &token))

		assert.Equal(t, uint64(0), token.ChainID)
	})

	t.Run("invalid json for auxToken", func(t *testing.T) {
		// Valid JSON but wrong type for auxToken fields triggers inner unmarshal error
		var token Token
		err := json.Unmarshal([]byte(`{"decimals": "not_a_number"}`), &token)
		assert.Error(t, err)
	})

	t.Run("all supported chains", func(t *testing.T) {
		for chainID, chainName := range chainToNameMap {
			data := `{"id":"0x6B175474E89094C44Da98b954EedeAC495271d0F","chain":"` + chainName + `","name":"Test","symbol":"TST","decimals":18}`
			var token Token
			require.NoError(t, json.Unmarshal([]byte(data), &token), "failed for chain %s", chainName)
			assert.Equal(t, chainID, token.ChainID, "wrong chainID for %s", chainName)
		}
	})
}

// ── tokenFilter ─────────────────────────────────────────────────────────

func TestTokenFilter(t *testing.T) {
	tests := []struct {
		name       string
		isVerified bool
		isCore     bool
		expected   bool
	}{
		{"verified and core", true, true, true},
		{"verified not core", true, false, true},
		{"not verified but core", false, true, true},
		{"not verified not core", false, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token := Token{IsVerified: tt.isVerified, IsCore: tt.isCore}
			assert.Equal(t, tt.expected, tokenFilter(token))
		})
	}
}

// ── ChainToName / ChainNameToID ─────────────────────────────────────────

func TestChainToName(t *testing.T) {
	t.Run("known chain", func(t *testing.T) {
		name, err := ChainToName(constants.MainnetChainID)
		require.NoError(t, err)
		assert.Equal(t, "eth", name)
	})

	t.Run("unknown chain", func(t *testing.T) {
		_, err := ChainToName(999999999)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
	})
}

func TestChainNameToID(t *testing.T) {
	t.Run("known name", func(t *testing.T) {
		assert.Equal(t, constants.MainnetChainID, ChainNameToID("eth"))
	})

	t.Run("unknown name", func(t *testing.T) {
		assert.Equal(t, uint64(0), ChainNameToID("unknown"))
	})
}

// ── Integration ─────────────────────────────────────────────────────────

func TestDebank_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	apiKey := os.Getenv("DEBANK_API_KEY")
	if apiKey == "" {
		t.Skip("DEBANK_API_KEY not set, skipping integration test")
	}

	d := New(apiKey)

	t.Run("get token", func(t *testing.T) {
		token, err := d.GetToken(context.Background(), constants.MainnetChainID, daiAddr)
		require.NoError(t, err)
		require.NotNil(t, token)
		assert.Equal(t, constants.MainnetChainID, token.ChainID)
		assert.Equal(t, daiAddr, token.Address)
		assert.NotZero(t, token.Decimals)
		assert.NotEmpty(t, token.Name)
		assert.NotEmpty(t, token.Symbol)
	})

	t.Run("get native token", func(t *testing.T) {
		token, err := d.GetToken(context.Background(), constants.MainnetChainID, nativeAddr)
		require.NoError(t, err)
		require.NotNil(t, token)
		assert.Equal(t, nativeAddr, token.Address)
		assert.NotZero(t, token.Price)
	})

	t.Run("get historical token price", func(t *testing.T) {
		ts := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
		price, err := d.GetHistoryTokenPrice(context.Background(), constants.MainnetChainID, daiAddr, ts)
		require.NoError(t, err)
		require.NotNil(t, price)
		assert.NotZero(t, *price)
	})

	t.Run("get token balances", func(t *testing.T) {
		tokens, err := d.GetTokenBalances(context.Background(), "0x9321d8117e73b0c79035f0e87debcfd8dbb1d75a", true)
		require.NoError(t, err)
		assert.NotEmpty(t, tokens)
		for _, token := range tokens {
			assert.True(t, token.IsVerified || token.IsCore)
		}
	})

	t.Run("token not found", func(t *testing.T) {
		token, err := d.GetToken(context.Background(), constants.MainnetChainID, common.HexToAddress("0x0000000000000000000000000000000000000001"))
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "token not found")
		assert.Nil(t, token)
	})
}
