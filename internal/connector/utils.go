package connector

import (
	"context"
	"time"
)

func sleepContext(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func nextBackoff(current time.Duration) time.Duration {
	next := current * 2
	if next > outboundRetryCap {
		return outboundRetryCap
	}
	return next
}

func retryDelay(attempt int) time.Duration {
	if attempt <= 1 {
		return outboundRetryBase
	}
	delay := outboundRetryBase
	for i := 1; i < attempt; i++ {
		delay *= 2
		if delay >= outboundRetryCap {
			return outboundRetryCap
		}
	}
	return delay
}

func notify(ch chan<- struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}
