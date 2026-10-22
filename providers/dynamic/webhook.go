// Package dynamic contains reusable Dynamic provider webhook verification.
package dynamic

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const SignatureHeader = "x-dynamic-signature-256"

var ErrInvalidWebhookPayload = errors.New("invalid Dynamic webhook payload")

type WebhookEvent struct {
	EventID       string          `json:"eventId"`
	EventName     string          `json:"eventName"`
	EnvironmentID string          `json:"environmentId"`
	Timestamp     time.Time       `json:"timestamp"`
	Data          json.RawMessage `json:"data"`
}

type Session struct {
	ID        string     `json:"id"`
	RevokedAt *time.Time `json:"revokedAt"`
}

// VerifyWebhook authenticates the exact request bytes before decoding. Do not
// reserialize a parsed body: whitespace and key ordering affect the signature.
func VerifyWebhook(body []byte, signature, secret string) (WebhookEvent, error) {
	var event WebhookEvent
	if secret == "" || !strings.HasPrefix(signature, "sha256=") {
		return event, errors.New("invalid Dynamic webhook signature")
	}
	provided, err := hex.DecodeString(strings.TrimPrefix(signature, "sha256="))
	if err != nil || len(provided) != sha256.Size {
		return event, errors.New("invalid Dynamic webhook signature")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	if !hmac.Equal(provided, mac.Sum(nil)) {
		return event, errors.New("invalid Dynamic webhook signature")
	}
	if err := json.Unmarshal(body, &event); err != nil {
		return event, ErrInvalidWebhookPayload
	}
	return event, nil
}
