package zerion

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	MaxWalletsPerPatch    = 100
	MaxWalletPageSize     = 2000
	maxAPIErrorBodyBytes  = 4 << 10
	maxAPIErrorReadBytes  = 64 << 10
	maxAPIResponseBytes   = 16 << 20
	defaultTxSubTimeout   = 15 * time.Second
	rateLimitSecondRemain = "RateLimit-Org-Second-Remaining"
	rateLimitSecondReset  = "RateLimit-Org-Second-Reset"
	rateLimitDayRemain    = "RateLimit-Org-Day-Remaining"
	rateLimitMonthRemain  = "RateLimit-Org-Month-Remaining"
)

var (
	errInvalidTxSubscriptionRequest = errors.New("zerion: invalid tx-subscription request")
	errUntrustedPaginationLink      = errors.New("zerion: untrusted pagination link")
)

// TxSubscriptionClient manages Zerion tx-subscriptions. It never retries; callers own retry and budget.
type TxSubscriptionClient interface {
	CreateSubscription(ctx context.Context, req CreateTxSubscriptionRequest) (TxSubscription, ResponseMeta, error)
	GetSubscription(ctx context.Context, id string) (TxSubscription, ResponseMeta, error)
	PatchWallets(ctx context.Context, id string, add, remove []string) (ResponseMeta, error)
	ListWallets(ctx context.Context, id string, pageSize int, cursor string) (WalletPage, ResponseMeta, error)
	CountWallets(ctx context.Context, id string) (int64, ResponseMeta, error)
	DisableSubscription(ctx context.Context, id string) (ResponseMeta, error)
	EnableSubscription(ctx context.Context, id string) (ResponseMeta, error)
	ListChains(ctx context.Context) ([]ChainInfo, ResponseMeta, error)
}

type ResponseMeta struct {
	StatusCode int
	RateLimit  RateLimit
}

type RateLimit struct {
	SecondRemaining *int64
	DayRemaining    *int64
	MonthRemaining  *int64
	SecondReset     time.Duration
}

type APIErrorObject struct {
	Title  string `json:"title"`
	Detail string `json:"detail"`
}

type APIError struct {
	StatusCode int
	Errors     []APIErrorObject
	Body       string // truncated to 4 KiB
	RateLimit  RateLimit
}

func (e *APIError) Error() string {
	msg := fmt.Sprintf("zerion: status %d", e.StatusCode)
	if len(e.Errors) > 0 {
		parts := make([]string, 0, len(e.Errors))
		for _, obj := range e.Errors {
			parts = append(parts, strings.TrimSpace(obj.Title+": "+obj.Detail))
		}
		return msg + ": " + strings.Join(parts, "; ")
	}
	if e.Body != "" {
		return msg + ": " + e.Body
	}
	return msg
}

type TxSubscription struct {
	ID          string
	CallbackURL string
	Status      string // empty when Zerion omits it
	ChainIDs    []string
	Filters     json.RawMessage
}

type CreateTxSubscriptionRequest struct {
	CallbackURL string
	Addresses   []string
	ChainIDs    []string
	Filters     json.RawMessage
}

type WalletPage struct {
	Addresses  []string
	NextCursor string // opaque; empty on the last page
}

type ChainInfo struct {
	ID                   string
	ChainID              uint64
	SupportsTransactions bool
	SupportsPositions    bool
}

type TxSubscriptionOption func(*txSubscriptionClient)

func WithTxSubscriptionBaseURL(baseURL string) TxSubscriptionOption {
	return func(c *txSubscriptionClient) {
		if baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/"); baseURL != "" {
			c.rawBaseURL = baseURL
		}
	}
}

func WithTxSubscriptionHTTPClient(client *http.Client) TxSubscriptionOption {
	return func(c *txSubscriptionClient) {
		if client != nil {
			c.httpClient = client
		}
	}
}

// WithTxSubscriptionTimeout bounds each request; zero or negative disables the per-request deadline.
func WithTxSubscriptionTimeout(d time.Duration) TxSubscriptionOption {
	return func(c *txSubscriptionClient) { c.timeout = d }
}

type txSubscriptionClient struct {
	apiKey     string
	rawBaseURL string
	baseURL    *url.URL
	httpClient *http.Client
	timeout    time.Duration
}

var _ TxSubscriptionClient = (*txSubscriptionClient)(nil)

func NewTxSubscriptionClient(apiKey string, opts ...TxSubscriptionOption) (TxSubscriptionClient, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, fmt.Errorf("zerion: apiKey is required")
	}
	c := &txSubscriptionClient{
		apiKey:     apiKey,
		rawBaseURL: zerionBaseURL,
		httpClient: &http.Client{},
		timeout:    defaultTxSubTimeout,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}
	base, err := url.Parse(c.rawBaseURL)
	if err != nil || (base.Scheme != "https" && base.Scheme != "http") || base.Host == "" {
		return nil, fmt.Errorf("zerion: invalid base url %q", c.rawBaseURL)
	}
	base.RawQuery, base.Fragment = "", ""
	c.baseURL = base
	return c, nil
}

type createSubscriptionBody struct {
	CallbackURL string          `json:"callback_url"`
	Addresses   []string        `json:"addresses"`
	ChainIDs    []string        `json:"chain_ids,omitempty"`
	Filters     json.RawMessage `json:"filters,omitempty"`
}

func (c *txSubscriptionClient) CreateSubscription(ctx context.Context, req CreateTxSubscriptionRequest) (TxSubscription, ResponseMeta, error) {
	if strings.TrimSpace(req.CallbackURL) == "" {
		return TxSubscription{}, ResponseMeta{}, fmt.Errorf("%w: callback url is required", errInvalidTxSubscriptionRequest)
	}
	if len(req.Addresses) > MaxWalletsPerPatch {
		return TxSubscription{}, ResponseMeta{}, fmt.Errorf("%w: %d addresses exceeds %d", errInvalidTxSubscriptionRequest, len(req.Addresses), MaxWalletsPerPatch)
	}
	if len(req.Filters) > 0 && !json.Valid(req.Filters) {
		return TxSubscription{}, ResponseMeta{}, fmt.Errorf("%w: filters is not valid JSON", errInvalidTxSubscriptionRequest)
	}
	body := createSubscriptionBody{CallbackURL: req.CallbackURL, Addresses: req.Addresses, ChainIDs: req.ChainIDs, Filters: req.Filters}
	if body.Addresses == nil {
		body.Addresses = []string{}
	}
	var out subscriptionDocument
	meta, err := c.do(ctx, http.MethodPost, c.endpoint("tx-subscriptions/"), body, &out)
	if err != nil {
		return TxSubscription{}, meta, err
	}
	sub, err := out.toSubscription()
	return sub, meta, err
}

func (c *txSubscriptionClient) GetSubscription(ctx context.Context, id string) (TxSubscription, ResponseMeta, error) {
	u, err := c.subscriptionEndpoint(id, "")
	if err != nil {
		return TxSubscription{}, ResponseMeta{}, err
	}
	var out subscriptionDocument
	meta, err := c.do(ctx, http.MethodGet, u, nil, &out)
	if err != nil {
		return TxSubscription{}, meta, err
	}
	sub, err := out.toSubscription()
	if err == nil && sub.ID != strings.TrimSpace(id) {
		return TxSubscription{}, meta, fmt.Errorf("zerion: subscription response ID %q does not match requested ID %q", sub.ID, id)
	}
	return sub, meta, err
}

type patchWalletsBody struct {
	Add    []string `json:"add,omitempty"`
	Remove []string `json:"remove,omitempty"`
}

func (c *txSubscriptionClient) PatchWallets(ctx context.Context, id string, add, remove []string) (ResponseMeta, error) {
	if n := len(add) + len(remove); n == 0 || n > MaxWalletsPerPatch {
		return ResponseMeta{}, fmt.Errorf("%w: %d wallets, want 1..%d", errInvalidTxSubscriptionRequest, n, MaxWalletsPerPatch)
	}
	for _, addr := range append(append([]string(nil), add...), remove...) {
		if strings.TrimSpace(addr) == "" {
			return ResponseMeta{}, fmt.Errorf("%w: empty wallet address", errInvalidTxSubscriptionRequest)
		}
	}
	u, err := c.subscriptionEndpoint(id, "/wallets")
	if err != nil {
		return ResponseMeta{}, err
	}
	return c.do(ctx, http.MethodPatch, u, patchWalletsBody{Add: add, Remove: remove}, nil)
}

func (c *txSubscriptionClient) ListWallets(ctx context.Context, id string, pageSize int, cursor string) (WalletPage, ResponseMeta, error) {
	if pageSize < 0 || pageSize > MaxWalletPageSize {
		return WalletPage{}, ResponseMeta{}, fmt.Errorf("%w: page size %d, want 0..%d", errInvalidTxSubscriptionRequest, pageSize, MaxWalletPageSize)
	}
	u, err := c.subscriptionEndpoint(id, "/wallets")
	if err != nil {
		return WalletPage{}, ResponseMeta{}, err
	}
	query := url.Values{}
	if cursor != "" {
		if query, err = url.ParseQuery(cursor); err != nil {
			return WalletPage{}, ResponseMeta{}, fmt.Errorf("%w: malformed cursor", errInvalidTxSubscriptionRequest)
		}
	}
	if pageSize > 0 {
		query.Set("page[size]", strconv.Itoa(pageSize))
	}
	u.RawQuery = query.Encode()

	var out struct {
		Links struct {
			Next string `json:"next"`
		} `json:"links"`
		Data json.RawMessage `json:"data"`
	}
	meta, err := c.do(ctx, http.MethodGet, u, nil, &out)
	if err != nil {
		return WalletPage{}, meta, err
	}
	page := WalletPage{Addresses: decodeWalletAddresses(out.Data)}
	if page.NextCursor, err = c.cursorFromNext(u, out.Links.Next); err != nil {
		return WalletPage{}, meta, err
	}
	return page, meta, nil
}

func (c *txSubscriptionClient) CountWallets(ctx context.Context, id string) (int64, ResponseMeta, error) {
	u, err := c.subscriptionEndpoint(id, "/wallets/count")
	if err != nil {
		return 0, ResponseMeta{}, err
	}
	var out struct {
		Data struct {
			Attributes struct {
				Count *int64 `json:"count"`
			} `json:"attributes"`
		} `json:"data"`
	}
	meta, err := c.do(ctx, http.MethodGet, u, nil, &out)
	if err != nil {
		return 0, meta, err
	}
	if out.Data.Attributes.Count == nil {
		return 0, meta, fmt.Errorf("zerion: wallet count response missing data.attributes.count")
	}
	return *out.Data.Attributes.Count, meta, nil
}

func (c *txSubscriptionClient) DisableSubscription(ctx context.Context, id string) (ResponseMeta, error) {
	u, err := c.subscriptionEndpoint(id, "/disable")
	if err != nil {
		return ResponseMeta{}, err
	}
	return c.do(ctx, http.MethodPatch, u, nil, nil)
}

func (c *txSubscriptionClient) EnableSubscription(ctx context.Context, id string) (ResponseMeta, error) {
	u, err := c.subscriptionEndpoint(id, "/enable")
	if err != nil {
		return ResponseMeta{}, err
	}
	return c.do(ctx, http.MethodPatch, u, nil, nil)
}

func (c *txSubscriptionClient) ListChains(ctx context.Context) ([]ChainInfo, ResponseMeta, error) {
	var out struct {
		Data []struct {
			ID         string `json:"id"`
			Attributes struct {
				ExternalID string `json:"external_id"`
				Flags      struct {
					SupportsTransactions bool `json:"supports_transactions"`
					SupportsPositions    bool `json:"supports_positions"`
				} `json:"flags"`
			} `json:"attributes"`
		} `json:"data"`
	}
	meta, err := c.do(ctx, http.MethodGet, c.endpoint("chains/"), nil, &out)
	if err != nil {
		return nil, meta, err
	}
	chains := make([]ChainInfo, 0, len(out.Data))
	for _, d := range out.Data {
		slug := strings.ToLower(strings.TrimSpace(d.ID))
		if slug == "" {
			continue
		}
		info := ChainInfo{
			ID:                   slug,
			SupportsTransactions: d.Attributes.Flags.SupportsTransactions,
			SupportsPositions:    d.Attributes.Flags.SupportsPositions,
		}
		if id, ok := parseHexChainID(d.Attributes.ExternalID); ok {
			info.ChainID = id
		} else if id, ok := ChainID(slug); ok {
			info.ChainID = id
		}
		chains = append(chains, info)
	}
	return chains, meta, nil
}

// InvalidWalletFromError returns the single batch address named by a 400 error; zero or several matches return false.
func InvalidWalletFromError(err error, batch []string) (string, bool) {
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr == nil || apiErr.StatusCode != http.StatusBadRequest {
		return "", false
	}
	var sb strings.Builder
	for _, obj := range apiErr.Errors {
		sb.WriteString(obj.Title)
		sb.WriteByte('\n')
		sb.WriteString(obj.Detail)
		sb.WriteByte('\n')
	}
	sb.WriteString(apiErr.Body)
	haystack := strings.ToLower(sb.String())

	var match string
	seen := make(map[string]struct{}, len(batch))
	for _, addr := range batch {
		norm := strings.ToLower(strings.TrimSpace(addr))
		if norm == "" {
			continue
		}
		if _, dup := seen[norm]; dup {
			continue
		}
		seen[norm] = struct{}{}
		if containsToken(haystack, norm) {
			if match != "" {
				return "", false
			}
			match = addr
		}
	}
	return match, match != ""
}

// containsToken reports whether needle occurs with no ASCII alphanumeric on either side.
func containsToken(haystack, needle string) bool {
	for offset := 0; ; {
		i := strings.Index(haystack[offset:], needle)
		if i < 0 {
			return false
		}
		start := offset + i
		end := start + len(needle)
		if (start == 0 || !isASCIIAlnum(haystack[start-1])) && (end == len(haystack) || !isASCIIAlnum(haystack[end])) {
			return true
		}
		offset = start + 1
	}
}

func isASCIIAlnum(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func (c *txSubscriptionClient) endpoint(path string) *url.URL {
	u := *c.baseURL
	u.Path = strings.TrimRight(u.Path, "/") + "/" + path
	u.RawPath = ""
	return &u
}

func (c *txSubscriptionClient) subscriptionEndpoint(id, suffix string) (*url.URL, error) {
	id = strings.TrimSpace(id)
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, "/?#%\\") || strings.IndexFunc(id, func(r rune) bool { return r <= ' ' || r == 0x7f }) >= 0 {
		return nil, fmt.Errorf("%w: invalid subscription id %q", errInvalidTxSubscriptionRequest, id)
	}
	return c.endpoint("tx-subscriptions/" + id + suffix), nil
}

// cursorFromNext turns links.next into an opaque cursor, refusing links off the base scheme/host or request path.
func (c *txSubscriptionClient) cursorFromNext(reqURL *url.URL, next string) (string, error) {
	next = strings.TrimSpace(next)
	if next == "" {
		return "", nil
	}
	nu, err := reqURL.Parse(next)
	if err != nil {
		return "", fmt.Errorf("%w: unparseable", errUntrustedPaginationLink)
	}
	switch {
	case !strings.EqualFold(nu.Scheme, c.baseURL.Scheme) || !strings.EqualFold(nu.Host, c.baseURL.Host):
		return "", fmt.Errorf("%w: %s://%s is not the api host", errUntrustedPaginationLink, nu.Scheme, nu.Host)
	case nu.User != nil:
		return "", fmt.Errorf("%w: userinfo present", errUntrustedPaginationLink)
	case strings.TrimRight(nu.Path, "/") != strings.TrimRight(reqURL.Path, "/"):
		return "", fmt.Errorf("%w: path %q differs from request", errUntrustedPaginationLink, nu.Path)
	case nu.RawQuery == "":
		return "", fmt.Errorf("%w: missing cursor query", errUntrustedPaginationLink)
	case nu.RawQuery == reqURL.RawQuery:
		return "", fmt.Errorf("%w: pagination did not advance", errUntrustedPaginationLink)
	}
	return nu.RawQuery, nil
}

func (c *txSubscriptionClient) do(ctx context.Context, method string, u *url.URL, body, out any) (ResponseMeta, error) {
	var meta ResponseMeta
	if ctx == nil {
		ctx = context.Background()
	}
	if c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return meta, fmt.Errorf("zerion: encode %s %s: %w", method, u.Path, err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), reader)
	if err != nil {
		return meta, fmt.Errorf("zerion: build %s %s: %w", method, u.Path, err)
	}
	req.Header.Set("Authorization", "Basic "+c.apiKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return meta, fmt.Errorf("zerion: %s %s: %w", method, u.Path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	meta.StatusCode = resp.StatusCode
	meta.RateLimit = parseRateLimit(resp.Header)
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return meta, newAPIError(resp, meta.RateLimit)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxAPIErrorReadBytes))
		return meta, nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxAPIResponseBytes)).Decode(out); err != nil {
		return meta, fmt.Errorf("zerion: decode %s %s: %w", method, u.Path, err)
	}
	return meta, nil
}

func newAPIError(resp *http.Response, rl RateLimit) *APIError {
	apiErr := &APIError{StatusCode: resp.StatusCode, RateLimit: rl}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxAPIErrorReadBytes))
	var parsed struct {
		Errors []APIErrorObject `json:"errors"`
	}
	if json.Unmarshal(raw, &parsed) == nil {
		apiErr.Errors = parsed.Errors
	}
	body := strings.TrimSpace(string(raw))
	if len(body) > maxAPIErrorBodyBytes {
		body = strings.ToValidUTF8(body[:maxAPIErrorBodyBytes], "")
	}
	apiErr.Body = body
	return apiErr
}

func parseRateLimit(h http.Header) RateLimit {
	rl := RateLimit{
		SecondRemaining: parseInt64Header(h, rateLimitSecondRemain),
		DayRemaining:    parseInt64Header(h, rateLimitDayRemain),
		MonthRemaining:  parseInt64Header(h, rateLimitMonthRemain),
	}
	// Reset is delta-seconds per the IETF RateLimit header convention.
	if v := strings.TrimSpace(h.Get(rateLimitSecondReset)); v != "" {
		if secs, err := strconv.ParseFloat(v, 64); err == nil && secs > 0 && secs < 1e9 {
			rl.SecondReset = time.Duration(secs * float64(time.Second))
		}
	}
	return rl
}

func parseInt64Header(h http.Header, name string) *int64 {
	v := strings.TrimSpace(h.Get(name))
	if v == "" {
		return nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return nil
	}
	return &n
}

func parseHexChainID(externalID string) (uint64, bool) {
	s := strings.ToLower(strings.TrimSpace(externalID))
	if !strings.HasPrefix(s, "0x") || len(s) < 3 {
		return 0, false
	}
	n, err := strconv.ParseUint(s[2:], 16, 64)
	return n, err == nil && n > 0
}

// decodeWalletAddresses accepts data as {attributes:{addresses:[]}} or as an array of strings / {id, attributes.address}.
func decodeWalletAddresses(raw json.RawMessage) []string {
	raw = bytes.TrimSpace(raw)
	out := []string{}
	add := func(addr string) {
		if addr = strings.ToLower(strings.TrimSpace(addr)); addr != "" {
			out = append(out, addr)
		}
	}
	if len(raw) == 0 {
		return out
	}
	switch raw[0] {
	case '{':
		var obj struct {
			Attributes struct {
				Addresses []string `json:"addresses"`
				Address   string   `json:"address"`
			} `json:"attributes"`
		}
		if json.Unmarshal(raw, &obj) == nil {
			for _, a := range obj.Attributes.Addresses {
				add(a)
			}
			add(obj.Attributes.Address)
		}
	case '[':
		var items []json.RawMessage
		if json.Unmarshal(raw, &items) != nil {
			return out
		}
		for _, item := range items {
			var s string
			if json.Unmarshal(item, &s) == nil {
				add(s)
				continue
			}
			var entry struct {
				ID         string `json:"id"`
				Attributes struct {
					Address string `json:"address"`
				} `json:"attributes"`
			}
			if json.Unmarshal(item, &entry) == nil {
				if entry.Attributes.Address != "" {
					add(entry.Attributes.Address)
				} else {
					add(entry.ID)
				}
			}
		}
	}
	return out
}

type subscriptionDocument struct {
	Data *subscriptionResource `json:"data"`
	subscriptionResource
}

// The API returns a bare subscription resource in production and documents a
// data wrapper. Accept both response shapes and require an ID.
func (d subscriptionDocument) toSubscription() (TxSubscription, error) {
	resource := d.subscriptionResource
	if d.Data != nil {
		resource = *d.Data
	}
	sub := resource.toSubscription()
	if sub.ID == "" {
		return TxSubscription{}, errors.New("zerion: subscription response missing ID")
	}
	return sub, nil
}

type subscriptionResource struct {
	ID         string `json:"id"`
	Attributes struct {
		CallbackURL string          `json:"callback_url"`
		Status      string          `json:"status"`
		Enabled     *bool           `json:"enabled"`
		ChainIDs    []string        `json:"chain_ids"`
		Filters     json.RawMessage `json:"filters"`
	} `json:"attributes"`
	Relationships struct {
		Chains json.RawMessage `json:"chains"`
	} `json:"relationships"`
}

func (r subscriptionResource) toSubscription() TxSubscription {
	sub := TxSubscription{
		ID:          strings.TrimSpace(r.ID),
		CallbackURL: r.Attributes.CallbackURL,
		Status:      strings.TrimSpace(r.Attributes.Status),
	}
	if sub.Status == "" && r.Attributes.Enabled != nil {
		sub.Status = "disabled"
		if *r.Attributes.Enabled {
			sub.Status = "enabled"
		}
	}
	if f := bytes.TrimSpace(r.Attributes.Filters); len(f) > 0 && !bytes.Equal(f, []byte("null")) {
		sub.Filters = append(json.RawMessage(nil), f...)
	}
	chainIDs := r.Attributes.ChainIDs
	if len(chainIDs) == 0 {
		chainIDs = relationshipChainIDs(r.Relationships.Chains)
	}
	for _, id := range chainIDs {
		if id = strings.ToLower(strings.TrimSpace(id)); id != "" {
			sub.ChainIDs = append(sub.ChainIDs, id)
		}
	}
	return sub
}

// relationshipChainIDs accepts chains as [{data:{id}}] (documented) or {data:[{id}]} (JSON:API to-many).
func relationshipChainIDs(raw json.RawMessage) []string {
	var ids []string
	var asArray []struct {
		Data ResourceIdentifier `json:"data"`
	}
	if json.Unmarshal(raw, &asArray) == nil {
		for _, item := range asArray {
			ids = append(ids, item.Data.ID)
		}
		return ids
	}
	var asObject struct {
		Data []ResourceIdentifier `json:"data"`
	}
	if json.Unmarshal(raw, &asObject) == nil {
		for _, item := range asObject.Data {
			ids = append(ids, item.ID)
		}
	}
	return ids
}
