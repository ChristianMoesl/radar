package workspacegc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"radar/internal/cleanup"
	"radar/internal/integration"
	"radar/internal/protocol"
	"radar/internal/state"
)

// Two registered workspaces belong to one task; a third belongs to an unrelated
// task. Providers intentionally preview the WHOLE owning task, as in production.
func candidateBoundaryFixture(t *testing.T) (*state.Store, string, time.Time) {
	t.Helper()
	store, root, done, tasks := expiryFixture(t)
	t.Setenv("RADAR_SOCKET", filepath.Join(root, "radar.sock"))
	t.Setenv("RADAR_AGENT_ROOT", filepath.Join(root, "agents"))
	second := filepath.Join(root, "second")
	tasks = append(tasks, makeTask("in_progress", "workspace", workspaceAnchorRef(second, "second")), makeTask("in_progress", "runtime", detachedSessionRef(second, "second-session")), makeTask("in_progress", "runtime", sandboxRef(second, "second-sandbox")))
	tasks = append(tasks, makeTask("in_progress", "runtime", sandboxRef(tasks[1].SourceRefs[0].Path, "first-sandbox")))
	third := filepath.Join(root, "third")
	author := githubRef("github:pr:acme/other:8", "acme/other", "other")
	author.Authority = protocol.SourceRefAuthorityPrimary
	author.Metadata = map[string]string{"completed_at": done.Format(time.RFC3339)}
	for _, ref := range []protocol.SourceRef{author, workspaceAnchorRef(third, "third"), detachedSessionRef(third, "third-session"), sandboxRef(third, "third-sandbox")} {
		ref.LinkingKeys = append([]string(nil), ref.LinkingKeys...)
		for i, key := range ref.LinkingKeys {
			if key == "mark:ABC-7" {
				ref.LinkingKeys[i] = "mark:ABC-8"
			}
		}
		tasks = append(tasks, makeTask(map[bool]string{true: "done", false: "in_progress"}[ref.Source == "github"], "unrelated", ref))
	}
	store.SetTasks(tasks)
	return store, root, done
}

type candidateBoundaryProvider struct {
	gcProvider
	previews *int
	modes    *[]integration.CleanupMode
}

func (p candidateBoundaryProvider) PreviewCleanup(ctx context.Context, req integration.CleanupPreviewRequest) ([]protocol.CleanupTarget, error) {
	(*p.previews)++
	return p.gcProvider.PreviewCleanup(ctx, req)
}
func (p candidateBoundaryProvider) Cleanup(ctx context.Context, req integration.CleanupRequest) (protocol.CleanupTarget, error) {
	*p.modes = append(*p.modes, req.Mode)
	return p.gcProvider.Cleanup(ctx, req)
}

func TestCandidateCleanupBoundarySelectsOneBundleWithoutSweep(t *testing.T) {
	for _, stage := range []string{"retire", "expire"} {
		for _, id := range []string{"registered", "second", "third"} {
			t.Run(stage+"/"+id, func(t *testing.T) {
				store, root, done := candidateBoundaryFixture(t)
				now := done.Add(25 * time.Hour)
				wantMode := integration.CleanupRetire
				if stage == "expire" {
					now = done.Add(ExpiryRetention)
					wantMode = integration.CleanupExpired
				}
				options := Options{WorkspaceRoot: root}
				plan, err := BuildPlan(store, now, options)
				if err != nil || len(plan.Candidates) != 3 {
					t.Fatalf("fixture: %+v %v", plan, err)
				}
				first, second, third := 0, 0, 0
				var selected Candidate
				for _, c := range plan.Candidates {
					switch c.WorkspaceID {
					case "registered":
						first = c.TaskID
					case "second":
						second = c.TaskID
					case "third":
						third = c.TaskID
					}
					if c.WorkspaceID == id {
						selected = c
					}
				}
				if first == 0 || first != second || third == 0 || third == first || selected.WorkspaceID != id {
					t.Fatal("fixture does not isolate two workspaces on one task plus unrelated task")
				}
				var calls []cleanupCall
				var modes []integration.CleanupMode
				previews := 0
				guards := 0
				options.GuardExecution = func(_ context.Context, task protocol.Task, execute func() error) error {
					guards++
					if task.ID != selected.TaskID {
						t.Fatal("wrong lifecycle task")
					}
					return execute()
				}
				providers := []integration.CleanupProvider{}
				for _, name := range []string{"tmux", "sbx", "workspace"} {
					providers = append(providers, candidateBoundaryProvider{gcProvider: gcProvider{name: name, calls: &calls}, previews: &previews, modes: &modes})
				}
				service := cleanup.New(providers)
				prepared, err := prepareCandidateCleanup(context.Background(), service, selected, now, options)
				if err != nil || guards != 0 || len(calls) != 0 || previews != 3 || prepared.mode != wantMode || prepared.alreadyMissing || len(prepared.selected.Targets) != 3 {
					t.Fatalf("preparation ran execution or selected an incomplete bundle: %+v %v", prepared, err)
				}
				for _, target := range prepared.selected.Targets {
					if target.Path != selected.Path {
						t.Fatal("preparation included another workspace", target)
					}
				}
				result, _, err := prepared.execute(context.Background(), store, service)
				if err != nil || guards != 1 || previews != 3 || len(result.Targets) != 3 || len(calls) != 3 {
					t.Fatalf("execution swept or replanned: %+v %v calls=%+v previews=%d guards=%d", result, err, calls, previews, guards)
				}
				for i, name := range []string{"tmux", "sbx", "workspace"} {
					if calls[i].source != name || calls[i].path != selected.Path || modes[i] != wantMode {
						t.Fatalf("wrong target order/scope/mode: %+v %+v", calls, modes)
					}
				}
			})
		}
	}
}

func TestCandidateCleanupBoundaryRetainsPolicyTimeAndPartialPrefix(t *testing.T) {
	for _, scenario := range []string{"stable", "caller-policy-time-change", "guard-denied", "guard-cancelled", "candidate-drift", "first-provider-error", "second-provider-error"} {
		t.Run(scenario, func(t *testing.T) {
			store, root, done, tasks := expiryFixture(t)
			t.Setenv("RADAR_SOCKET", filepath.Join(root, "radar.sock"))
			t.Setenv("RADAR_AGENT_ROOT", filepath.Join(root, "agents"))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var calls []cleanupCall
			var modes []integration.CleanupMode
			previews, guards := 0, 0
			options := Options{WorkspaceRoot: root}
			options.GuardExecution = func(_ context.Context, _ protocol.Task, execute func() error) error {
				guards++
				switch scenario {
				case "guard-denied":
					return errors.New("authored admission denied")
				case "guard-cancelled":
					cancel()
				case "candidate-drift":
					tasks[1].SourceRefs[0].WorkspaceID = "replacement"
					store.SetTasks(tasks)
				}
				return execute()
			}
			now := done.Add(25 * time.Hour)
			candidates, err := BuildPlan(store, now, options)
			if err != nil || len(candidates.Candidates) != 1 {
				t.Fatal(candidates, err)
			}
			candidate := candidates.Candidates[0]
			providers := []integration.CleanupProvider{}
			for i, name := range []string{"tmux", "workspace"} {
				provider := candidateBoundaryProvider{gcProvider: gcProvider{name: name, calls: &calls}, previews: &previews, modes: &modes}
				if scenario == "first-provider-error" && i == 0 || scenario == "second-provider-error" && i == 1 {
					provider.failPath = candidate.Path
				}
				providers = append(providers, provider)
			}
			service := cleanup.New(providers)
			prepared, err := prepareCandidateCleanup(ctx, service, candidate, now, options)
			if err != nil {
				t.Fatal(err)
			}
			// Execution must retain the original options/mode/time by value. No caller
			// can escalate an old retirement plan into expiry or remove its guard.
			now = done.Add(10 * 24 * time.Hour)
			options.Retention = 48 * time.Hour
			options.WorkspaceRoot = filepath.Join(root, "elsewhere")
			options.GuardExecution = func(context.Context, protocol.Task, func() error) error {
				t.Fatal("replacement guard invoked")
				return nil
			}
			if now.Before(done.Add(ExpiryRetention)) {
				t.Fatal("fixture clock did not cross expiry")
			}
			result, _, err := prepared.execute(ctx, store, service)
			if guards != 1 || previews != 2 {
				t.Fatalf("original guard or planner count changed: %d %d", guards, previews)
			}
			blocked := scenario == "guard-denied" || scenario == "guard-cancelled" || scenario == "candidate-drift"
			partial := scenario == "first-provider-error" || scenario == "second-provider-error"
			if blocked {
				if err == nil || len(calls) != 0 || len(result.Targets) != 0 || result.ExecutionObservation != nil {
					t.Fatalf("rejected candidate dispatched: %+v %v %+v", result, err, calls)
				}
				return
			}
			wantCalls, wantPrefix := 2, 2
			if scenario == "first-provider-error" {
				wantCalls, wantPrefix = 1, 0
			}
			if scenario == "second-provider-error" {
				wantPrefix = 1
			}
			if (err != nil) != partial || len(calls) != wantCalls || len(result.Targets) != wantPrefix || result.ExecutionObservation == nil || result.ExecutionObservation.AttemptedTargets != wantCalls {
				t.Fatalf("native prefix/attempt evidence lost: %+v %v calls=%+v", result, err, calls)
			}
			for _, mode := range modes {
				if mode != integration.CleanupRetire {
					t.Fatal("retirement escalated", mode)
				}
			}
			wantStage := "completed"
			if partial {
				wantStage = "provider_call"
			}
			if result.ExecutionObservation.Stage != wantStage {
				t.Fatal(result.ExecutionObservation)
			}
		})
	}
}

func TestCandidateCleanupBoundaryMissingPathStillRequiresGuard(t *testing.T) {
	for _, scenario := range []string{"stable", "reappeared", "cancelled", "denied"} {
		t.Run(scenario, func(t *testing.T) {
			store, root, done, _ := expiryFixture(t)
			options := Options{WorkspaceRoot: root}
			guards := 0
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			plan, err := BuildPlan(store, done.Add(ExpiryRetention), options)
			if err != nil || len(plan.Candidates) != 1 {
				t.Fatal(plan, err)
			}
			candidate := plan.Candidates[0]
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
				}
				return execute()
			}
			prepared, err := prepareCandidateCleanup(ctx, cleanup.New(nil), candidate, done.Add(ExpiryRetention), options)
			if err != nil || !prepared.alreadyMissing {
				t.Fatalf("missing preparation: %+v %v", prepared, err)
			}
			result, _, err := prepared.execute(ctx, store, cleanup.New(nil))
			if guards != 1 || !reflect.DeepEqual(result, protocol.CleanupResult{}) || (err == nil) != (scenario == "stable") {
				t.Fatalf("missing path bypassed admission: %+v %v guards=%d", result, err, guards)
			}
			if scenario == "reappeared" && !strings.Contains(err.Error(), "no longer missing") {
				t.Fatal(err)
			}
			if scenario == "reappeared" {
				if body, e := os.ReadFile(candidate.Path); e != nil || string(body) != "preserve" {
					t.Fatal("replacement path changed")
				}
			}
		})
	}
}
