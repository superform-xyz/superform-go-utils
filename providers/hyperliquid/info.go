// Package hyperliquid implements Hyperliquid's wire protocol without application
// eligibility, caching, budgeting, or execution policy.
package hyperliquid

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

const MainnetInfoURL = "https://api.hyperliquid.xyz/info"

var (
	ErrRateLimited    = errors.New("hyperliquid: rate limited by upstream")
	ErrInvalidAddress = errors.New("hyperliquid: invalid address")
	addressPattern    = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)
)

// NormalizeAddress validates an EVM address before using it in a wire request.
func NormalizeAddress(address string) (string, error) {
	trimmed := strings.TrimSpace(address)
	if !addressPattern.MatchString(trimmed) {
		return "", fmt.Errorf("%w: %q", ErrInvalidAddress, address)
	}
	return strings.ToLower(trimmed), nil
}

// HTTPDoer lets callers inject their transport, timeout, and instrumentation.
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// HTTPError describes a non-success status. Body is bounded to 512 bytes.
type HTTPError struct {
	StatusCode int
	Body       string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("hyperliquid: unexpected status %d: %s", e.StatusCode, e.Body)
}

// ErrorKind identifies the phase of a failed request for caller instrumentation.
type ErrorKind string

const (
	ErrorEncode    ErrorKind = "encode"
	ErrorRequest   ErrorKind = "request"
	ErrorTransport ErrorKind = "transport"
	ErrorDecode    ErrorKind = "decode"
)

type RequestError struct {
	Kind ErrorKind
	Err  error
}

func (e *RequestError) Error() string { return e.Err.Error() }
func (e *RequestError) Unwrap() error { return e.Err }

// Response describes the actual HTTP attempt, including unsuccessful requests.
// Dispatched means Do was invoked; it does not establish server receipt.
type Response struct {
	Dispatched bool
	StatusCode int
	Date       string
}

// InfoClient performs reads only. It does not retry or reserve request budget.
// A caller-supplied transport must not retry implicitly if exact accounting is
// required. Signed exchange writes have a separate dispatch lifecycle.
type InfoClient struct {
	endpoint      string
	httpClient    HTTPDoer
	responseLimit int64
}

type InfoOption func(*InfoClient)

// WithResponseLimit changes the maximum successful JSON response size.
func WithResponseLimit(limit int64) InfoOption {
	return func(c *InfoClient) {
		if limit > 0 {
			c.responseLimit = limit
		}
	}
}

func NewInfoClient(endpoint string, client HTTPDoer, opts ...InfoOption) *InfoClient {
	if endpoint == "" {
		endpoint = MainnetInfoURL
	}
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	c := &InfoClient{endpoint: endpoint, httpClient: client, responseLimit: 4 << 20}
	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}
	return c
}

// Post sends one info request and decodes exactly one bounded JSON response.
// Budget reservation, pacing, source selection and fallback belong to callers.
func (c *InfoClient) Post(ctx context.Context, payload, out any) (Response, error) {
	var result Response
	body, err := json.Marshal(payload)
	if err != nil {
		return result, &RequestError{Kind: ErrorEncode, Err: fmt.Errorf("hyperliquid: request encoding failed: %w", err)}
	}
	if c == nil || c.httpClient == nil {
		return result, &RequestError{Kind: ErrorRequest, Err: errors.New("hyperliquid: info client is not configured")}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return result, &RequestError{Kind: ErrorRequest, Err: fmt.Errorf("hyperliquid: request construction failed: %w", err)}
	}
	req.Header.Set("Content-Type", "application/json")
	result.Dispatched = true
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return result, &RequestError{Kind: ErrorTransport, Err: fmt.Errorf("hyperliquid: info request: %w", err)}
	}
	defer resp.Body.Close()
	result.StatusCode = resp.StatusCode
	result.Date = resp.Header.Get("Date")
	if resp.StatusCode == http.StatusTooManyRequests {
		return result, ErrRateLimited
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 512))
		if readErr != nil {
			return result, &HTTPError{StatusCode: resp.StatusCode}
		}
		return result, &HTTPError{StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(raw))}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, c.responseLimit+1))
	if err != nil {
		return result, &RequestError{Kind: ErrorDecode, Err: fmt.Errorf("hyperliquid: reading info response: %w", err)}
	}
	if int64(len(raw)) > c.responseLimit {
		return result, &RequestError{Kind: ErrorDecode, Err: fmt.Errorf("hyperliquid: response exceeds %d bytes", c.responseLimit)}
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return result, &RequestError{Kind: ErrorDecode, Err: fmt.Errorf("hyperliquid: response decoding failed: %w", err)}
	}
	return result, nil
}

func (c *InfoClient) userRequest(ctx context.Context, kind string, user common.Address, out any) error {
	_, err := c.Post(ctx, map[string]string{"type": kind, "user": strings.ToLower(user.Hex())}, out)
	if err != nil {
		return fmt.Errorf("hyperliquid: %s: %w", kind, err)
	}
	return nil
}

func (c *InfoClient) ClearinghouseState(ctx context.Context, user common.Address) (*PerpState, error) {
	out := new(PerpState)
	if err := c.userRequest(ctx, "clearinghouseState", user, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *InfoClient) SpotClearinghouseState(ctx context.Context, user common.Address) (*SpotState, error) {
	out := new(SpotState)
	if err := c.userRequest(ctx, "spotClearinghouseState", user, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *InfoClient) ExtraAgents(ctx context.Context, user common.Address) ([]ExtraAgent, error) {
	var out []ExtraAgent
	if err := c.userRequest(ctx, "extraAgents", user, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// RawExtraAgents preserves each address exactly as returned by the venue.
func (c *InfoClient) RawExtraAgents(ctx context.Context, user common.Address) ([]RawExtraAgent, error) {
	var out []RawExtraAgent
	if err := c.userRequest(ctx, "extraAgents", user, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// PerpMeta returns venue market metadata without filtering markets.
func (c *InfoClient) PerpMeta(ctx context.Context) (*PerpMeta, error) {
	out := new(PerpMeta)
	if _, err := c.Post(ctx, map[string]string{"type": "meta"}, out); err != nil {
		return nil, fmt.Errorf("hyperliquid: meta: %w", err)
	}
	return out, nil
}

func (c *InfoClient) UserRateLimit(ctx context.Context, user common.Address) (*UserRateLimit, error) {
	out := new(UserRateLimit)
	if err := c.userRequest(ctx, "userRateLimit", user, out); err != nil {
		return nil, err
	}
	return out, nil
}

// OrderStatus reads one order by numeric order ID or client order ID string.
// Absence, expiry and approval policy belong to the consuming application.
func (c *InfoClient) OrderStatus(ctx context.Context, user common.Address, oid any) (*OrderStatusResponse, error) {
	out := new(OrderStatusResponse)
	_, err := c.Post(ctx, map[string]any{"type": "orderStatus", "user": strings.ToLower(user.Hex()), "oid": oid}, out)
	if err != nil {
		return nil, fmt.Errorf("hyperliquid: orderStatus: %w", err)
	}
	return out, nil
}

func (c *InfoClient) NonFundingLedgerUpdates(ctx context.Context, user common.Address, startTime int64) ([]LedgerUpdate, error) {
	return c.NonFundingLedgerUpdatesPage(ctx, user, startTime, nil)
}

// NonFundingLedgerUpdatesPage reads exactly one raw provider page. endTime is
// optional; hashless entries, order and repeated rows remain untouched.
// Pagination, deduplication and execution-evidence validation belong to callers.
func (c *InfoClient) NonFundingLedgerUpdatesPage(ctx context.Context, user common.Address, startTime int64, endTime *int64) ([]LedgerUpdate, error) {
	var out []LedgerUpdate
	request := struct {
		Type      string `json:"type"`
		User      string `json:"user"`
		StartTime int64  `json:"startTime"`
		EndTime   *int64 `json:"endTime,omitempty"`
	}{Type: "userNonFundingLedgerUpdates", User: strings.ToLower(user.Hex()), StartTime: startTime, EndTime: endTime}
	_, err := c.Post(ctx, request, &out)
	if err != nil {
		return nil, fmt.Errorf("hyperliquid: userNonFundingLedgerUpdates: %w", err)
	}
	return out, nil
}

func (c *InfoClient) SpotMetaAndAssetCtxs(ctx context.Context) (*SpotMeta, []SpotAssetCtx, error) {
	var raw []json.RawMessage
	if _, err := c.Post(ctx, map[string]string{"type": "spotMetaAndAssetCtxs"}, &raw); err != nil {
		return nil, nil, fmt.Errorf("hyperliquid: spotMetaAndAssetCtxs: %w", err)
	}
	return DecodeSpotMetaAndAssetCtxs(raw)
}

// DecodeSpotMetaAndAssetCtxs decodes the two-part provider response without
// applying pricing, cache or supported-token policy.
func DecodeSpotMetaAndAssetCtxs(raw []json.RawMessage) (*SpotMeta, []SpotAssetCtx, error) {
	if len(raw) != 2 {
		return nil, nil, fmt.Errorf("hyperliquid: spotMetaAndAssetCtxs returned %d elements, want 2", len(raw))
	}
	var meta SpotMeta
	if err := json.Unmarshal(raw[0], &meta); err != nil {
		return nil, nil, fmt.Errorf("hyperliquid: decoding spot meta: %w", err)
	}
	var contexts []SpotAssetCtx
	if err := json.Unmarshal(raw[1], &contexts); err != nil {
		return nil, nil, fmt.Errorf("hyperliquid: decoding spot asset contexts: %w", err)
	}
	return &meta, contexts, nil
}
