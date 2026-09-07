package zerion

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
)

// Reduced from the successful 2026-09-07 17:52:31 UTC positions capture for
// 0x97e4E814F615B7e3044a880360f2d9f41B13d623. Original response SHA-256:
// ddc1569e842cf0bf7219f93e1ec7629a454c917d1ca6980446b0086bc8a00442.
// The receipt is metadata only: 60.300075658389176566 UP is NOT the sUP balance.
const capturedSUPPosition = `{
  "id": "0x5b2193fdc451c1f847be09ca9d13a4bf60f8c86b-base-superform yield: up pool (#0)-deposit",
  "attributes": {
    "parent": null,
    "protocol": "Superform",
    "protocol_module": "yield",
    "pool_address": "0x2c71f70e2ec720ae061ae7e0316fc9654d94f417",
    "group_id": "c8a230aa898c7e6083fb09c79458e6d386abb231ffa585d2f65398fc6bf887d3",
    "name": "Superform Yield: UP Pool (#0)",
    "position_type": "deposit",
    "quantity": {"int": "60300075658389176566", "decimals": 18},
    "value": 3.235824728273075,
    "price": 0.0536620343,
    "fungible_info": {
      "name": "Superform",
      "symbol": "UP",
      "flags": {"verified": true},
      "implementations": [{"chain_id": "base", "address": "0x5b2193fdc451c1f847be09ca9d13a4bf60f8c86b", "decimals": 18}]
    },
    "flags": {"displayable": true, "is_trash": false},
    "updated_at": "2026-09-02T13:00:19Z",
    "receipt": {
      "fungible_info": {
        "name": "UP SuperVault",
        "symbol": "sUP",
        "icon": null,
        "flags": {"verified": true},
        "implementations": [{"chain_id": "base", "address": "0x2c71f70e2ec720ae061ae7e0316fc9654d94f417", "decimals": 18}]
      }
    }
  },
  "relationships": {
    "chain": {"data": {"type": "chains", "id": "base"}},
    "dapp": {"data": {"type": "dapps", "id": "superform"}}
  }
}`

func TestGetWalletPositionsPreservesDepositReceiptIdentity(t *testing.T) {
	z := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`{"data":[` + capturedSUPPosition + `]}`))
		require.NoError(t, err)
	})

	positions, err := z.GetWalletPositions(context.Background(), "0x97e4E814F615B7e3044a880360f2d9f41B13d623", WalletPositionsRequest{
		PositionsFilter: WalletPositionsNoFilter,
	})
	require.NoError(t, err)
	require.Len(t, positions, 1)
	position := positions[0]
	require.Equal(t, uint64(8453), position.ChainID)
	require.Equal(t, "0x5b2193fdc451c1f847be09ca9d13a4bf60f8c86b-base-superform yield: up pool (#0)-deposit", position.ID)
	require.Equal(t, "superform", position.Relationships.Dapp.Data.ID)
	require.Equal(t, "yield", position.Attributes.ProtocolModule)
	require.Equal(t, "Superform Yield: UP Pool (#0)", position.Attributes.Name)
	require.Equal(t, "c8a230aa898c7e6083fb09c79458e6d386abb231ffa585d2f65398fc6bf887d3", position.Attributes.GroupID)
	require.Nil(t, position.Attributes.Parent)
	require.Equal(t, "60300075658389176566", position.Attributes.Quantity.RawAmount.String())
	require.True(t, position.Attributes.HasValue)
	require.True(t, position.Attributes.HasPrice)
	require.Equal(t, 3.235824728273075, position.Attributes.Value)
	require.Equal(t, "2026-09-02T13:00:19Z", position.Attributes.UpdatedAt)
	require.True(t, position.Attributes.Flags.Displayable)
	require.False(t, position.Attributes.Flags.IsTrash)
	require.NotNil(t, position.Attributes.Receipt)
	require.Nil(t, position.Attributes.Receipt.NFTInfo)
	receipt := position.Attributes.Receipt.FungibleInfo
	require.NotNil(t, receipt)
	require.Equal(t, "sUP", receipt.Symbol)
	require.True(t, receipt.Flags.Verified)
	require.Len(t, receipt.Implementations, 1)
	require.Equal(t, uint64(8453), receipt.Implementations[0].ChainID)
	require.Equal(t, int32(18), receipt.Implementations[0].Decimals)
	require.Equal(t, common.HexToAddress("0x2c71f70e2ec720ae061ae7e0316fc9654d94f417"), receipt.Implementations[0].Address)
	require.Equal(t, position.Attributes.PoolAddress, receipt.Implementations[0].Address)
	require.NotEqual(t, receipt.Implementations[0].Address, position.Attributes.FungibleInfo.Implementations[0].Address)
}

func TestPositionAttributesDistinguishesUnknownAndZeroValues(t *testing.T) {
	for _, tc := range []struct {
		name, fields       string
		hasValue, hasPrice bool
	}{
		{name: "absent", fields: `{}`},
		{name: "null", fields: `{"value":null,"price":null}`},
		{name: "unpriced share", fields: `{"value":null,"price":0}`, hasPrice: true},
		{name: "known zero", fields: `{"value":0,"price":0}`, hasValue: true, hasPrice: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var position Position
			require.NoError(t, json.Unmarshal([]byte(capturedSUPPosition), &position))
			// Reuse the destination to ensure stale identities and prices disappear.
			require.NoError(t, json.Unmarshal([]byte(`{"attributes":`+tc.fields+`}`), &position))
			require.Equal(t, tc.hasValue, position.Attributes.HasValue)
			require.Equal(t, tc.hasPrice, position.Attributes.HasPrice)
			require.Zero(t, position.Attributes.Value)
			require.Zero(t, position.Attributes.Price)
			require.Empty(t, position.ID)
			require.Zero(t, position.ChainID)
			require.Nil(t, position.Attributes.Receipt)
			require.Empty(t, position.Attributes.GroupID)
			require.Zero(t, position.Attributes.PoolAddress)
			require.Nil(t, position.Relationships.Dapp.Data)
		})
	}
}

func TestPositionPreservesProviderDisplayability(t *testing.T) {
	for _, tc := range []struct {
		name, flags          string
		displayable, isTrash bool
	}{
		{name: "eligible", flags: `{"displayable":true,"is_trash":false}`, displayable: true},
		{name: "hidden receipt", flags: `{"displayable":false,"is_trash":false}`},
		{name: "trash", flags: `{"displayable":false,"is_trash":true}`, isTrash: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var position Position
			require.NoError(t, json.Unmarshal([]byte(capturedSUPPosition), &position))
			require.NoError(t, json.Unmarshal([]byte(`{"attributes":{"flags":`+tc.flags+`}}`), &position))
			require.NotNil(t, position.Attributes.Flags)
			require.Equal(t, tc.displayable, position.Attributes.Flags.Displayable)
			require.Equal(t, tc.isTrash, position.Attributes.Flags.IsTrash)
		})
	}
}

func TestPositionPreservesNFTReceiptAndOpaqueRelationships(t *testing.T) {
	// Synthetic coverage for the alternative NFT receipt documented at:
	// https://developers.zerion.io/api-reference/wallets/get-wallet-fungible-positions
	const body = `{
      "id":"opaque-position-id",
      "attributes": {
        "parent":"opaque-parent-id",
        "group_id":"opaque-group-id",
        "updated_at_block":50782336,
        "quantity":{"int":"5","decimals":0},
        "fungible_info":{"id":"opaque-fungible-id"},
        "receipt":{"nft_info":{
          "chain_id":"ethereum",
          "contract_address":"0xc36442b4a4522e871399cd717abdd847ab11fe88",
          "token_id":"115792089237316195423570985008687907853269984665640564039457584007913129639935"
        }}
      },
      "relationships": {
        "chain":{"data":{"id":"ethereum"}},
        "fungible":{"data":{"type":"fungibles","id":"opaque-fungible-id"}},
        "dapp":{"data":null}
      }
    }`
	var position Position
	require.NoError(t, json.Unmarshal([]byte(body), &position))
	require.Equal(t, "opaque-position-id", position.ID)
	require.Equal(t, "opaque-parent-id", *position.Attributes.Parent)
	require.Equal(t, "opaque-group-id", position.Attributes.GroupID)
	require.Equal(t, uint64(50782336), *position.Attributes.UpdatedAtBlock)
	require.Equal(t, "5", position.Attributes.Quantity.RawAmount.String())
	require.Zero(t, position.Attributes.Quantity.Decimals)
	require.Equal(t, "opaque-fungible-id", position.Attributes.FungibleInfo.ID)
	require.Equal(t, "opaque-fungible-id", position.Relationships.Fungible.Data.ID)
	require.Nil(t, position.Relationships.Dapp.Data)
	require.Nil(t, position.Attributes.Receipt.FungibleInfo)
	nft := position.Attributes.Receipt.NFTInfo
	require.Equal(t, "ethereum", nft.ChainSlug)
	require.Equal(t, "0xc36442b4a4522e871399cd717abdd847ab11fe88", nft.ContractAddress)
	require.Equal(t, "115792089237316195423570985008687907853269984665640564039457584007913129639935", nft.TokenID)
}
