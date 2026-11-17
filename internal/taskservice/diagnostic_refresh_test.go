package taskservice

import (
	"context"
	"testing"
	"time"
)

func TestDiagnosticRefreshUsesNativeLocalAndRemoteCollection(t *testing.T) {
	remote := &testSource{name: "remote"}
	f := newFixture(t, remote)
	before := remote.calls.Load()
	result, err := f.service.RefreshDiagnostics(context.Background())
	if err != nil || remote.calls.Load() != before+1 {
		t.Fatalf("native refresh: %v calls=%d", err, remote.calls.Load())
	}
	for _, name := range []string{"obsidian", "remote"} {
		if result.Attempts[name].Outcome != "complete" {
			t.Fatalf("missing exact attempt %s: %+v", name, result.Attempts)
		}
	}
	_, _, attempts, _ := f.store.SourceInspectionSnapshot()
	if len(attempts) != 2 {
		t.Fatalf("native returned evidence not reused: %+v", attempts)
	}
}

func TestDiagnosticRefreshRejectsBusyOrCancelledAdmissionWithoutQueuedWork(t *testing.T) {
	for _, mode := range []string{"full_busy", "local_busy", "pre_cancelled", "publication_busy"} {
		t.Run(mode, func(t *testing.T) {
			remote := &testSource{name: "remote"}
			f := newFixture(t, remote)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			switch mode {
			case "full_busy":
				f.service.fullMu.Lock()
				defer f.service.fullMu.Unlock()
			case "local_busy":
				f.service.localMu.Lock()
				defer f.service.localMu.Unlock()
			case "pre_cancelled":
				cancel()
			case "publication_busy":
				f.service.applyMu.Lock()
				defer f.service.applyMu.Unlock()
			}
			before := f.store.Revision()
			calls := f.author.calls.Load()
			start := time.Now()
			result, err := f.service.RefreshDiagnostics(ctx)
			if err == nil || len(result.Attempts) != 0 || remote.calls.Load() != 0 || f.author.calls.Load() != calls || f.store.Revision() != before || time.Since(start) > time.Second {
				t.Fatalf("unsafe admission %s: %+v %v", mode, result, err)
			}
		})
	}
}

func TestDiagnosticRefreshCancellationRetainsLocalAndReturnedRemoteAttempts(t *testing.T) {
	f, remote, _ := concurrentRefreshFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		result, err := f.service.RefreshDiagnostics(ctx)
		if err == nil || result.Attempts["remote"].Outcome != "cancelled_or_timed_out" || result.Attempts["obsidian"].Outcome != "complete" {
			t.Errorf("cancelled native evidence lost: %+v %v", result.Attempts, err)
		}
	}()
	wait(t, remote.started)
	cancel()
	wait(t, done)
	// Native locks must be released, and no delayed remote refresh starts later.
	if !f.service.fullMu.TryLock() {
		t.Fatal("full collection lock leaked")
	}
	f.service.fullMu.Unlock()
	if !f.service.localMu.TryLock() {
		t.Fatal("local collection lock leaked")
	}
	f.service.localMu.Unlock()
}

func TestDiagnosticRefreshUsesProductionIncompleteAndSupersededEvidence(t *testing.T) {
	f, remote, _ := concurrentRefreshFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		result, err := f.service.RefreshDiagnostics(ctx)
		if err != nil || result.Attempts["remote"].Outcome != "complete" || result.Attempts["obsidian"].Outcome != "complete" {
			t.Errorf("native attempts=%+v error=%v", result.Attempts, err)
		}
	}()
	wait(t, remote.started)
	f.service.MarkResourceRefreshPending()
	close(remote.release)
	wait(t, done)
	if !f.service.ResourceSnapshot().ResourceRefreshPending {
		t.Fatal("superseded collection certified fresh resources")
	}
}
