package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"radar/internal/cleanup"
	"radar/internal/integration"
	"radar/internal/notification"
	"radar/internal/protocol"
	"radar/internal/state"
	"radar/internal/taskservice"
)

// Diagnostic probes describe the current scheduling and publication contracts.
// All state is temporary, providers are synthetic, and GC has no manager.
type refreshProbeSource struct {
	name    string
	local   bool
	collect func(context.Context) integration.CollectResult
}

func (s refreshProbeSource) Descriptor() integration.Descriptor {
	return integration.Descriptor{Name: s.name}
}
func (s refreshProbeSource) Local() bool { return s.local }
func (s refreshProbeSource) Collect(ctx context.Context, _ integration.CollectRequest) integration.CollectResult {
	return s.collect(ctx)
}

func refreshProbeFixture(t testing.TB, providers ...integration.Integration) (*state.Store, *taskservice.Service, integration.Registry, *slog.Logger) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("RADAR_DISABLE_COLLECTION", "0")
	t.Setenv("RADAR_STATE", filepath.Join(dir, "state.db"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	configDir := filepath.Join(dir, "config", "radar")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := state.NewStore(logger)
	if err != nil {
		t.Fatal(err)
	}
	registry := integration.NewRegistry(providers...)
	return store, taskservice.New(store, logger, registry), registry, logger
}

func TestRefreshKeepsLocalPublicationResponsive(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var calls atomic.Int32
	remote := refreshProbeSource{name: "remote", collect: func(ctx context.Context) integration.CollectResult {
		started <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
		}
		return integration.CollectResult{Complete: true}
	}}
	local := refreshProbeSource{name: "local", local: true, collect: func(context.Context) integration.CollectResult {
		calls.Add(1)
		return integration.CollectResult{Complete: true, Observations: []integration.Observation{{Ref: protocol.SourceRef{ID: "local:1", Kind: "item", Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleWorkItem, Authority: protocol.SourceRefAuthorityContributing, CanonicalKey: "local:1"}, Signal: integration.SignalInProgress}}}
	}}
	store, tasks, registry, logger := refreshProbeFixture(t, remote, local)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	var mu sync.Mutex
	full := refresher(ctx, store, logger, &mu, registry, cleanup.New(nil), notification.Service{}, tasks)
	fullDone := make(chan struct{})
	t.Cleanup(func() { cancel(); <-fullDone })
	go func() { defer close(fullDone); full(refreshFull, true) }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("remote did not start")
	}
	if len(store.Tasks()) != 1 {
		t.Fatal("initial local data waited for the remote source")
	}
	localDone := make(chan struct{})
	go func() { defer close(localDone); localRefresher(ctx, &mu, tasks)() }()
	select {
	case <-localDone:
	case <-time.After(time.Second):
		t.Fatal("local refresh waited for remote collection")
	}
	select {
	case <-fullDone:
		t.Fatal("full refresh did not wait for the remote result")
	default:
	}
	close(release)
	select {
	case <-fullDone:
	case <-ctx.Done():
		t.Fatal("full refresh stalled")
	}
	if calls.Load() != 2 || len(store.Tasks()) != 1 {
		t.Fatal("remote publication recollected or lost local observations")
	}
}

type coalescingProbeHandler struct {
	slog.Handler
	coalesced chan struct{}
}

func (h coalescingProbeHandler) Handle(ctx context.Context, record slog.Record) error {
	if record.Message == "full refresh coalesced" {
		h.coalesced <- struct{}{}
	}
	return h.Handler.Handle(ctx, record)
}

func TestRefreshForcedRequestsShareInFlightCollection(t *testing.T) {
	started, release := make(chan struct{}, 1), make(chan struct{})
	var calls atomic.Int32
	remote := refreshProbeSource{name: "remote", collect: func(ctx context.Context) integration.CollectResult {
		calls.Add(1)
		started <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
		}
		return integration.CollectResult{Complete: true}
	}}
	store, tasks, registry, _ := refreshProbeFixture(t, remote)
	coalesced := make(chan struct{}, 1)
	logger := slog.New(coalescingProbeHandler{Handler: slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}), coalesced: coalesced})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var mu sync.Mutex
	full := refresher(ctx, store, logger, &mu, registry, cleanup.New(nil), notification.Service{}, tasks)
	var wg sync.WaitGroup
	wg.Go(func() { full(refreshFull, true) })
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("remote did not start")
	}
	secondDone := make(chan struct{})
	wg.Go(func() { defer close(secondDone); full(refreshFull, true) })
	select {
	case <-coalesced:
	case <-ctx.Done():
		t.Fatal("second request did not join collection")
	}
	select {
	case <-secondDone:
		t.Fatal("coalesced caller returned before collection completed")
	default:
	}
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("remote calls=%d, want one shared collection", calls.Load())
	}
}

func TestRefreshSlowFullDoesNotPauseLocalSchedule(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		start := time.Now()
		var locals []time.Duration
		fullCalls := 0
		done := make(chan struct{})
		go func() {
			defer close(done)
			refreshLoop(ctx, func(scope refreshScope, _ bool) {
				if scope == refreshLocal {
					locals = append(locals, time.Since(start))
					return
				}
				fullCalls++
				if fullCalls == 2 {
					time.Sleep(45 * time.Second)
				}
			})
		}()
		time.Sleep(3 * time.Minute)
		cancel()
		<-done
		var largest time.Duration
		for i := 1; i < len(locals); i++ {
			largest = max(largest, locals[i]-locals[i-1])
		}
		if len(locals) < 11 || largest > localRefreshInterval {
			t.Fatalf("polls=%d, largest gap=%s, want uninterrupted %s polling", len(locals), largest, localRefreshInterval)
		}
		t.Logf("45s simulated full refresh: largest local polling gap %s", largest)
	})
}

func TestRefreshNotifiesLocalAttentionOnceAcrossFullPublication(t *testing.T) {
	local := refreshProbeSource{name: "local", local: true, collect: func(context.Context) integration.CollectResult {
		return integration.CollectResult{Complete: true, Observations: []integration.Observation{{Ref: protocol.SourceRef{ID: "local:1", Kind: "item", Title: "Needs attention", Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleWorkItem, Authority: protocol.SourceRefAuthorityContributing, CanonicalKey: "local:1"}, Signal: integration.SignalAttention}}}
	}}
	_, tasks, registry, logger := refreshProbeFixture(t, local)
	sender := &recordingNotificationSender{}
	notifier := notification.NewWithSender(logger, sender)
	tasks.SetOnRefresh(func(previous, current []protocol.Task) {
		notifyActionableTransitions(context.Background(), previous, current, logger, registry, notifier)
	})
	tasks.Refresh(context.Background(), false)
	if len(sender.titles) != 1 {
		t.Fatalf("notifications=%d, want one local attention transition", len(sender.titles))
	}
}

func BenchmarkRefreshProbePublication(b *testing.B) {
	for _, count := range []int{10, 100, 1000} {
		b.Run(fmt.Sprintf("tasks=%d", count), func(b *testing.B) {
			observations := make([]integration.Observation, count)
			for i := range observations {
				id := fmt.Sprintf("local:%d", i)
				observations[i] = integration.Observation{Ref: protocol.SourceRef{ID: id, Kind: "item", Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleWorkItem, Authority: protocol.SourceRefAuthorityContributing, CanonicalKey: id, Title: "Synthetic task", Metadata: map[string]string{"fixture": "refresh"}}, Signal: integration.SignalInProgress}
			}
			source := refreshProbeSource{name: "local", local: true, collect: func(context.Context) integration.CollectResult {
				return integration.CollectResult{Complete: true, Observations: observations}
			}}
			store, tasks, _, _ := refreshProbeFixture(b, source)
			tasks.Refresh(context.Background(), true)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				tasks.Refresh(context.Background(), true)
			}
			b.StopTimer()
			if len(store.Tasks()) != count {
				b.Fatalf("tasks=%d, want %d", len(store.Tasks()), count)
			}
		})
	}
}
