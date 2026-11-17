package zerion

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetWalletPositionsPreservesProxyPresence(t *testing.T) {
	for _, tc := range []struct {
		name, field string
		want        *PositionProxy
	}{
		{name: "absent"},
		{name: "null", field: `,"proxy":null`},
		{name: "empty object", field: `,"proxy":{}`, want: &PositionProxy{}},
		{name: "empty address", field: `,"proxy":{"address":""}`, want: &PositionProxy{}},
		{
			name:  "owned Safe",
			field: `,"proxy":{"address":"0xa879f694fa995058208408d27488775f9ef00687"}`,
			want:  &PositionProxy{Address: "0xa879f694fa995058208408d27488775f9ef00687"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				_, err := w.Write([]byte(`{"data":[{"attributes":{"position_type":"wallet"` + tc.field + `},"relationships":{"chain":{"data":{"id":"base"}}}}]}`))
				require.NoError(t, err)
			})
			positions, err := client.GetWalletPositions(context.Background(), "0xEfbC53e0AFdc9f649Bc1Ab692461df57E70A560f", WalletPositionsRequest{})
			require.NoError(t, err)
			require.Len(t, positions, 1, "the SDK preserves proxy positions for consumer policy")
			require.Equal(t, tc.want, positions[0].Attributes.Proxy)
			require.Equal(t, uint64(8453), positions[0].ChainID)

			position := Position{Attributes: PositionAttributes{Proxy: &PositionProxy{Address: "old"}}}
			require.NoError(t, json.Unmarshal([]byte(`{"attributes":{"position_type":"wallet"`+tc.field+`}}`), &position))
			require.Equal(t, tc.want, position.Attributes.Proxy, "reused destinations must not retain a stale proxy")
		})
	}
}
