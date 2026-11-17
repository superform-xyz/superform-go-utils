package eventindexer

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/stretchr/testify/require"
)

type testClient struct {
	subscribe func(context.Context, ethereum.FilterQuery, chan<- types.Log) (ethereum.Subscription, error)
}

func (c testClient) SubscribeFilterLogs(ctx context.Context, q ethereum.FilterQuery, logs chan<- types.Log) (ethereum.Subscription, error) {
	return c.subscribe(ctx, q, logs)
}

type testSubscription struct {
	errors chan error
	done   chan struct{}
	once   sync.Once
}

func newTestSubscription() *testSubscription {
	return &testSubscription{errors: make(chan error, 1), done: make(chan struct{})}
}

func (s *testSubscription) Err() <-chan error { return s.errors }
func (s *testSubscription) Unsubscribe() {
	s.once.Do(func() { close(s.done) })
}

func receive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for subscription")
		var zero T
		return zero
	}
}

func TestReconnectReportsErrorsAndResumesDelivery(t *testing.T) {
	first := newTestSubscription()
	reconnected := newTestSubscription()
	wantFailure := errors.New("connection lost")
	wantReconnectFailure := errors.New("endpoint unavailable")
	streams := make(chan chan<- types.Log, 2)
	calls := 0
	client := testClient{subscribe: func(ctx context.Context, query ethereum.FilterQuery, logs chan<- types.Log) (ethereum.Subscription, error) {
		calls++
		switch calls {
		case 1:
			streams <- logs
			return first, nil
		case 2:
			return nil, wantReconnectFailure
		default:
			streams <- logs
			return reconnected, nil
		}
	}}
	reported := make(chan error, 2)
	i := New(map[uint64]EthClient{1: client}, WithReconnectInterval(time.Millisecond), WithOnError(func(err error) { reported <- err }))
	require.NoError(t, i.Start(context.Background()))
	t.Cleanup(i.Stop)
	output := make(chan RawEvent, 1)
	address := common.HexToAddress("0x1")
	topic := common.HexToHash("0x2")
	require.NoError(t, i.AddContract(context.Background(), map[uint64]common.Address{1: address}, []common.Hash{topic}, output))
	receive(t, streams)
	first.errors <- wantFailure
	require.ErrorIs(t, receive(t, reported), wantFailure)
	require.ErrorIs(t, receive(t, reported), wantReconnectFailure)
	logs := receive(t, streams)
	log := types.Log{Address: address, Topics: []common.Hash{topic}, BlockNumber: 42}
	logs <- log
	event := receive(t, output)
	require.Equal(t, RawEvent{ChainID: 1, Address: address, Raw: log}, event)
}

func TestStartCancellationStopsIndependentRegistrations(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	sub := newTestSubscription()
	i := New(map[uint64]EthClient{1: testClient{subscribe: func(context.Context, ethereum.FilterQuery, chan<- types.Log) (ethereum.Subscription, error) {
		return sub, nil
	}}})
	t.Cleanup(i.Stop)
	require.NoError(t, i.Start(ctx))
	output := make(chan RawEvent)
	require.NoError(t, i.AddContract(context.Background(), map[uint64]common.Address{1: {}}, nil, output))
	cancel()
	receive(t, sub.done)
	select {
	case _, open := <-output:
		require.False(t, open)
	case <-time.After(time.Second):
		t.Fatal("startup cancellation did not close the subscription output")
	}
}

func TestStopCancelsInitialSubscription(t *testing.T) {
	dialing := make(chan struct{})
	i := New(map[uint64]EthClient{1: testClient{subscribe: func(ctx context.Context, _ ethereum.FilterQuery, _ chan<- types.Log) (ethereum.Subscription, error) {
		close(dialing)
		<-ctx.Done()
		return nil, ctx.Err()
	}}})
	added := make(chan error, 1)
	go func() {
		added <- i.AddContract(context.Background(), map[uint64]common.Address{1: {}}, nil, make(chan RawEvent))
	}()
	receive(t, dialing)
	stopped := make(chan struct{})
	go func() { i.Stop(); close(stopped) }()
	receive(t, stopped)
	require.ErrorIs(t, receive(t, added), context.Canceled)
}

func TestRemoveOneChainKeepsSharedOutputOpen(t *testing.T) {
	subscriptions := map[uint64]*testSubscription{1: newTestSubscription(), 2: newTestSubscription()}
	streams := make(map[uint64]chan<- types.Log)
	clients := make(map[uint64]EthClient)
	for chainID, sub := range subscriptions {
		clients[chainID] = testClient{subscribe: func(ctx context.Context, query ethereum.FilterQuery, logs chan<- types.Log) (ethereum.Subscription, error) {
			streams[chainID] = logs
			return sub, nil
		}}
	}
	i := New(clients)
	t.Cleanup(i.Stop)
	address := common.HexToAddress("0x1")
	output := make(chan RawEvent, 1)
	require.NoError(t, i.AddContract(context.Background(), map[uint64]common.Address{1: address, 2: address}, nil, output))
	require.NoError(t, i.RemoveContract([]uint64{1}, address.Hex()))
	receive(t, subscriptions[1].done)
	streams[2] <- types.Log{BlockNumber: 7}
	require.Equal(t, uint64(2), receive(t, output).ChainID)
	require.NoError(t, i.RemoveContract([]uint64{2}, address.Hex()))
	receive(t, subscriptions[2].done)
	_, open := <-output
	require.False(t, open)
}

func TestRemoveContractCancelsBlockedDelivery(t *testing.T) {
	sub := newTestSubscription()
	var logs chan<- types.Log
	i := New(map[uint64]EthClient{1: testClient{subscribe: func(ctx context.Context, query ethereum.FilterQuery, stream chan<- types.Log) (ethereum.Subscription, error) {
		logs = stream
		return sub, nil
	}}})
	t.Cleanup(i.Stop)
	address := common.HexToAddress("0x1")
	output := make(chan RawEvent)
	require.NoError(t, i.AddContract(context.Background(), map[uint64]common.Address{1: address}, nil, output))
	logs <- types.Log{}
	removed := make(chan error, 1)
	go func() { removed <- i.RemoveContract(nil, address.Hex()) }()
	require.NoError(t, receive(t, removed))
	receive(t, sub.done)
	_, open := <-output
	require.False(t, open)
}

func TestStopCancelsReconnect(t *testing.T) {
	sub := newTestSubscription()
	reconnecting := make(chan struct{})
	reported := make(chan error, 2)
	calls := 0
	i := New(map[uint64]EthClient{1: testClient{subscribe: func(ctx context.Context, query ethereum.FilterQuery, logs chan<- types.Log) (ethereum.Subscription, error) {
		calls++
		if calls == 1 {
			return sub, nil
		}
		close(reconnecting)
		<-ctx.Done()
		return nil, ctx.Err()
	}}}, WithReconnectInterval(time.Millisecond), WithOnError(func(err error) { reported <- err }))
	require.NoError(t, i.Start(context.Background()))
	output := make(chan RawEvent)
	require.NoError(t, i.AddContract(context.Background(), map[uint64]common.Address{1: {}}, nil, output))
	sub.errors <- errors.New("disconnected")
	receive(t, reported)
	receive(t, reconnecting)
	stopped := make(chan struct{})
	go func() { i.Stop(); close(stopped) }()
	receive(t, stopped)
	_, open := <-output
	require.False(t, open)
	select {
	case err := <-reported:
		t.Fatalf("shutdown reported a subscription error: %v", err)
	default:
	}
}

func TestFailedMultiChainSetupUnsubscribesEarlierChains(t *testing.T) {
	sub := newTestSubscription()
	wantErr := errors.New("subscription rejected")
	calls := 0
	client := testClient{subscribe: func(ctx context.Context, query ethereum.FilterQuery, logs chan<- types.Log) (ethereum.Subscription, error) {
		calls++
		if calls == 1 {
			return sub, nil
		}
		return nil, wantErr
	}}
	i := New(map[uint64]EthClient{1: client, 2: client})
	t.Cleanup(i.Stop)
	output := make(chan RawEvent)
	err := i.AddContract(context.Background(), map[uint64]common.Address{1: {}, 2: {}}, nil, output)
	require.ErrorIs(t, err, wantErr)
	receive(t, sub.done)
	select {
	case <-output:
		t.Fatal("failed setup must leave the caller's channel open")
	default:
	}
}
