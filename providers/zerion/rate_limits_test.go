package zerion

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseRateLimits(t *testing.T) {
	now := time.Date(2026, 10, 15, 8, 0, 0, 0, time.UTC)
	h := make(http.Header)
	h.Set("RateLimit-Org-Day-Limit", "1000")
	h.Set("RateLimit-Org-Day-Remaining", "0")
	h.Set("RateLimit-Org-Day-Reset", "60")
	h.Set("RateLimit-Org-Month-Reset", "9223372036854775807")
	h.Set("RateLimit-Org-Month-Remaining", "-1")
	h.Set("Retry-After", now.Add(2*time.Minute).Format(http.TimeFormat))
	limits := ParseRateLimits(h, now)
	require.EqualValues(t, 1000, *limits.Day.Limit)
	require.EqualValues(t, 0, *limits.Day.Remaining)
	require.Equal(t, now.Add(time.Minute), limits.Day.ResetAt)
	require.Nil(t, limits.Second.Remaining)
	require.Nil(t, limits.Month.Remaining)
	require.True(t, limits.Month.ResetAt.IsZero())
	require.Equal(t, now.Add(2*time.Minute), limits.RetryAfter)
}
