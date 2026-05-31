package notification

import (
	"testing"
	"time"
)

func TestRetryBackoffExponentialCapped(t *testing.T) {
	p := RetryPolicy{
		BaseDelay:   time.Second,
		MaxDelay:    5 * time.Second,
		Jitter:      retryJitterFraction,
		RandFloat64: func() float64 { return 0.5 },
	}
	if got := p.BackoffDelay(1); got != time.Second {
		t.Fatalf("attempt 1 delay = %s, want 1s", got)
	}
	if got := p.BackoffDelay(2); got != 2*time.Second {
		t.Fatalf("attempt 2 delay = %s, want 2s", got)
	}
	if got := p.BackoffDelay(4); got != 5*time.Second {
		t.Fatalf("attempt 4 delay = %s, want capped 5s", got)
	}
}

func TestRetryJitterBounds(t *testing.T) {
	base := RetryPolicy{BaseDelay: 10 * time.Second, MaxDelay: time.Minute, Jitter: retryJitterFraction}
	low := base
	low.RandFloat64 = func() float64 { return 0 }
	high := base
	high.RandFloat64 = func() float64 { return 1 }

	if got := low.BackoffDelay(1); got != 8*time.Second {
		t.Fatalf("low jitter = %s, want 8s", got)
	}
	if got := high.BackoffDelay(1); got != 12*time.Second {
		t.Fatalf("high jitter = %s, want 12s", got)
	}
}

func TestErrorClassificationRetry(t *testing.T) {
	if ClassifyError("invalid_request") != ErrorPermanent {
		t.Fatal("invalid_request should be permanent")
	}
	if ShouldRetry("invalid_request", 1, 5) {
		t.Fatal("permanent errors should not retry")
	}
	if !ShouldRetry("timeout", 1, 5) {
		t.Fatal("transient errors under max attempts should retry")
	}
	if ShouldRetry("timeout", 5, 5) {
		t.Fatal("max attempts should stop retry")
	}
}
