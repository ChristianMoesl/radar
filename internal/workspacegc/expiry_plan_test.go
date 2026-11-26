package workspacegc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"radar/internal/cleanup"
	"radar/internal/integration"
	"radar/internal/protocol"
)

func TestExactExpiryNativePlanOwnsOneOrderedBundle(t *testing.T) {
	for _, id := range []string{"registered", "second", "third"} {
		t.Run(id, func(t *testing.T) {
			store, root, done := candidateBoundaryFixture(t)
			now := done.Add(ExpiryRetention)
			options := Options{WorkspaceRoot: root}
			candidates, err := BuildPlan(store, now, options)
			if err != nil || len(candidates.Candidates) != 3 {
				t.Fatal(candidates, err)
			}
			var candidate Candidate
			for _, c := range candidates.Candidates {
				if c.WorkspaceID == id {
					candidate = c
				}
			}
			guards, previews := 0, 0
			options.GuardExecution = func(_ context.Context, task protocol.Task, execute func() error) error {
				guards++
				if task.ID != candidate.TaskID {
					t.Fatal("wrong lifecycle task")
				}
				return execute()
			}
			var calls []cleanupCall
			var modes []integration.CleanupMode
			var observed []protocol.CleanupTarget
			providers := []integration.CleanupProvider{}
			for _, name := range []string{"tmux", "sbx", "git", "workspace"} {
				providers = append(providers, retirementOwnedProvider{candidateBoundaryProvider: candidateBoundaryProvider{gcProvider: gcProvider{name: name, calls: &calls}, previews: &previews, modes: &modes}, observed: &observed})
			}
			service := cleanup.New(providers)
			plan, err := PrepareExpiry(context.Background(), store, service, candidate, now, options)
			if err != nil || guards != 0 || len(calls) != 0 || previews != 4 {
				t.Fatalf("preview dispatched/swept: %v %d %d %v", err, guards, previews, calls)
			}
			before := plan.Preview()
			wantSources := []string{"tmux", "sbx", "workspace"}
			if id == "registered" {
				wantSources = []string{"tmux", "sbx", "git", "workspace"}
			}
			if before.TaskID != candidate.TaskID || len(before.Targets) != len(wantSources) {
				t.Fatal(before)
			}
			for i, source := range wantSources {
				if before.Targets[i].Source != source || (source != "git" && before.Targets[i].Path != candidate.Path) || (source == "git" && before.Targets[i].Path != filepath.Join(candidate.Path, "repo--work")) {
					t.Fatal("wrong bundle", before)
				}
			}
			disclosure := plan.Preview()
			disclosure.Targets[0].Operation["lifetime"] = "replacement"
			disclosure.Targets[0].Safety[0].Summary = "replacement"
			disclosure.Targets[0].Path = root
			disclosure.Targets[1] = protocol.CleanupTarget{}
			for i := range candidate.Task.SourceRefs {
				if candidate.Task.SourceRefs[i].Metadata != nil {
					candidate.Task.SourceRefs[i].Metadata["completed_at"] = "replacement"
				}
			}
			options.WorkspaceRoot = root + "-replacement"
			options.Retention = 30 * 24 * time.Hour
			options.GuardExecution = nil
			now = done.Add(time.Hour) // Cannot demote the retained expiry mode/time.
			if !reflect.DeepEqual(before, plan.Preview()) {
				t.Fatal("caller changed retained bundle")
			}
			result, changed, err := plan.Execute(context.Background(), store, service)
			if err != nil || changed || guards != 1 || previews != 4 || len(result.Targets) != len(wantSources) || len(calls) != len(wantSources) {
				t.Fatalf("exact execution: %+v %v %v calls=%v guards=%d previews=%d", result, changed, err, calls, guards, previews)
			}
			for i, source := range wantSources {
				if calls[i].source != source || calls[i].path != before.Targets[i].Path || !calls[i].force || modes[i] != integration.CleanupExpired || observed[i].Operation["lifetime"] != "original" || observed[i].Safety[0].Summary != "original" {
					t.Fatal("order/mode/ownership lost", calls, modes, observed)
				}
			}
			if !reflect.DeepEqual(before, plan.Preview()) {
				t.Fatal("native execution changed disclosure")
			}
		})
	}
}

func TestExactExpiryNativePreparationRejectsStageAndIdentityChanges(t *testing.T) {
	for _, scenario := range []string{"before-day-one", "day-one", "before-expiry", "expiry", "after-expiry", "manual-before-expiry", "retention-held", "standalone", "wrong-workspace", "wrong-path", "relative-path", "unclean-path", "wrong-task", "wrong-record", "wrong-done", "changed-private-ref", "cancelled", "no-root", "no-store"} {
		t.Run(scenario, func(t *testing.T) {
			store, root, done, _ := expiryFixture(t)
			options := Options{WorkspaceRoot: root}
			now := done.Add(ExpiryRetention)
			candidates, err := BuildPlan(store, now, options)
			if err != nil || len(candidates.Candidates) != 1 {
				t.Fatal(candidates, err)
			}
			candidate := candidates.Candidates[0]
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch scenario {
			case "before-day-one":
				now = done.Add(DefaultRetention - time.Nanosecond)
			case "day-one":
				now = done.Add(DefaultRetention)
			case "before-expiry":
				now = done.Add(ExpiryRetention - time.Nanosecond)
			case "after-expiry":
				now = done.Add(ExpiryRetention + time.Hour)
			case "manual-before-expiry":
				now = done.Add(ExpiryRetention - time.Nanosecond)
				options.IgnoreRetention = true
			case "retention-held":
				options.Retention = 30 * 24 * time.Hour
			case "standalone":
				candidate.WorkspaceID = ""
			case "wrong-workspace":
				candidate.WorkspaceID = "replacement"
			case "wrong-path":
				candidate.Path = root
			case "relative-path":
				candidate.Path = "private-relative"
			case "unclean-path":
				candidate.Path += "/../completed"
			case "wrong-task":
				candidate.TaskID++
			case "wrong-record":
				candidate.RecordID += "replacement"
			case "wrong-done":
				candidate.DoneAt = done.Add(time.Hour).Format(time.RFC3339)
			case "changed-private-ref":
				candidate.Task.SourceRefs[0].ID += "replacement"
			case "cancelled":
				cancel()
			case "no-root":
				options.WorkspaceRoot = ""
			case "no-store":
				store = nil
			}
			previews := 0
			var calls []cleanupCall
			var modes []integration.CleanupMode
			service := cleanup.New([]integration.CleanupProvider{candidateBoundaryProvider{gcProvider: gcProvider{name: "workspace", calls: &calls}, previews: &previews, modes: &modes}})
			plan, err := PrepareExpiry(ctx, store, service, candidate, now, options)
			allowed := scenario == "expiry" || scenario == "after-expiry"
			wantPreviews := 0
			if allowed {
				wantPreviews = 1
			}
			if (err == nil) != allowed || len(calls) != 0 || previews != wantPreviews {
				t.Fatalf("stage/identity gate: %+v %v previews=%d", plan.Preview(), err, previews)
			}
			if !allowed {
				if result, changed, e := plan.Execute(context.Background(), store, service); e == nil || changed || !reflect.DeepEqual(result, protocol.CleanupResult{}) || len(calls) != 0 {
					t.Fatal("refused plan dispatched", result, e)
				}
			}
		})
	}
}

func TestExactExpiryNativeExecutionRetainsFinalAdmissionAndPrefix(t *testing.T) {
	for _, scenario := range []string{"unchanged", "guard-denied", "guard-cancelled", "candidate-drift", "first-error", "second-error"} {
		t.Run(scenario, func(t *testing.T) {
			store, root, done, tasks := expiryFixture(t)
			t.Setenv("RADAR_SOCKET", filepath.Join(root, "radar.sock"))
			t.Setenv("RADAR_AGENT_ROOT", filepath.Join(root, "agents"))
			now := done.Add(ExpiryRetention)
			options := Options{WorkspaceRoot: root}
			candidates, err := BuildPlan(store, now, options)
			if err != nil || len(candidates.Candidates) != 1 {
				t.Fatal(candidates, err)
			}
			candidate := candidates.Candidates[0]
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			guards, previews := 0, 0
			options.GuardExecution = func(_ context.Context, _ protocol.Task, execute func() error) error {
				guards++
				switch scenario {
				case "guard-denied":
					return errors.New("denied")
				case "guard-cancelled":
					cancel()
				case "candidate-drift":
					tasks[1].SourceRefs[0].WorkspaceID = "replacement"
					store.SetTasks(tasks)
				}
				return execute()
			}
			var calls []cleanupCall
			var modes []integration.CleanupMode
			providers := []integration.CleanupProvider{}
			for i, name := range []string{"tmux", "workspace"} {
				provider := candidateBoundaryProvider{gcProvider: gcProvider{name: name, calls: &calls}, previews: &previews, modes: &modes}
				if scenario == "first-error" && i == 0 || scenario == "second-error" && i == 1 {
					provider.failPath = candidate.Path
				}
				providers = append(providers, provider)
			}
			service := cleanup.New(providers)
			plan, err := PrepareExpiry(ctx, store, service, candidate, now, options)
			if err != nil {
				t.Fatal(err)
			}
			result, changed, err := plan.Execute(ctx, store, service)
			if changed || guards != 1 || previews != 2 {
				t.Fatal("guard/planner/runtime flag", guards, previews, changed)
			}
			if scenario == "guard-denied" || scenario == "guard-cancelled" || scenario == "candidate-drift" {
				if err == nil || len(calls) != 0 || len(result.Targets) != 0 || result.ExecutionObservation != nil {
					t.Fatal("refused candidate dispatched", result, err, calls)
				}
				return
			}
			wantCalls, wantPrefix, wantStage := 2, 2, "completed"
			if scenario == "first-error" {
				wantCalls, wantPrefix, wantStage = 1, 0, "provider_call"
			}
			if scenario == "second-error" {
				wantPrefix, wantStage = 1, "provider_call"
			}
			if (err == nil) != (scenario == "unchanged") || len(calls) != wantCalls || len(result.Targets) != wantPrefix || result.ExecutionObservation == nil || result.ExecutionObservation.AttemptedTargets != wantCalls || result.ExecutionObservation.Stage != wantStage {
				t.Fatal("native prefix/dispatch lost", result, err, calls)
			}
			for _, mode := range modes {
				if mode != integration.CleanupExpired {
					t.Fatal("mode substitution", modes)
				}
			}
		})
	}
}

func TestExactExpiryNativeMissingPathStillRequiresFinalAdmission(t *testing.T) {
	for _, scenario := range []string{"stable", "reappeared", "cancelled", "denied", "candidate-drift"} {
		t.Run(scenario, func(t *testing.T) {
			store, root, done, tasks := expiryFixture(t)
			options := Options{WorkspaceRoot: root}
			now := done.Add(ExpiryRetention)
			candidates, err := BuildPlan(store, now, options)
			if err != nil || len(candidates.Candidates) != 1 {
				t.Fatal(candidates, err)
			}
			candidate := candidates.Candidates[0]
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			guards := 0
			options.GuardExecution = func(_ context.Context, _ protocol.Task, execute func() error) error {
				guards++
				switch scenario {
				case "reappeared":
					if err := os.WriteFile(candidate.Path, []byte("preserve"), 0600); err != nil {
						t.Fatal(err)
					}
				case "cancelled":
					cancel()
				case "denied":
					return errors.New("denied")
				case "candidate-drift":
					tasks[1].SourceRefs[0].WorkspaceID = "replacement"
					store.SetTasks(tasks)
				}
				return execute()
			}
			service := cleanup.New(nil)
			plan, err := PrepareExpiry(ctx, store, service, candidate, now, options)
			if err != nil || guards != 0 || len(plan.Preview().Targets) != 0 {
				t.Fatal("missing-path preparation", err, guards, plan.Preview())
			}
			result, changed, err := plan.Execute(ctx, store, service)
			if guards != 1 || changed || !reflect.DeepEqual(result, protocol.CleanupResult{}) || (err == nil) != (scenario == "stable") {
				t.Fatal("missing-path admission bypass", result, changed, err, guards)
			}
			if scenario == "reappeared" {
				if body, e := os.ReadFile(candidate.Path); e != nil || string(body) != "preserve" {
					t.Fatal("reappeared path changed", e)
				}
			}
		})
	}
}

func TestExactExpiryNativePreservesExpiryWarningsAndStructuralBlockers(t *testing.T) {
	for _, scenario := range []string{"expiring-data", "structural", "provider-error", "preview-cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			store, root, done, _ := expiryFixture(t)
			options := Options{WorkspaceRoot: root}
			now := done.Add(ExpiryRetention)
			candidates, err := BuildPlan(store, now, options)
			if err != nil || len(candidates.Candidates) != 1 {
				t.Fatal(candidates, err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var calls []cleanupCall
			provider := expiryProvider{gcProvider: gcProvider{name: "workspace", calls: &calls}, hardBlock: scenario == "structural"}
			if scenario == "provider-error" {
				provider.previewErr = errors.New("unavailable")
			}
			if scenario == "preview-cancelled" {
				provider.previewHook = cancel
			}
			service := cleanup.New([]integration.CleanupProvider{expiryProvider{gcProvider: gcProvider{name: "git", calls: &calls, dirty: true, unpublished: true, publicationUnknown: true}}, provider})
			plan, err := PrepareExpiry(ctx, store, service, candidates.Candidates[0], now, options)
			if (err == nil) != (scenario == "expiring-data") || len(calls) != 0 {
				t.Fatal("expiry warnings/blockers changed", plan.Preview(), err, calls)
			}
			if scenario == "expiring-data" {
				if len(plan.Preview().Targets) != 2 || len(plan.Preview().Targets[0].Safety) != 3 {
					t.Fatal("warnings lost", plan.Preview())
				}
			}
		})
	}
}
