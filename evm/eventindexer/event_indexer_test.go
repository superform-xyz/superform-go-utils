package eventindexer

import (
	"context"
	"math/big"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient/simulated"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/superform-xyz/superform-go-utils/evm/eventindexer/test"
	"github.com/superform-xyz/superform-go-utils/evm/internal/testutil"
)

const chainId = int64(1337)

// createSimulatedBackend creates a simulated backend with the basic setup
func createSimulatedBackend(t *testing.T) (*simulated.Backend, common.Address, *test.SimpleEvent, *bind.TransactOpts) {
	t.Helper()

	// Create simulated backend with 2 accounts
	cfg := &testingHelpers.SimBackendConfig{
		ChainID:     big.NewInt(testingHelpers.DefaultChainID),
		NumAccounts: 2,
	}
	backend, accounts := testingHelpers.NewSimulatedBackendWithAccounts(t, cfg)
	t.Cleanup(func() { require.NoError(t, backend.Close()) })

	// Get the two accounts
	var userAuth, deployerAuth *bind.TransactOpts
	for _, acc := range accounts {
		if userAuth == nil {
			userAuth = acc.Auth
		} else {
			deployerAuth = acc.Auth
		}
	}

	// Deploy SimpleEvent contract
	addr, contract, err := testingHelpers.DeployContract(
		context.Background(),
		t,
		backend,
		deployerAuth,
		test.DeploySimpleEvent,
	)
	require.NoError(t, err)

	// Get the current value
	val, err := contract.GetValue(&bind.CallOpts{})
	require.NoError(t, err)
	require.Equal(t, "0", val.String())

	return backend, addr, contract, userAuth
}

func Test_eventIndexer_E2E(t *testing.T) {
	t.Run("subscribing to single event", func(t *testing.T) {
		backend, seAddr, se, userAuth := createSimulatedBackend(t)

		// Create event indexer
		indexer := New(map[uint64]EthClient{
			uint64(chainId): backend.Client(),
		})

		// Start event indexer
		ctx := context.Background()
		err := indexer.Start(ctx)
		require.NoError(t, err)
		defer indexer.Stop()

		// Create event channel
		eventChan := make(chan RawEvent)

		addresses := map[uint64]common.Address{
			uint64(chainId): seAddr,
		}

		// Subscribe to ValueChanged event
		err = indexer.AddContract(
			ctx,
			addresses,
			[]common.Hash{crypto.Keccak256Hash([]byte("ValueChanged(address,uint256)"))},
			eventChan,
		)
		require.NoError(t, err)

		// Set value to trigger event
		tx, err := se.SetValue(userAuth, big.NewInt(100))
		require.NoError(t, err)
		backend.Commit()

		// Wait for event
		select {
		case event := <-eventChan:
			assert.Equal(t, uint64(chainId), event.ChainID)
			assert.Equal(t, seAddr, event.Address)
			assert.Equal(t, tx.Hash(), event.Raw.TxHash)
			// Verify event topic matches the event signature hash
			expectedTopic := crypto.Keccak256Hash([]byte("ValueChanged(address,uint256)"))
			assert.Equal(t, expectedTopic, event.Raw.Topics[0], "event topic should match signature hash")
		case <-time.After(5 * time.Second):
			t.Fatal("timeout waiting for event")
		}

		// Unsubscribe from event
		err = indexer.RemoveContract([]uint64{uint64(chainId)}, seAddr.Hex())
		require.NoError(t, err)
	})

	t.Run("subscribing to multiple events", func(t *testing.T) {
		backend, seAddr, se, userAuth := createSimulatedBackend(t)

		// Create event indexer
		indexer := New(map[uint64]EthClient{
			uint64(chainId): backend.Client(),
		})

		// Start event indexer
		ctx := context.Background()
		err := indexer.Start(ctx)
		require.NoError(t, err)
		defer indexer.Stop()

		// Create event channels
		eventChan := make(chan RawEvent)

		addresses := map[uint64]common.Address{
			uint64(chainId): seAddr,
		}

		// Subscribe to both events
		err = indexer.AddContract(
			ctx,
			addresses,
			[]common.Hash{
				crypto.Keccak256Hash([]byte("ValueChanged(address,uint256)")),
				crypto.Keccak256Hash([]byte("MultipleValuesChanged(address,uint256[])")),
			},
			eventChan,
		)
		require.NoError(t, err)

		// Set multiple values to trigger both events
		values := []*big.Int{big.NewInt(100), big.NewInt(200), big.NewInt(300)}
		tx, err := se.SetMultipleValues(userAuth, values)
		require.NoError(t, err)
		backend.Commit()

		// Wait for MultipleValuesChanged event
		select {
		case event := <-eventChan:
			assert.Equal(t, uint64(chainId), event.ChainID)
			assert.Equal(t, seAddr, event.Address)
			assert.Equal(t, tx.Hash(), event.Raw.TxHash)
			// Verify event topic matches the event signature hash
			expectedTopic := crypto.Keccak256Hash([]byte("MultipleValuesChanged(address,uint256[])"))
			assert.Equal(t, expectedTopic, event.Raw.Topics[0], "event topic should match signature hash")
		case <-time.After(5 * time.Second):
			t.Fatal("timeout waiting for MultipleValuesChanged event")
		}

		// Unsubscribe from events
		err = indexer.RemoveContract([]uint64{uint64(chainId)}, seAddr.Hex())
		require.NoError(t, err)
	})

	t.Run("subscribing to non-existent event", func(t *testing.T) {
		backend, seAddr, _, _ := createSimulatedBackend(t)

		// Create event indexer
		indexer := New(map[uint64]EthClient{
			uint64(chainId): backend.Client(),
		})

		// Start event indexer
		ctx := context.Background()
		err := indexer.Start(ctx)
		require.NoError(t, err)
		defer indexer.Stop()

		// Create event channel
		eventChan := make(chan RawEvent)

		addresses := map[uint64]common.Address{
			uint64(chainId): seAddr,
		}

		// Try to subscribe to non-existent event
		err = indexer.AddContract(
			ctx,
			addresses,
			[]common.Hash{crypto.Keccak256Hash([]byte("NonExistentEvent(address,uint256)"))},
			eventChan,
		)
		require.NoError(t, err) // Adding should succeed as we don't validate event existence

		// No events should be received
		select {
		case <-eventChan:
			t.Fatal("received event for non-existent event signature")
		case <-time.After(2 * time.Second):
			// This is expected
		}
	})
}

func Test_eventIndexer_Stress(t *testing.T) {
	const eventCount = 50

	// Define default balance for each sender - increase balance to cover gas costs
	balance, ok := new(big.Int).SetString("100000000000000000000000000000", 10) // 100k ETH
	require.True(t, ok, "failed to set balance")

	// Pre-generate all accounts
	accounts := make(map[common.Address]types.Account, eventCount+1) // +1 for contract deployer
	auths := make([]*bind.TransactOpts, eventCount)
	for i := 0; i < eventCount; i++ {
		senderPK, err := crypto.GenerateKey()
		require.NoError(t, err)
		auth, err := bind.NewKeyedTransactorWithChainID(senderPK, big.NewInt(chainId))
		require.NoError(t, err)
		auth.GasLimit = uint64(1000000) // Set explicit gas limit
		accounts[auth.From] = types.Account{Balance: balance}
		auths[i] = auth
	}

	// Create deployer account with balance
	deployerPK, err := crypto.GenerateKey()
	require.NoError(t, err)
	deployerAuth, err := bind.NewKeyedTransactorWithChainID(deployerPK, big.NewInt(chainId))
	require.NoError(t, err)
	deployerAuth.GasLimit = uint64(3000000) // Higher gas limit for contract deployment
	accounts[deployerAuth.From] = types.Account{Balance: balance}

	// Create backend with pre-funded accounts
	backend := simulated.NewBackend(accounts)
	t.Cleanup(func() { require.NoError(t, backend.Close()) })

	// Deploy contract
	seAddr, seTx, se, err := test.DeploySimpleEvent(deployerAuth, backend.Client())
	require.NoError(t, err)
	backend.Commit()
	_, err = bind.WaitDeployed(context.Background(), backend.Client(), seTx)
	require.NoError(t, err)

	// Create event indexer
	indexer := New(map[uint64]EthClient{
		uint64(chainId): backend.Client(),
	})

	// Start event indexer
	ctx := context.Background()
	err = indexer.Start(ctx)
	require.NoError(t, err)
	defer indexer.Stop()

	// Create event channel with buffer to prevent blocking
	eventChan := make(chan RawEvent, eventCount) // Match buffer size to event count

	addresses := map[uint64]common.Address{
		uint64(chainId): seAddr,
	}

	// Subscribe to ValueChanged event
	eventID := "ValueChanged(address,uint256)"
	err = indexer.AddContract(
		ctx,
		addresses,
		[]common.Hash{crypto.Keccak256Hash([]byte(eventID))},
		eventChan,
	)
	require.NoError(t, err)

	receivedEvents := make(chan struct{}, eventCount)

	// Mutex to protect backend.Commit()
	var backendMu sync.Mutex

	// Channel to coordinate transaction processing
	txChan := make(chan *types.Transaction, eventCount)

	// WaitGroup to track when the transaction processing goroutine is done
	var txProcessingWg sync.WaitGroup
	txProcessingWg.Add(1)

	// WaitGroup to track transaction sending goroutines
	var wg sync.WaitGroup

	// Start a single goroutine to handle commits
	go func() {
		defer txProcessingWg.Done()

		for tx := range txChan {
			backendMu.Lock()
			backend.Commit()
			backendMu.Unlock()

			// Wait for the event with longer timeout
			select {
			case event := <-eventChan:
				if event.Raw.TxHash == tx.Hash() {
					receivedEvents <- struct{}{}
				}
			case <-time.After(1 * time.Second): // Increased timeout further
				t.Logf("timeout waiting for event from tx: %s", tx.Hash().Hex())
			}
		}
	}()

	// Send transactions from different addresses
	for i := 0; i < eventCount; i++ {
		wg.Add(1)

		// Capture loop variable
		val := int64(i)
		auth := auths[i]

		go func() {
			defer wg.Done()

			// Set value to trigger event
			backendMu.Lock()
			tx, err := se.SetValue(auth, big.NewInt(val))
			backendMu.Unlock()

			if err != nil {
				t.Logf("failed to set value: %v", err)
				return
			}

			// Send transaction for processing
			txChan <- tx
		}()

		// Add small delay between transaction sends
		time.Sleep(20 * time.Millisecond)
	}

	// Wait for all transactions to be sent
	wg.Wait()

	// Close transaction channel after all transactions are sent
	close(txChan)

	// Wait for the transaction processing goroutine to complete
	txProcessingWg.Wait()

	// Create a separate WaitGroup for the event processing goroutine
	var processingWg sync.WaitGroup
	processingWg.Add(1)

	// Signal channel to indicate when the processing goroutine is done
	processingDone := make(chan struct{})

	// Now it's safe to close the receivedEvents channel
	close(receivedEvents)

	go func() {
		defer processingWg.Done()
		defer close(processingDone)

		// Count received events
		received := 0
		for range receivedEvents {
			received++
		}

		// Check if we received all events
		require.Equal(t, eventCount, received, "did not receive all events, got %d/%d", received, eventCount)
	}()

	// Wait for the event processing goroutine to finish or timeout
	select {
	case <-processingDone:
		// Processing completed successfully
	case <-time.After(5 * time.Second):
		t.Log("timeout waiting for event processing to complete")
	}

	// Wait for the processing goroutine to finish
	processingWg.Wait()
}

func Test_eventIndexer_Cleanup(t *testing.T) {
	backend, seAddr, _, _ := createSimulatedBackend(t)

	// Create event indexer
	indexer := New(map[uint64]EthClient{
		uint64(chainId): backend.Client(),
	})

	// Start event indexer
	ctx := context.Background()
	err := indexer.Start(ctx)
	require.NoError(t, err)

	// Create multiple event channels and subscriptions
	eventChan1 := make(chan RawEvent)
	eventChan2 := make(chan RawEvent)
	eventChan3 := make(chan RawEvent)

	// Subscribe to multiple events
	events := []struct {
		name string
		ch   chan RawEvent
	}{
		{"ValueChanged(address,uint256)", eventChan1},
		{"MultipleValuesChanged(address,uint256[])", eventChan2},
		{"NonExistentEvent(address,uint256)", eventChan3},
	}

	addresses := map[uint64]common.Address{
		uint64(chainId): seAddr,
	}

	// Add all subscriptions
	for _, event := range events {
		err = indexer.AddContract(
			ctx,
			addresses,
			[]common.Hash{crypto.Keccak256Hash([]byte(event.name))},
			event.ch,
		)
		require.NoError(t, err)
	}

	// Store initial number of goroutines
	initialGoroutines := runtime.NumGoroutine()

	// Call Stop - this will generate expected subscription error logs for each subscription
	indexer.Stop()

	// Give a small window for cleanup and logs
	time.Sleep(100 * time.Millisecond)

	// Verify all channels are closed
	for i, event := range events {
		select {
		case _, ok := <-event.ch:
			assert.False(t, ok, "event channel %d should be closed", i+1)
		default:
			t.Errorf("event channel %d should be closed and readable", i+1)
		}
	}

	// Verify no goroutine leaks
	currentGoroutines := runtime.NumGoroutine()
	assert.LessOrEqual(t, currentGoroutines, initialGoroutines,
		"number of goroutines should not increase after Stop()")

	// Try to add a new subscription after Stop - should fail
	eventChan4 := make(chan RawEvent)
	err = indexer.AddContract(
		ctx,
		addresses,
		[]common.Hash{crypto.Keccak256Hash([]byte("ValueChanged(address,uint256)"))},
		eventChan4,
	)
	assert.Error(t, err, "adding contract after Stop should fail")
}
