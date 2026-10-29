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

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
)

func TestL1ExplorerUsesInjectedTransportWithoutRetrying(t *testing.T) {
	attempts := 0
	client := NewL1ExplorerClient("https://explorer.example/custom", doerFunc(func(request *http.Request) (*http.Response, error) {
		attempts++
		require.Equal(t, http.MethodPost, request.Method)
		require.Equal(t, "/custom", request.URL.Path)
		var body map[string]string
		require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
		require.Equal(t, "txDetails", body["type"])
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("unavailable"))}, nil
	}))

	_, err := client.TxDetails(context.Background(), common.Hash{1})
	var status *HTTPError
	require.ErrorAs(t, err, &status)
	require.Equal(t, http.StatusServiceUnavailable, status.StatusCode)
	require.Equal(t, 1, attempts)
}

func TestL1TransactionDecodesLiveActionFixtures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		json       string
		assertions func(*testing.T, L1Transaction)
	}{
		{
			name: "USD class transfer success",
			json: `{"time":1787230637367,"user":"0x2321636903018992ae34f735b13054548b550bf6","action":{"type":"SystemUsdClassTransferAction","ntl":25650686,"toPerp":false},"block":1117072034,"hash":"0x0fe08320d827e078115a0442952aa200001b9b06732aff4ab3a92e73972bba62","error":null}`,
			assertions: func(t *testing.T, tx L1Transaction) {
				action, ok := tx.Action.(*L1USDClassTransferAction)
				require.True(t, ok)
				require.Equal(t, uint64(25_650_686), action.Ntl)
				require.False(t, action.ToPerp)
				require.Nil(t, tx.Error)
			},
		},
		{
			name: "API wallet approval",
			json: `{"time":1787231183388,"user":"0xdcbbfc41c414375d730208e4c1cba6081564bf8b","action":{"type":"approveAgent","signatureChainId":"0x0","hyperliquidChain":"Mainnet","agentAddress":"0x8ab6e71cd969177f8ccfdba2829c3785d13263ea","agentName":"trading-agent","nonce":0},"block":1117079742,"hash":"0xead8270fc2eed851ec5104429548be0000133ef55de1f7238ea0d26281e2b23c","error":null}`,
			assertions: func(t *testing.T, tx L1Transaction) {
				action, ok := tx.Action.(*L1ApproveAgentAction)
				require.True(t, ok)
				require.Equal(t, common.HexToAddress("0x8ab6e71cd969177f8ccfdba2829c3785d13263ea"), action.AgentAddress)
				require.Equal(t, "trading-agent", action.AgentName)
			},
		},
		{
			name: "builder fee approval",
			json: `{"time":1787231221443,"user":"0x9421c6a9c4394bed7cd1c7c1a6754523c51be791","action":{"type":"SystemApproveBuilderFeeAction","maxFeeRate":100,"builder":"0x2d105e9c16edd512c608432ddc20337f32a2c72c"},"block":1117080291,"hash":"0x6e323a3fa18498cb6fab0442954ae301010052253c87b79d11fae592608872b6","error":null}`,
			assertions: func(t *testing.T, tx L1Transaction) {
				action, ok := tx.Action.(*L1ApproveBuilderFeeAction)
				require.True(t, ok)
				require.Equal(t, uint64(100), action.MaxFeeRate)
				require.Equal(t, common.HexToAddress("0x2d105e9c16edd512c608432ddc20337f32a2c72c"), action.Builder)
			},
		},
		{
			name: "gateway send",
			json: `{"time":1787229538380,"user":"0x6b9e773128f453f5c2c60935ee2de2cbc5390a24","action":{"type":"SystemSendAssetAction","destination":"0xac21a983ff4e65613187432272d374adfded1b64","fromSubAccount":null,"sourceDexOrSpot":null,"destinationDexOrSpot":0,"token":0,"wei":24618000000},"block":1117056343,"hash":"0xb4dec3cb423cf722b658044294ed57000019dbb0dd3015f458a76f1e0130d10d","error":null}`,
			assertions: func(t *testing.T, tx L1Transaction) {
				action, ok := tx.Action.(*L1SendAssetAction)
				require.True(t, ok)
				require.Equal(t, common.HexToAddress("0xac21a983ff4e65613187432272d374adfded1b64"), action.Destination)
				require.Nil(t, action.FromSubAccount)
				require.Nil(t, action.SourceDexOrSpot)
				require.NotNil(t, action.DestinationDexOrSpot)
				require.Zero(t, *action.DestinationDexOrSpot)
				require.Equal(t, uint64(24_618_000_000), action.Wei)
			},
		},
		{
			name: "explicit execution error",
			json: `{"time":1787296603360,"user":"0x2321636903018992ae34f735b13054548b550bf6","action":{"type":"SystemUsdClassTransferAction","ntl":107887868,"toPerp":false},"block":1117997328,"hash":"0x1358441b051c51b714d20442a349100101005c00a01f7089b720ef6dc4102ba1","error":"Insufficient balance for withdrawal."}`,
			assertions: func(t *testing.T, tx L1Transaction) {
				require.NotNil(t, tx.Error)
				require.Equal(t, "Insufficient balance for withdrawal.", *tx.Error)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var tx L1Transaction
			require.NoError(t, json.Unmarshal([]byte(tt.json), &tx))
			require.NotEmpty(t, tx.Action.L1ActionType())
			tt.assertions(t, tx)
		})
	}
}

func TestL1TransactionPreservesUnknownAction(t *testing.T) {
	t.Parallel()

	var tx L1Transaction
	require.NoError(t, json.Unmarshal([]byte(`{
		"time":1,
		"user":"0x0000000000000000000000000000000000000001",
		"action":{"type":"newSystemAction","value":42},
		"block":2,
		"hash":"0x0000000000000000000000000000000000000000000000000000000000000003",
		"error":null
	}`), &tx))
	unknown, ok := tx.Action.(UnknownL1Action)
	require.True(t, ok)
	require.Equal(t, "newSystemAction", unknown.Type)
	require.JSONEq(t, `{"type":"newSystemAction","value":42}`, string(unknown.Raw))
}

func TestL1TransactionRequiresCompleteEvidenceEnvelope(t *testing.T) {
	t.Parallel()

	valid := map[string]any{
		"time":   int64(1),
		"user":   "0x0000000000000000000000000000000000000001",
		"action": map[string]any{"type": "SystemApproveBuilderFeeAction", "maxFeeRate": 100, "builder": "0x0000000000000000000000000000000000000002"},
		"block":  uint64(2),
		"hash":   "0x0000000000000000000000000000000000000000000000000000000000000003",
		"error":  nil,
	}
	for _, field := range []string{"time", "user", "action", "block", "hash", "error"} {
		t.Run(field, func(t *testing.T) {
			payload := make(map[string]any, len(valid)-1)
			for key, value := range valid {
				if key != field {
					payload[key] = value
				}
			}
			data, err := json.Marshal(payload)
			require.NoError(t, err)
			var transaction L1Transaction
			require.ErrorContains(t, json.Unmarshal(data, &transaction), field)
		})
	}
}

func TestL1TransactionRequiresMatchingActionFields(t *testing.T) {
	t.Parallel()

	data := []byte(`{
		"time":1,
		"user":"0x0000000000000000000000000000000000000001",
		"action":{"type":"SystemSendAssetAction","destination":"0x0000000000000000000000000000000000000002","fromSubAccount":null,"sourceDexOrSpot":null,"destinationDexOrSpot":null,"token":0},
		"block":2,
		"hash":"0x0000000000000000000000000000000000000000000000000000000000000003",
		"error":null
	}`)
	var transaction L1Transaction
	require.ErrorContains(t, json.Unmarshal(data, &transaction), `required field "wei" is missing`)
}

func TestL1ExplorerClientParsesUserAndTransactionDetails(t *testing.T) {
	t.Parallel()

	user := common.HexToAddress("0x9421c6a9c4394bed7cd1c7c1a6754523c51be791")
	hash := common.HexToHash("0x6e323a3fa18498cb6fab0442954ae301010052253c87b79d11fae592608872b6")
	tx := `{"time":1787231221443,"user":"0x9421c6a9c4394bed7cd1c7c1a6754523c51be791","action":{"type":"SystemApproveBuilderFeeAction","maxFeeRate":100,"builder":"0x2d105e9c16edd512c608432ddc20337f32a2c72c"},"block":1117080291,"hash":"0x6e323a3fa18498cb6fab0442954ae301010052253c87b79d11fae592608872b6","error":null}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]string
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		w.Header().Set("Content-Type", "application/json")
		switch request["type"] {
		case "userDetails":
			require.Equal(t, strings.ToLower(user.Hex()), request["user"])
			_, _ = w.Write([]byte(`{"type":"userDetails","txs":[` + tx + `]}`))
		case "txDetails":
			require.Equal(t, hash.Hex(), request["hash"])
			_, _ = w.Write([]byte(`{"type":"txDetails","tx":` + tx + `}`))
		default:
			http.Error(w, "unexpected request", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	client := NewL1ExplorerClient(server.URL, server.Client())
	userDetails, err := client.UserDetails(context.Background(), user)
	require.NoError(t, err)
	require.Len(t, userDetails.Transactions, 1)
	require.Equal(t, hash, userDetails.Transactions[0].Hash)

	txDetails, err := client.TxDetails(context.Background(), hash)
	require.NoError(t, err)
	require.Equal(t, hash, txDetails.Transaction.Hash)
	_, ok := txDetails.Transaction.Action.(*L1ApproveBuilderFeeAction)
	require.True(t, ok)
}

func TestL1ExplorerClientParsesCompleteBlockDetails(t *testing.T) {
	t.Parallel()

	const (
		height    = uint64(1_117_080_291)
		blockTime = int64(1_787_231_221_443)
	)
	blockHash := common.HexToHash("0x4f17e64e571da7bd6110b0d997abed5393dd2b08d12ab8c6d024971daeada881")
	txHash := common.HexToHash("0x6e323a3fa18498cb6fab0442954ae301010052253c87b79d11fae592608872b6")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Type   string `json:"type"`
			Height uint64 `json:"height"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		require.Equal(t, "blockDetails", request.Type)
		require.Equal(t, height, request.Height)
		_, _ = w.Write([]byte(`{
			"type":"blockDetails",
			"blockDetails":{
				"blockTime":1787231221443,
				"hash":"0x4f17e64e571da7bd6110b0d997abed5393dd2b08d12ab8c6d024971daeada881",
				"height":1117080291,
				"numTxs":1,
				"proposer":"0x5ac99df645f3414876c816caa18b2d234024b487",
				"txs":[{
					"time":1787231221443,
					"user":"0x9421c6a9c4394bed7cd1c7c1a6754523c51be791",
					"action":{"type":"SystemApproveBuilderFeeAction","maxFeeRate":100,"builder":"0x2d105e9c16edd512c608432ddc20337f32a2c72c"},
					"block":1117080291,
					"hash":"0x6e323a3fa18498cb6fab0442954ae301010052253c87b79d11fae592608872b6",
					"error":null
				}]
			}
		}`))
	}))
	defer server.Close()

	block, err := NewL1ExplorerClient(server.URL, server.Client()).BlockDetails(context.Background(), height)
	require.NoError(t, err)
	require.Equal(t, height, block.Height)
	require.Equal(t, blockTime, block.BlockTime)
	require.Equal(t, blockHash, block.Hash)
	require.Equal(t, uint64(1), block.NumTransactions)
	require.Len(t, block.Transactions, 1)
	require.Equal(t, txHash, block.Transactions[0].Hash)
}

func TestL1BlockDetailsRejectsPartialOrInconsistentSnapshots(t *testing.T) {
	t.Parallel()

	newBlock := func() map[string]any {
		return map[string]any{
			"blockTime": int64(1_000),
			"hash":      "0x0000000000000000000000000000000000000000000000000000000000000001",
			"height":    uint64(10),
			"numTxs":    uint64(1),
			"txs": []any{map[string]any{
				"time":   int64(1_000),
				"user":   "0x0000000000000000000000000000000000000001",
				"action": map[string]any{"type": "futureAction"},
				"block":  uint64(10),
				"hash":   "0x0000000000000000000000000000000000000000000000000000000000000002",
				"error":  nil,
			}},
		}
	}
	tests := []struct {
		name   string
		mutate func(map[string]any)
		want   string
	}{
		{
			name: "partial transaction list",
			mutate: func(block map[string]any) {
				block["numTxs"] = uint64(2)
			},
			want: "contains 1 transactions; numTxs is 2",
		},
		{
			name: "wrong transaction block",
			mutate: func(block map[string]any) {
				block["txs"].([]any)[0].(map[string]any)["block"] = uint64(9)
			},
			want: "reports block 9",
		},
		{
			name: "wrong transaction time",
			mutate: func(block map[string]any) {
				block["txs"].([]any)[0].(map[string]any)["time"] = int64(999)
			},
			want: "reports time 999",
		},
		{
			name: "duplicate transaction",
			mutate: func(block map[string]any) {
				transaction := block["txs"].([]any)[0]
				block["txs"] = append(block["txs"].([]any), transaction)
				block["numTxs"] = uint64(2)
			},
			want: "repeats transaction",
		},
		{
			name: "missing transaction list",
			mutate: func(block map[string]any) {
				delete(block, "txs")
			},
			want: "no transaction list",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wire := newBlock()
			tt.mutate(wire)
			data, err := json.Marshal(wire)
			require.NoError(t, err)
			var block L1BlockDetails
			require.ErrorContains(t, json.Unmarshal(data, &block), tt.want)
		})
	}
}

func TestL1ExplorerClientRejectsMismatchedBlockHeight(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"type":"blockDetails",
			"blockDetails":{
				"blockTime":1000,
				"hash":"0x0000000000000000000000000000000000000000000000000000000000000001",
				"height":11,
				"numTxs":0,
				"txs":[]
			}
		}`))
	}))
	defer server.Close()

	_, err := NewL1ExplorerClient(server.URL, server.Client()).BlockDetails(context.Background(), 10)
	require.ErrorContains(t, err, "returned block 11 for requested height 10")
}

func TestL1ExplorerClientSurfacesAPIErrorsAndMismatches(t *testing.T) {
	t.Parallel()

	hash := common.HexToHash("0x1234")
	tests := []struct {
		name string
		body string
		want string
		api  bool
	}{
		{name: "API error", body: `{"type":"error","message":"invalid block height: 0"}`, want: "invalid block height", api: true},
		{name: "unexpected type", body: `{"type":"blockDetails"}`, want: "want \"txDetails\""},
		{name: "missing transaction", body: `{"type":"txDetails","tx":null}`, want: "has no transaction"},
		{name: "malformed JSON", body: `{`, want: "decoding txDetails response"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			_, err := NewL1ExplorerClient(server.URL, server.Client()).TxDetails(context.Background(), hash)
			require.ErrorContains(t, err, tt.want)
			if tt.api {
				var apiErr *L1ExplorerError
				require.True(t, errors.As(err, &apiErr))
			}
		})
	}
}

func TestL1ExplorerClientRejectsOversizedResponse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", (4<<20)+1)))
	}))
	defer server.Close()

	_, err := NewL1ExplorerClient(server.URL, server.Client()).TxDetails(context.Background(), common.Hash{1})
	require.ErrorContains(t, err, "exceeds")
}

func TestL1ExplorerClientRejectsJSONShapedNonSuccessResponse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"type":"txDetails","tx":null}`))
	}))
	defer server.Close()

	_, err := NewL1ExplorerClient(server.URL, server.Client()).TxDetails(context.Background(), common.Hash{1})
	require.ErrorContains(t, err, "status 503")
}
