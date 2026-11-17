package phasetiming

import (
	"context"
	"testing"
	"time"
)

func TestNativePhaseCancellationDoesNotEraseCompletedFailedInterval(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	count := 0
	ctx = WithObserver(ctx, func(phase string, elapsed time.Duration) {
		if phase != "cleanup_runtime" || elapsed < 0 {
			t.Fatal("invalid completed sample")
		}
		count++
	})
	done := Start(ctx, "cleanup_runtime")
	cancel()
	done()
	done()
	if count != 1 {
		t.Fatal("cancellation hid completed interval or duplicated it")
	}
}
