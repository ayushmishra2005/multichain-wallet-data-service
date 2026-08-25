package zerion

import (
	"context"
	"errors"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	maxAttempts  = 3
	maxRetryWait = 2 * time.Second
)

func defaultSleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func defaultJitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	// Full jitter in [0.5d, d].
	return time.Duration(float64(d) * (0.5 + rand.Float64()*0.5))
}

func retryDelay(plan retryPlan, attempt int) time.Duration {
	if plan.delay > 0 || plan.reason == "429" {
		return plan.delay
	}
	return backoffDelay(attempt)
}

func backoffDelay(attempt int) time.Duration {
	switch attempt {
	case 1:
		return 200 * time.Millisecond
	default:
		return 400 * time.Millisecond
	}
}

func remainingZero(h http.Header, name string) bool {
	v := strings.TrimSpace(h.Get(name))
	if v == "" {
		return false
	}
	n, err := strconv.Atoi(v)
	return err == nil && n == 0
}

func secondResetDelay(h http.Header) time.Duration {
	v := strings.TrimSpace(h.Get("RateLimit-Org-Second-Reset"))
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 500 * time.Millisecond
	}
	d := time.Duration(n) * time.Second
	if d > maxRetryWait {
		return maxRetryWait
	}
	return d
}

func parseRetryAfter(h http.Header) (time.Duration, bool) {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0, false
	}
	if sec, err := strconv.Atoi(v); err == nil {
		if sec < 0 {
			return 0, false
		}
		return time.Duration(sec) * time.Second, true
	}
	t, err := http.ParseTime(v)
	if err != nil {
		return 0, false
	}
	d := time.Until(t)
	if d < 0 {
		return 0, true
	}
	return d, true
}

type retryPlan struct {
	kind   Kind
	retry  bool
	delay  time.Duration
	reason string
	result string
}

func planForStatus(status int, h http.Header) retryPlan {
	switch {
	case status == http.StatusBadRequest:
		return retryPlan{kind: KindBadRequest, result: "bad_request"}
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return retryPlan{kind: KindAuth, result: "auth_error"}
	case status == http.StatusNotFound:
		return retryPlan{kind: KindNotFound, result: "decode_error"}
	case status == http.StatusUnprocessableEntity:
		return retryPlan{kind: KindUnprocessable, result: "bad_request"}
	case status == http.StatusTooManyRequests:
		if remainingZero(h, "RateLimit-Org-Day-Remaining") || remainingZero(h, "RateLimit-Org-Month-Remaining") {
			return retryPlan{kind: KindRateLimited, result: "rate_limited"}
		}
		return retryPlan{
			kind:   KindRateLimited,
			retry:  true,
			delay:  secondResetDelay(h),
			reason: "429",
			result: "rate_limited",
		}
	case status == http.StatusAccepted || status == http.StatusServiceUnavailable:
		delay, ok := parseRetryAfter(h)
		if ok && delay > maxRetryWait {
			return retryPlan{kind: KindPending, result: "pending"}
		}
		if !ok {
			delay = 200 * time.Millisecond
		}
		return retryPlan{
			kind:   KindPending,
			retry:  true,
			delay:  delay,
			reason: "5xx",
			result: "pending",
		}
	case status >= 500:
		return retryPlan{kind: KindUnavailable, retry: true, reason: "5xx", result: "server_error"}
	default:
		return retryPlan{kind: KindInvalidResponse, result: "decode_error"}
	}
}

func planForTransport(err error) retryPlan {
	if errors.Is(err, context.Canceled) {
		return retryPlan{kind: KindCanceled, result: "canceled"}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return retryPlan{kind: KindTimeout, result: "timeout"}
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return retryPlan{kind: KindTimeout, retry: true, reason: "timeout", result: "timeout"}
	}
	return retryPlan{kind: KindUnavailable, retry: true, reason: "net", result: "network_error"}
}

func (c *Client) wait(ctx context.Context, delay time.Duration, waited *time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
	}
	if *waited+delay > maxRetryWait {
		return errRetryBudget
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.Sleep(ctx, c.Jitter(delay)); err != nil {
		return err
	}
	*waited += delay
	return ctx.Err()
}

var errRetryBudget = errors.New("retry wait exceeds budget")
