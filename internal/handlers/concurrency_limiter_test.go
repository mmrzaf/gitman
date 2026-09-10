package handlers

import "testing"

func TestRequestConcurrencyLimiterBoundsGlobalAndPerKey(t *testing.T) {
	limiter := newRequestConcurrencyLimiter(2, 1)
	releaseA, ok := limiter.tryAcquire("client-a")
	if !ok {
		t.Fatal("client-a should acquire its first slot")
	}
	defer releaseA()
	if _, ok := limiter.tryAcquire("client-a"); ok {
		t.Fatal("client-a should be capped by the per-key limit")
	}
	releaseB, ok := limiter.tryAcquire("client-b")
	if !ok {
		t.Fatal("client-b should be able to use the remaining global slot")
	}
	if _, ok := limiter.tryAcquire("client-c"); ok {
		t.Fatal("global limit should reject a third active request")
	}
	releaseB()
	releaseC, ok := limiter.tryAcquire("client-c")
	if !ok {
		t.Fatal("releasing a slot should allow another client")
	}
	releaseC()
}

func TestRequestConcurrencyLimiterReleaseIsIdempotent(t *testing.T) {
	limiter := newRequestConcurrencyLimiter(1, 1)
	release, ok := limiter.tryAcquire("client")
	if !ok {
		t.Fatal("expected slot")
	}
	release()
	release()
	second, ok := limiter.tryAcquire("client")
	if !ok {
		t.Fatal("double release must not corrupt the limiter")
	}
	second()
}
