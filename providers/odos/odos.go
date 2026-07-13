package odos

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/superform-xyz/superform-go-utils/pkg/http_client"
)

const odosBaseURL = "https://api.odos.xyz"

// Client represents the behavior of the Odos API.
type Client interface {
	GetQuote(ctx context.Context, req QuoteRequest) (*Quote, error)
	Assemble(ctx context.Context, req AssembleRequest) (*Assemble, error)
	Close() error
}

type odos struct {
	baseURL    string
	client     *http_client.Client
	timeout    *time.Duration
	maxRetries *uint
	retryDelay *time.Duration
}

var _ Client = (*odos)(nil)

type Option func(*odos)

func WithBaseURL(baseURL string) Option {
	return func(o *odos) {
		baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
		if baseURL != "" {
			o.baseURL = baseURL
		}
	}
}

func WithHTTPClient(client *http.Client) Option {
	return func(o *odos) {
		if client != nil {
			o.client = &http_client.Client{Client: client}
		}
	}
}

func WithTimeout(timeout time.Duration) Option {
	return func(o *odos) {
		o.timeout = &timeout
	}
}

func WithRetry(maxRetries uint, retryDelay time.Duration) Option {
	return func(o *odos) {
		o.maxRetries = &maxRetries
		o.retryDelay = &retryDelay
	}
}

// New creates a new Odos API client.
func New(opts ...Option) (Client, error) {
	o := &odos{
		baseURL: odosBaseURL,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}
	if o.client == nil {
		builder := http_client.NewClientBuilder().SetRetry(0, 0)
		if o.timeout != nil {
			builder.SetTimeout(*o.timeout)
		}
		if o.maxRetries != nil && o.retryDelay != nil {
			builder.SetRetry(*o.maxRetries, *o.retryDelay)
		}
		o.client = builder.BuildClient()
	}
	return o, nil
}

// GetQuote fetches an Odos route quote.
func (o *odos) GetQuote(ctx context.Context, req QuoteRequest) (*Quote, error) {
	var quote Quote
	if err := o.postJSON(ctx, "/sor/quote/v2", req, "odos quote", &quote); err != nil {
		return nil, err
	}
	return &quote, nil
}

// Assemble builds executable transaction data for a quoted Odos route.
func (o *odos) Assemble(ctx context.Context, req AssembleRequest) (*Assemble, error) {
	var assemble Assemble
	if err := o.postJSON(ctx, "/sor/assemble", req, "odos assemble", &assemble); err != nil {
		return nil, err
	}
	return &assemble, nil
}

func (o *odos) Close() error {
	o.client.CloseIdleConnections()
	return nil
}

func (o *odos) postJSON(ctx context.Context, path string, requestBody any, operation string, responseBody any) error {
	resp, err := o.client.PostJSONWithContext(ctx, o.baseURL+path, requestBody)
	if err != nil {
		if statusCode, body, ok := http_client.ResponseStatus(err); ok {
			body = strings.TrimSpace(body)
			if body == "" {
				return fmt.Errorf("%s API returned status %d", operation, statusCode)
			}
			return fmt.Errorf("%s API returned status %d: %s", operation, statusCode, body)
		}
		return fmt.Errorf("failed to call %s API: %w", operation, err)
	}
	if resp.Body != nil {
		defer func() { _ = resp.Body.Close() }()
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body := ""
		if resp.Body != nil {
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			body = strings.TrimSpace(string(raw))
		}
		if body == "" {
			return fmt.Errorf("%s API returned status %d", operation, resp.StatusCode)
		}
		return fmt.Errorf("%s API returned status %d: %s", operation, resp.StatusCode, body)
	}

	if err := json.NewDecoder(resp.Body).Decode(responseBody); err != nil {
		return fmt.Errorf("failed to decode %s response: %w", operation, err)
	}
	return nil
}
