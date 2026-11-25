package workspacegc

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"radar/internal/cleanup"
	"radar/internal/integration"
	"radar/internal/linking"
	"radar/internal/protocol"
)

// All providers return the original preview even after collection changes the
// cached task. Native provider guards cannot establish that old task/ref scope.
func TestGCRevalidatesSelectedCandidateAfterPreview(t *testing.T) {
	for _, stage := range []string{"retire", "expire", "standalone"} {
		for _, boundary := range []string{"preview", "execution-guard"} {
			for _, change := range []string{"removed-anchor", "moved-anchor", "changed-workspace", "removed-member", "changed-runtime", "added-runtime", "busy", "review", "unchanged-publication", "unrelated-publication"} {
				t.Run(stage+"/"+boundary+"/"+change, func(t *testing.T) {
					store, root, done, tasks := expiryFixture(t)
					t.Setenv("RADAR_SOCKET", filepath.Join(root, "radar.sock"))
					t.Setenv("RADAR_AGENT_ROOT", filepath.Join(root, "agents"))
					options := Options{WorkspaceRoot: root}
					now := done.Add(25 * time.Hour)
					if stage == "expire" {
						now = done.Add(ExpiryRetention)
					}
					if stage == "standalone" {
						// A standalone owning worktree and its resources still need fresh task
						// membership even when an explicitly requested GC bypasses retention.
						tasks[1] = makeTask("in_progress", "workspace", worktreeRef(filepath.Join(root, "standalone"), "acme/app", "work"))
						tasks[2].SourceRefs[0].WorkspaceID = ""
						options.IgnoreRetention = true
						store.SetTasks(tasks)
					}
					before, err := BuildPlan(store, now, options)
					if err != nil || len(before.Candidates) != 1 {
						t.Fatalf("initial plan: %+v %v", before, err)
					}
					original := before.Candidates[0]
					changed := false
					mutate := func() {
						if changed {
							t.Fatal("duplicate mutation")
						}
						changed = true
						switch change {
						case "removed-anchor":
							tasks = append(tasks[:1], tasks[2:]...)
						case "moved-anchor":
							ref := &tasks[1].SourceRefs[0]
							ref.Path = filepath.Join(root, "replacement")
							ref.CanonicalKey = linking.WorkspaceKey(ref.Path)
							ref.LinkingKeys = linking.Keys("mark:ABC-7", linking.WorkspaceKey(ref.Path))
						case "changed-workspace":
							tasks[1].SourceRefs[0].WorkspaceID = "replacement"
						case "removed-member":
							tasks = append(tasks[:2], tasks[3:]...)
						case "changed-runtime":
							ref := &tasks[3].SourceRefs[0]
							ref.ID, ref.EntityID = "tmux:session:replacement", "tmux:session:replacement"
							ref.Metadata = map[string]string{"session": "replacement", "attached_count": "1"}
						case "added-runtime":
							tasks = append(tasks, makeTask("in_progress", "runtime", sandboxRef(original.Path, "additional")))
						case "busy":
							tasks[2].SourceRefs[0].Activity = protocol.ActivityBusy
						case "review":
							tasks[2].SourceRefs[0].AgentReview = true
						case "unchanged-publication":
						case "unrelated-publication":
							ref := protocol.SourceRef{ID: "github:other", EntityID: "github:other", Source: "github", Kind: "pull_request", Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleWorkItem, CanonicalKey: "github:other"}
							tasks = append(tasks, makeTask("in_progress", "unrelated", ref))
						default:
							t.Fatal(change)
						}
						store.SetTasks(tasks)
						// Regressions must not merely repeat the existing completion check.
						found := false
						for _, record := range store.Records() {
							if record.ID == original.RecordID {
								found = record.State == "done" && record.DoneAt == original.DoneAt
							}
						}
						if !found && change != "busy" && change != "review" {
							t.Fatal("fixture changed completion instead of candidate membership")
						}
					}
					var calls []cleanupCall
					provider := expiryProvider{gcProvider: gcProvider{name: "workspace", calls: &calls}}
					if boundary == "preview" {
						provider.previewHook = mutate
					} else {
						options.GuardExecution = func(_ context.Context, _ protocol.Task, execute func() error) error { mutate(); return execute() }
					}
					providers := []integration.CleanupProvider{gcProvider{name: "tmux", calls: &calls}, gcProvider{name: "git", calls: &calls}, provider}
					result, err := Run(context.Background(), store, cleanup.New(providers), nil, now, options)
					allow := change == "unchanged-publication" || change == "unrelated-publication"
					if err != nil || !changed {
						t.Fatalf("run: %+v changed=%v err=%v", result, changed, err)
					}
					if !allow {
						if len(calls) != 0 || len(result.Retired) != 0 || len(result.Deleted) != 0 || len(result.Skipped) != 1 || !strings.Contains(result.Skipped[0].Reason, "changed") {
							t.Fatalf("stale candidate dispatched: calls=%+v result=%+v", calls, result)
						}
						return
					}
					wantCalls := 3
					if stage == "retire" {
						wantCalls = 2
					}
					if stage == "standalone" {
						wantCalls = 1
					}
					if len(calls) != wantCalls || len(result.Skipped) != 0 || len(result.Retired)+len(result.Deleted) != 1 {
						t.Fatalf("stable selected candidate blocked: calls=%+v result=%+v", calls, result)
					}
				})
			}
		}
	}
}

func TestGCSelectedCandidateUsesOriginalPolicyAndImmutableEvidence(t *testing.T) {
	store, root, done, _ := expiryFixture(t)
	now := done.Add(25 * time.Hour)
	options := Options{WorkspaceRoot: root}
	plan, err := BuildPlan(store, now, options)
	if err != nil || len(plan.Candidates) != 1 {
		t.Fatal(plan, err)
	}
	candidate := plan.Candidates[0]
	if !candidateStillCurrent(store, candidate, now, options) {
		t.Fatal("stable candidate rejected")
	}
	for _, field := range []string{"task", "record", "done", "path", "workspace", "reference"} {
		t.Run(field, func(t *testing.T) {
			// Rebuild to avoid mutating any other control through task slices/maps.
			current, err := BuildPlan(store, now, options)
			if err != nil {
				t.Fatal(err)
			}
			changed := current.Candidates[0]
			switch field {
			case "task":
				changed.TaskID++
			case "record":
				changed.RecordID = "other"
			case "done":
				changed.DoneAt = done.Add(-time.Hour).Format(time.RFC3339)
			case "path":
				changed.Path = filepath.Join(root, "other")
			case "workspace":
				changed.WorkspaceID = "other"
			case "reference":
				changed.Task.SourceRefs[0].Metadata = map[string]string{"private": "changed"}
			}
			if candidateStillCurrent(store, changed, now, options) {
				t.Fatal("changed identity accepted", field)
			}
		})
	}
	for _, options := range []Options{{}, {WorkspaceRoot: filepath.Join(root, "elsewhere")}, {WorkspaceRoot: root, Retention: 48 * time.Hour}} {
		if candidateStillCurrent(store, candidate, now, options) {
			t.Fatalf("changed planner policy accepted: %s", fmt.Sprint(options.WorkspaceRoot, options.Retention))
		}
	}
	// Candidate capture owns its nested reference data, not mutable store aliases.
	candidate.Task.SourceRefs[0].Metadata = map[string]string{"private": "caller mutation"}
	fresh, err := BuildPlan(store, now, Options{WorkspaceRoot: root})
	if err != nil || len(fresh.Candidates) != 1 || fresh.Candidates[0].Task.SourceRefs[0].Metadata["private"] != "" {
		t.Fatal("candidate mutated retained state", err)
	}
}
