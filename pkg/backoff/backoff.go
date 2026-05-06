package backoff

import (
	"context"
	"fmt"
	"time"
)

// Exponential performs exponential backoff attempts on a given action
func Exponential(action func() (err error, retry bool), max uint, wait time.Duration) error {
	var (
		retry bool
		err   error
	)

	for i := uint(0); i < max; i++ {
		if err, retry = action(); err == nil {
			return nil
		}

		if !retry {
			break
		}

		// no need to wait on the last attempt
		if i < max-1 {
			time.Sleep(wait)
			wait *= 2
		}
	}
	return err
}

// ExponentialWithContext is like Exponential but respects context cancellation
// during the wait between retries. This prevents wasting time on retries when
// the parent context (e.g. http.Client.Timeout) has already been cancelled.
func ExponentialWithContext(ctx context.Context, action func() (err error, retry bool), max uint, wait time.Duration) error {
	var (
		retry bool
		err   error
	)

	for i := uint(0); i < max; i++ {
		if err, retry = action(); err == nil {
			return nil
		}

		if !retry {
			break
		}

		// no need to wait on the last attempt
		if i < max-1 {
			select {
			case <-ctx.Done():
				return fmt.Errorf("%w: %w", ctx.Err(), err)
			case <-time.After(wait):
			}
			wait *= 2
		}
	}
	return err
}
