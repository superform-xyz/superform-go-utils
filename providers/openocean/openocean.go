package openocean

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/superform-xyz/superform-go-utils/pkg/http_client"
)

const (
	openOceanBaseURL = "https://open-api-pro.openocean.finance"
	apiKeyHeader     = "apikey"
)

// Client represents the behavior of the OpenOcean API.
type Client interface {
	GetSwap(ctx context.Context, req SwapRequest) (*Swap, error)
	Close() error
}

type openOcean struct {
	apiKey  string
	baseURL string
	client  *http_client.Client
}

var _ Client = (*openOcean)(nil)

type Option func(*openOcean)

func WithBaseURL(baseURL string) Option {
	return func(o *openOcean) {
		baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
		if baseURL != "" {
			o.baseURL = baseURL
		}
	}
}

func WithHTTPClient(client *http.Client) Option {
	return func(o *openOcean) {
		if client != nil {
			o.client = &http_client.Client{Client: client}
		}
	}
}

// New creates a new OpenOcean API client.
func New(apiKey string, opts ...Option) (Client, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, fmt.Errorf("openocean: apiKey is required")
	}
	o := &openOcean{
		apiKey:  apiKey,
		baseURL: openOceanBaseURL,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}
	if o.client == nil {
		builder := http_client.NewClientBuilder().SetRetry(0, 10*time.Second)
		builder = builder.SetAuth(apiKeyHeader, o.apiKey)
		o.client = builder.BuildClient()
	}

	return o, nil
}

// GetSwap fetches executable swap transaction data from OpenOcean.
func (o *openOcean) GetSwap(ctx context.Context, req SwapRequest) (*Swap, error) {
	v := url.Values{}
	v.Set("chain", strconv.FormatUint(req.ChainID, 10))
	v.Set("inTokenAddress", req.InTokenAddress)
	v.Set("outTokenAddress", req.OutTokenAddress)
	v.Set("amountDecimals", req.AmountDecimals)
	v.Set("slippage", req.Slippage)
	v.Set("account", req.Account)
	v.Set("enabledDexIds", req.EnabledDexIDs)
	v.Set("gasPriceDecimals", req.GasPriceDecimals)
	if req.Referrer != "" {
		v.Set("referrer", req.Referrer)
	}

	swapURL := fmt.Sprintf("%s/v4/%d/swap?%s", o.baseURL, req.ChainID, v.Encode())
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, swapURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create OpenOcean swap request: %w", err)
	}
	if o.apiKey != "" {
		httpReq.Header.Set(apiKeyHeader, o.apiKey)
	}

	resp, err := o.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to get OpenOcean swap quote: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("OpenOcean swap API returned status %d: %s", resp.StatusCode, string(body))
	}

	var swapResp swapResponse
	if err := json.NewDecoder(resp.Body).Decode(&swapResp); err != nil {
		return nil, fmt.Errorf("failed to decode OpenOcean swap response: %w", err)
	}
	if swapResp.Code != http.StatusOK {
		return nil, fmt.Errorf("OpenOcean swap API error (code %d): %s", swapResp.Code, swapResp.Message)
	}
	if swapResp.Data == nil {
		return nil, fmt.Errorf("OpenOcean swap response missing data")
	}

	return swapResp.Data, nil
}

func (o *openOcean) Close() error {
	o.client.CloseIdleConnections()
	return nil
}
