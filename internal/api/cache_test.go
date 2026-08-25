package api

import (
	"testing"
	"time"
)

func sampleSummary(addr string, total float64) Summary {
	return Summary{
		Address:     addr,
		AddressType: "evm",
		Currency:    "usd",
		Total:       total,
		ByType:      map[string]float64{"wallet": total},
		ByChain:     map[string]float64{"ethereum": total},
	}
}

func TestCacheMissHitExpire(t *testing.T) {
	c := NewCache(8, 15*time.Second)
	now := time.Unix(1_000, 0)
	c.now = func() time.Time { return now }

	if _, ok := c.Get("k"); ok {
		t.Fatal("expected miss")
	}
	c.Set("k", sampleSummary(testEVM, 10))
	got, ok := c.Get("k")
	if !ok || got.Total != 10 {
		t.Fatalf("hit=%v val=%+v", ok, got)
	}

	now = now.Add(15 * time.Second)
	if _, ok := c.Get("k"); ok {
		t.Fatal("expected expire")
	}
}

func TestCacheZeroSummary(t *testing.T) {
	c := NewCache(8, 15*time.Second)
	c.Set("z", sampleSummary(testEVM, 0))
	got, ok := c.Get("z")
	if !ok || got.Total != 0 {
		t.Fatalf("hit=%v val=%+v", ok, got)
	}
}

func TestCacheMaxSize(t *testing.T) {
	c := NewCache(2, time.Hour)
	now := time.Unix(1_000, 0)
	c.now = func() time.Time { return now }

	c.Set("a", sampleSummary("a", 1))
	now = now.Add(time.Second)
	c.Set("b", sampleSummary("b", 2))
	now = now.Add(time.Second)
	c.Set("c", sampleSummary("c", 3))

	if c.Len() != 2 {
		t.Fatalf("len=%d", c.Len())
	}
	if _, ok := c.Get("a"); ok {
		t.Fatal("oldest entry should be evicted")
	}
	if _, ok := c.Get("b"); !ok {
		t.Fatal("b missing")
	}
	if _, ok := c.Get("c"); !ok {
		t.Fatal("c missing")
	}
}

func TestCacheClonesMaps(t *testing.T) {
	c := NewCache(8, time.Hour)
	s := sampleSummary(testEVM, 5)
	c.Set("k", s)
	s.ByType["wallet"] = 99
	got, _ := c.Get("k")
	if got.ByType["wallet"] != 5 {
		t.Fatalf("cache stored alias: %v", got.ByType["wallet"])
	}
	got.ByType["wallet"] = 7
	got2, _ := c.Get("k")
	if got2.ByType["wallet"] != 5 {
		t.Fatalf("get returned alias: %v", got2.ByType["wallet"])
	}
}
