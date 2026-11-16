package taskservice

import (
	"context"
	"testing"
	"time"
)

func TestRefreshRetainsReturnedAttemptsWithoutCertifyingCancelledOrSupersededPublication(t *testing.T) {
	for _, mode := range []string{"cancelled", "superseded"} {
		t.Run(mode, func(t *testing.T) {
			f, remote, _ := concurrentRefreshFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			done := make(chan struct{})
			go func() { defer close(done); f.service.Refresh(ctx, false) }()
			t.Cleanup(func() { cancel(); <-done })
			wait(t, remote.started)
			// In-flight collection must never be described as a returned attempt.
			_, _, active, _ := f.store.SourceInspectionSnapshot()
			if _, ok := active["remote"]; ok {
				t.Fatal("active collection published as completed attempt")
			}
			if mode == "cancelled" {
				cancel()
			} else {
				f.service.MarkResourceRefreshPending()
				close(remote.release)
			}
			wait(t, done)
			_, _, attempts, _ := f.store.SourceInspectionSnapshot()
			a, ok := attempts["remote"]
			if !ok || a.Latest.StartedAt == "" || a.Latest.FinishedAt == "" || a.Latest.CollectionStartedAt == "" {
				t.Fatalf("returned attempt lost: %+v", attempts)
			}
			if mode == "cancelled" {
				if a.Latest.Outcome != "cancelled_or_timed_out" || a.LastCompleteAt != "" {
					t.Fatalf("cancelled attempt=%+v", a)
				}
			} else {
				if a.Latest.Outcome != "complete" || a.LastCompleteAt != a.Latest.FinishedAt || !f.service.ResourceSnapshot().ResourceRefreshPending {
					t.Fatalf("native complete was confused with publication freshness: %+v", a)
				}
			}
		})
	}
}
