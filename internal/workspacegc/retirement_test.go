package workspacegc

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"radar/internal/cleanup"
	"radar/internal/integration"
	"radar/internal/protocol"
)

type retirementOwnedProvider struct {
	candidateBoundaryProvider
	observed *[]protocol.CleanupTarget
}

func (p retirementOwnedProvider) PreviewCleanup(ctx context.Context, req integration.CleanupPreviewRequest) ([]protocol.CleanupTarget, error) {
	targets, err := p.candidateBoundaryProvider.PreviewCleanup(ctx, req)
	for i := range targets {
		targets[i].Operation = map[string]string{"lifetime": "original"}
		targets[i].Safety = []protocol.CleanupSafety{{Kind: "notice", Summary: "original"}}
	}
	return targets, err
}
func (p retirementOwnedProvider) Cleanup(ctx context.Context, req integration.CleanupRequest) (protocol.CleanupTarget, error) {
	*p.observed = append(*p.observed, req.Target)
	return p.candidateBoundaryProvider.Cleanup(ctx, req)
}

func TestExactRetirementNativePlanOwnsOneSelectedBundle(t *testing.T) {
	for _, id := range []string{"registered", "second", "third"} {
		for _, mutate := range []bool{false, true} {
			t.Run(id+map[bool]string{false: "/stable", true: "/caller-mutates"}[mutate], func(t *testing.T) {
				store, root, done := candidateBoundaryFixture(t)
				now := done.Add(25 * time.Hour)
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
				var calls []cleanupCall
				var modes []integration.CleanupMode
				var observed []protocol.CleanupTarget
				options.GuardExecution = func(_ context.Context, task protocol.Task, execute func() error) error {
					guards++
					if task.ID != candidate.TaskID {
						t.Fatal("wrong lifecycle task")
					}
					return execute()
				}
				providers := []integration.CleanupProvider{}
				for _, name := range []string{"tmux", "sbx", "git", "workspace"} {
					providers = append(providers, retirementOwnedProvider{candidateBoundaryProvider: candidateBoundaryProvider{gcProvider: gcProvider{name: name, calls: &calls}, previews: &previews, modes: &modes}, observed: &observed})
				}
				service := cleanup.New(providers)
				plan, err := PrepareRetirement(context.Background(), store, service, candidate, now, options)
				if err != nil || guards != 0 || len(calls) != 0 || previews != 4 {
					t.Fatalf("preparation dispatched/swept: %v guards=%d previews=%d calls=%v", err, guards, previews, calls)
				}
				before := plan.Preview()
				if len(before.Targets) != 3 || before.TaskID != candidate.TaskID {
					t.Fatal(before)
				}
				for i, name := range []string{"tmux", "sbx", "workspace"} {
					if before.Targets[i].Source != name || before.Targets[i].Path != candidate.Path {
						t.Fatal("wrong selected bundle", before)
					}
				}
				if mutate {
					// Neither a returned disclosure nor the original caller's native task
					// and options can rewrite a retained executable plan or remove its guard.
					copy := plan.Preview()
					copy.TaskID = 999
					copy.Targets[0].Operation["lifetime"] = "replacement"
					copy.Targets[0].Safety[0].Summary = "replacement"
					copy.Targets[0].Path = filepath.Join(root, "other")
					copy.Targets[1] = protocol.CleanupTarget{}
					for i := range candidate.Task.SourceRefs {
						if candidate.Task.SourceRefs[i].Metadata != nil {
							candidate.Task.SourceRefs[i].Metadata["completed_at"] = "replacement"
						}
					}
					options.GuardExecution = nil
					options.WorkspaceRoot = filepath.Join(root, "wrong")
					now = done.Add(ExpiryRetention)
				}
				if !reflect.DeepEqual(before, plan.Preview()) {
					t.Fatal("public copy rewrote retained preview")
				}
				result, changed, err := plan.Execute(context.Background(), store, service)
				if err != nil || !changed || guards != 1 || previews != 4 || len(result.Targets) != 3 || len(calls) != 3 || len(observed) != 3 {
					t.Fatalf("exact execution: %+v changed=%v err=%v calls=%v guards=%d previews=%d", result, changed, err, calls, guards, previews)
				}
				for i, name := range []string{"tmux", "sbx", "workspace"} {
					if calls[i].source != name || calls[i].path != before.Targets[i].Path || modes[i] != integration.CleanupRetire || observed[i].Operation["lifetime"] != "original" || observed[i].Safety[0].Summary != "original" {
						t.Fatal("scope/order/mode/ownership lost", calls, modes, observed)
					}
				}
				if !reflect.DeepEqual(before, plan.Preview()) {
					t.Fatal("provider return changed immutable disclosure")
				}
			})
		}
	}
}

func TestExactRetirementNativePreparationRejectsNonRetirement(t *testing.T) {
	for _, scenario := range []string{"before-day-one", "day-one", "before-expiry", "expiry", "standalone", "wrong-path", "wrong-task", "changed-private-ref", "cancelled", "no-root"} {
		t.Run(scenario, func(t *testing.T) {
			store, root, done, _ := expiryFixture(t)
			options := Options{WorkspaceRoot: root}
			now := done.Add(25 * time.Hour)
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
			case "expiry":
				now = done.Add(ExpiryRetention)
			case "standalone":
				candidate.WorkspaceID = ""
			case "wrong-path":
				candidate.Path = filepath.Join(root, "other")
			case "wrong-task":
				candidate.TaskID++
			case "changed-private-ref":
				candidate.Task.SourceRefs[0].ID += "replacement"
			case "cancelled":
				cancel()
			case "no-root":
				options.WorkspaceRoot = ""
			}
			previews := 0
			var calls []cleanupCall
			var modes []integration.CleanupMode
			provider := candidateBoundaryProvider{gcProvider: gcProvider{name: "workspace", calls: &calls}, previews: &previews, modes: &modes}
			service := cleanup.New([]integration.CleanupProvider{provider})
			plan, err := PrepareRetirement(ctx, store, service, candidate, now, options)
			allowed := scenario == "day-one" || scenario == "before-expiry"
			if (err == nil) != allowed || len(calls) != 0 || previews != map[bool]int{true: 1, false: 0}[allowed] {
				t.Fatalf("stage/identity gate changed: %+v %v previews=%d", plan.Preview(), err, previews)
			}
			if !allowed {
				if result, changed, e := plan.Execute(context.Background(), store, service); e == nil || changed || !reflect.DeepEqual(result, protocol.CleanupResult{}) || len(calls) != 0 {
					t.Fatal("zero/refused plan dispatched", result, e)
				}
			}
		})
	}
}

func TestExactRetirementNativeExecutionKeepsGuardDriftCancellationAndPrefix(t *testing.T) {
	for _, scenario := range []string{"unchanged", "guard-denied", "guard-cancelled", "candidate-drift", "first-error", "second-error"} {
		t.Run(scenario, func(t *testing.T) {
			store, root, done, tasks := expiryFixture(t)
			t.Setenv("RADAR_SOCKET", filepath.Join(root, "radar.sock"))
			t.Setenv("RADAR_AGENT_ROOT", filepath.Join(root, "agents"))
			options := Options{WorkspaceRoot: root}
			now := done.Add(25 * time.Hour)
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
			plan, err := PrepareRetirement(ctx, store, service, candidate, now, options)
			if err != nil {
				t.Fatal(err)
			}
			result, _, err := plan.Execute(ctx, store, service)
			if guards != 1 || previews != 2 {
				t.Fatal("guard/planner count", guards, previews)
			}
			blocked := scenario == "guard-denied" || scenario == "guard-cancelled" || scenario == "candidate-drift"
			if blocked {
				if err == nil || len(calls) != 0 || len(result.Targets) != 0 || result.ExecutionObservation != nil {
					t.Fatal("refused plan dispatched", result, err, calls)
				}
				return
			}
			wantCalls, wantPrefix := 2, 2
			if scenario == "first-error" {
				wantCalls, wantPrefix = 1, 0
			}
			if scenario == "second-error" {
				wantPrefix = 1
			}
			if (err == nil) != (scenario == "unchanged") || len(calls) != wantCalls || len(result.Targets) != wantPrefix || result.ExecutionObservation == nil || result.ExecutionObservation.AttemptedTargets != wantCalls {
				t.Fatal("native prefix lost", result, err, calls)
			}
			for _, mode := range modes {
				if mode != integration.CleanupRetire {
					t.Fatal("mode escalation", modes)
				}
			}
		})
	}
}
