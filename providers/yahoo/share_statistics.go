package yahoo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/superform-xyz/superform-go-utils/pkg/http_client"
	"golang.org/x/time/rate"
)

// WithCookieURL overrides the anonymous session endpoint, primarily for tests.
func WithCookieURL(endpoint string) Option {
	return func(c *client) { c.cookieURL = strings.TrimSpace(endpoint) }
}

type shareSession struct {
	crumb      string
	generation uint64
}

type shareSessionAttempt struct {
	done    chan struct{}
	session shareSession
	err     error
}

func (c *client) GetShareStatistics(ctx context.Context, symbol string) (*ShareStatistics, error) {
	symbol, err := normalizeSymbol(symbol)
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	session, err := c.getShareSession(ctx, 0)
	if err != nil {
		return nil, fmt.Errorf("yahoo share statistics session: %w", err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		body, err := c.getBody(ctx, c.endpoint("/v10/finance/quoteSummary/"+url.PathEscape(symbol), url.Values{
			"modules": {"price,defaultKeyStatistics"}, "formatted": {"false"}, "crumb": {session.crumb},
		}))
		var status *HTTPError
		if attempt == 0 && errors.As(err, &status) && status.StatusCode == http.StatusUnauthorized {
			session, err = c.getShareSession(ctx, session.generation)
			if err != nil {
				return nil, fmt.Errorf("refresh Yahoo share statistics session: %w", err)
			}
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("yahoo share statistics: %w", err)
		}
		return decodeShareStatistics(body)
	}
	return nil, errors.New("Yahoo share statistics authorization failed")
}

func decodeShareStatistics(body []byte) (*ShareStatistics, error) {
	var wire struct {
		QuoteSummary struct {
			Result []struct {
				Price struct {
					Symbol, Currency, QuoteType, Exchange, ExchangeName string
				}
				DefaultKeyStatistics struct {
					FloatShares, SharesOutstanding *float64
				}
			}
			Error *YahooError
		}
	}
	if err := decodeJSON(body, &wire); err != nil {
		return nil, errors.New("decode Yahoo share statistics failed")
	}
	if wire.QuoteSummary.Error != nil || len(wire.QuoteSummary.Result) != 1 {
		return nil, errors.New("Yahoo share statistics unavailable")
	}
	result := wire.QuoteSummary.Result[0]
	return &ShareStatistics{
		Symbol: strings.ToUpper(strings.TrimSpace(result.Price.Symbol)), Currency: strings.ToUpper(strings.TrimSpace(result.Price.Currency)),
		QuoteType: strings.ToUpper(strings.TrimSpace(result.Price.QuoteType)), Exchange: firstString(result.Price.Exchange, result.Price.ExchangeName),
		FloatShares: result.DefaultKeyStatistics.FloatShares, SharesOutstanding: result.DefaultKeyStatistics.SharesOutstanding,
	}, nil
}

func (c *client) getShareSession(ctx context.Context, rejected uint64) (shareSession, error) {
	if err := ctx.Err(); err != nil {
		return shareSession{}, err
	}
	c.sessionMu.Lock()
	if c.session.crumb != "" && c.session.generation != rejected {
		session := c.session
		c.sessionMu.Unlock()
		return session, nil
	}
	if pending := c.sessionPending; pending != nil {
		c.sessionMu.Unlock()
		select {
		case <-ctx.Done():
			return shareSession{}, ctx.Err()
		case <-pending.done:
			return pending.session, pending.err
		}
	}
	pending := &shareSessionAttempt{done: make(chan struct{})}
	c.sessionPending = pending
	c.session.crumb = ""
	c.sessionMu.Unlock()

	crumb, err := c.fetchShareSession(ctx)
	c.sessionMu.Lock()
	if err == nil {
		c.session.generation++
		c.session.crumb = crumb
	}
	pending.session, pending.err = c.session, err
	c.sessionPending = nil
	close(pending.done)
	c.sessionMu.Unlock()
	return pending.session, pending.err
}

func (c *client) fetchShareSession(ctx context.Context) (string, error) {
	// Yahoo normally sets its cookie on a 404. Skip the shared retry layer for
	// this bootstrap only, retaining the inner cookie, pacing, and metrics path.
	cookieClient := *c.httpClient.Client
	if retry, ok := cookieClient.Transport.(*http_client.Transport); ok {
		cookieClient.Transport = retry.BaseTransport
	}
	resp, err := c.doGETWithClient(ctx, c.cookieURL, &cookieClient)
	var status *HTTPError
	if err != nil && !(errors.As(err, &status) && status.StatusCode == http.StatusNotFound) {
		return "", err
	}
	if resp != nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorResponse))
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound && (resp.StatusCode < 200 || resp.StatusCode >= 300) {
			return "", &HTTPError{StatusCode: resp.StatusCode}
		}
	}
	base, err := url.Parse(c.baseURL)
	if err != nil || len(c.jar.Cookies(base)) == 0 {
		return "", errors.New("Yahoo session cookie unavailable")
	}
	body, err := c.getBody(ctx, c.endpoint("/v1/test/getcrumb", nil))
	if err != nil {
		return "", fmt.Errorf("fetch Yahoo crumb: %w", err)
	}
	crumb := strings.TrimSpace(string(body))
	if crumb == "" || len(crumb) > 1024 || strings.ContainsAny(crumb, " \t\r\n<>{}\"") {
		return "", errors.New("invalid Yahoo session response")
	}
	return crumb, nil
}

type sessionTransport struct {
	base    http.RoundTripper
	jar     http.CookieJar
	limiter *rate.Limiter
}

func (t *sessionTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.limiter != nil {
		if err := t.limiter.Wait(req.Context()); err != nil {
			return nil, err
		}
	}
	request := req.Clone(req.Context())
	request.Header.Del("Cookie")
	for _, cookie := range t.jar.Cookies(request.URL) {
		request.AddCookie(cookie)
	}
	resp, err := t.base.RoundTrip(request)
	if resp != nil {
		t.jar.SetCookies(request.URL, resp.Cookies())
	}
	return resp, err
}

func (t *sessionTransport) CloseIdleConnections() {
	if closer, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

// Shared transports can include response bodies and credential-bearing URLs in
// errors. Retain status/context classifications without exposing session data.
func safeRequestError(err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	if status, _, ok := http_client.ResponseStatus(err); ok {
		return &HTTPError{StatusCode: status}
	}
	return errors.New("Yahoo HTTP request failed")
}
