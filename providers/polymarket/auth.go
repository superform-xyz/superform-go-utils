package polymarket

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	protocol "github.com/superform-xyz/superform-go-utils/utils/polymarket"
)

// L1Authorization is a wallet-produced proof for one credential nonce. It
// authenticates an API signer, independently of the order's funding wallet.
type L1Authorization struct {
	Address   string
	Timestamp int64
	Nonce     uint64
	Signature string
}

func (L1Authorization) String() string   { return "[redacted polymarket authorization]" }
func (L1Authorization) GoString() string { return "[redacted polymarket authorization]" }
func (L1Authorization) MarshalJSON() ([]byte, error) {
	return []byte(`"[redacted polymarket authorization]"`), nil
}

type CredentialClient interface {
	CreateOrDeriveCredentials(context.Context, L1Authorization) (Credentials, error)
	RevokeCredentials(context.Context, Credentials) error
}

// NewCredentialClient shares the trading client's HTTPS, timeout, and redirect
// protections. Credential creation does not enable trading.
func NewCredentialClient(options ...Option) (CredentialClient, error) {
	c, err := New(options...)
	if err != nil {
		return nil, err
	}
	return c.(*client), nil
}

// CreateOrDeriveCredentials attempts creation once and then derives the same
// signer/nonce on failure, including an ambiguous creation response. It never
// retries a credential-creation POST or changes the nonce behind the caller.
func (c *client) CreateOrDeriveCredentials(ctx context.Context, auth L1Authorization) (Credentials, error) {
	now := c.now()
	at := time.Unix(auth.Timestamp, 0)
	if at.Before(now.Add(-5*time.Minute)) || at.After(now.Add(30*time.Second)) {
		return Credentials{}, errors.New("polymarket: authentication proof is expired or future-dated")
	}
	if err := protocol.VerifyAuthSignature(auth.Address, auth.Timestamp, auth.Nonce, auth.Signature); err != nil {
		return Credentials{}, err
	}
	creds, err := c.exchangeCredentials(ctx, http.MethodPost, "/auth/api-key", auth)
	if err == nil {
		return creds, nil
	}
	if ctx.Err() != nil {
		return Credentials{}, ctx.Err()
	}
	var providerErr *HTTPError
	if errors.As(err, &providerErr) && (providerErr.StatusCode == http.StatusUnauthorized || providerErr.StatusCode == http.StatusForbidden || providerErr.StatusCode == http.StatusTooManyRequests) {
		return Credentials{}, err
	}
	return c.exchangeCredentials(ctx, http.MethodGet, "/auth/derive-api-key", auth)
}

func (c *client) exchangeCredentials(ctx context.Context, method, path string, auth L1Authorization) (Credentials, error) {
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, nil)
	if err != nil {
		return Credentials{}, errors.New("polymarket: cannot construct credential request")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("POLY_ADDRESS", strings.ToLower(auth.Address))
	request.Header.Set("POLY_TIMESTAMP", strconv.FormatInt(auth.Timestamp, 10))
	request.Header.Set("POLY_NONCE", strconv.FormatUint(auth.Nonce, 10))
	request.Header.Set("POLY_SIGNATURE", auth.Signature)
	response, err := c.httpClient.Do(request)
	if err != nil {
		return Credentials{}, errors.New("polymarket: credential request failed")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		// Provider errors can echo credentials or signatures; retain only status.
		return Credentials{}, &HTTPError{StatusCode: response.StatusCode, RetryAfter: retryAfter(response)}
	}
	payload, err := readBounded(response.Body, 4096)
	if err != nil {
		return Credentials{}, errors.New("polymarket: invalid credential response")
	}
	defer clear(payload)
	var wire struct {
		APIKey     string `json:"apiKey"`
		Secret     string `json:"secret"`
		Passphrase string `json:"passphrase"`
	}
	if json.Unmarshal(payload, &wire) != nil {
		return Credentials{}, errors.New("polymarket: invalid credential response")
	}
	creds := Credentials{Address: strings.ToLower(auth.Address), APIKey: wire.APIKey, APISecret: wire.Secret, Passphrase: wire.Passphrase}
	if err := creds.Validate(); err != nil {
		return Credentials{}, errors.New("polymarket: invalid credential response")
	}
	return creds, nil
}

// RevokeCredentials revokes the signer credential itself and therefore affects
// every connection using it. Disconnecting one funding wallet is an app concern.
func (c *client) RevokeCredentials(ctx context.Context, credentials Credentials) error {
	if err := requireCredentials(credentials); err != nil {
		return err
	}
	err := c.do(ctx, http.MethodDelete, "/auth/api-key", nil, nil, &credentials, 4096, nil)
	var providerErr *HTTPError
	if errors.As(err, &providerErr) {
		return &HTTPError{StatusCode: providerErr.StatusCode, RetryAfter: providerErr.RetryAfter}
	}
	return err
}
