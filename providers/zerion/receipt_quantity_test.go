package zerion

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetWalletPositionsPreservesReceiptQuantity(t *testing.T) {
	// Synthetic September 25 receipt contract applied to the older capture;
	// these share units are deliberately distinct from the underlying quantity.
	const shares = "115792089237316195423570985008687907853269984665640564039457584007913129639935"
	body := strings.Replace(capturedSUPPosition, `"receipt": {`, `"receipt": {"quantity":{"int":"`+shares+`","decimals":8},`, 1)
	z := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, WalletPositionsNoFilter, r.URL.Query().Get("filter[positions]"))
		_, err := w.Write([]byte(`{"data":[` + body + `]}`))
		require.NoError(t, err)
	})
	positions, err := z.GetWalletPositions(context.Background(), "0x93AB0CD091DC8Dd513eBFd11972e64A2AC558552", WalletPositionsRequest{PositionsFilter: WalletPositionsNoFilter})
	require.NoError(t, err)
	require.Len(t, positions, 1)
	a := positions[0].Attributes
	require.NotNil(t, a.Receipt.Quantity)
	require.Equal(t, shares, a.Receipt.Quantity.RawAmount.String())
	require.Equal(t, int32(8), a.Receipt.Quantity.Decimals)
	require.Equal(t, "60300075658389176566", a.Quantity.RawAmount.String())
	require.Equal(t, int32(18), a.Quantity.Decimals)
	require.Equal(t, "sUP", a.Receipt.FungibleInfo.Symbol)
	require.NotEmpty(t, a.GroupID)
}

func TestReceiptQuantityUnknownIsNotZero(t *testing.T) {
	for _, body := range []string{
		`{}`, `{"quantity":null}`, `{"quantity":{}}`,
		`{"quantity":{"int":null,"decimals":18}}`,
		`{"quantity":{"int":"","decimals":18}}`,
		`{"quantity":{"int":"   ","decimals":18}}`,
		`{"quantity":{"decimals":18}}`,
		`{"quantity":{"int":"7"}}`,
		`{"quantity":{"int":"7","decimals":null}}`,
	} {
		t.Run(body, func(t *testing.T) {
			var receipt Receipt
			require.NoError(t, json.Unmarshal([]byte(`{"quantity":{"int":"9","decimals":8},"fungible_info":{"symbol":"old"}}`), &receipt))
			require.NoError(t, json.Unmarshal([]byte(body), &receipt))
			require.Nil(t, receipt.Quantity)
			require.Nil(t, receipt.FungibleInfo, "reused destinations must not retain stale metadata")
		})
	}
	var receipt Receipt
	require.NoError(t, json.Unmarshal([]byte(`{"quantity":{"int":"0","decimals":0}}`), &receipt))
	require.NotNil(t, receipt.Quantity)
	require.Zero(t, receipt.Quantity.RawAmount.Sign())
	require.Zero(t, receipt.Quantity.Decimals)
}

func TestReceiptQuantityRejectsMalformedValues(t *testing.T) {
	for _, quantity := range []string{
		`{"int":"abc","decimals":18}`, `{"int":"1.5","decimals":18}`,
		`{"int":7,"decimals":18}`, `{"int":"7","decimals":1.5}`,
		`{"int":"7","decimals":2147483648}`,
	} {
		t.Run(quantity, func(t *testing.T) {
			var receipt Receipt
			require.Error(t, json.Unmarshal([]byte(`{"quantity":`+quantity+`}`), &receipt))
		})
	}
}
