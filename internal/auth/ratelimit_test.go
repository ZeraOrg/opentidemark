// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"strconv"
	"testing"
	"time"
)

func TestKeyedLimiter(t *testing.T) {
	c := newFakeClock()
	l := NewKeyedLimiter(c, 3, time.Minute)
	for i := range 3 {
		if !l.Allow("ip") {
			t.Fatalf("attempt %d denied", i)
		}
	}
	if l.Allow("ip") {
		t.Error("4th allowed")
	}
	if !l.Allow("other") {
		t.Error("other key denied")
	}
	c.Advance(20 * time.Second) // refills one token
	if !l.Allow("ip") || l.Allow("ip") {
		t.Error("refill wrong")
	}
	if ok, wait := l.AllowWait("ip"); ok || wait <= 0 {
		t.Errorf("AllowWait %v %v", ok, wait)
	}
	if l.AllowN("ip", 10) {
		t.Error("AllowN over burst")
	}
}

func TestAllowRate(t *testing.T) {
	c := newFakeClock()
	l := NewKeyedLimiter(c, 1, time.Second)
	ok, _ := l.AllowRate("conn", 5, 10)
	if !ok {
		t.Fatal("first batch denied")
	}
	ok, wait := l.AllowRate("conn", 10, 10)
	if ok || wait <= 0 || wait > time.Second {
		t.Errorf("second batch ok=%v wait=%v", ok, wait)
	}
	c.Advance(time.Second)
	if ok, _ := l.AllowRate("conn", 50, 10); !ok {
		t.Error("batch larger than rate should consume the full bucket")
	}
}

func TestLimiterBounded(t *testing.T) {
	c := newFakeClock()
	l := NewKeyedLimiter(c, 1, time.Second)
	l.maxKeys = 10
	for i := range 25 {
		l.Allow(strconv.Itoa(i))
	}
	if l.Len() > 10 {
		t.Errorf("len %d", l.Len())
	}
	c.Advance(time.Hour)
	l.Allow("new")
	if l.Len() > 10 {
		t.Errorf("len after sweep %d", l.Len())
	}
}

func TestFailureBlocker(t *testing.T) {
	c := newFakeClock()
	b := NewFailureBlocker(c, 3, time.Minute, 5*time.Minute)
	for range 3 {
		b.RecordFailure("1.2.3.4")
	}
	if b.Blocked("1.2.3.4") {
		t.Fatal("blocked at limit")
	}
	b.RecordFailure("1.2.3.4")
	if !b.Blocked("1.2.3.4") || b.Blocked("5.6.7.8") {
		t.Fatal("block state wrong")
	}
	c.Advance(5 * time.Minute)
	if b.Blocked("1.2.3.4") {
		t.Error("still blocked after block period")
	}
	b.blocked["x"] = c.Now().Add(-time.Second)
	b.sweep()
	if _, ok := b.blocked["x"]; ok {
		t.Error("sweep kept expired entry")
	}
}

func TestTTLLRU(t *testing.T) {
	c := newFakeClock()
	l := newTTLLRU[int](c, 2)
	l.put("a", 1, c.Now().Add(time.Minute))
	l.put("b", 2, c.Now().Add(time.Minute))
	l.get("a")
	l.put("c", 3, c.Now().Add(time.Minute)) // evicts b (least recently used)
	if _, ok := l.get("b"); ok {
		t.Error("b not evicted")
	}
	if v, ok := l.get("a"); !ok || v != 1 {
		t.Error("a missing")
	}
	l.put("a", 10, c.Now().Add(time.Second))
	c.Advance(2 * time.Second)
	if _, ok := l.get("a"); ok {
		t.Error("expired entry returned")
	}
	l.purge()
	if l.len() != 0 {
		t.Error("purge")
	}
}
