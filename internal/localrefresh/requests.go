package localrefresh

import (
	"context"
	"time"
)

const coalescingWindow = 100 * time.Millisecond

// Start returns a nonblocking invalidation sink and a shutdown
// join. The first event owns a fixed window: subsequent events cannot postpone
// it. One queued token retains events arriving during collection, causing a
// fresh pass after that collection returns (including a fenced/stale pass).
func Start(ctx context.Context, refresh func()) (func(), <-chan struct{}) {
	requests, done := make(chan struct{}, 1), make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case <-requests:
			}
			timer := time.NewTimer(coalescingWindow)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			// Drain duplicates before collection, never after it: an event
			// concurrent with collection must be retained for another pass.
			select {
			case <-requests:
			default:
			}
			if ctx.Err() == nil {
				refresh()
			}
		}
	}()
	return func() {
		if ctx.Err() != nil {
			return
		}
		select {
		case requests <- struct{}{}:
		default:
		}
	}, done
}
