package hyperliquid

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const MainnetExchangeURL = "https://api.hyperliquid.xyz/exchange"

// ErrorDispatch identifies refusal by the caller's pre-dispatch callback.
const ErrorDispatch ErrorKind = "dispatch"

var ErrMalformedEnvelope = errors.New("hyperliquid: malformed signed envelope")

// SignedEnvelope retains the exact action JSON and the signature fields. It
// carries no application admission rules or approved-agent decision.
type SignedEnvelope struct {
	Action       json.RawMessage
	Nonce        int64
	Signature    []byte
	VaultAddress string
	ExpiresAfter *int64
}

type ExchangeSignature struct {
	R string `json:"r"`
	S string `json:"s"`
	V uint64 `json:"v"`
}

type ExchangeRequest struct {
	Action       json.RawMessage   `json:"action"`
	Nonce        int64             `json:"nonce"`
	Signature    ExchangeSignature `json:"signature"`
	VaultAddress *string           `json:"vaultAddress"`
	ExpiresAfter *int64            `json:"expiresAfter,omitempty"`
}

// ExchangeResponse is provider evidence. Callers interpret acceptance,
// rejection, uncertainty and each batch member's lifecycle.
type ExchangeResponse struct {
	Status   string          `json:"status"`
	Response json.RawMessage `json:"response"`
}

type ExchangeOrderReference struct {
	OrderID uint64 `json:"oid"`
	Cloid   string `json:"cloid"`
}

type ExchangeOrderStatus struct {
	Resting *ExchangeOrderReference `json:"resting"`
	Filled  *ExchangeOrderReference `json:"filled"`
	Error   string                  `json:"error"`
}

type ExchangeOrderResponse struct {
	Type string `json:"type"`
	Data struct {
		Statuses []ExchangeOrderStatus `json:"statuses"`
	} `json:"data"`
}

// ExchangeStatusesResponse preserves heterogeneous order/cancel batch statuses,
// including waitingForFill, waitingForTrigger and future venue variants.
type ExchangeStatusesResponse struct {
	Type string `json:"type"`
	Data struct {
		Statuses []json.RawMessage `json:"statuses"`
	} `json:"data"`
}

// DecodeSignatureScalar accepts the reference SDK's integer hex, including odd
// digit counts, and returns exactly 32 bytes.
func DecodeSignatureScalar(raw string) ([]byte, error) {
	if !strings.HasPrefix(raw, "0x") || len(raw) < 3 || len(raw) > 66 {
		return nil, ErrMalformedSignature
	}
	out, err := hex.DecodeString(strings.Repeat("0", 66-len(raw)) + raw[2:])
	if err != nil {
		return nil, ErrMalformedSignature
	}
	return out, nil
}

// DecodeEnvelope parses strict wire fields without requiring expiry or a
// current nonce. Applications enforce their own size and admission limits.
func DecodeEnvelope(raw []byte) (SignedEnvelope, error) {
	var request ExchangeRequest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF {
		return SignedEnvelope{}, ErrMalformedEnvelope
	}
	if request.Nonce < 0 || len(request.Action) == 0 || (request.ExpiresAfter != nil && *request.ExpiresAfter <= 0) {
		return SignedEnvelope{}, ErrMalformedEnvelope
	}
	r, err := DecodeSignatureScalar(request.Signature.R)
	if err != nil {
		return SignedEnvelope{}, ErrMalformedEnvelope
	}
	s, err := DecodeSignatureScalar(request.Signature.S)
	if err != nil {
		return SignedEnvelope{}, ErrMalformedEnvelope
	}
	v := request.Signature.V
	if v != 0 && v != 1 && v != 27 && v != 28 {
		return SignedEnvelope{}, ErrMalformedEnvelope
	}
	envelope := SignedEnvelope{
		Action: request.Action, Nonce: request.Nonce, ExpiresAfter: request.ExpiresAfter,
		Signature: append(append(r, s...), byte(v)),
	}
	if request.VaultAddress != nil {
		envelope.VaultAddress = *request.VaultAddress
	}
	return envelope, nil
}

// NewExchangeRequest serializes signature scalars without changing the signed
// action bytes, vault identity or expiry.
func NewExchangeRequest(envelope SignedEnvelope) (ExchangeRequest, error) {
	if len(envelope.Signature) != SignatureLength {
		return ExchangeRequest{}, ErrMalformedSignature
	}
	if len(envelope.Action) == 0 {
		return ExchangeRequest{}, errors.New("hyperliquid: action is required")
	}
	recovery := uint64(envelope.Signature[64])
	switch recovery {
	case 0, 1:
		recovery += 27
	case 27, 28:
	default:
		return ExchangeRequest{}, ErrMalformedSignature
	}
	request := ExchangeRequest{
		Action: envelope.Action, Nonce: envelope.Nonce, ExpiresAfter: envelope.ExpiresAfter,
		Signature: ExchangeSignature{
			R: "0x" + hex.EncodeToString(envelope.Signature[:32]),
			S: "0x" + hex.EncodeToString(envelope.Signature[32:64]), V: recovery,
		},
	}
	if vault := strings.TrimSpace(envelope.VaultAddress); vault != "" {
		normalized, err := NormalizeAddress(vault)
		if err != nil {
			return ExchangeRequest{}, ErrInvalidAddress
		}
		request.VaultAddress = &normalized
	}
	return request, nil
}

type ExchangeClient struct {
	endpoint      string
	httpClient    *http.Client
	responseLimit int64
}

type ExchangeOption func(*ExchangeClient)

func WithExchangeResponseLimit(limit int64) ExchangeOption {
	return func(c *ExchangeClient) {
		if limit > 0 {
			c.responseLimit = limit
		}
	}
}

// NewExchangeClient copies the injected HTTP client and disables redirects so
// signed requests cannot be replayed to another endpoint. It never retries;
// callers must also supply a transport that does not retry signed POSTs.
func NewExchangeClient(endpoint string, client *http.Client, opts ...ExchangeOption) *ExchangeClient {
	if endpoint == "" {
		endpoint = MainnetExchangeURL
	}
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	httpClient := *client
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	c := &ExchangeClient{endpoint: endpoint, httpClient: &httpClient, responseLimit: 4 << 20}
	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}
	return c
}

// Post makes one signed attempt. beforeDispatch runs after construction and
// immediately before Do; a refusal sends nothing. Once it succeeds, callers must
// preserve their durable attempt record even when Do or response decoding fails.
// Response.Dispatched records invocation of Do, never proof of server receipt.
// Signed transport errors omit URLs, payloads and provider response prose.
func (c *ExchangeClient) Post(ctx context.Context, payload, out any, beforeDispatch func(context.Context) error) (Response, error) {
	var result Response
	if c == nil || c.httpClient == nil {
		return result, &RequestError{Kind: ErrorRequest, Err: errors.New("hyperliquid: exchange client is not configured")}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return result, &RequestError{Kind: ErrorEncode, Err: errors.New("hyperliquid: exchange request encoding failed")}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return result, &RequestError{Kind: ErrorRequest, Err: errors.New("hyperliquid: exchange request construction failed")}
	}
	req.Header.Set("Content-Type", "application/json")
	if err := ctx.Err(); err != nil {
		return result, &RequestError{Kind: ErrorRequest, Err: fmt.Errorf("hyperliquid: exchange request canceled before dispatch: %w", err)}
	}
	if beforeDispatch != nil {
		if err := beforeDispatch(ctx); err != nil {
			return result, &RequestError{Kind: ErrorDispatch, Err: err}
		}
	}
	result.Dispatched = true
	resp, err := c.httpClient.Do(req)
	if err != nil {
		cause := errors.New("hyperliquid: exchange transport failed")
		if ctx.Err() != nil {
			cause = fmt.Errorf("hyperliquid: exchange transport failed: %w", ctx.Err())
		}
		return result, &RequestError{Kind: ErrorTransport, Err: cause}
	}
	defer resp.Body.Close()
	result.StatusCode = resp.StatusCode
	result.Date = resp.Header.Get("Date")
	if resp.StatusCode == http.StatusTooManyRequests {
		return result, ErrRateLimited
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return result, &HTTPError{StatusCode: resp.StatusCode}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, c.responseLimit+1))
	if err != nil || int64(len(raw)) > c.responseLimit || json.Unmarshal(raw, out) != nil {
		return result, &RequestError{Kind: ErrorDecode, Err: errors.New("hyperliquid: exchange response decoding failed")}
	}
	return result, nil
}
