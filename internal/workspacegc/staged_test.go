package workspacegc

import (
	"context"
	"testing"
	"time"

	"radar/internal/cleanup"
	"radar/internal/integration"
)

func TestRegisteredWorkspaceNeverDeletesBeforeSevenDays(t *testing.T) {
	for _, age := range []time.Duration{time.Hour, 24*time.Hour - time.Nanosecond, 24 * time.Hour, 7*24*time.Hour - time.Nanosecond, 7 * 24 * time.Hour} {
		for _, manualGC := range []bool{false, true} {
			store, root, done, _ := expiryFixture(t)
			var calls []cleanupCall
			provider := gcProvider{name: "workspace", calls: &calls}
			result, err := Run(context.Background(), store, cleanup.New([]integration.CleanupProvider{provider}), nil, done.Add(age), Options{WorkspaceRoot: root, IgnoreRetention: manualGC})
			if err != nil {
				t.Fatal(err)
			}
			if got, want := len(result.Deleted), 0; age >= 7*24*time.Hour {
				if got != 1 {
					t.Fatalf("age=%s manualGC=%v deleted=%d, want 1", age, manualGC, got)
				}
			} else if got != want {
				t.Fatalf("age=%s manualGC=%v deleted=%d, want 0", age, manualGC, got)
			}
			if age < 24*time.Hour && len(calls) != 0 {
				t.Fatalf("early runtime removal: age=%s manualGC=%v calls=%+v", age, manualGC, calls)
			}
		}
	}
}
