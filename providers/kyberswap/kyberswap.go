package kyberswap

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/superform-xyz/superform-go-utils/pkg/http_client"
	"github.com/superform-xyz/superform-go-utils/utils/constants"
	"github.com/superform-xyz/superform-go-utils/utils/units"
)

const (
	kyberswapBaseURL = "https://aggregator-api.kyberswap.com"
	clientIDHeader   = "x-client-id"
)

// Client represents the behavior of the KyberSwap Aggregator API.
type Client interface {
	GetRoute(ctx context.Context, req RouteRequest) (*Route, error)
	BuildRoute(ctx context.Context, req BuildRequest) (*Build, error)
	SupportedChains() []uint64
	Close() error
}

type kyberswap struct {
	clientID string
	baseURL  string
	client   *http_client.Client
}

var _ Client = (*kyberswap)(nil)

type Option func(*kyberswap)

func WithBaseURL(baseURL string) Option {
	return func(k *kyberswap) {
		baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
		if baseURL != "" {
			k.baseURL = baseURL
		}
	}
}

func WithHTTPClient(client *http.Client) Option {
	return func(k *kyberswap) {
		if client != nil {
			k.client = &http_client.Client{Client: client}
		}
	}
}

// WithClientID sets the optional x-client-id header.
func WithClientID(clientID string) Option {
	return func(k *kyberswap) {
		k.clientID = strings.TrimSpace(clientID)
	}
}

// New creates a new KyberSwap Aggregator API client.
func New(opts ...Option) (Client, error) {
	k := &kyberswap{
		baseURL: kyberswapBaseURL,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(k)
		}
	}
	if k.client == nil {
		builder := http_client.NewClientBuilder().SetRetry(0, 10*time.Second)
		if k.clientID != "" {
			builder = builder.SetAuth(clientIDHeader, k.clientID)
		}
		k.client = builder.BuildClient()
	}

	return k, nil
}

// GetRoute fetches a route summary from KyberSwap.
func (k *kyberswap) GetRoute(ctx context.Context, req RouteRequest) (*Route, error) {
	chainName, err := chainToName(req.ChainID)
	if err != nil {
		return nil, err
	}

	tokenIn, tokenOut := kyberTokenAddress(req.TokenIn), kyberTokenAddress(req.TokenOut)
	if strings.EqualFold(tokenIn, tokenOut) {
		return nil, fmt.Errorf("from and to tokens are the same")
	}

	v := url.Values{}
	v.Set("tokenIn", tokenIn)
	v.Set("tokenOut", tokenOut)
	v.Set("amountIn", req.AmountIn)
	if len(req.ExcludedSources) > 0 {
		v.Set("excludedSources", strings.Join(req.ExcludedSources, ","))
	}
	if req.OnlyScalableSources {
		v.Set("onlyScalableSources", "true")
	}

	path := fmt.Sprintf("%s/%s/api/v1/routes?%s", k.baseURL, chainName, v.Encode())
	var routeResult routeResponse
	if err := k.doJSON(ctx, http.MethodGet, path, nil, &routeResult); err != nil {
		return nil, fmt.Errorf("failed to get kyberswap route: %w", err)
	}

	if routeResult.Code != 0 {
		return nil, fmt.Errorf("kyberswap route API error (code %d): %s", routeResult.Code, routeResult.Message)
	}

	routeSummary := bytes.TrimSpace(routeResult.Data.RouteSummary)
	if len(routeSummary) == 0 || bytes.Equal(routeSummary, []byte("null")) {
		return nil, fmt.Errorf("kyberswap returned no route for %s to %s on chain %d", req.TokenIn, req.TokenOut, req.ChainID)
	}

	return &Route{
		RouteSummary:  routeResult.Data.RouteSummary,
		RouterAddress: routeResult.Data.RouterAddress,
	}, nil
}

// BuildRoute builds executable swap transaction data from a route summary.
func (k *kyberswap) BuildRoute(ctx context.Context, req BuildRequest) (*Build, error) {
	chainName, err := chainToName(req.ChainID)
	if err != nil {
		return nil, err
	}
	if req.SlippageTolerance < 0 || req.SlippageTolerance > units.BpsBase {
		return nil, fmt.Errorf("slippage tolerance %d bps is outside [0, %d]", req.SlippageTolerance, units.BpsBase)
	}

	buildReq := buildRequestJSON{
		RouteSummary:      req.RouteSummary,
		Sender:            req.Sender,
		Recipient:         req.Recipient,
		SlippageTolerance: req.SlippageTolerance,
	}

	path := fmt.Sprintf("%s/%s/api/v1/route/build", k.baseURL, chainName)
	var buildResult buildResponse
	if err := k.doJSON(ctx, http.MethodPost, path, buildReq, &buildResult); err != nil {
		return nil, fmt.Errorf("failed to build kyberswap route: %w", err)
	}

	if buildResult.Code != 0 {
		return nil, fmt.Errorf("kyberswap build API error (code %d): %s", buildResult.Code, buildResult.Message)
	}
	if buildResult.Data == nil {
		return nil, fmt.Errorf("kyberswap build response missing data")
	}

	buildResult.Data.AmountOutMin = units.ApplySlippageBps(buildResult.Data.AmountOut, req.SlippageTolerance)
	return buildResult.Data, nil
}

func (k *kyberswap) SupportedChains() []uint64 {
	supportedChains := make([]uint64, 0, len(chainIDToName))
	for chainID := range chainIDToName {
		supportedChains = append(supportedChains, chainID)
	}
	return supportedChains
}

func (k *kyberswap) Close() error {
	k.client.CloseIdleConnections()
	return nil
}

func (k *kyberswap) doJSON(ctx context.Context, method, path string, requestBody any, responseBody any) error {
	var body io.Reader
	if requestBody != nil {
		payload, err := json.Marshal(requestBody)
		if err != nil {
			return fmt.Errorf("failed to encode kyberswap request: %w", err)
		}
		body = bytes.NewReader(payload)
	}

	req, err := http.NewRequestWithContext(ctx, method, path, body)
	if err != nil {
		return fmt.Errorf("failed to create kyberswap request: %w", err)
	}
	if requestBody != nil {
		req.Header.Set("Content-Type", http_client.ContentTypeJSON)
	}
	if k.clientID != "" {
		req.Header.Set(clientIDHeader, k.clientID)
	}

	resp, err := k.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("kyberswap API returned status %d: %s", resp.StatusCode, string(body))
	}

	if err := json.NewDecoder(resp.Body).Decode(responseBody); err != nil {
		return fmt.Errorf("failed to decode kyberswap response: %w", err)
	}

	return nil
}

// kyberTokenAddress maps the zero address to KyberSwap's 0xEeee… native sentinel.
func kyberTokenAddress(token string) string {
	token = strings.TrimSpace(token)
	if common.IsHexAddress(token) && constants.IsNativeToken(token) {
		return constants.GetNativeToken().Hex()
	}
	return token
}
