package cleanup

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"radar/internal/integration"
	"radar/internal/protocol"
)

func TestAutomaticBlockersExpireOnlyKnownLocalDataWarnings(t *testing.T) {
	for _, kind := range []string{"local_changes", "unpublished_data", "safety_check_unavailable", "unknown_entries", "expired_entries", "", "future_warning", "ownership_conflict", "lease_conflict", "deletes_local_data", "disposable_data", "LOCAL_CHANGES", "local_changes "} {
		t.Run(kind, func(t *testing.T) {
			known := kind == "local_changes" || kind == "unpublished_data" || kind == "safety_check_unavailable" || kind == "unknown_entries" || kind == "expired_entries"
			for _, blocks := range []bool{false, true} {
				for _, expires := range []bool{false, true} {
					for _, expired := range []bool{false, true} {
						targets := []protocol.CleanupTarget{{Safety: []protocol.CleanupSafety{{Kind: kind, Message: "retain this blocker", BlocksAutomatic: blocks, Expires: expires}}}}
						var want []string
						if blocks && !(expired && expires && known) {
							want = []string{"retain this blocker"}
						}
						if got := AutomaticBlockingMessages(targets, expired); !reflect.DeepEqual(got, want) {
							t.Fatalf("blocks=%v expires=%v expired=%v: got %v, want %v", blocks, expires, expired, got, want)
						}
					}
				}
			}
		})
	}
}

func TestExpiredCleanupRejectsUnsupportedExpiringWarningBeforeAnyProviderCall(t *testing.T) {
	for _, scenario := range []string{"unknown", "structural", "empty-kind", "known-without-flag", "known-data", "confirmed-unknown"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("RADAR_SOCKET", filepath.Join(dir, "radar.sock"))
			safety := protocol.CleanupSafety{Kind: "future_warning", Message: "keep the whole bundle", BlocksAutomatic: true, Expires: true}
			mode := integration.CleanupExpired
			blocked := true
			switch scenario {
			case "structural":
				safety.Kind = "ownership_conflict"
			case "empty-kind":
				safety.Kind = ""
			case "known-without-flag":
				safety.Kind, safety.Expires = "local_changes", false
			case "known-data":
				safety.Kind, blocked = "local_changes", false
			case "confirmed-unknown":
				mode, blocked = integration.CleanupConfirmed, false
			}
			var calls []string
			provider := fakeProvider{name: "git", calls: &calls, failPath: "/never"}
			preview := protocol.CleanupPreview{TaskID: 7, Targets: []protocol.CleanupTarget{
				{Source: "git", Kind: "worktree", ResourceID: "first"},
				{Source: "git", Kind: "worktree", ResourceID: "second", Safety: []protocol.CleanupSafety{safety}},
			}}
			result, err := New([]integration.CleanupProvider{provider}).Execute(context.Background(), preview, ExecuteOptions{Mode: mode})
			if blocked {
				if err == nil || err.Error() != safety.Message || len(calls) != 0 || len(result.Targets) != 0 || result.ExecutionObservation.Stage != "admission" || result.ExecutionObservation.AttemptedTargets != 0 {
					t.Fatalf("unsafe warning admitted bundle: result=%+v calls=%v err=%v", result, calls, err)
				}
			} else if err != nil || len(calls) != 2 || len(result.Targets) != 2 {
				t.Fatalf("existing explicit/known-data policy changed: result=%+v calls=%v err=%v", result, calls, err)
			}
		})
	}
}
