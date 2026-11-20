package main

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"radar/internal/integration"
	"radar/internal/localrefresh"
)

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
	request, done := localrefresh.Start(ctx, localRefresher(ctx, &mu, tasks))
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
	request, done := localrefresh.Start(ctx, localRefresher(ctx, &mu, tasks))
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
