package backoff

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestExponential(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		i := 0
		outcomes := []bool{false, false, true}
		t0 := time.Now()
		err := Exponential(func() (err error, retry bool) {
			outcome := outcomes[i]
			i++
			if outcome {
				return nil, false
			}
			return errors.New("bad"), true
		}, 3, 150*time.Millisecond)

		elapsed := time.Since(t0)

		require.NoError(t, err)
		require.Equal(t, i, 3)
		require.True(t, elapsed >= 300*time.Millisecond)
	})

	t.Run("failed", func(t *testing.T) {
		i := 0
		t0 := time.Now()
		err := Exponential(func() (err error, retry bool) {
			i++
			return errors.New("bad"), true
		}, 3, 100*time.Millisecond)

		elapsed := time.Since(t0)

		require.Error(t, err)
		require.Equal(t, i, 3)
		require.True(t, elapsed >= 300*time.Millisecond)
	})

	t.Run("exit on no retry", func(t *testing.T) {
		i := 0
		t0 := time.Now()
		err := errors.New("some error")

		err = Exponential(func() (error, bool) {
			i++
			return err, false // Return error but don't retry
		}, 3, 100*time.Millisecond)

		elapsed := time.Since(t0)

		require.Error(t, err)
		require.Equal(t, 1, i)                          // Should only be called once
		require.True(t, elapsed < 100*time.Millisecond) // Should exit immediately without waiting
	})
}

func TestExponentialWithContext(t *testing.T) {
	t.Run("success with live context", func(t *testing.T) {
		i := 0
		outcomes := []bool{false, false, true}
		t0 := time.Now()
		err := ExponentialWithContext(context.Background(), func() (err error, retry bool) {
			outcome := outcomes[i]
			i++
			if outcome {
				return nil, false
			}
			return errors.New("bad"), true
		}, 3, 150*time.Millisecond)

		elapsed := time.Since(t0)

		require.NoError(t, err)
		require.Equal(t, 3, i)
		require.True(t, elapsed >= 300*time.Millisecond)
	})

	t.Run("cancelled context short-circuits retry sleep", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // cancel immediately

		actionErr := errors.New("bad")
		i := 0
		t0 := time.Now()
		err := ExponentialWithContext(ctx, func() (error, bool) {
			i++
			return actionErr, true
		}, 5, 5*time.Second) // large wait to make the test obvious

		elapsed := time.Since(t0)

		require.Error(t, err)
		require.ErrorIs(t, err, context.Canceled)  // callers can detect cancellation
		require.ErrorIs(t, err, actionErr)          // last action error is also preserved
		require.Equal(t, 1, i)                          // action called once, then cancelled during wait
		require.True(t, elapsed < 500*time.Millisecond) // should NOT sleep 5s
	})

	t.Run("deadline exceeded wraps ctx error", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
		defer cancel()
		time.Sleep(5 * time.Millisecond) // ensure deadline passes

		actionErr := errors.New("timeout action error")
		err := ExponentialWithContext(ctx, func() (error, bool) {
			return actionErr, true
		}, 5, 5*time.Second)

		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.ErrorIs(t, err, actionErr)
	})
}
