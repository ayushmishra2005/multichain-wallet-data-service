package zerion

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	testKey = "test-key"
	evmAddr = "0x1111111111111111111111111111111111111111"
	solAddr = "11111111111111111111111111111111"
)

func newTestClient(t *testing.T) (*Client, *http.ServeMux) {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL, testKey)
	c.Sleep = func(context.Context, time.Duration) error { return nil }
	c.Jitter = func(d time.Duration) time.Duration { return d }
	return c, mux
}

func portfolioBody(total float64) []byte {
	return []byte(`{
		"links": {"self": "/portfolio"},
		"data": {
			"type": "portfolio",
			"id": "p1",
			"attributes": {
				"positions_distribution_by_type": {
					"wallet": ` + f(total) + `,
					"deposited": 0,
					"borrowed": 0,
					"locked": 0,
					"staked": 0
				},
				"positions_distribution_by_chain": {"ethereum": ` + f(total) + `},
				"total": {"positions": ` + f(total) + `},
				"changes": {"absolute_1d": 102.02, "percent_1d": 5.33}
			}
		}
	}`)
}

func f(v float64) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func txBody(next string, opType string) []byte {
	nextJSON := "null"
	if next != "" {
		b, _ := json.Marshal(next)
		nextJSON = string(b)
	}
	if opType == "" {
		opType = "trade"
	}
	return []byte(`{
		"links": {"next": ` + nextJSON + `},
		"data": [{
			"type": "transactions",
			"id": "tx1",
			"attributes": {
				"operation_type": "` + opType + `",
				"hash": "0xabc",
				"mined_at": "2026-01-01T00:00:00Z",
				"sent_from": "` + evmAddr + `",
				"sent_to": "` + evmAddr + `",
				"status": "confirmed",
				"nonce": 1,
				"fee": {"value": 1.23},
				"transfers": [{
					"direction": "out",
					"quantity": {"float": 0.5},
					"value": 1060.0,
					"fungible_info": {"symbol": "ETH"}
				}]
			},
			"relationships": {"chain": {"data": {"type": "chains", "id": "ethereum"}}}
		}]
	}`)
}

func emptyTxBody() []byte {
	return []byte(`{"links": {}, "data": []}`)
}

func checkBasicAuth(t *testing.T, r *http.Request) {
	t.Helper()
	user, pass, ok := r.BasicAuth()
	if !ok {
		t.Fatal("missing basic auth")
	}
	if user != testKey || pass != "" {
		t.Fatalf("basic auth user=%q pass_empty=%t", user, pass == "")
	}
}

func TestPortfolioSuccessEVM(t *testing.T) {
	c, mux := newTestClient(t)
	var gotURL string
	mux.HandleFunc("/v1/wallets/"+evmAddr+"/portfolio", func(w http.ResponseWriter, r *http.Request) {
		checkBasicAuth(t, r)
		gotURL = r.URL.RawQuery
		if r.URL.Query().Get("filter[positions]") != "only_simple" {
			t.Errorf("filter[positions]=%q", r.URL.Query().Get("filter[positions]"))
		}
		if r.URL.Query().Get("currency") != "usd" {
			t.Errorf("currency=%q", r.URL.Query().Get("currency"))
		}
		w.Write(portfolioBody(2017.48))
	})

	p, err := c.Portfolio(context.Background(), evmAddr, "usd")
	if err != nil {
		t.Fatal(err)
	}
	if p.Total != 2017.48 {
		t.Fatalf("total=%v", p.Total)
	}
	if p.ByType["wallet"] != 2017.48 {
		t.Fatalf("wallet=%v", p.ByType["wallet"])
	}
	if p.ByChain["ethereum"] != 2017.48 {
		t.Fatalf("chain=%v", p.ByChain["ethereum"])
	}
	if p.ChangeAbs != 102.02 || p.ChangePct != 5.33 {
		t.Fatalf("change=%v %v", p.ChangeAbs, p.ChangePct)
	}
	if !strings.Contains(gotURL, "filter") {
		t.Fatalf("query=%s", gotURL)
	}
}

func TestPortfolioZero(t *testing.T) {
	c, mux := newTestClient(t)
	mux.HandleFunc("/v1/wallets/"+evmAddr+"/portfolio", func(w http.ResponseWriter, r *http.Request) {
		w.Write(portfolioBody(0))
	})
	p, err := c.Portfolio(context.Background(), evmAddr, "usd")
	if err != nil {
		t.Fatal(err)
	}
	if p.Total != 0 {
		t.Fatalf("total=%v", p.Total)
	}
}

func TestPortfolioSolanaPath(t *testing.T) {
	c, mux := newTestClient(t)
	var hit bool
	mux.HandleFunc("/v1/wallets/"+solAddr+"/portfolio", func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.Write([]byte(`{
			"data": {
				"attributes": {
					"positions_distribution_by_type": {"wallet": 50},
					"positions_distribution_by_chain": {"solana": 50},
					"total": {"positions": 50},
					"changes": {"absolute_1d": 0, "percent_1d": 0}
				}
			}
		}`))
	})
	p, err := c.Portfolio(context.Background(), solAddr, "usd")
	if err != nil {
		t.Fatal(err)
	}
	if !hit {
		t.Fatal("solana path not used")
	}
	if p.Total != 50 || p.ByChain["solana"] != 50 {
		t.Fatalf("%+v", p)
	}
}

func TestPortfolioCurrencyForwarded(t *testing.T) {
	c, mux := newTestClient(t)
	mux.HandleFunc("/v1/wallets/"+evmAddr+"/portfolio", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("currency") != "eur" {
			t.Fatalf("currency=%q", r.URL.Query().Get("currency"))
		}
		w.Write(portfolioBody(1))
	})
	if _, err := c.Portfolio(context.Background(), evmAddr, "eur"); err != nil {
		t.Fatal(err)
	}
}

func TestPortfolioMalformedJSON(t *testing.T) {
	c, mux := newTestClient(t)
	mux.HandleFunc("/v1/wallets/"+evmAddr+"/portfolio", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "{not-json")
	})
	_, err := c.Portfolio(context.Background(), evmAddr, "usd")
	var zerr *Error
	if !errors.As(err, &zerr) || zerr.Kind != KindInvalidResponse {
		t.Fatalf("err=%v", err)
	}
}

func TestPortfolioOversizedResponse(t *testing.T) {
	c, mux := newTestClient(t)
	c.maxBody = 64
	mux.HandleFunc("/v1/wallets/"+evmAddr+"/portfolio", func(w http.ResponseWriter, r *http.Request) {
		w.Write(bytesRepeat(200, 'x'))
	})
	_, err := c.Portfolio(context.Background(), evmAddr, "usd")
	var zerr *Error
	if !errors.As(err, &zerr) || zerr.Kind != KindInvalidResponse {
		t.Fatalf("err=%v", err)
	}
}

func bytesRepeat(n int, b byte) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}

func TestPortfolio400(t *testing.T) {
	c, mux := newTestClient(t)
	var n int
	mux.HandleFunc("/v1/wallets/"+evmAddr+"/portfolio", func(w http.ResponseWriter, r *http.Request) {
		n++
		http.Error(w, `{"errors":[{"title":"bad"}]}`, http.StatusBadRequest)
	})
	_, err := c.Portfolio(context.Background(), evmAddr, "usd")
	var zerr *Error
	if !errors.As(err, &zerr) || zerr.Kind != KindBadRequest {
		t.Fatalf("err=%v", err)
	}
	if n != 1 {
		t.Fatalf("attempts=%d", n)
	}
}

func TestPortfolio401(t *testing.T) {
	c, mux := newTestClient(t)
	var n int
	mux.HandleFunc("/v1/wallets/"+evmAddr+"/portfolio", func(w http.ResponseWriter, r *http.Request) {
		n++
		http.Error(w, `{"errors":[{"title":"Unauthorized Error"}]}`, http.StatusUnauthorized)
	})
	_, err := c.Portfolio(context.Background(), evmAddr, "usd")
	var zerr *Error
	if !errors.As(err, &zerr) || zerr.Kind != KindAuth {
		t.Fatalf("err=%v", err)
	}
	if n != 1 {
		t.Fatalf("attempts=%d", n)
	}
}

func TestPortfolioCancellation(t *testing.T) {
	c, mux := newTestClient(t)
	var n atomic.Int64
	mux.HandleFunc("/v1/wallets/"+evmAddr+"/portfolio", func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.Portfolio(ctx, evmAddr, "usd")
	var zerr *Error
	if !errors.As(err, &zerr) || zerr.Kind != KindCanceled {
		t.Fatalf("err=%v", err)
	}
	if n.Load() != 0 {
		t.Fatalf("attempts=%d", n.Load())
	}
}

func TestPortfolioTimeout(t *testing.T) {
	c, mux := newTestClient(t)
	mux.HandleFunc("/v1/wallets/"+evmAddr+"/portfolio", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := c.Portfolio(ctx, evmAddr, "usd")
	var zerr *Error
	if !errors.As(err, &zerr) || (zerr.Kind != KindTimeout && zerr.Kind != KindCanceled) {
		t.Fatalf("err=%v kind=%v", err, zerr)
	}
}

func TestPortfolioNetworkFailure(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	c := NewClient("http://"+addr, testKey)
	c.Sleep = func(context.Context, time.Duration) error { return nil }
	c.Jitter = func(d time.Duration) time.Duration { return d }

	_, err = c.Portfolio(context.Background(), evmAddr, "usd")
	var zerr *Error
	if !errors.As(err, &zerr) || zerr.Kind != KindUnavailable {
		t.Fatalf("err=%v", err)
	}
}

func TestTransactionsSuccessAndNextLink(t *testing.T) {
	c, mux := newTestClient(t)
	next := c.BaseURL + "/v1/wallets/" + evmAddr + "/transactions/?page%5BlastId%5D=abc&page%5BlastTimestamp%5D=2026-01-01T00:00:00Z&page%5Bsize%5D=20"
	mux.HandleFunc("/v1/wallets/"+evmAddr+"/transactions/", func(w http.ResponseWriter, r *http.Request) {
		checkBasicAuth(t, r)
		if r.URL.Query().Get("filter[trash]") != "only_non_trash" {
			t.Errorf("trash=%q", r.URL.Query().Get("filter[trash]"))
		}
		if r.URL.Query().Get("page[size]") != "20" {
			t.Errorf("page[size]=%q", r.URL.Query().Get("page[size]"))
		}
		if r.URL.Query().Get("page[after]") != "" || r.URL.Query().Get("page[lastId]") != "" {
			t.Errorf("first page sent a cursor: %s", r.URL.RawQuery)
		}
		w.Write(txBody(next, "trade"))
	})

	page, err := c.Transactions(context.Background(), TxQuery{Address: evmAddr, Currency: "usd"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("items=%d", len(page.Items))
	}
	item := page.Items[0]
	if item.ID != "tx1" || item.Hash != "0xabc" || item.Chain != "ethereum" {
		t.Fatalf("%+v", item)
	}
	if item.FeeValue == nil || *item.FeeValue != 1.23 {
		t.Fatalf("fee=%v", item.FeeValue)
	}
	if len(item.Transfers) != 1 || item.Transfers[0].Symbol != "ETH" {
		t.Fatalf("transfers=%+v", item.Transfers)
	}
	if page.Next == "" || strings.Contains(page.Next, "://") {
		t.Fatalf("next=%q", page.Next)
	}
	if !strings.Contains(page.Next, "page[lastId]=abc") && !strings.Contains(page.Next, "page%5BlastId%5D=abc") {
		t.Fatalf("next=%q", page.Next)
	}
}

func TestTransactionsEmptyPage(t *testing.T) {
	c, mux := newTestClient(t)
	mux.HandleFunc("/v1/wallets/"+evmAddr+"/transactions/", func(w http.ResponseWriter, r *http.Request) {
		w.Write(emptyTxBody())
	})
	page, err := c.Transactions(context.Background(), TxQuery{Address: evmAddr})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 0 || page.Next != "" {
		t.Fatalf("%+v", page)
	}
}

func TestTransactionsUnknownOperationType(t *testing.T) {
	c, mux := newTestClient(t)
	mux.HandleFunc("/v1/wallets/"+evmAddr+"/transactions/", func(w http.ResponseWriter, r *http.Request) {
		w.Write(txBody("", "brand_new_op"))
	})
	page, err := c.Transactions(context.Background(), TxQuery{Address: evmAddr})
	if err != nil {
		t.Fatal(err)
	}
	if page.Items[0].OperationType != "brand_new_op" {
		t.Fatalf("op=%q", page.Items[0].OperationType)
	}
}

func TestTransactionsFiltersAndPageSize(t *testing.T) {
	c, mux := newTestClient(t)
	mux.HandleFunc("/v1/wallets/"+solAddr+"/transactions/", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("filter[chain_ids]") != "solana,ethereum" {
			t.Errorf("chains=%q", q.Get("filter[chain_ids]"))
		}
		if q.Get("filter[operation_types]") != "trade,send" {
			t.Errorf("ops=%q", q.Get("filter[operation_types]"))
		}
		if q.Get("page[size]") != "100" {
			t.Errorf("size=%q", q.Get("page[size]"))
		}
		if q.Get("currency") != "eur" {
			t.Errorf("currency=%q", q.Get("currency"))
		}
		w.Write(emptyTxBody())
	})
	_, err := c.Transactions(context.Background(), TxQuery{
		Address:        solAddr,
		Currency:       "eur",
		PageSize:       100,
		ChainIDs:       []string{"solana", "ethereum"},
		OperationTypes: []string{"trade", "send"},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestTransactionsDefaultPageSize(t *testing.T) {
	c, mux := newTestClient(t)
	mux.HandleFunc("/v1/wallets/"+evmAddr+"/transactions/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page[size]") != "20" {
			t.Fatalf("size=%q", r.URL.Query().Get("page[size]"))
		}
		w.Write(emptyTxBody())
	})
	if _, err := c.Transactions(context.Background(), TxQuery{Address: evmAddr}); err != nil {
		t.Fatal(err)
	}
}

func TestTransactionsMalformedNextLink(t *testing.T) {
	c, mux := newTestClient(t)
	mux.HandleFunc("/v1/wallets/"+evmAddr+"/transactions/", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"links":{"next":"://bad"},"data":[]}`)
	})
	_, err := c.Transactions(context.Background(), TxQuery{Address: evmAddr})
	var zerr *Error
	if !errors.As(err, &zerr) || zerr.Kind != KindInvalidResponse {
		t.Fatalf("err=%v", err)
	}
}

func TestTransactionsWrongNextHost(t *testing.T) {
	c, mux := newTestClient(t)
	mux.HandleFunc("/v1/wallets/"+evmAddr+"/transactions/", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"links":{"next":"https://evil.example/v1/wallets/`+evmAddr+`/transactions/"},"data":[]}`)
	})
	_, err := c.Transactions(context.Background(), TxQuery{Address: evmAddr})
	var zerr *Error
	if !errors.As(err, &zerr) || zerr.Kind != KindInvalidResponse {
		t.Fatalf("err=%v", err)
	}
}

func TestTransactionsWrongNextPath(t *testing.T) {
	c, mux := newTestClient(t)
	mux.HandleFunc("/v1/wallets/"+evmAddr+"/transactions/", func(w http.ResponseWriter, r *http.Request) {
		next := c.BaseURL + "/v1/wallets/" + evmAddr + "/portfolio"
		io.WriteString(w, `{"links":{"next":"`+next+`"},"data":[]}`)
	})
	_, err := c.Transactions(context.Background(), TxQuery{Address: evmAddr})
	var zerr *Error
	if !errors.As(err, &zerr) || zerr.Kind != KindInvalidResponse {
		t.Fatalf("err=%v", err)
	}
}

func TestTransactionsWrongNextWallet(t *testing.T) {
	c, mux := newTestClient(t)
	other := "0x2222222222222222222222222222222222222222"
	mux.HandleFunc("/v1/wallets/"+evmAddr+"/transactions/", func(w http.ResponseWriter, r *http.Request) {
		next := c.BaseURL + "/v1/wallets/" + other + "/transactions/"
		io.WriteString(w, `{"links":{"next":"`+next+`"},"data":[]}`)
	})
	_, err := c.Transactions(context.Background(), TxQuery{Address: evmAddr})
	var zerr *Error
	if !errors.As(err, &zerr) || zerr.Kind != KindInvalidResponse {
		t.Fatalf("err=%v", err)
	}
}

func TestTransactionsNextUserinfoRejected(t *testing.T) {
	c, mux := newTestClient(t)
	mux.HandleFunc("/v1/wallets/"+evmAddr+"/transactions/", func(w http.ResponseWriter, r *http.Request) {
		u := strings.Replace(c.BaseURL, "http://", "http://user:pass@", 1)
		next := u + "/v1/wallets/" + evmAddr + "/transactions/?page[after]=x"
		io.WriteString(w, `{"links":{"next":"`+next+`"},"data":[]}`)
	})
	_, err := c.Transactions(context.Background(), TxQuery{Address: evmAddr})
	var zerr *Error
	if !errors.As(err, &zerr) || zerr.Kind != KindInvalidResponse {
		t.Fatalf("err=%v", err)
	}
}

func TestTransactionsContinuationUsesRelPath(t *testing.T) {
	c, mux := newTestClient(t)
	var seen string
	mux.HandleFunc("/v1/wallets/"+evmAddr+"/transactions/", func(w http.ResponseWriter, r *http.Request) {
		seen = r.URL.RawQuery
		w.Write(emptyTxBody())
	})
	_, err := c.Transactions(context.Background(), TxQuery{
		Address: evmAddr,
		RelPath: "/v1/wallets/" + evmAddr + "/transactions/?page[lastId]=abc&page[size]=20",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(seen, "lastId=abc") && !strings.Contains(seen, "page[lastId]=abc") {
		t.Fatalf("query=%s", seen)
	}
}

func TestParseNextLinkEVMCase(t *testing.T) {
	c := NewClient("https://api.zerion.io", testKey)
	next := "https://api.zerion.io/v1/wallets/" + strings.ToUpper(evmAddr) + "/transactions/?page[after]=x"
	rel, err := c.parseNextLink(next, evmAddr)
	if err != nil || rel == "" {
		t.Fatalf("rel=%q err=%v", rel, err)
	}
}
