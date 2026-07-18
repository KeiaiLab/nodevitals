// Package queue provides retry-with-backoff delivery to sinks. Full Jitter
// backoff and injected clock/random keep it deterministic under test.
package queue

import (
	"context"
	"fmt"
	"time"

	"github.com/KeiaiLab/nodevitals/internal/model"
	"github.com/KeiaiLab/nodevitals/internal/sink"
)

// Backoff computes Full Jitter delays: random(0, min(Max, Base*2^attempt)).
type Backoff struct {
	Base time.Duration
	Max  time.Duration
}

// For returns the delay for a 0-indexed attempt. rnd is in [0,1]. A negative
// attempt is clamped to 0; very large attempts saturate at Max.
func (b Backoff) For(attempt int, rnd float64) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	if attempt > 62 {
		return time.Duration(rnd * float64(b.Max))
	}
	window := b.Base << attempt
	if window > b.Max || window <= 0 {
		window = b.Max
	}
	return time.Duration(rnd * float64(window))
}

// DeliverWithRetry emits events, retrying transient failures with backoff.
// sleep and rnd are injected for deterministic tests (use time.Sleep and
// rand.Float64 in production). It returns early if ctx is cancelled.
func DeliverWithRetry(
	ctx context.Context, s sink.Sink, events []model.Event, maxAttempts int,
	b Backoff, sleep func(time.Duration), rnd func() float64,
) error {
	if maxAttempts <= 0 {
		return fmt.Errorf("sink %s: maxAttempts must be positive, got %d", s.Name(), maxAttempts)
	}
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("sink %s: delivery cancelled: %w", s.Name(), err)
		}
		if attempt > 0 {
			sleep(b.For(attempt-1, rnd()))
		}
		lastErr = s.EmitEvents(ctx, events)
		if lastErr == nil {
			return nil
		}
	}
	return fmt.Errorf("sink %s failed after %d attempts: %w", s.Name(), maxAttempts, lastErr)
}
