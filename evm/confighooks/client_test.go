package confighooks

import (
	"context"
	"crypto/ecdsa"
	"errors"
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":"0x1"}`)
	}))
	t.Cleanup(server.Close)
	ctx := t.Context()
	var dialed, verified bool
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
			Verify: func(gotCtx context.Context, client *ethclient.Client, endpoint string) error {
				require.Equal(t, ctx, gotCtx)
				require.Equal(t, server.URL, endpoint)
				verified = true
				_, err := client.ChainID(gotCtx)
				return err
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
	require.True(t, verified)
	require.Equal(t, common.HexToAddress(address), got.Address)
	require.Equal(t, []common.Address{got.Address}, got.Addresses)
	require.Equal(t, map[string]common.Address{"oracle": got.Address}, got.Map)
	require.NotNil(t, got.Key)
	require.Equal(t, 10*time.Second, got.Duration)
}

func TestNewEthClientHookWithoutVerification(t *testing.T) {
	t.Parallel()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	t.Cleanup(server.Close)
	hook := NewEthClientHook(ClientConfig{Context: t.Context()})
	result, err := hook(reflect.TypeOf(""), reflect.TypeOf(ethclient.Client{}), server.URL)
	require.NoError(t, err)
	client := result.(*ethclient.Client)
	t.Cleanup(client.Close)
	require.Zero(t, requests.Load(), "decoding must not choose an RPC probe for the application")
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
	t.Run("failed verification closes client", func(t *testing.T) {
		server := rpc.NewServer()
		t.Cleanup(server.Stop)
		client := ethclient.NewClient(rpc.DialInProc(server))
		t.Cleanup(client.Close)
		verificationErr := errors.New("required RPC capability unavailable")
		hook := NewEthClientHook(ClientConfig{
			Dial:   func(context.Context, string) (*ethclient.Client, error) { return client, nil },
			Verify: func(context.Context, *ethclient.Client, string) error { return verificationErr },
		})
		_, err := hook(reflect.TypeOf(""), reflect.TypeOf(ethclient.Client{}), "rpc-endpoint")
		require.ErrorIs(t, err, verificationErr)
		_, err = client.ChainID(t.Context())
		require.ErrorIs(t, err, rpc.ErrClientQuit)
	})
	t.Run("empty RPC leaves client unset", func(t *testing.T) {
		hook := NewEthClientHook(ClientConfig{})
		result, err := hook(reflect.TypeOf(""), reflect.TypeOf(ethclient.Client{}), "")
		require.NoError(t, err)
		require.Nil(t, result)
	})
}
