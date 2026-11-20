package localrefresh

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestLocalRefreshRequestsCoalesceWithoutBurstStarvation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		var mu sync.Mutex
		var recorded []time.Time
		snapshot := func() []time.Time { mu.Lock(); defer mu.Unlock(); return slices.Clone(recorded) }
		request, done := Start(ctx, func() { mu.Lock(); recorded = append(recorded, time.Now()); mu.Unlock() })
		defer func() { cancel(); <-done }()
		start := time.Now()
		request()
		synctest.Wait()
		for range 20 {
			time.Sleep(10 * time.Millisecond)
			for range 100 {
				request()
			}
			synctest.Wait()
		}
		synctest.Wait()
		calls := snapshot()
		if len(calls) == 0 || calls[0].Sub(start) != coalescingWindow {
			t.Fatalf("burst deferred first collection or created per-event work: %v", calls)
		}
		time.Sleep(2 * coalescingWindow)
		synctest.Wait()
		calls = snapshot()
		// An event at the exact window boundary may coalesce into either
		// window, depending on which runnable goroutine executes first.
		if len(calls) < 2 || len(calls) > 3 {
			t.Fatalf("collections=%d, want two or three bounded windows for the 200ms burst", len(calls))
		}
	})
}

func TestLocalRefreshRequestsRetainEventsDuringBlockedCollection(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		started, release := make(chan struct{}), make(chan struct{})
		var calls atomic.Int32
		request, done := Start(ctx, func() {
			if calls.Add(1) == 1 {
				close(started)
				select {
				case <-release:
				case <-ctx.Done():
				}
			}
		})
		defer func() { cancel(); <-done }()
		request()
		<-started
		for range 10000 {
			request()
		}
		time.Sleep(time.Second)
		if calls.Load() != 1 {
			t.Fatal("collection was not serialized")
		}
		close(release)
		time.Sleep(2 * coalescingWindow)
		synctest.Wait()
		if calls.Load() != 2 {
			t.Fatalf("in-flight events lost or unbounded: calls=%d", calls.Load())
		}
		cancel()
		<-done
	})
}

func TestLocalRefreshRequestsCancelQueuedWork(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		request, done := Start(ctx, func() { t.Error("collected after cancellation") })
		request()
		synctest.Wait()
		cancel()
		<-done
	})
}
