package stablekit

import (
	"context"
	"math"
	"net/http"
	"time"
)

// transportError is an internal wrapper carrying retry information.
type transportError struct {
	err       error
	retryable bool
}

func (e *transportError) Error() string { return e.err.Error() }
func (e *transportError) Unwrap() error { return e.err }

func isRetryable(err error) bool {
	te, ok := err.(*transportError)
	return ok && te.retryable
}

// retryableStatus returns true for HTTP status codes that may succeed on retry.
func retryableStatus(statusCode int) bool {
	if statusCode == http.StatusTooManyRequests {
		return true
	}
	return statusCode >= http.StatusInternalServerError
}

// backoff: 500ms × 2^attempt, capped at 10 seconds.
func backoff(attempt int) time.Duration {
	base := 500 * time.Millisecond
	d := time.Duration(float64(base) * math.Pow(2, float64(attempt)))
	const maxBackoff = 10 * time.Second
	if d > maxBackoff {
		d = maxBackoff
	}
	return d
}

// sleepCtx sleeps for d or returns ctx.Err() if cancelled first.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
