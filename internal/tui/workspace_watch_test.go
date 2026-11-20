package tui

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"radar/internal/localrefresh"
	"radar/internal/workspacewatch"
)

// Native filesystem -> bounded production scheduler -> real workspace provider
// -> task service/store -> Unix revision watch -> model Update/View. No periodic
// refresh, live providers, terminal emulator or synthetic resource projection.
func TestWorkspaceWatcherRetainsEventDuringNativeProviderRead(t *testing.T) {
	f := newReactivityBaselineFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	started, release := make(chan struct{}), make(chan struct{})
	var pause atomic.Bool
	var calls atomic.Int32
	f.source.afterCollect = func(ctx context.Context) {
		calls.Add(1)
		if pause.Swap(false) {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
	}
	observed, receipts := make(chan struct{}, 10), make(chan struct{}, 100)
	request, refreshDone := localrefresh.Start(ctx, func() { f.service.Refresh(ctx, true); observed <- struct{}{} })
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		workspacewatch.Run(ctx, filepath.Dir(f.anchor), func() []string { return []string{f.anchor} }, func() { receipts <- struct{}{}; request() }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}()
	t.Cleanup(func() { cancel(); <-watchDone; <-refreshDone })
	wait := func(ch <-chan struct{}) {
		t.Helper()
		select {
		case <-ch:
		case <-ctx.Done():
			t.Fatal("filesystem-triggered reconciliation stalled")
		}
	}
	wait(observed)
	for len(receipts) > 0 {
		<-receipts
	}
	f.model.applyResponse(f.service.ResourceSnapshot(), false)
	pause.Store(true)
	if err := os.Rename(f.anchor, f.away); err != nil {
		t.Fatal(err)
	}
	wait(receipts)
	wait(started) // Native provider has already observed missing.
	if err := os.Rename(f.away, f.anchor); err != nil {
		t.Fatal(err)
	}
	wait(receipts) // Real OS invalidation must be received while read is stalled.
	if calls.Load() != 2 {
		t.Fatal("watch event started a concurrent provider read")
	}
	close(release)
	wait(observed)
	wait(observed)
	if calls.Load() != 3 {
		t.Fatalf("dirty follow-up lost or unbounded: %d reads", calls.Load())
	}
	msg := f.model.watch(f.model.revision)().(watchMsg)
	if msg.err != nil {
		t.Fatal(msg.err)
	}
	updated, _ := f.model.Update(msg)
	f.model = updated.(model)
	if strings.Contains(ansi.Strip(f.model.View()), "workspace partial") || len(f.model.tasks) != 1 || !f.model.tasks[0].SourceRefs[0].ProvidesWorkspace {
		t.Fatal("in-flight event failed to restore real source health without periodic polling")
	}
}

func TestWorkspaceWatcherUpdatesRealSourceHealthWithoutPeriodicTick(t *testing.T) {
	f := newReactivityBaselineFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	type observation struct {
		start, finish time.Time
		provider      time.Duration
	}
	observed := make(chan observation, 20)
	receipts := make(chan time.Time, 100)
	request, refreshDone := localrefresh.Start(ctx, func() {
		start := time.Now()
		f.service.Refresh(ctx, true)
		observed <- observation{start, time.Now(), f.source.duration}
	})
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		workspacewatch.Run(ctx, filepath.Dir(f.anchor), func() []string {
			var anchors []string
			for _, task := range f.store.CollectionTasks() {
				for _, ref := range task.SourceRefs {
					if ref.Source == "workspace" && ref.Kind == "workspace" {
						anchors = append(anchors, ref.Path)
					}
				}
			}
			return anchors
		}, func() { receipts <- time.Now(); request() }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}()
	t.Cleanup(func() { cancel(); <-watchDone; <-refreshDone })
	select {
	case <-observed: // Post-arming authoritative read, not a sleep-based ready guess.
	case <-ctx.Done():
		t.Fatal("watcher did not arm and request initial observation")
	}
	for len(receipts) > 0 {
		<-receipts
	}
	// Consume the post-arming snapshot too: startup collection may publish an
	// updated revision even when presence is unchanged. Do not confuse that
	// queued snapshot with the later filesystem-triggered observation.
	f.model.applyResponse(f.service.ResourceSnapshot(), false)

	for _, step := range []struct {
		name    string
		missing bool
		mutate  func() error
	}{
		{"rename-away", true, func() error { return os.Rename(f.anchor, f.away) }},
		{"rename-back", false, func() error { return os.Rename(f.away, f.anchor) }},
		{"remove", true, func() error { return os.Remove(f.anchor) }},
		{"recreate", false, func() error { return os.Mkdir(f.anchor, 0o700) }},
	} {
		start := time.Now()
		if err := step.mutate(); err != nil {
			t.Fatal(err)
		}
		var receipt time.Time
		select {
		case receipt = <-receipts:
		case <-ctx.Done():
			t.Fatalf("%s: no filesystem invalidation", step.name)
		}
		// The existing production watch command reads the real daemon socket.
		msg := f.model.watch(f.model.revision)().(watchMsg)
		watched := time.Now()
		if msg.err != nil || msg.response.Revision <= f.model.revision {
			t.Fatalf("%s: watch response: %+v", step.name, msg)
		}
		updated, _ := f.model.Update(msg)
		f.model = updated.(model)
		view := ansi.Strip(f.model.View())
		rendered := time.Now()
		if strings.Contains(view, "workspace partial") != step.missing {
			t.Fatalf("%s: real display did not follow anchor presence: %s", step.name, view)
		}
		if len(f.model.tasks) != 1 || !f.model.tasks[0].SourceRefs[0].ProvidesWorkspace || msg.response.ResourceRefreshPending {
			t.Fatalf("%s: event fabricated cleanup, ownership or managed-mutation freshness: %+v", step.name, msg.response)
		}
		f.model.sourcesExpanded = true
		if strings.Contains(ansi.Strip(f.model.sourceList(500)), "workspace anchor is missing") != step.missing {
			t.Fatalf("%s: diagnostic mismatch", step.name)
		}
		f.model.sourcesExpanded = false
		select {
		case read := <-observed:
			// Collection includes publication; do not advertise this aggregate as
			// pure publication latency or an OS keyboard/terminal measurement.
			t.Logf("%s detection=%s request-to-read-start=%s provider=%s read-and-publication=%s read-start-to-watch=%s model-render=%s", step.name, receipt.Sub(start), read.start.Sub(receipt), read.provider, read.finish.Sub(read.start), watched.Sub(read.start), rendered.Sub(watched))
		case <-ctx.Done():
			t.Fatal("authoritative read did not finish")
		}
		for len(receipts) > 0 {
			<-receipts
		}
	}
}
