package zerion

import (
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/superform-xyz/superform-go-utils/utils/constants"
)

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	require.NoError(t, err)
	return u
}

// Shape follows developers.zerion.io/webhooks.md; implementations and sender/recipient follow the OpenAPI transfer schema.
const confirmedCallback = `{
  "data": {
    "id": "cb-1",
    "type": "callback",
    "attributes": {"timestamp": "2024-07-31T00:17:36Z", "callback_url": "https://example.com/webhooks/zerion", "address": "0xAbC0000000000000000000000000000000000001"},
    "relationships": {"subscription": {"type": "tx-subscriptions", "id": "sub-1"}}
  },
  "included": [{
    "type": "transactions",
    "id": "tx-1",
    "attributes": {
      "operation_type": "receive",
      "hash": "0xhash1",
      "mined_at": "2024-07-31T00:17:35Z",
      "mined_at_block": 7490818,
      "sent_from": "0xsender",
      "sent_to": "0xabc0000000000000000000000000000000000001",
      "status": "confirmed",
      "nonce": 1,
      "fee": {"quantity": {"int": "21000", "decimals": 18, "float": 0.000000000000021}},
      "transfers": [
        {
          "direction": "in",
          "quantity": {"int": "420000000000000", "decimals": 18, "float": 999.5, "numeric": "0.00042"},
          "value": null, "price": null,
          "sender": "0xsender", "recipient": "0xabc0000000000000000000000000000000000001", "act_id": "act-1",
          "fungible_info": {"id": "eth", "name": "Ethereum", "symbol": "ETH", "implementations": [
            {"chain_id": "ethereum", "address": null, "decimals": 18},
            {"chain_id": "base", "address": null, "decimals": 18}
          ]}
        },
        {
          "direction": "in",
          "quantity": {"int": "1500000", "decimals": 6, "float": 1.5},
          "sender": "0xsender", "recipient": "0xabc0000000000000000000000000000000000001", "act_id": "act-1",
          "fungible_info": {"id": "usdc", "symbol": "USDC", "implementations": [
            {"chain_id": "ethereum", "address": "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48", "decimals": 6},
            {"chain_id": "base", "address": "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913", "decimals": 6},
            {"chain_id": "some-new-chain", "address": "0x1111111111111111111111111111111111111111", "decimals": 6}
          ]}
        }
      ],
      "approvals": [],
      "flags": {"is_trash": false}
    },
    "relationships": {"chain": {"type": "chains", "id": "base"}, "dapp": {"type": "dapps", "id": ""}}
  }]
}`

func TestParseCallbackConfirmed(t *testing.T) {
	cb, err := ParseCallback([]byte(confirmedCallback))
	require.NoError(t, err)
	assert.Equal(t, "0xabc0000000000000000000000000000000000001", cb.WalletAddress())
	assert.Equal(t, "sub-1", cb.SubscriptionID())
	require.Len(t, cb.Included, 1)

	tx := cb.Included[0]
	assert.Equal(t, "0xhash1", tx.Attributes.Hash)
	assert.Equal(t, "confirmed", tx.Attributes.Status)
	assert.Equal(t, "receive", tx.Attributes.OperationType)
	assert.False(t, tx.Attributes.Deleted)
	assert.False(t, tx.IsTrash())
	require.NotNil(t, tx.Attributes.MinedAt)
	assert.Equal(t, time.Date(2024, 7, 31, 0, 17, 35, 0, time.UTC), *tx.Attributes.MinedAt)
	require.NotNil(t, tx.Attributes.MinedAtBlock)
	assert.Equal(t, uint64(7490818), *tx.Attributes.MinedAtBlock)
	assert.Equal(t, "base", tx.ChainSlug())
	chainID, ok := tx.ChainID()
	require.True(t, ok)
	assert.Equal(t, constants.BaseChainID, chainID)

	transfers, err := tx.DecodeTransfers()
	require.NoError(t, err)
	require.Len(t, transfers, 2)

	eth := transfers[0]
	assert.Equal(t, "in", eth.Direction)
	assert.Equal(t, "420000000000000", eth.Quantity.RawAmount.String(), "quantity.float must be ignored")
	assert.Equal(t, "0xsender", eth.Sender)
	assert.Equal(t, "act-1", eth.ActID)
	addr, decimals, ok := eth.TokenAddress(constants.BaseChainID)
	require.True(t, ok)
	assert.Equal(t, "0xeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", addr)
	assert.Equal(t, 18, decimals)

	usdc := transfers[1]
	addr, decimals, ok = usdc.TokenAddress(constants.BaseChainID)
	require.True(t, ok)
	assert.Equal(t, "0x833589fcd6edb6e08f4c7c32d4f71b54bda02913", addr)
	assert.Equal(t, 6, decimals)
	_, _, ok = usdc.TokenAddress(constants.ArbitrumChainID)
	assert.False(t, ok, "no implementation for chain")
	_, _, ok = usdc.TokenAddress(0)
	assert.False(t, ok, "unknown slug never matches")
}

func TestParseCallbackChainShapes(t *testing.T) {
	for name, rel := range map[string]string{
		"chain.id":              `{"chain": {"type": "chains", "id": "Arbitrum"}}`,
		"chain.data.id":         `{"chain": {"links": {"related": "x"}, "data": {"type": "chains", "id": "arbitrum"}}}`,
		"data.id wins":          `{"chain": {"id": "base", "data": {"type": "chains", "id": "arbitrum"}}}`,
		"empty data falls back": `{"chain": {"id": "arbitrum", "data": {"type": "chains", "id": ""}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			cb, err := ParseCallback([]byte(`{"data":{"attributes":{"address":"0x1"}},"included":[{"attributes":{"hash":"0x1"},"relationships":` + rel + `}]}`))
			require.NoError(t, err)
			assert.Equal(t, "arbitrum", cb.Included[0].ChainSlug())
			id, ok := cb.Included[0].ChainID()
			require.True(t, ok)
			assert.Equal(t, constants.ArbitrumChainID, id)
		})
	}

	t.Run("subscription data.id", func(t *testing.T) {
		cb, err := ParseCallback([]byte(`{"data":{"relationships":{"subscription":{"data":{"type":"subscription","id":"sub-2"}}}}}`))
		require.NoError(t, err)
		assert.Equal(t, "sub-2", cb.SubscriptionID())
	})

	t.Run("unknown slug", func(t *testing.T) {
		cb, err := ParseCallback([]byte(`{"included":[{"relationships":{"chain":{"id":"not-a-chain"}}}]}`))
		require.NoError(t, err)
		assert.Equal(t, "not-a-chain", cb.Included[0].ChainSlug())
		_, ok := cb.Included[0].ChainID()
		assert.False(t, ok)
	})

	t.Run("odd relationship shape decodes empty", func(t *testing.T) {
		cb, err := ParseCallback([]byte(`{"included":[{"relationships":{"chain":"base"}}]}`))
		require.NoError(t, err)
		assert.Empty(t, cb.Included[0].ChainSlug())
	})
}

func TestParseCallbackRollbackOnly(t *testing.T) {
	body := `{"data":{"id":"cb-2","type":"callback","attributes":{"timestamp":"2024-07-31T00:20:00Z","callback_url":"https://x","address":"0xABC"}},
	  "included":[{"type":"transactions","id":"tx-1","attributes":{"hash":"0xhash1","deleted":true}}]}`
	cb, err := ParseCallback([]byte(body))
	require.NoError(t, err)
	assert.Equal(t, "0xabc", cb.WalletAddress())
	assert.Empty(t, cb.SubscriptionID())
	require.Len(t, cb.Included, 1)
	tx := cb.Included[0]
	assert.True(t, tx.Attributes.Deleted)
	assert.Equal(t, "0xhash1", tx.Attributes.Hash)
	assert.False(t, tx.IsTrash(), "missing flags are not spam")
	assert.Empty(t, tx.ChainSlug())
	assert.Nil(t, tx.Attributes.MinedAt)
	assert.Nil(t, tx.Attributes.MinedAtBlock)
	transfers, err := tx.DecodeTransfers()
	require.NoError(t, err)
	assert.Empty(t, transfers)
}

func TestCallbackIsTrash(t *testing.T) {
	for name, tc := range map[string]struct {
		flags string
		want  bool
	}{
		"true":          {`,"flags":{"is_trash":true}`, true},
		"false":         {`,"flags":{"is_trash":false}`, false},
		"missing flags": {``, false},
		"null flags":    {`,"flags":null`, false},
		"empty flags":   {`,"flags":{}`, false},
	} {
		t.Run(name, func(t *testing.T) {
			cb, err := ParseCallback([]byte(`{"included":[{"attributes":{"hash":"0x1"` + tc.flags + `}}]}`))
			require.NoError(t, err)
			assert.Equal(t, tc.want, cb.Included[0].IsTrash())
		})
	}
}

func TestCallbackTransfersDecodedLazily(t *testing.T) {
	for name, transfers := range map[string]string{
		"object":          `{"direction":"in"}`,
		"string":          `"oops"`,
		"bad quantity":    `[{"direction":"in","quantity":{"int":"1.5","decimals":18}}]`,
		"quantity number": `[{"direction":"in","quantity":{"int":15,"decimals":18}}]`,
		"bad impl":        `[{"fungible_info":{"implementations":[{"chain_id":"base","decimals":"x"}]}}]`,
	} {
		t.Run(name, func(t *testing.T) {
			cb, err := ParseCallback([]byte(`{"included":[{"attributes":{"hash":"0x1","flags":{"is_trash":true},"transfers":` + transfers + `}}]}`))
			require.NoError(t, err)
			assert.True(t, cb.Included[0].IsTrash())
			_, err = cb.Included[0].DecodeTransfers()
			assert.Error(t, err)
		})
	}
}

func TestCallbackMinedAtDefensive(t *testing.T) {
	for name, tc := range map[string]struct {
		attrs     string
		wantTime  bool
		wantBlock *uint64
	}{
		"string block":   {`"mined_at":"2024-07-31T00:17:35.5Z","mined_at_block":"42"`, true, ptr(uint64(42))},
		"garbage time":   {`"mined_at":"soon","mined_at_block":42`, false, ptr(uint64(42))},
		"numeric time":   {`"mined_at":1722385055,"mined_at_block":-1`, false, nil},
		"null both":      {`"mined_at":null,"mined_at_block":null`, false, nil},
		"fractional blk": {`"mined_at_block":4.2`, false, nil},
	} {
		t.Run(name, func(t *testing.T) {
			cb, err := ParseCallback([]byte(`{"included":[{"attributes":{"hash":"0x1",` + tc.attrs + `}}]}`))
			require.NoError(t, err)
			a := cb.Included[0].Attributes
			assert.Equal(t, tc.wantTime, a.MinedAt != nil)
			assert.Equal(t, tc.wantBlock, a.MinedAtBlock)
			assert.Equal(t, "0x1", a.Hash)
		})
	}
}

func TestCallbackTransferTokenAddressDecimalsFallback(t *testing.T) {
	cb, err := ParseCallback([]byte(`{"included":[{"attributes":{"transfers":[
	  {"quantity":{"int":"5","decimals":8},"fungible_info":{"implementations":[{"chain_id":"ethereum","address":"","decimals":null}]}}
	]}}]}`))
	require.NoError(t, err)
	transfers, err := cb.Included[0].DecodeTransfers()
	require.NoError(t, err)
	addr, decimals, ok := transfers[0].TokenAddress(constants.MainnetChainID)
	require.True(t, ok)
	assert.Equal(t, "0xeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", addr)
	assert.Equal(t, 8, decimals)

	_, _, ok = CallbackTransfer{}.TokenAddress(constants.MainnetChainID)
	assert.False(t, ok, "nft or missing fungible_info")
}

func TestParseCallbackInvalidJSON(t *testing.T) {
	_, err := ParseCallback([]byte(`{"data":`))
	assert.Error(t, err)
}

func ptr[T any](v T) *T { return &v }
