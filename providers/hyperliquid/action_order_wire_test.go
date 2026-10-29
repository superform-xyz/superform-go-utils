package hyperliquid

import (
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalTpslWireEncoding(t *testing.T) {
	entry := OrderWire{Asset: 1, IsBuy: true, LimitPrice: "100", Size: "1", OrderType: OrderType{Limit: &LimitOrderType{Tif: "Ioc"}}, Cloid: "0x00000000000000000000000000000001"}
	child := OrderWire{Asset: 1, LimitPrice: "90", Size: "1", ReduceOnly: true, OrderType: OrderType{Trigger: &TriggerOrderType{IsMarket: true, TriggerPrice: "95", TPSL: "sl"}}, Cloid: "0x00000000000000000000000000000002"}
	action := OrderAction{Type: "order", Orders: []OrderWire{entry, child}, Grouping: "normalTpsl"}
	raw, err := json.Marshal(action)
	require.NoError(t, err)
	require.Equal(t, `{"type":"order","orders":[{"a":1,"b":true,"p":"100","s":"1","r":false,"t":{"limit":{"tif":"Ioc"}},"c":"0x00000000000000000000000000000001"},{"a":1,"b":false,"p":"90","s":"1","r":true,"t":{"trigger":{"isMarket":true,"triggerPx":"95","tpsl":"sl"}},"c":"0x00000000000000000000000000000002"}],"grouping":"normalTpsl"}`, string(raw))
	// Manually encoded msgpack, in the reference SDK's documented field order.
	// This independently pins the omitted limit branch and all trigger fields.
	const prefix = "83a474797065a56f72646572a66f726465727392"
	const entryHex = "87a16101a162c3a170a3313030a173a131a172c2a17481a56c696d697481a3746966a3496f63a163d922"
	const childHex = "87a16101a162c2a170a23930a173a131a172c3a17481a77472696767657283a869734d61726b6574c3a9747269676765725078a23935a47470736ca2736ca163d922"
	const suffix = "a867726f7570696e67aa6e6f726d616c5470736c"
	encoded, err := EncodeAction(action)
	require.NoError(t, err)
	require.Equal(t, prefix+entryHex+hex.EncodeToString([]byte(entry.Cloid))+childHex+hex.EncodeToString([]byte(child.Cloid))+suffix, hex.EncodeToString(encoded))
}
