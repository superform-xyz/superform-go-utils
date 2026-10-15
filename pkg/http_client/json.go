package http_client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Doer is satisfied by *Client and *http.Client.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// GetJSON sends a GET request and decodes a JSON response into T.
func GetJSON[T any](ctx context.Context, c Doer, url string) (T, error) {
	var zero T
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return zero, fmt.Errorf("creating GET request: %w", err)
	}
	req.Header.Set("Accept", ContentTypeJSON)
	return DoJSON[T](c, req)
}

// PostJSON sends payload as a JSON POST request and decodes a JSON response into T.
func PostJSON[T any](ctx context.Context, c Doer, url string, payload any) (T, error) {
	var zero T
	body, err := json.Marshal(payload)
	if err != nil {
		return zero, fmt.Errorf("marshalling JSON request body: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return zero, fmt.Errorf("creating POST request: %w", err)
	}
	req.Header.Set("Content-Type", ContentTypeJSON)
	req.Header.Set("Accept", ContentTypeJSON)
	return DoJSON[T](c, req)
}

// DoJSON sends req and decodes a JSON response into T. Non-2xx responses become
// errors readable with ResponseStatus, even when c is a plain *http.Client.
func DoJSON[T any](c Doer, req *http.Request) (T, error) {
	var zero T
	resp, err := c.Do(req)
	if err != nil {
		return zero, err
	}
	return DecodeJSON[T](resp)
}

// DecodeJSON decodes a JSON response body into T and closes it.
func DecodeJSON[T any](resp *http.Response) (T, error) {
	var out T
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return out, newStatusError(resp)
	}
	defer resp.Body.Close()

	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return out, fmt.Errorf("decoding JSON response: %w", err)
	}
	// Drain trailing whitespace so the connection can be reused.
	_, _ = io.Copy(io.Discard, resp.Body)
	return out, nil
}
