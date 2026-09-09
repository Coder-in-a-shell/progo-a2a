package dispatcher

import (
	"context"
	"math"
	"time"
)

// BackoffPolicy defines exponential backoff configuration for retries.
type BackoffPolicy struct {
	InitialInterval time.Duration
	Multiplier      float64
	MaxInterval     time.Duration
}

// DefaultBackoffPolicy returns the default backoff configuration:
// 100ms initial interval, multiplier 2.0, max interval 2s.
func DefaultBackoffPolicy() *BackoffPolicy {
	return &BackoffPolicy{
		InitialInterval: 100 * time.Millisecond,
		Multiplier:      2.0,
		MaxInterval:     2 * time.Second,
	}
}

// Duration computes the backoff duration for the given retry attempt (0-indexed).
func (b *BackoffPolicy) Duration(attempt int) time.Duration {
	if b == nil || b.InitialInterval <= 0 || attempt < 0 {
		return 0
	}
	mult := b.Multiplier
	if mult < 1.0 {
		mult = 2.0
	}
	d := float64(b.InitialInterval) * math.Pow(mult, float64(attempt))
	if b.MaxInterval > 0 && d > float64(b.MaxInterval) {
		d = float64(b.MaxInterval)
	}
	return time.Duration(d)
}

// Sleep blocks until the duration for the given retry attempt elapses or ctx is cancelled.
func (b *BackoffPolicy) Sleep(ctx context.Context, attempt int) error {
	d := b.Duration(attempt)
	if d <= 0 {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return nil
	}

	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
