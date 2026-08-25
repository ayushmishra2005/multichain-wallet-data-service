package zerion

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestRetry500ThenSuccess(t *testing.T) {
	c, mux := newTestClient(t)
	var n atomic.Int64
	var sleeps []time.Duration
	c.Sleep = func(_ context.Context, d time.Duration) error {
		sleeps = append(sleeps, d)
		return nil
	}
	mux.HandleFunc("/v1/wallets/"+evmAddr+"/portfolio", func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			http.Error(w, `{"errors":[{"title":"Internal Server Error"}]}`, http.StatusInternalServerError)
			return
		}
		w.Write(portfolioBody(10))
	})
	p, err := c.Portfolio(context.Background(), evmAddr, "usd")
	if err != nil {
		t.Fatal(err)
	}
	if p.Total != 10 {
		t.Fatalf("total=%v", p.Total)
	}
	if n.Load() != 2 {
		t.Fatalf("attempts=%d", n.Load())
	}
	if len(sleeps) != 1 || sleeps[0] != 200*time.Millisecond {
		t.Fatalf("sleeps=%v", sleeps)
	}
}

func TestRetry500Exhaustion(t *testing.T) {
	c, mux := newTestClient(t)
	var n atomic.Int64
	mux.HandleFunc("/v1/wallets/"+evmAddr+"/portfolio", func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		http.Error(w, `{"errors":[{"title":"Internal Server Error"}]}`, http.StatusInternalServerError)
	})
	_, err := c.Portfolio(context.Background(), evmAddr, "usd")
	var zerr *Error
	if !errors.As(err, &zerr) || zerr.Kind != KindUnavailable {
		t.Fatalf("err=%v", err)
	}
	if n.Load() != 3 {
		t.Fatalf("attempts=%d", n.Load())
	}
}

func TestRetry429ThenSuccess(t *testing.T) {
	c, mux := newTestClient(t)
	var n atomic.Int64
	var sleeps []time.Duration
	c.Sleep = func(_ context.Context, d time.Duration) error {
		sleeps = append(sleeps, d)
		return nil
	}
	mux.HandleFunc("/v1/wallets/"+evmAddr+"/portfolio", func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			w.Header().Set("RateLimit-Org-Second-Reset", "0")
			w.Header().Set("RateLimit-Org-Day-Remaining", "10")
			w.Header().Set("RateLimit-Org-Month-Remaining", "10")
			http.Error(w, `{"errors":[{"title":"Too many requests"}]}`, http.StatusTooManyRequests)
			return
		}
		w.Write(portfolioBody(3))
	})
	p, err := c.Portfolio(context.Background(), evmAddr, "usd")
	if err != nil {
		t.Fatal(err)
	}
	if p.Total != 3 {
		t.Fatalf("total=%v", p.Total)
	}
	if n.Load() != 2 {
		t.Fatalf("attempts=%d", n.Load())
	}
	if len(sleeps) != 0 {
		t.Fatalf("sleeps=%v", sleeps)
	}
}

func TestRetry429DayQuotaZero(t *testing.T) {
	c, mux := newTestClient(t)
	var n atomic.Int64
	mux.HandleFunc("/v1/wallets/"+evmAddr+"/portfolio", func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.Header().Set("RateLimit-Org-Day-Remaining", "0")
		w.Header().Set("RateLimit-Org-Month-Remaining", "10")
		http.Error(w, `{"errors":[{"title":"Too many requests"}]}`, http.StatusTooManyRequests)
	})
	_, err := c.Portfolio(context.Background(), evmAddr, "usd")
	var zerr *Error
	if !errors.As(err, &zerr) || zerr.Kind != KindRateLimited {
		t.Fatalf("err=%v", err)
	}
	if n.Load() != 1 {
		t.Fatalf("attempts=%d", n.Load())
	}
}

func TestRetry503AcceptableRetryAfter(t *testing.T) {
	c, mux := newTestClient(t)
	var n atomic.Int64
	var sleeps []time.Duration
	c.Sleep = func(_ context.Context, d time.Duration) error {
		sleeps = append(sleeps, d)
		return nil
	}
	mux.HandleFunc("/v1/wallets/"+evmAddr+"/portfolio", func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			http.Error(w, `{"errors":[{"title":"Service is temporarily unavailable"}]}`, http.StatusServiceUnavailable)
			return
		}
		w.Write(portfolioBody(7))
	})
	p, err := c.Portfolio(context.Background(), evmAddr, "usd")
	if err != nil {
		t.Fatal(err)
	}
	if p.Total != 7 {
		t.Fatalf("total=%v", p.Total)
	}
	if n.Load() != 2 {
		t.Fatalf("attempts=%d", n.Load())
	}
	if len(sleeps) != 1 || sleeps[0] != time.Second {
		t.Fatalf("sleeps=%v", sleeps)
	}
}

func TestRetry503ExcessiveRetryAfter(t *testing.T) {
	c, mux := newTestClient(t)
	var n atomic.Int64
	c.Sleep = func(context.Context, time.Duration) error {
		t.Fatal("should not wait on a long Retry-After")
		return nil
	}
	mux.HandleFunc("/v1/wallets/"+evmAddr+"/portfolio", func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.Header().Set("Retry-After", "30")
		http.Error(w, `{"errors":[{"title":"Service is temporarily unavailable"}]}`, http.StatusServiceUnavailable)
	})
	_, err := c.Portfolio(context.Background(), evmAddr, "usd")
	var zerr *Error
	if !errors.As(err, &zerr) || zerr.Kind != KindPending {
		t.Fatalf("err=%v", err)
	}
	if n.Load() != 1 {
		t.Fatalf("attempts=%d", n.Load())
	}
}

func TestRetryCanceledDuringBackoff(t *testing.T) {
	c, mux := newTestClient(t)
	mux.HandleFunc("/v1/wallets/"+evmAddr+"/portfolio", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"errors":[{"title":"Internal Server Error"}]}`, http.StatusInternalServerError)
	})
	ctx, cancel := context.WithCancel(context.Background())
	c.Sleep = func(ctx context.Context, d time.Duration) error {
		cancel()
		return ctx.Err()
	}
	_, err := c.Portfolio(ctx, evmAddr, "usd")
	var zerr *Error
	if !errors.As(err, &zerr) || zerr.Kind != KindCanceled {
		t.Fatalf("err=%v", err)
	}
}

func TestPlanForStatusNoRetry(t *testing.T) {
	cases := []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusNotFound, http.StatusUnprocessableEntity}
	for _, status := range cases {
		plan := planForStatus(status, http.Header{})
		if plan.retry {
			t.Fatalf("status %d should not retry", status)
		}
	}
}
