package snapshotd

import (
	"context"
	"fmt"
	"net/http"

	"github.com/ethereum/go-ethereum/common"
)

// Client defines the snapshotd REST API surface shared across backend services.
type Client interface {
	GetPPS(ctx context.Context, query Query) (*PPSResult, error)
	GetAllocation(ctx context.Context, query Query) (*Allocation, error)
	Close() error
}

// Query identifies a strategy snapshot. A nil BlockNumber requests the latest state.
type Query struct {
	ChainID     uint64
	Strategy    common.Address
	BlockNumber *uint64
}

// PPSResult contains the integer-scaled price per share returned by snapshotd.
type PPSResult struct {
	PPS string `json:"pps"`
}

// Allocation contains vault-asset-normalized balances for a strategy and its sources.
type Allocation struct {
	Strategy      common.Address `json:"strategy"`
	ChainID       uint64         `json:"chainId"`
	BlockNumber   *uint64        `json:"blockNumber,omitempty"`
	Asset         common.Address `json:"asset"`
	AssetSymbol   string         `json:"assetSymbol"`
	AssetDecimals uint8          `json:"assetDecimals"`
	TotalAssets   string         `json:"totalAssets,omitempty"`
	TotalSupply   string         `json:"totalSupply,omitempty"`
	IdleBalance   string         `json:"idleBalance,omitempty"`
	Sources       []Source       `json:"sources"`
}

// Source contains one yield source's balance normalized into the vault asset.
type Source struct {
	Source      common.Address `json:"source"`
	Oracle      common.Address `json:"oracle"`
	Kind        uint8          `json:"kind"`
	Error       string         `json:"error,omitempty"`
	RawShares   string         `json:"rawShares,omitempty"`
	AssetTVL    string         `json:"assetTvl,omitempty"`
	AssetSymbol string         `json:"assetSymbol,omitempty"`
	Active      bool           `json:"active,omitempty"`
}

// HTTPError describes a non-success response from snapshotd.
type HTTPError struct {
	StatusCode int
	Code       string
	Message    string
	RequestID  string
	Err        error
}

func (e *HTTPError) Error() string {
	if e.Code != "" && e.Message != "" {
		return fmt.Sprintf("snapshotd status %d (%s): %s", e.StatusCode, e.Code, e.Message)
	}
	if e.Message != "" {
		return fmt.Sprintf("snapshotd status %d: %s", e.StatusCode, e.Message)
	}
	return fmt.Sprintf("snapshotd status %d: %v", e.StatusCode, e.Err)
}

func (e *HTTPError) Unwrap() error {
	return e.Err
}

type errorEnvelope struct {
	Error struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		RequestID string `json:"request_id,omitempty"`
	} `json:"error"`
}

func httpErrorSentinel(statusCode int) error {
	switch statusCode {
	case http.StatusBadRequest:
		return ErrBadRequest
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrUnauthorized
	case http.StatusNotFound:
		return ErrNotFound
	case http.StatusTooManyRequests:
		return ErrRateLimited
	default:
		return ErrUpstream
	}
}
