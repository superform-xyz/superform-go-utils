package zerion

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// RateLimitWindow preserves unknown values when the provider omits a header.
type RateLimitWindow struct {
	Limit     *int64
	Remaining *int64
	ResetAt   time.Time
}

type RateLimits struct {
	Second, Day, Month RateLimitWindow
	RetryAfter         time.Time
}

// ParseRateLimits reads documented organization quotas; Reset is seconds until
// reset, not a Unix timestamp. No advanced-quota headers are inferred.
// https://developers.zerion.io/rate-limits
func ParseRateLimits(headers http.Header, observedAt time.Time) RateLimits {
	window := func(period string) RateLimitWindow {
		prefix := "RateLimit-Org-" + period + "-"
		result := RateLimitWindow{
			Limit:     nonnegativeHeader(headers.Get(prefix + "Limit")),
			Remaining: nonnegativeHeader(headers.Get(prefix + "Remaining")),
		}
		if seconds := nonnegativeHeader(headers.Get(prefix + "Reset")); seconds != nil && *seconds <= int64((366*24*time.Hour)/time.Second) {
			result.ResetAt = observedAt.Add(time.Duration(*seconds) * time.Second)
		}
		return result
	}
	result := RateLimits{Second: window("Second"), Day: window("Day"), Month: window("Month")}
	if seconds := nonnegativeHeader(headers.Get("Retry-After")); seconds != nil && *seconds <= int64((366*24*time.Hour)/time.Second) {
		result.RetryAfter = observedAt.Add(time.Duration(*seconds) * time.Second)
	} else if at, err := http.ParseTime(headers.Get("Retry-After")); err == nil {
		result.RetryAfter = at
	}
	return result
}

func nonnegativeHeader(value string) *int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || n < 0 {
		return nil
	}
	return &n
}
