package eventindexer

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// EthClient supplies live Ethereum log subscriptions.
type EthClient interface {
	SubscribeFilterLogs(context.Context, ethereum.FilterQuery, chan<- types.Log) (ethereum.Subscription, error)
}

// EventIndexer manages live contract subscriptions and reconnects failed ones.
type EventIndexer interface {
	Start(context.Context) error
	Stop()
	AddContract(context.Context, map[uint64]common.Address, []common.Hash, chan RawEvent) error
	RemoveContract([]uint64, string) error
}

// RawEvent identifies the chain and contract that produced a log.
type RawEvent struct {
	ChainID uint64
	Address common.Address
	Raw     types.Log
}

// Option configures the shared indexer.
type Option func(*indexer)

// WithReconnectInterval sets how often failed subscriptions are retried.
func WithReconnectInterval(interval time.Duration) Option {
	return func(i *indexer) { i.reconnectInterval = interval }
}

// WithOnError handles subscription failures and failed reconnects. The callback
// runs on the subscription goroutine, outside the indexer's lock.
func WithOnError(callback func(error)) Option {
	return func(i *indexer) { i.onError = callback }
}

type contractInfo struct {
	chainID   uint64
	address   common.Address
	events    []common.Hash
	eventChan chan RawEvent
	ctx       context.Context
	cancel    context.CancelFunc
	retry     chan struct{}
	done      chan struct{}
}

type indexer struct {
	clients           map[uint64]EthClient
	contracts         map[string]*contractInfo
	outputs           map[chan RawEvent]int
	reconnectInterval time.Duration
	onError           func(error)
	mu                sync.Mutex
	wg                sync.WaitGroup
	stop              chan struct{}
	ctx               context.Context
	cancel            context.CancelFunc
	started           bool
	stopped           bool
}

var _ EventIndexer = (*indexer)(nil)

// New creates an indexer with a 30-second reconnect interval and no logging.
func New(clients map[uint64]EthClient, options ...Option) EventIndexer {
	ctx, cancel := context.WithCancel(context.Background())
	i := &indexer{
		clients:           clients,
		contracts:         make(map[string]*contractInfo),
		outputs:           make(map[chan RawEvent]int),
		reconnectInterval: 30 * time.Second,
		stop:              make(chan struct{}),
		ctx:               ctx,
		cancel:            cancel,
	}
	for _, option := range options {
		option(i)
	}
	return i
}

// Start ties the indexer's lifetime to ctx. Cancelling it stops all registrations.
func (i *indexer) Start(ctx context.Context) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.stopped {
		return fmt.Errorf("event indexer is stopped")
	}
	if i.started {
		return nil
	}
	if i.reconnectInterval <= 0 {
		return fmt.Errorf("reconnect interval must be positive")
	}
	i.started = true
	i.wg.Add(1)
	go i.watch(ctx)
	return nil
}

func (i *indexer) Stop() {
	i.shutdown()
	i.wg.Wait()
}

func (i *indexer) shutdown() {
	// Cancel before taking the lock: AddContract may hold it while dialing.
	i.cancel()
	i.mu.Lock()
	if !i.stopped {
		i.stopped = true
		close(i.stop)
		for _, contract := range i.contracts {
			contract.cancel()
		}
	}
	i.mu.Unlock()
}

// AddContract subscribes to each chain before returning. The indexer closes
// eventChan when the last registration using it is removed or stops.
func (i *indexer) AddContract(ctx context.Context, addresses map[uint64]common.Address, events []common.Hash, eventChan chan RawEvent) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.stopped {
		return fmt.Errorf("event indexer is stopped")
	}
	for chainID, address := range addresses {
		if _, ok := i.clients[chainID]; !ok {
			return fmt.Errorf("no client found for chain %d", chainID)
		}
		if _, exists := i.contracts[getContractKey(chainID, address.Hex(), events)]; exists {
			return fmt.Errorf("contract already being watched on chain %d", chainID)
		}
	}

	type subscription struct {
		contract *contractInfo
		logs     chan types.Log
		sub      ethereum.Subscription
	}
	var subscriptions []subscription
	for chainID, address := range addresses {
		contractCtx, cancel := context.WithCancel(ctx)
		stopCancellation := context.AfterFunc(i.ctx, cancel)
		cancelRegistration := func() {
			stopCancellation()
			cancel()
		}
		contract := &contractInfo{
			chainID: chainID, address: address, events: append([]common.Hash(nil), events...),
			eventChan: eventChan, ctx: contractCtx, cancel: cancelRegistration,
			retry: make(chan struct{}, 1), done: make(chan struct{}),
		}
		logs, sub, err := i.subscribe(contract)
		if err != nil {
			cancelRegistration()
			for _, existing := range subscriptions {
				existing.contract.cancel()
				existing.sub.Unsubscribe()
			}
			return fmt.Errorf("failed to setup subscription for chain %d: %w", chainID, err)
		}
		subscriptions = append(subscriptions, subscription{contract, logs, sub})
	}
	for _, subscription := range subscriptions {
		contract := subscription.contract
		i.contracts[getContractKey(contract.chainID, contract.address.Hex(), contract.events)] = contract
		i.outputs[eventChan]++
		i.wg.Add(1)
		go i.processLogs(contract, subscription.logs, subscription.sub)
	}
	return nil
}

func (i *indexer) RemoveContract(chainIDs []uint64, address string) error {
	i.mu.Lock()
	var removed []*contractInfo
	for key, contract := range i.contracts {
		if contract.address.Hex() != address || (len(chainIDs) > 0 && !slices.Contains(chainIDs, contract.chainID)) {
			continue
		}
		delete(i.contracts, key)
		contract.cancel()
		removed = append(removed, contract)
	}
	i.mu.Unlock()
	for _, contract := range removed {
		<-contract.done
	}
	return nil
}

func (i *indexer) watch(ctx context.Context) {
	defer i.wg.Done()
	ticker := time.NewTicker(i.reconnectInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			i.shutdown()
			return
		case <-i.stop:
			return
		case <-ticker.C:
			i.mu.Lock()
			for _, contract := range i.contracts {
				select {
				case contract.retry <- struct{}{}:
				default:
				}
			}
			i.mu.Unlock()
		}
	}
}

func (i *indexer) subscribe(contract *contractInfo) (chan types.Log, ethereum.Subscription, error) {
	logs := make(chan types.Log)
	sub, err := i.clients[contract.chainID].SubscribeFilterLogs(contract.ctx, ethereum.FilterQuery{
		Addresses: []common.Address{contract.address},
		Topics:    [][]common.Hash{contract.events},
	}, logs)
	return logs, sub, err
}

func (i *indexer) processLogs(contract *contractInfo, logs chan types.Log, sub ethereum.Subscription) {
	defer i.wg.Done()
	defer close(contract.done)
	defer func() {
		contract.cancel()
		if sub != nil {
			sub.Unsubscribe()
		}
		i.mu.Lock()
		defer i.mu.Unlock()
		key := getContractKey(contract.chainID, contract.address.Hex(), contract.events)
		if i.contracts[key] == contract {
			delete(i.contracts, key)
		}
		i.outputs[contract.eventChan]--
		if i.outputs[contract.eventChan] == 0 {
			close(contract.eventChan)
			delete(i.outputs, contract.eventChan)
		}
	}()

	subErrors := sub.Err()
	for {
		select {
		case <-contract.ctx.Done():
			return
		case <-i.stop:
			return
		case <-contract.retry:
			if sub == nil {
				var err error
				logs, sub, err = i.subscribe(contract)
				if err != nil {
					logs = nil
					i.reportError(contract, "reconnecting subscription", err)
				} else {
					subErrors = sub.Err()
				}
			}
		case err := <-subErrors:
			sub.Unsubscribe()
			sub, logs, subErrors = nil, nil, nil
			i.reportError(contract, "subscription failed", err)
		case log := <-logs:
			select {
			case contract.eventChan <- RawEvent{ChainID: contract.chainID, Address: contract.address, Raw: log}:
			case <-contract.ctx.Done():
				return
			case <-i.stop:
				return
			}
		}
	}
}

func (i *indexer) reportError(contract *contractInfo, operation string, err error) {
	if i.onError != nil && contract.ctx.Err() == nil {
		if err == nil {
			err = fmt.Errorf("subscription closed")
		}
		i.onError(fmt.Errorf("%s on chain %d for %s: %w", operation, contract.chainID, contract.address.Hex(), err))
	}
}

func getContractKey(chainID uint64, address string, events []common.Hash) string {
	eventStrings := make([]string, len(events))
	for j, event := range events {
		eventStrings[j] = event.Hex()
	}
	return fmt.Sprintf("%d_%s_%s", chainID, address, strings.Join(eventStrings, ","))
}
