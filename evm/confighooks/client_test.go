package confighooks

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/mitchellh/mapstructure"
	"github.com/stretchr/testify/require"
)

func TestCfgHooksDecode(t *testing.T) {
	t.Parallel()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":"0x1"}`)
	}))
	t.Cleanup(server.Close)
	ctx := t.Context()
	var dialed bool
	var got struct {
		Client    *ethclient.Client         `mapstructure:"rpc"`
		Address   common.Address            `mapstructure:"address"`
		Addresses []common.Address          `mapstructure:"addresses"`
		Map       map[string]common.Address `mapstructure:"map"`
		Key       *ecdsa.PrivateKey         `mapstructure:"key"`
		Duration  time.Duration             `mapstructure:"duration"`
	}
	decoder, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		Result: &got,
		DecodeHook: CfgHooks(ClientConfig{
			Context: ctx,
			Dial: func(gotCtx context.Context, endpoint string) (*ethclient.Client, error) {
				require.Equal(t, ctx, gotCtx)
				require.Equal(t, server.URL, endpoint)
				dialed = true
				return ethclient.DialContext(gotCtx, endpoint)
			},
		}),
	})
	require.NoError(t, err)
	const address = "0x1234567890123456789012345678901234567890"
	require.NoError(t, decoder.Decode(map[string]interface{}{
		"rpc":       server.URL,
		"address":   address,
		"addresses": []interface{}{address},
		"map":       map[string]interface{}{"oracle": address},
		"key":       "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
		"duration":  "10s",
	}))
	t.Cleanup(got.Client.Close)
	require.True(t, dialed)
	require.EqualValues(t, 1, requests.Load())
	require.Equal(t, common.HexToAddress(address), got.Address)
	require.Equal(t, []common.Address{got.Address}, got.Addresses)
	require.Equal(t, map[string]common.Address{"oracle": got.Address}, got.Map)
	require.NotNil(t, got.Key)
	require.Equal(t, 10*time.Second, got.Duration)
}

func TestNewEthClientHookHTTPProbe(t *testing.T) {
	t.Parallel()
	var requestMethod atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		requestMethod.Store(request.Method)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":"0x1"}`)
	}))
	t.Cleanup(server.Close)
	hook := NewEthClientHook(ClientConfig{})
	result, err := hook(reflect.TypeOf(""), reflect.TypeOf(ethclient.Client{}), server.URL)
	require.NoError(t, err)
	client := result.(*ethclient.Client)
	t.Cleanup(client.Close)
	require.Equal(t, "eth_chainId", requestMethod.Load())
}

type testHeaderSubscriber struct {
	subscribed   atomic.Bool
	unsubscribed chan struct{}
}

func (s *testHeaderSubscriber) NewHeads(ctx context.Context) (*rpc.Subscription, error) {
	notifier, supported := rpc.NotifierFromContext(ctx)
	if !supported {
		return nil, rpc.ErrNotificationsUnsupported
	}
	sub := notifier.CreateSubscription()
	s.subscribed.Store(true)
	go func() {
		<-sub.Err()
		close(s.unsubscribed)
	}()
	return sub, nil
}

func TestNewEthClientHookWSSProbe(t *testing.T) {
	t.Parallel()
	server := rpc.NewServer()
	t.Cleanup(server.Stop)
	service := &testHeaderSubscriber{unsubscribed: make(chan struct{})}
	require.NoError(t, server.RegisterName("eth", service))
	client := ethclient.NewClient(rpc.DialInProc(server))
	t.Cleanup(client.Close)
	hook := NewEthClientHook(ClientConfig{
		Context: t.Context(),
		Dial:    func(context.Context, string) (*ethclient.Client, error) { return client, nil },
	})
	result, err := hook(reflect.TypeOf(""), reflect.TypeOf(ethclient.Client{}), "wss://example.invalid")
	require.NoError(t, err)
	require.Same(t, client, result)
	require.True(t, service.subscribed.Load())
	select {
	case <-service.unsubscribed:
	case <-time.After(time.Second):
		t.Fatal("probe subscription was not unsubscribed")
	}
}

func TestNewEthClientHookErrors(t *testing.T) {
	t.Parallel()
	t.Run("dial receives context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		hook := NewEthClientHook(ClientConfig{
			Context: ctx,
			Dial: func(got context.Context, _ string) (*ethclient.Client, error) {
				require.Equal(t, ctx, got)
				return nil, got.Err()
			},
		})
		_, err := hook(reflect.TypeOf(""), reflect.TypeOf(ethclient.Client{}), "rpc-endpoint")
		require.ErrorIs(t, err, context.Canceled)
	})
	for _, tt := range []struct {
		name     string
		endpoint string
		errText  string
	}{
		{name: "HTTP", endpoint: "http://example.invalid", errText: "failed to get chain ID"},
		{name: "WSS", endpoint: "wss://example.invalid", errText: "failed to create subscription"},
	} {
		t.Run(tt.name+" failed probe closes client", func(t *testing.T) {
			server := rpc.NewServer()
			t.Cleanup(server.Stop)
			client := ethclient.NewClient(rpc.DialInProc(server))
			t.Cleanup(client.Close)
			hook := NewEthClientHook(ClientConfig{
				Dial: func(context.Context, string) (*ethclient.Client, error) { return client, nil },
			})
			result, err := hook(reflect.TypeOf(""), reflect.TypeOf(ethclient.Client{}), tt.endpoint)
			require.ErrorContains(t, err, tt.errText)
			require.Nil(t, result)
			_, err = client.ChainID(t.Context())
			require.ErrorIs(t, err, rpc.ErrClientQuit)
		})
		t.Run(tt.name+" probe receives canceled context", func(t *testing.T) {
			server := rpc.NewServer()
			t.Cleanup(server.Stop)
			client := ethclient.NewClient(rpc.DialInProc(server))
			t.Cleanup(client.Close)
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			hook := NewEthClientHook(ClientConfig{
				Context: ctx,
				Dial:    func(context.Context, string) (*ethclient.Client, error) { return client, nil },
			})
			result, err := hook(reflect.TypeOf(""), reflect.TypeOf(ethclient.Client{}), tt.endpoint)
			require.ErrorIs(t, err, context.Canceled)
			require.ErrorContains(t, err, tt.errText)
			require.Nil(t, result)
			_, err = client.ChainID(t.Context())
			require.ErrorIs(t, err, rpc.ErrClientQuit)
		})
	}
	t.Run("empty RPC leaves client unset", func(t *testing.T) {
		hook := NewEthClientHook(ClientConfig{})
		result, err := hook(reflect.TypeOf(""), reflect.TypeOf(ethclient.Client{}), "")
		require.NoError(t, err)
		require.Nil(t, result)
	})
}
