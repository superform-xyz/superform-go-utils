package dynamic

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestVerifyWebhookUsesExactBodyAndRejectsTampering(t *testing.T) {
	body := []byte(`{ "environmentId":"env", "eventName":"user.session.revoked", "data":{"id":"session"} }`)
	mac := hmac.New(sha256.New, []byte("secret"))
	_, _ = mac.Write(body)
	signature := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	event, err := VerifyWebhook(body, signature, "secret")
	require.NoError(t, err)
	require.Equal(t, "env", event.EnvironmentID)
	for _, test := range []struct {
		body              []byte
		signature, secret string
	}{
		{append(append([]byte{}, body...), ' '), signature, "secret"},
		{body, signature, "wrong"}, {body, signature, ""}, {body, "", "secret"},
		{body, "sha256=00", "secret"}, {body, "sha256=zz", "secret"},
	} {
		_, err := VerifyWebhook(test.body, test.signature, test.secret)
		require.Error(t, err)
	}
}

func TestVerifyWebhookDecodesRevocationAndDistinguishesInvalidPayload(t *testing.T) {
	for _, test := range []struct {
		name, body string
		invalid    bool
	}{
		{"session revocation", `{"eventId":"event","eventName":"admin.user.session.revoked","environmentId":"env","timestamp":"2026-10-22T05:00:00Z","data":{"id":"session","revokedAt":"2026-10-22T05:00:00Z"}}`, false},
		{"invalid JSON with valid signature", `{`, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			mac := hmac.New(sha256.New, []byte("secret"))
			_, _ = mac.Write([]byte(test.body))
			event, err := VerifyWebhook([]byte(test.body), "sha256="+hex.EncodeToString(mac.Sum(nil)), "secret")
			if test.invalid {
				require.ErrorIs(t, err, ErrInvalidWebhookPayload)
				return
			}
			require.NoError(t, err)
			require.Equal(t, "event", event.EventID)
			require.Equal(t, "admin.user.session.revoked", event.EventName)
			require.False(t, event.Timestamp.IsZero())
			var session Session
			require.NoError(t, json.Unmarshal(event.Data, &session))
			require.Equal(t, "session", session.ID)
			require.NotNil(t, session.RevokedAt)
		})
	}
}
