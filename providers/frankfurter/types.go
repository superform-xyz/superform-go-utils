package frankfurter

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var ErrRateUnavailable = errors.New("frankfurter rate unavailable")

// Client defines the Frankfurter exchange-rate API surface shared across
// backend services.
type Client interface {
	GetRate(ctx context.Context, req RateRequest) (*Rate, error)
	Close() error
}

// RateRequest selects one exchange rate at an effective time.
type RateRequest struct {
	Base        string
	Quote       string
	EffectiveAt time.Time
}

// Rate is the number of quote-currency units represented by one base-currency
// unit. Date is the effective UTC calendar date returned by Frankfurter.
type Rate struct {
	Date  time.Time
	Base  string
	Quote string
	Value float64
	Stale bool
}

// HTTPError describes a non-success response from Frankfurter.
type HTTPError struct {
	StatusCode int
	Body       string
}

func (e *HTTPError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("frankfurter status %d", e.StatusCode)
	}
	return fmt.Sprintf("frankfurter status %d: %s", e.StatusCode, e.Body)
}
