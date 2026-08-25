package zerion

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultMaxBody    = 2 << 20
	defaultSem        = 16
	defaultPageSize   = 20
	maxPageSize       = 100
	maxNextQueryLen   = 1500
	txPathSuffix      = "/transactions"
	txPathSuffixSlash = "/transactions/"
)

// Client talks to the Zerion HTTP API.
type Client struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client

	OnRequest func(operation, result string, d time.Duration)
	OnRetry   func(operation, reason string)

	sem     chan struct{}
	maxBody int64
	Sleep   func(context.Context, time.Duration) error
	Jitter  func(time.Duration) time.Duration
}

func NewClient(baseURL, apiKey string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		HTTP: &http.Client{
			Transport: &http.Transport{
				Proxy: http.ProxyFromEnvironment,
				DialContext: (&net.Dialer{
					Timeout:   3 * time.Second,
					KeepAlive: 30 * time.Second,
				}).DialContext,
				ForceAttemptHTTP2:     true,
				TLSHandshakeTimeout:   3 * time.Second,
				ResponseHeaderTimeout: 5 * time.Second,
				IdleConnTimeout:       90 * time.Second,
				MaxIdleConns:          32,
				MaxIdleConnsPerHost:   16,
			},
		},
		sem:     make(chan struct{}, defaultSem),
		maxBody: defaultMaxBody,
		Sleep:   defaultSleep,
		Jitter:  defaultJitter,
	}
}

func (c *Client) Portfolio(ctx context.Context, address, currency string) (*Portfolio, error) {
	u, err := url.Parse(c.BaseURL)
	if err != nil {
		return nil, wrapKind(KindInvalidResponse, 0, "invalid base url")
	}
	u.Path = "/v1/wallets/" + address + "/portfolio"
	q := u.Query()
	q.Set("currency", currency)
	q.Set("filter[positions]", "only_simple")
	u.RawQuery = q.Encode()

	var raw portfolioResponse
	if err := c.getJSON(ctx, "portfolio", u.String(), &raw); err != nil {
		return nil, err
	}
	if raw.Data == nil || raw.Data.Attributes == nil {
		return nil, wrapKind(KindInvalidResponse, http.StatusOK, "portfolio is missing attributes")
	}
	attr := raw.Data.Attributes
	p := &Portfolio{
		Total:     attr.Total.Positions,
		ChangeAbs: attr.Changes.Absolute1d,
		ChangePct: attr.Changes.Percent1d,
		ByType:    attr.ByType,
		ByChain:   attr.ByChain,
	}
	if p.ByType == nil {
		p.ByType = map[string]float64{}
	}
	if p.ByChain == nil {
		p.ByChain = map[string]float64{}
	}
	return p, nil
}

func (c *Client) Transactions(ctx context.Context, q TxQuery) (*TxPage, error) {
	rawURL, err := c.transactionsURL(q)
	if err != nil {
		return nil, err
	}
	var raw txResponse
	if err := c.getJSON(ctx, "transactions", rawURL, &raw); err != nil {
		return nil, err
	}
	next, err := c.parseNextLink(raw.Links.Next, q.Address)
	if err != nil {
		return nil, err
	}
	page := &TxPage{Next: next, Items: make([]Tx, 0, len(raw.Data))}
	for _, item := range raw.Data {
		page.Items = append(page.Items, mapTx(item))
	}
	return page, nil
}

func (c *Client) transactionsURL(q TxQuery) (string, error) {
	if q.RelPath != "" {
		return c.resolve(q.RelPath)
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil {
		return "", wrapKind(KindInvalidResponse, 0, "invalid base url")
	}
	u.Path = "/v1/wallets/" + q.Address + "/transactions/"
	size := q.PageSize
	if size <= 0 {
		size = defaultPageSize
	}
	if size > maxPageSize {
		size = maxPageSize
	}
	vals := u.Query()
	if q.Currency != "" {
		vals.Set("currency", q.Currency)
	}
	vals.Set("page[size]", strconv.Itoa(size))
	vals.Set("filter[trash]", "only_non_trash")
	if len(q.ChainIDs) > 0 {
		vals.Set("filter[chain_ids]", strings.Join(q.ChainIDs, ","))
	}
	if len(q.OperationTypes) > 0 {
		vals.Set("filter[operation_types]", strings.Join(q.OperationTypes, ","))
	}
	u.RawQuery = vals.Encode()
	return u.String(), nil
}

func (c *Client) resolve(rel string) (string, error) {
	base, err := url.Parse(c.BaseURL)
	if err != nil {
		return "", wrapKind(KindInvalidResponse, 0, "invalid base url")
	}
	ref, err := url.Parse(rel)
	if err != nil {
		return "", wrapKind(KindInvalidResponse, 0, "invalid continuation path")
	}
	if ref.Scheme != "" || ref.Host != "" || ref.User != nil || ref.Fragment != "" || ref.Opaque != "" {
		return "", wrapKind(KindInvalidResponse, 0, "invalid continuation path")
	}
	return base.ResolveReference(ref).String(), nil
}

func (c *Client) parseNextLink(raw, address string) (string, error) {
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", wrapKind(KindInvalidResponse, 0, "malformed next link")
	}
	if u.User != nil || u.Fragment != "" {
		return "", wrapKind(KindInvalidResponse, 0, "invalid next link")
	}
	base, err := url.Parse(c.BaseURL)
	if err != nil {
		return "", wrapKind(KindInvalidResponse, 0, "invalid base url")
	}
	if !strings.EqualFold(u.Scheme, base.Scheme) || !strings.EqualFold(u.Host, base.Host) {
		return "", wrapKind(KindInvalidResponse, 0, "invalid next link host")
	}
	if !sameWalletPath(u.Path, address) {
		return "", wrapKind(KindInvalidResponse, 0, "invalid next link path")
	}
	if len(u.RawQuery) > maxNextQueryLen {
		return "", wrapKind(KindInvalidResponse, 0, "next link query too long")
	}
	return (&url.URL{Path: u.Path, RawQuery: u.RawQuery}).RequestURI(), nil
}

func sameWalletPath(path, address string) bool {
	const prefix = "/v1/wallets/"
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	rest := strings.TrimPrefix(path, prefix)
	var got string
	switch {
	case strings.HasSuffix(rest, txPathSuffixSlash):
		got = strings.TrimSuffix(rest, txPathSuffixSlash)
	case strings.HasSuffix(rest, txPathSuffix):
		got = strings.TrimSuffix(rest, txPathSuffix)
	default:
		return false
	}
	if got == "" || strings.Contains(got, "/") {
		return false
	}
	if looksEVM(got) || looksEVM(address) {
		return strings.EqualFold(got, address)
	}
	return got == address
}

func looksEVM(s string) bool {
	return len(s) == 42 && (strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X"))
}

func (c *Client) getJSON(ctx context.Context, op, rawURL string, dest any) error {
	body, err := c.get(ctx, op, rawURL)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, dest); err != nil {
		return wrapKind(KindInvalidResponse, http.StatusOK, "malformed json")
	}
	return nil
}

func (c *Client) get(ctx context.Context, op, rawURL string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, mapCtxErr(err)
	}
	if err := c.acquire(ctx); err != nil {
		return nil, err
	}
	defer c.release()

	var (
		last   *Error
		waited time.Duration
	)
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, mapCtxErr(err)
		}

		start := time.Now()
		body, status, hdr, err := c.attempt(ctx, rawURL)
		dur := time.Since(start)

		if err != nil {
			if errors.Is(err, errTooLarge) {
				c.observe(op, "decode_error", dur)
				return nil, wrapKind(KindInvalidResponse, status, "response too large")
			}
			plan := planForTransport(err)
			c.observe(op, plan.result, dur)
			last = wrapKind(plan.kind, 0, plan.result)
			if !plan.retry || attempt == maxAttempts {
				return nil, last
			}
			delay := retryDelay(plan, attempt)
			if werr := c.wait(ctx, delay, &waited); werr != nil {
				return nil, lastOrCtx(last, werr)
			}
			c.observeRetry(ctx, op, plan.reason)
			continue
		}

		if status == http.StatusOK {
			c.observe(op, "success", dur)
			return body, nil
		}

		plan := planForStatus(status, hdr)
		c.observe(op, plan.result, dur)
		last = wrapKind(plan.kind, status, plan.result)
		if !plan.retry || attempt == maxAttempts {
			return nil, last
		}
		delay := retryDelay(plan, attempt)
		if werr := c.wait(ctx, delay, &waited); werr != nil {
			return nil, lastOrCtx(last, werr)
		}
		c.observeRetry(ctx, op, plan.reason)
	}
	if last != nil {
		return nil, last
	}
	return nil, wrapKind(KindUnavailable, 0, "upstream unavailable")
}

func (c *Client) attempt(ctx context.Context, rawURL string) ([]byte, int, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, 0, nil, err
	}
	req.SetBasicAuth(c.APIKey, "")
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, 0, nil, err
	}
	defer resp.Body.Close()

	limited := io.LimitReader(resp.Body, c.maxBody+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		if ctx.Err() != nil {
			return nil, resp.StatusCode, nil, ctx.Err()
		}
		return nil, resp.StatusCode, nil, err
	}
	if int64(len(body)) > c.maxBody {
		return nil, resp.StatusCode, resp.Header.Clone(), errTooLarge
	}
	return body, resp.StatusCode, resp.Header.Clone(), nil
}

func (c *Client) acquire(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return mapCtxErr(err)
	}
	select {
	case c.sem <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-c.sem
			return mapCtxErr(err)
		}
		return nil
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.Canceled) {
			return wrapKind(KindCanceled, 0, "canceled")
		}
		return wrapKind(KindBusy, 0, "service is busy")
	}
}

func (c *Client) release() {
	<-c.sem
}

func (c *Client) observe(op, result string, d time.Duration) {
	if c.OnRequest != nil {
		c.OnRequest(op, result, d)
	}
}

func (c *Client) observeRetry(ctx context.Context, op, reason string) {
	if p, ok := ctx.Value(retryCountKey{}).(*int); ok && p != nil {
		*p++
	}
	if c.OnRetry != nil && reason != "" {
		c.OnRetry(op, reason)
	}
}

type retryCountKey struct{}

func TrackRetries(ctx context.Context, n *int) context.Context {
	return context.WithValue(ctx, retryCountKey{}, n)
}

func mapCtxErr(err error) error {
	if errors.Is(err, context.Canceled) {
		return wrapKind(KindCanceled, 0, "canceled")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return wrapKind(KindTimeout, 0, "timeout")
	}
	return wrapKind(KindUnavailable, 0, "upstream unavailable")
}

func lastOrCtx(last *Error, err error) error {
	if errors.Is(err, context.Canceled) {
		return wrapKind(KindCanceled, 0, "canceled")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return wrapKind(KindTimeout, 0, "timeout")
	}
	if last != nil {
		return last
	}
	return wrapKind(KindUnavailable, 0, "upstream unavailable")
}

var errTooLarge = errors.New("response too large")
