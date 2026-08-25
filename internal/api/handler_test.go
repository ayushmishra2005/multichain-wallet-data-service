package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ayushmishra2005/multichain-wallet-data-service/internal/zerion"
)

func newAPI(t *testing.T) (*httptest.Server, *http.ServeMux, *Handler) {
	t.Helper()
	upstream := http.NewServeMux()
	zs := httptest.NewServer(upstream)
	t.Cleanup(zs.Close)

	zc := zerion.NewClient(zs.URL, "test-key")
	zc.Sleep = func(context.Context, time.Duration) error { return nil }
	zc.Jitter = func(d time.Duration) time.Duration { return d }

	h := NewHandler(zc, slog.New(slog.NewTextHandler(io.Discard, nil)))
	api := httptest.NewServer(h.Routes())
	t.Cleanup(api.Close)
	return api, upstream, h
}

func portfolioOK(total float64) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{
			"data": {
				"attributes": {
					"positions_distribution_by_type": {
						"wallet": `+fnum(total)+`, "deposited": 0, "borrowed": 0, "locked": 0, "staked": 0
					},
					"positions_distribution_by_chain": {"ethereum": `+fnum(total)+`},
					"total": {"positions": `+fnum(total)+`},
					"changes": {"absolute_1d": 1.5, "percent_1d": 2.5}
				}
			}
		}`)
	}
}

func txOK(next string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		nextJSON := "null"
		if next != "" {
			b, _ := json.Marshal(next)
			nextJSON = string(b)
		}
		io.WriteString(w, `{
			"links": {"next": `+nextJSON+`},
			"data": [{
				"id": "tx1",
				"attributes": {
					"operation_type": "trade",
					"hash": "0xabc",
					"mined_at": "2026-01-01T00:00:00Z",
					"sent_from": "`+testEVM+`",
					"sent_to": "`+testEVM+`",
					"status": "confirmed",
					"fee": {"value": 1.23},
					"transfers": []
				},
				"relationships": {"chain": {"data": {"id": "ethereum"}}}
			}]
		}`)
	}
}

func fnum(v float64) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func getJSON(t *testing.T, url string) (int, map[string]any) {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil && err != io.EOF {
		t.Fatal(err)
	}
	return res.StatusCode, body
}

func errCode(body map[string]any) string {
	e, _ := body["error"].(map[string]any)
	c, _ := e["code"].(string)
	return c
}

func TestHealthz(t *testing.T) {
	api, _, _ := newAPI(t)
	code, body := getJSON(t, api.URL+"/healthz")
	if code != 200 || body["status"] != "ok" {
		t.Fatalf("%d %+v", code, body)
	}
}

func TestMetrics(t *testing.T) {
	api, mux, _ := newAPI(t)
	var n atomic.Int64
	mux.HandleFunc("/v1/wallets/"+testEVM+"/portfolio", func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			http.Error(w, `{"errors":[{"title":"Internal Server Error"}]}`, 500)
			return
		}
		portfolioOK(1)(w, r)
	})
	http.Get(api.URL + "/healthz")
	http.Get(api.URL + "/v1/wallets/" + testEVM + "/summary")
	res, err := http.Get(api.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	text := string(b)
	for _, name := range []string{
		"http_requests_total",
		"http_request_duration_seconds",
		"zerion_requests_total",
		"zerion_request_duration_seconds",
		"zerion_retries_total",
		"cache_requests_total",
	} {
		if !strings.Contains(text, name) {
			t.Fatalf("missing metric %s\n%s", name, text)
		}
	}
}

func TestSummaryEVM(t *testing.T) {
	api, mux, _ := newAPI(t)
	mux.HandleFunc("/v1/wallets/"+testEVM+"/portfolio", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("filter[positions]") != "only_simple" {
			t.Errorf("positions filter=%q", r.URL.Query().Get("filter[positions]"))
		}
		portfolioOK(2017.48)(w, r)
	})
	code, body := getJSON(t, api.URL+"/v1/wallets/"+testEVM+"/summary")
	if code != 200 {
		t.Fatalf("%d %+v", code, body)
	}
	if body["address_type"] != "evm" || body["total"] != 2017.48 {
		t.Fatalf("%+v", body)
	}
}

func TestSummarySolana(t *testing.T) {
	api, mux, _ := newAPI(t)
	mux.HandleFunc("/v1/wallets/"+testSOL+"/portfolio", portfolioOK(50))
	code, body := getJSON(t, api.URL+"/v1/wallets/"+testSOL+"/summary")
	if code != 200 || body["address_type"] != "solana" {
		t.Fatalf("%d %+v", code, body)
	}
}

func TestSummaryZeroPortfolio(t *testing.T) {
	api, mux, _ := newAPI(t)
	mux.HandleFunc("/v1/wallets/"+testEVM+"/portfolio", portfolioOK(0))
	code, body := getJSON(t, api.URL+"/v1/wallets/"+testEVM+"/summary")
	if code != 200 || body["total"] != 0.0 {
		t.Fatalf("%d %+v", code, body)
	}
}

func TestActivityEVM(t *testing.T) {
	api, mux, _ := newAPI(t)
	mux.HandleFunc("/v1/wallets/"+testEVM+"/transactions/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page[size]") != "20" {
			t.Errorf("page[size]=%q", r.URL.Query().Get("page[size]"))
		}
		if r.URL.Query().Get("filter[trash]") != "only_non_trash" {
			t.Errorf("trash=%q", r.URL.Query().Get("filter[trash]"))
		}
		txOK("")(w, r)
	})
	code, body := getJSON(t, api.URL+"/v1/wallets/"+testEVM+"/activity")
	if code != 200 {
		t.Fatalf("%d %+v", code, body)
	}
	items, _ := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items=%v", body["items"])
	}
}

func TestActivitySolana(t *testing.T) {
	api, mux, _ := newAPI(t)
	mux.HandleFunc("/v1/wallets/"+testSOL+"/transactions/", txOK(""))
	code, body := getJSON(t, api.URL+"/v1/wallets/"+testSOL+"/activity")
	if code != 200 || body["address_type"] != "solana" {
		t.Fatalf("%d %+v", code, body)
	}
}

func TestActivityEmptyStays200(t *testing.T) {
	api, mux, _ := newAPI(t)
	mux.HandleFunc("/v1/wallets/"+testEVM+"/transactions/", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"links":{},"data":[]}`)
	})
	code, body := getJSON(t, api.URL+"/v1/wallets/"+testEVM+"/activity")
	if code != 200 {
		t.Fatalf("%d %+v", code, body)
	}
	items, _ := body["items"].([]any)
	if len(items) != 0 {
		t.Fatalf("items=%v", body["items"])
	}
	if _, ok := body["next_cursor"]; ok {
		t.Fatalf("unexpected cursor %v", body["next_cursor"])
	}
}

func TestInvalidAddress(t *testing.T) {
	api, mux, _ := newAPI(t)
	var n int
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { n++ })
	code, body := getJSON(t, api.URL+"/v1/wallets/not-a-wallet/summary")
	if code != 400 || errCode(body) != "invalid_address" {
		t.Fatalf("%d %+v", code, body)
	}
	if n != 0 {
		t.Fatal("upstream was called")
	}
}

func TestInvalidCurrency(t *testing.T) {
	api, mux, _ := newAPI(t)
	var n int
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { n++ })
	code, body := getJSON(t, api.URL+"/v1/wallets/"+testEVM+"/summary?currency=xyz")
	if code != 400 || errCode(body) != "invalid_request" {
		t.Fatalf("%d %+v", code, body)
	}
	if n != 0 {
		t.Fatal("upstream was called")
	}
}

func TestInvalidPageSize(t *testing.T) {
	api, _, _ := newAPI(t)
	for _, q := range []string{"0", "101", "abc"} {
		code, body := getJSON(t, api.URL+"/v1/wallets/"+testEVM+"/activity?page_size="+q)
		if code != 400 || errCode(body) != "invalid_request" {
			t.Fatalf("page_size=%s -> %d %+v", q, code, body)
		}
	}
}

func TestUnknownOperationFilter(t *testing.T) {
	api, _, _ := newAPI(t)
	code, body := getJSON(t, api.URL+"/v1/wallets/"+testEVM+"/activity?operation_types=not_a_type")
	if code != 400 || errCode(body) != "invalid_request" {
		t.Fatalf("%d %+v", code, body)
	}
}

func TestActivityCursorFirstPageAndContinue(t *testing.T) {
	api, mux, h := newAPI(t)
	next := h.Zerion.BaseURL + "/v1/wallets/" + testEVM + "/transactions/?page[lastId]=abc&page[size]=20"
	var pages []string
	mux.HandleFunc("/v1/wallets/"+testEVM+"/transactions/", func(w http.ResponseWriter, r *http.Request) {
		pages = append(pages, r.URL.RawQuery)
		if r.URL.Query().Get("page[lastId]") == "abc" {
			io.WriteString(w, `{"links":{},"data":[]}`)
			return
		}
		if r.URL.Query().Get("page[after]") != "" || r.URL.Query().Get("page[lastId]") != "" {
			t.Errorf("first page sent cursor %s", r.URL.RawQuery)
		}
		txOK(next)(w, r)
	})

	code, body := getJSON(t, api.URL+"/v1/wallets/"+testEVM+"/activity")
	if code != 200 {
		t.Fatalf("%d %+v", code, body)
	}
	cur, _ := body["next_cursor"].(string)
	if cur == "" {
		t.Fatal("missing next_cursor")
	}

	code, body = getJSON(t, api.URL+"/v1/wallets/"+testEVM+"/activity?cursor="+url.QueryEscape(cur))
	if code != 200 {
		t.Fatalf("continue %d %+v", code, body)
	}
	if len(pages) != 2 {
		t.Fatalf("pages=%v", pages)
	}
	if !strings.Contains(pages[1], "lastId=abc") && !strings.Contains(pages[1], "page[lastId]=abc") {
		t.Fatalf("continuation query=%s", pages[1])
	}
}

func TestInvalidCursor(t *testing.T) {
	api, mux, _ := newAPI(t)
	var n int
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { n++ })
	code, body := getJSON(t, api.URL+"/v1/wallets/"+testEVM+"/activity?cursor=not-a-cursor")
	if code != 400 || errCode(body) != "invalid_request" {
		t.Fatalf("%d %+v", code, body)
	}
	if n != 0 {
		t.Fatal("upstream was called")
	}
}

func TestCursorOtherAddress(t *testing.T) {
	api, _, _ := newAPI(t)
	other := "0x2222222222222222222222222222222222222222"
	cur := encodeCursor("/v1/wallets/" + other + "/transactions/?page[after]=x")
	code, body := getJSON(t, api.URL+"/v1/wallets/"+testEVM+"/activity?cursor="+url.QueryEscape(cur))
	if code != 400 || errCode(body) != "invalid_request" {
		t.Fatalf("%d %+v", code, body)
	}
}

func TestCursorPlusFiltersRejected(t *testing.T) {
	api, _, _ := newAPI(t)
	cur := encodeCursor("/v1/wallets/" + testEVM + "/transactions/?page[after]=x")
	code, body := getJSON(t, api.URL+"/v1/wallets/"+testEVM+"/activity?cursor="+url.QueryEscape(cur)+"&currency=usd")
	if code != 400 || errCode(body) != "invalid_request" {
		t.Fatalf("%d %+v", code, body)
	}
}

func TestUpstream400Mapping(t *testing.T) {
	api, mux, _ := newAPI(t)
	mux.HandleFunc("/v1/wallets/"+testEVM+"/portfolio", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"errors":[{"title":"bad"}]}`, 400)
	})
	code, body := getJSON(t, api.URL+"/v1/wallets/"+testEVM+"/summary")
	if code != 400 || errCode(body) != "upstream_rejected_request" {
		t.Fatalf("%d %+v", code, body)
	}
}

func TestUpstreamAuthMapping(t *testing.T) {
	api, mux, _ := newAPI(t)
	mux.HandleFunc("/v1/wallets/"+testEVM+"/portfolio", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"errors":[{"title":"Unauthorized Error"}]}`, 401)
	})
	code, body := getJSON(t, api.URL+"/v1/wallets/"+testEVM+"/summary")
	if code != 502 || errCode(body) != "upstream_authentication_failed" {
		t.Fatalf("%d %+v", code, body)
	}
}

func TestUpstreamRateLimitMapping(t *testing.T) {
	api, mux, _ := newAPI(t)
	mux.HandleFunc("/v1/wallets/"+testEVM+"/portfolio", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("RateLimit-Org-Day-Remaining", "0")
		http.Error(w, `{"errors":[{"title":"Too many requests"}]}`, 429)
	})
	code, body := getJSON(t, api.URL+"/v1/wallets/"+testEVM+"/summary")
	if code != 503 || errCode(body) != "upstream_rate_limited" {
		t.Fatalf("%d %+v", code, body)
	}
}

func TestUpstreamTimeoutMapping(t *testing.T) {
	api, mux, h := newAPI(t)
	h.Timeout = 40 * time.Millisecond
	mux.HandleFunc("/v1/wallets/"+testEVM+"/portfolio", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	})
	code, body := getJSON(t, api.URL+"/v1/wallets/"+testEVM+"/summary")
	if code != 504 || errCode(body) != "upstream_timeout" {
		t.Fatalf("%d %+v", code, body)
	}
}

func TestSummaryCacheHitAndFailuresNotCached(t *testing.T) {
	api, mux, _ := newAPI(t)
	var n atomic.Int64
	mux.HandleFunc("/v1/wallets/"+testEVM+"/portfolio", func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) <= 3 {
			http.Error(w, `{"errors":[{"title":"Internal Server Error"}]}`, 500)
			return
		}
		portfolioOK(9)(w, r)
	})
	code, _ := getJSON(t, api.URL+"/v1/wallets/"+testEVM+"/summary")
	if code != 502 {
		t.Fatalf("first=%d", code)
	}
	code, body := getJSON(t, api.URL+"/v1/wallets/"+testEVM+"/summary")
	if code != 200 || body["total"] != 9.0 {
		t.Fatalf("second %d %+v", code, body)
	}
	code, _ = getJSON(t, api.URL+"/v1/wallets/"+testEVM+"/summary")
	if code != 200 {
		t.Fatalf("third=%d", code)
	}
	if n.Load() != 4 { // 3 retries on first + 1 success
		t.Fatalf("upstream calls=%d", n.Load())
	}
}

func TestSummaryCacheZeroAndHit(t *testing.T) {
	api, mux, _ := newAPI(t)
	var n atomic.Int64
	mux.HandleFunc("/v1/wallets/"+testEVM+"/portfolio", func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		portfolioOK(0)(w, r)
	})
	getJSON(t, api.URL+"/v1/wallets/"+testEVM+"/summary")
	code, body := getJSON(t, api.URL+"/v1/wallets/"+testEVM+"/summary")
	if code != 200 || body["total"] != 0.0 {
		t.Fatalf("%d %+v", code, body)
	}
	if n.Load() != 1 {
		t.Fatalf("calls=%d", n.Load())
	}
}

func TestSummarySingleflight(t *testing.T) {
	api, mux, _ := newAPI(t)
	var n atomic.Int64
	started := make(chan struct{})
	release := make(chan struct{})
	mux.HandleFunc("/v1/wallets/"+testEVM+"/portfolio", func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		portfolioOK(4)(w, r)
	})

	var wg sync.WaitGroup
	wg.Add(2)
	got := make(chan int, 2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			code, _ := getJSON(t, api.URL+"/v1/wallets/"+testEVM+"/summary")
			got <- code
		}()
	}
	<-started
	time.Sleep(50 * time.Millisecond)
	if n.Load() != 1 {
		close(release)
		t.Fatalf("expected one upstream call, got %d", n.Load())
	}
	close(release)
	wg.Wait()
	close(got)
	for code := range got {
		if code != 200 {
			t.Fatalf("status=%d", code)
		}
	}
}

func TestSummaryDifferentKeysIndependent(t *testing.T) {
	api, mux, _ := newAPI(t)
	other := "0x2222222222222222222222222222222222222222"
	var a, b atomic.Int64
	block := make(chan struct{})
	mux.HandleFunc("/v1/wallets/"+testEVM+"/portfolio", func(w http.ResponseWriter, r *http.Request) {
		a.Add(1)
		<-block
		portfolioOK(1)(w, r)
	})
	mux.HandleFunc("/v1/wallets/"+other+"/portfolio", func(w http.ResponseWriter, r *http.Request) {
		b.Add(1)
		<-block
		portfolioOK(2)(w, r)
	})

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		getJSON(t, api.URL+"/v1/wallets/"+testEVM+"/summary")
	}()
	go func() {
		defer wg.Done()
		getJSON(t, api.URL+"/v1/wallets/"+other+"/summary")
	}()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && (a.Load() == 0 || b.Load() == 0) {
		time.Sleep(5 * time.Millisecond)
	}
	if a.Load() != 1 || b.Load() != 1 {
		close(block)
		t.Fatalf("a=%d b=%d", a.Load(), b.Load())
	}
	close(block)
	wg.Wait()
}

func TestParseAddress(t *testing.T) {
	_, typ, key, ok := parseAddress(testEVM)
	if !ok || typ != "evm" || key != strings.ToLower(testEVM) {
		t.Fatalf("%s %s %v", typ, key, ok)
	}
	_, typ, key, ok = parseAddress(testSOL)
	if !ok || typ != "solana" || key != testSOL {
		t.Fatalf("%s %s %v", typ, key, ok)
	}
	if _, _, _, ok := parseAddress("0x123"); ok {
		t.Fatal("short evm")
	}
}
