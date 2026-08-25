package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/sync/singleflight"

	"github.com/ayushmishra2005/multichain-wallet-data-service/internal/zerion"
)

const upstreamBudget = 8 * time.Second

type Handler struct {
	Zerion  *zerion.Client
	Cache   *Cache
	Log     *slog.Logger
	Timeout time.Duration
	reg     *prometheus.Registry
	metrics *metrics
	flight  singleflight.Group
}

func (h *Handler) timeout() time.Duration {
	if h.Timeout > 0 {
		return h.Timeout
	}
	return upstreamBudget
}

type Summary struct {
	Address     string             `json:"address"`
	AddressType string             `json:"address_type"`
	Currency    string             `json:"currency"`
	Total       float64            `json:"total"`
	Change1d    Change             `json:"change_1d"`
	ByType      map[string]float64 `json:"by_type"`
	ByChain     map[string]float64 `json:"by_chain"`
}

type Change struct {
	Absolute float64 `json:"absolute"`
	Percent  float64 `json:"percent"`
}

type Activity struct {
	Address     string         `json:"address"`
	AddressType string         `json:"address_type"`
	Items       []ActivityItem `json:"items"`
	NextCursor  string         `json:"next_cursor,omitempty"`
}

type ActivityItem struct {
	ID            string     `json:"id"`
	Hash          string     `json:"hash"`
	Chain         string     `json:"chain"`
	OperationType string     `json:"operation_type"`
	Status        string     `json:"status"`
	MinedAt       string     `json:"mined_at"`
	From          string     `json:"from"`
	To            string     `json:"to"`
	FeeValue      *float64   `json:"fee_value,omitempty"`
	Transfers     []Transfer `json:"transfers"`
}

type Transfer struct {
	Direction string  `json:"direction"`
	Symbol    string  `json:"symbol,omitempty"`
	Amount    float64 `json:"amount"`
	Value     float64 `json:"value"`
}

func NewHandler(z *zerion.Client, log *slog.Logger) *Handler {
	if log == nil {
		log = slog.Default()
	}
	reg := prometheus.NewRegistry()
	m := newMetrics(reg)
	z.OnRequest = func(op, result string, d time.Duration) {
		m.zerionRequests.WithLabelValues(op, result).Inc()
		m.zerionDuration.WithLabelValues(op).Observe(d.Seconds())
	}
	z.OnRetry = func(op, reason string) {
		m.zerionRetries.WithLabelValues(op, reason).Inc()
	}
	return &Handler{
		Zerion:  z,
		Cache:   NewCache(1024, 15*time.Second),
		Log:     log,
		reg:     reg,
		metrics: m,
	}
}

func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", h.wrap("/healthz", h.healthz))
	mux.Handle("GET /metrics", h.wrap("/metrics", promhttp.HandlerFor(h.reg, promhttp.HandlerOpts{}).ServeHTTP))
	mux.HandleFunc("GET /v1/wallets/{address}/summary", h.wrap("/v1/wallets/{address}/summary", h.summary))
	mux.HandleFunc("GET /v1/wallets/{address}/activity", h.wrap("/v1/wallets/{address}/activity", h.activity))
	return mux
}

type reqInfo struct {
	cache    string
	upstream string
	retries  int
}

type infoKey struct{}

func (h *Handler) wrap(route string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		info := &reqInfo{cache: "n/a", upstream: "none"}
		r = r.WithContext(context.WithValue(r.Context(), infoKey{}, info))
		sw := &statusWriter{ResponseWriter: w, code: http.StatusOK}
		next(sw, r)

		class := statusClass(sw.code)
		h.metrics.httpRequests.WithLabelValues(route, r.Method, class).Inc()
		h.metrics.httpDuration.WithLabelValues(route, r.Method).Observe(time.Since(start).Seconds())
		if route == "/healthz" || route == "/metrics" {
			return
		}
		h.Log.Info("request",
			"method", r.Method,
			"route", route,
			"status", sw.code,
			"duration_ms", time.Since(start).Milliseconds(),
			"upstream_result", info.upstream,
			"retries", info.retries,
			"cache", info.cache,
		)
	}
}

func (h *Handler) healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) summary(w http.ResponseWriter, r *http.Request) {
	info := reqInfoFrom(r.Context())
	addr, typ, cacheAddr, ok := parseAddress(r.PathValue("address"))
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_address", "wallet address is not valid")
		return
	}
	if err := onlyQuery(r, "currency"); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	currency := r.URL.Query().Get("currency")
	if currency == "" {
		currency = "usd"
	}
	if !validCurrency(currency) {
		writeError(w, http.StatusBadRequest, "invalid_request", "currency is not supported")
		return
	}

	key := typ + "|" + cacheAddr + "|" + currency
	if s, hit := h.Cache.Get(key); hit {
		info.cache = "hit"
		h.metrics.cacheRequests.WithLabelValues("hit").Inc()
		writeJSON(w, http.StatusOK, s)
		return
	}
	info.cache = "miss"
	h.metrics.cacheRequests.WithLabelValues("miss").Inc()

	ctx, cancel := context.WithTimeout(r.Context(), h.timeout())
	defer cancel()
	var retries int
	ctx = zerion.TrackRetries(ctx, &retries)

	v, err, _ := h.flight.Do(key, func() (any, error) {
		if s, hit := h.Cache.Get(key); hit {
			return s, nil
		}
		p, err := h.Zerion.Portfolio(ctx, addr, currency)
		if err != nil {
			return nil, err
		}
		s := mapSummary(addr, typ, currency, p)
		h.Cache.Set(key, s)
		return s, nil
	})
	info.retries = retries
	if err != nil {
		setUpstream(info, err)
		writeZerionError(w, r, err)
		return
	}
	info.upstream = "success"
	writeJSON(w, http.StatusOK, v)
}

func (h *Handler) activity(w http.ResponseWriter, r *http.Request) {
	info := reqInfoFrom(r.Context())
	addr, typ, _, ok := parseAddress(r.PathValue("address"))
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_address", "wallet address is not valid")
		return
	}

	q := r.URL.Query()
	cursor := q.Get("cursor")
	var req zerion.TxQuery
	req.Address = addr

	if cursor != "" {
		for k := range q {
			if k != "cursor" {
				writeError(w, http.StatusBadRequest, "invalid_request", "cursor cannot be combined with other filters")
				return
			}
		}
		rel, err := decodeCursor(cursor, addr)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "invalid cursor")
			return
		}
		req.RelPath = rel
	} else {
		if err := onlyQuery(r, "currency", "page_size", "chain_ids", "operation_types"); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		currency := q.Get("currency")
		if currency == "" {
			currency = "usd"
		}
		if !validCurrency(currency) {
			writeError(w, http.StatusBadRequest, "invalid_request", "currency is not supported")
			return
		}
		pageSize := 20
		if s := q.Get("page_size"); s != "" {
			n, err := strconv.Atoi(s)
			if err != nil || n < 1 || n > 100 {
				writeError(w, http.StatusBadRequest, "invalid_request", "page_size must be between 1 and 100")
				return
			}
			pageSize = n
		}
		chains, err := splitList(q.Get("chain_ids"), 8, validChainID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		ops, err := splitList(q.Get("operation_types"), 8, validOperationType)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		req.Currency = currency
		req.PageSize = pageSize
		req.ChainIDs = chains
		req.OperationTypes = ops
	}

	ctx, cancel := context.WithTimeout(r.Context(), h.timeout())
	defer cancel()
	var retries int
	ctx = zerion.TrackRetries(ctx, &retries)
	page, err := h.Zerion.Transactions(ctx, req)
	info.retries = retries
	if err != nil {
		setUpstream(info, err)
		writeZerionError(w, r, err)
		return
	}
	info.upstream = "success"
	writeJSON(w, http.StatusOK, mapActivity(addr, typ, page))
}

func mapSummary(addr, typ, currency string, p *zerion.Portfolio) Summary {
	byType := map[string]float64{
		"wallet":    p.ByType["wallet"],
		"deposited": p.ByType["deposited"],
		"borrowed":  p.ByType["borrowed"],
		"locked":    p.ByType["locked"],
		"staked":    p.ByType["staked"],
	}
	byChain := p.ByChain
	if byChain == nil {
		byChain = map[string]float64{}
	}
	return Summary{
		Address:     addr,
		AddressType: typ,
		Currency:    currency,
		Total:       p.Total,
		Change1d:    Change{Absolute: p.ChangeAbs, Percent: p.ChangePct},
		ByType:      byType,
		ByChain:     byChain,
	}
}

func mapActivity(addr, typ string, page *zerion.TxPage) Activity {
	out := Activity{
		Address:     addr,
		AddressType: typ,
		Items:       make([]ActivityItem, 0, len(page.Items)),
	}
	if page.Next != "" {
		out.NextCursor = encodeCursor(page.Next)
	}
	for _, item := range page.Items {
		ai := ActivityItem{
			ID:            item.ID,
			Hash:          item.Hash,
			Chain:         item.Chain,
			OperationType: item.OperationType,
			Status:        item.Status,
			MinedAt:       item.MinedAt,
			From:          item.From,
			To:            item.To,
			FeeValue:      item.FeeValue,
			Transfers:     make([]Transfer, 0, len(item.Transfers)),
		}
		for _, tr := range item.Transfers {
			ai.Transfers = append(ai.Transfers, Transfer{
				Direction: tr.Direction,
				Symbol:    tr.Symbol,
				Amount:    tr.Amount,
				Value:     tr.Value,
			})
		}
		out.Items = append(out.Items, ai)
	}
	return out
}

func writeZerionError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, context.Canceled) && r.Context().Err() != nil {
		return
	}
	var zerr *zerion.Error
	if errors.As(err, &zerr) {
		switch zerr.Kind {
		case zerion.KindCanceled:
			return
		case zerion.KindBadRequest, zerion.KindUnprocessable:
			writeError(w, http.StatusBadRequest, "upstream_rejected_request", "upstream rejected the request")
		case zerion.KindAuth:
			writeError(w, http.StatusBadGateway, "upstream_authentication_failed", "upstream authentication failed")
		case zerion.KindRateLimited:
			writeError(w, http.StatusServiceUnavailable, "upstream_rate_limited", "upstream rate limited")
		case zerion.KindPending:
			writeError(w, http.StatusServiceUnavailable, "wallet_data_pending", "wallet data is not ready")
		case zerion.KindTimeout:
			writeError(w, http.StatusGatewayTimeout, "upstream_timeout", "upstream timeout")
		case zerion.KindInvalidResponse, zerion.KindNotFound:
			writeError(w, http.StatusBadGateway, "invalid_upstream_response", "upstream returned an unexpected response")
		case zerion.KindBusy:
			writeError(w, http.StatusServiceUnavailable, "upstream_unavailable", "service is busy")
		default:
			writeError(w, http.StatusBadGateway, "upstream_unavailable", "upstream unavailable")
		}
		return
	}
	if errors.Is(err, context.DeadlineExceeded) {
		writeError(w, http.StatusGatewayTimeout, "upstream_timeout", "upstream timeout")
		return
	}
	writeError(w, http.StatusBadGateway, "upstream_unavailable", "upstream unavailable")
}

func setUpstream(info *reqInfo, err error) {
	var zerr *zerion.Error
	if errors.As(err, &zerr) {
		info.upstream = zerr.Result()
		return
	}
	info.upstream = "error"
}

func reqInfoFrom(ctx context.Context) *reqInfo {
	info, _ := ctx.Value(infoKey{}).(*reqInfo)
	if info == nil {
		return &reqInfo{cache: "n/a", upstream: "none"}
	}
	return info
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{
			"code":    code,
			"message": message,
		},
	})
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(code int) {
	w.code = code
	w.ResponseWriter.WriteHeader(code)
}

func statusClass(code int) string {
	switch {
	case code >= 500:
		return "5xx"
	case code >= 400:
		return "4xx"
	default:
		return "2xx"
	}
}

var currencies = map[string]struct{}{
	"eth": {}, "btc": {}, "usd": {}, "eur": {},
	"krw": {}, "rub": {}, "gbp": {}, "aud": {},
	"cad": {}, "inr": {}, "jpy": {}, "nzd": {},
	"try": {}, "zar": {}, "cny": {}, "chf": {},
}

var operationTypes = map[string]struct{}{
	"approve": {}, "bid": {}, "burn": {}, "claim": {},
	"delegate": {}, "deploy": {}, "deposit": {}, "execute": {},
	"mint": {}, "receive": {}, "revoke": {}, "revoke_delegation": {},
	"send": {}, "trade": {}, "withdraw": {},
}

func parseAddress(s string) (display, addrType, cacheKey string, ok bool) {
	if evmAddress(s) {
		return s, "evm", strings.ToLower(s), true
	}
	if solAddress(s) {
		return s, "solana", s, true
	}
	return "", "", "", false
}

func evmAddress(s string) bool {
	if len(s) != 42 || !strings.HasPrefix(strings.ToLower(s), "0x") {
		return false
	}
	for i := 2; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func solAddress(s string) bool {
	if len(s) < 32 || len(s) > 44 {
		return false
	}
	const alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(alphabet, s[i]) < 0 {
			return false
		}
	}
	return true
}

func validCurrency(s string) bool {
	_, ok := currencies[s]
	return ok
}

func validOperationType(s string) bool {
	_, ok := operationTypes[s]
	return ok
}

func validChainID(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
			continue
		}
		return false
	}
	return true
}

func onlyQuery(r *http.Request, allowed ...string) error {
	ok := make(map[string]struct{}, len(allowed))
	for _, k := range allowed {
		ok[k] = struct{}{}
	}
	for k := range r.URL.Query() {
		if _, found := ok[k]; !found {
			return errors.New("unknown query parameter")
		}
	}
	return nil
}

func splitList(s string, max int, valid func(string) bool) ([]string, error) {
	if s == "" {
		return nil, nil
	}
	parts := strings.Split(s, ",")
	if len(parts) > max {
		return nil, errors.New("too many filter values")
	}
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || !valid(p) {
			return nil, errors.New("invalid filter value")
		}
		out = append(out, p)
	}
	return out, nil
}
