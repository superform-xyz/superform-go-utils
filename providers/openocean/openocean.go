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

// New creates a new OpenOcean API client.
func New(apiKey string, baseURLs ...string) Client {
	clientBuilder := http_client.NewClientBuilder().SetRetry(0, 10*time.Second)
	if apiKey != "" {
		clientBuilder = clientBuilder.SetAuth(apiKeyHeader, apiKey)
	}

	baseURL := openOceanBaseURL
	if len(baseURLs) > 0 {
		candidate := strings.TrimRight(strings.TrimSpace(baseURLs[0]), "/")
		if candidate != "" {
			baseURL = candidate
		}
	}

	return &openOcean{
		apiKey:  apiKey,
		baseURL: baseURL,
		client:  clientBuilder.BuildClient(),
	}
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
