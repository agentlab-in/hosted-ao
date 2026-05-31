package notification

import (
	crand "crypto/rand"
	"encoding/binary"
	"math"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
)

const retryJitterFraction = 0.20

type RetryPolicy struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
	LeaseTTL    time.Duration
	BatchSize   int
	Jitter      float64
	RandFloat64 func() float64
}

func RetryPolicyFromConfig(cfg config.NotificationRetryConfig) RetryPolicy {
	settings := NormalizeSettings(config.NotificationConfig{Enabled: true, Retry: cfg})
	return RetryPolicy{
		MaxAttempts: settings.Retry.MaxAttempts,
		BaseDelay:   settings.Retry.BaseDelay,
		MaxDelay:    settings.Retry.MaxDelay,
		LeaseTTL:    settings.Retry.LeaseTTL,
		BatchSize:   settings.Retry.BatchSize,
		Jitter:      retryJitterFraction,
		RandFloat64: cryptoRandFloat64,
	}
}

func (p RetryPolicy) normalized() RetryPolicy {
	cfg := config.NotificationRetryConfig{
		MaxAttempts: p.MaxAttempts,
		BaseDelay:   p.BaseDelay,
		MaxDelay:    p.MaxDelay,
		LeaseTTL:    p.LeaseTTL,
		BatchSize:   p.BatchSize,
	}
	out := RetryPolicyFromConfig(cfg)
	if p.Jitter != 0 {
		out.Jitter = p.Jitter
	}
	if p.RandFloat64 != nil {
		out.RandFloat64 = p.RandFloat64
	}
	return out
}

// BackoffDelay returns exponential backoff for the already-recorded attempt
// count. attempt=1 returns the base delay; delays are capped before jitter.
func (p RetryPolicy) BackoffDelay(attempt int) time.Duration {
	p = p.normalized()
	if attempt < 1 {
		attempt = 1
	}
	mult := math.Pow(2, float64(attempt-1))
	delay := time.Duration(float64(p.BaseDelay) * mult)
	if delay > p.MaxDelay || delay <= 0 {
		delay = p.MaxDelay
	}
	if p.Jitter <= 0 {
		return delay
	}
	randFloat := p.RandFloat64
	if randFloat == nil {
		randFloat = cryptoRandFloat64
	}
	// rand in [0,1) -> factor in [1-jitter, 1+jitter)
	factor := 1 - p.Jitter + (2 * p.Jitter * randFloat())
	return time.Duration(float64(delay) * factor)
}

func cryptoRandFloat64() float64 {
	var b [8]byte
	if _, err := crand.Read(b[:]); err != nil {
		// Fall back to a time-derived value only if the OS CSPRNG fails. The
		// fallback still avoids math/rand's deterministic process seed.
		return float64(time.Now().UnixNano()&((1<<53)-1)) / float64(1<<53)
	}
	// Match math/rand.Float64's 53 bits of precision in [0,1).
	return float64(binary.BigEndian.Uint64(b[:])>>11) / float64(1<<53)
}

func (p RetryPolicy) NextAttemptAt(now time.Time, attempt int) time.Time {
	return now.Add(p.BackoffDelay(attempt))
}

type ErrorClass string

const (
	ErrorTransient ErrorClass = "transient"
	ErrorPermanent ErrorClass = "permanent"
)

func ClassifyError(code string) ErrorClass {
	switch strings.ToLower(strings.TrimSpace(code)) {
	case "permanent", "invalid_request", "bad_request", "unauthorized", "forbidden", "not_found", "unsupported_route", "route_disabled":
		return ErrorPermanent
	default:
		return ErrorTransient
	}
}

func ShouldRetry(code string, attempts, maxAttempts int) bool {
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	return ClassifyError(code) != ErrorPermanent && attempts < maxAttempts
}
