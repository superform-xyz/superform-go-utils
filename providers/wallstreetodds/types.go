package wallstreetodds

import (
	"context"
	"fmt"
)

// Client defines the WallStreetOdds REST API surface shared across backend services.
type Client interface {
	GetAllTimeHighs(ctx context.Context, symbols []string) ([]StockAllTimeHigh, error)
	Close() error
}

// StockAllTimeHigh is WallStreetOdds' highest split-adjusted stock price since 2010.
type StockAllTimeHigh struct {
	Symbol      string   `json:"symbol"`
	AllTimeHigh *float64 `json:"allTimeHigh"`
}

// HTTPError describes a non-success response from WallStreetOdds.
type HTTPError struct {
	StatusCode int
	Body       string
}

func (e *HTTPError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("wallstreetodds status %d", e.StatusCode)
	}
	return fmt.Sprintf("wallstreetodds status %d: %s", e.StatusCode, e.Body)
}

// APIError describes an error embedded in a successful HTTP response.
type APIError struct {
	Message string
}

func (e *APIError) Error() string {
	return "wallstreetodds api: " + e.Message
}
