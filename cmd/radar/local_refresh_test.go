package main

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"radar/internal/integration"
)

func TestLocalRefreshRequestsCoalesceWithoutBurstStarvation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		var calls []time.Time
		request, done := localRefreshRequests(ctx, func() { calls = append(calls, time.Now()) })
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
		if len(calls) == 0 || calls[0].Sub(start) != localRefreshCoalescingWindow {
			t.Fatalf("burst deferred first collection or created per-event work: %v", calls)
		}
		time.Sleep(2 * localRefreshCoalescingWindow)
		synctest.Wait()
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
		calls := 0
		request, done := localRefreshRequests(ctx, func() {
			calls++
			if calls == 1 {
				close(started)
				<-release
			}
		})
		request()
		<-started
		for range 10000 {
			request()
		}
		time.Sleep(time.Second)
		if calls != 1 {
			t.Fatal("collection was not serialized")
		}
		close(release)
		time.Sleep(2 * localRefreshCoalescingWindow)
		synctest.Wait()
		if calls != 2 {
			t.Fatalf("in-flight events lost or unbounded: calls=%d", calls)
		}
		cancel()
		<-done
	})
}

func TestLocalRefreshRequestsCancelQueuedWork(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		request, done := localRefreshRequests(ctx, func() { t.Error("collected after cancellation") })
		request()
		synctest.Wait()
		cancel()
		<-done
	})
}

func TestManagedInvalidationCollectsLocallyWhileRemoteIsStalled(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	remote := refreshProbeSource{name: "remote", collect: func(ctx context.Context) integration.CollectResult {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return integration.CollectResult{Complete: true}
	}}
	store, tasks, _, _ := refreshProbeFixture(t, remote)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	fullDone := make(chan struct{})
	var mu sync.Mutex
	request, done := localRefreshRequests(ctx, localRefresher(ctx, &mu, tasks))
	tasks.SetLocalRefreshRequest(request)
	go func() { defer close(fullDone); tasks.Refresh(ctx, false) }()
	t.Cleanup(func() { cancel(); <-done; <-fullDone })
	<-started
	tasks.MarkResourceRefreshPending()
	revision := store.Revision()
	for tasks.ResourceSnapshot().ResourceRefreshPending {
		if ctx.Err() != nil {
			t.Fatal("local invalidation waited for stalled remote")
		}
		revision = store.WaitForRevision(ctx, revision)
	}
	select {
	case <-fullDone:
		t.Fatal("fixture remote was not stalled")
	default:
	}
	close(release)
	<-fullDone
}

func TestManagedInvalidationDuringLocalCollectionConvergesWithoutPeriodicTick(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	local := refreshProbeSource{name: "local", local: true, collect: func(ctx context.Context) integration.CollectResult {
		if calls.Add(1) == 2 {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
		return integration.CollectResult{Complete: true}
	}}
	store, tasks, _, _ := refreshProbeFixture(t, local)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	var mu sync.Mutex
	request, done := localRefreshRequests(ctx, localRefresher(ctx, &mu, tasks))
	t.Cleanup(func() { cancel(); <-done })
	tasks.SetLocalRefreshRequest(request)
	tasks.Refresh(ctx, true)
	tasks.MarkResourceRefreshPending()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("invalidation did not start local collection")
	}
	for range 1000 {
		tasks.MarkResourceRefreshPending()
	}
	revision := store.Revision()
	close(release)
	for tasks.ResourceSnapshot().ResourceRefreshPending {
		if ctx.Err() != nil {
			t.Fatal("event during collection was lost; pending until periodic tick")
		}
		revision = store.WaitForRevision(ctx, revision)
	}
	if calls.Load() != 3 {
		t.Fatalf("collected %d times, want initial, superseded and one follow-up", calls.Load())
	}
}
