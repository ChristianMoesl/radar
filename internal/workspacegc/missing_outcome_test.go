package workspacegc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"radar/internal/cleanup"
	"radar/internal/integration"
	"radar/internal/linking"
	"radar/internal/protocol"
)

// A provider may finish a slow preview without any targets for this path.
// Neither that result nor an early Lstat can bypass final lifecycle admission.
type missingGCProvider struct {
	hook    func()
	targets []protocol.CleanupTarget
	calls   *int
}

func (missingGCProvider) Descriptor() integration.Descriptor {
	return integration.Descriptor{Name: "git"}
}
func (p missingGCProvider) PreviewCleanup(context.Context, integration.CleanupPreviewRequest) ([]protocol.CleanupTarget, error) {
	if p.hook != nil {
		p.hook()
	}
	return p.targets, nil
}
func (p missingGCProvider) Cleanup(_ context.Context, req integration.CleanupRequest) (protocol.CleanupTarget, error) {
	(*p.calls)++
	return req.Target, nil
}

func TestGCAlreadyMissingUsesFinalCandidateGuard(t *testing.T) {
	for _, shape := range []string{"no-resources", "unselected-resources"} {
		for _, boundary := range []string{"preview", "guard"} {
			for _, change := range []string{"stable", "unchanged-publication", "unrelated-publication", "reopen", "recomplete", "removed-anchor", "moved-anchor", "added-runtime", "busy", "review", "cancel", "denied"} {
				if change == "denied" && boundary != "guard" {
					continue
				}
				t.Run(shape+"/"+boundary+"/"+change, func(t *testing.T) {
					store := testStore(t)
					root := t.TempDir()
					path := filepath.Join(root, "missing")
					tasks := []protocol.Task{
						makeTask("done", "merged", githubRef("github:pr:acme/app:7", "acme/app", "work")),
						makeTask("in_progress", "worktree", worktreeRef(path, "acme/app", "work")),
					}
					// Anchor provider completion across republication. Without it, the
					// separate in-progress worktree observation clears the fallback
					// DoneAt and lifecycle derivation supplies a new wall-clock second.
					tasks[0].SourceRefs[0].Metadata = map[string]string{"completed_at": "2026-01-02T10:20:30Z"}
					store.SetTasks(tasks)
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					calls, guarded, mutated := 0, 0, false
					mutate := func() {
						if mutated {
							t.Fatal("duplicate mutation")
						}
						mutated = true
						switch change {
						case "stable", "denied":
							return
						case "unchanged-publication":
						case "unrelated-publication":
							ref := githubRef("github:pr:acme/other:8", "acme/other", "unrelated")
							ref.LinkingKeys = []string{ref.ID}
							tasks = append(tasks, makeTask("in_progress", "unrelated", ref))
						case "reopen", "recomplete":
							tasks[0].Attention, tasks[0].SourceRefs[0].Signal = "in_progress", "in_progress"
							store.SetTasks(tasks)
							if change == "reopen" {
								return
							}
							tasks[0].Attention, tasks[0].SourceRefs[0].Signal = "done", "done"
							tasks[0].SourceRefs[0].Metadata = map[string]string{"completed_at": time.Now().Add(time.Hour).Format(time.RFC3339)}
						case "removed-anchor":
							tasks = tasks[:1]
						case "moved-anchor":
							ref := &tasks[1].SourceRefs[0]
							ref.Path = filepath.Join(root, "replacement")
							ref.CanonicalKey = linking.WorkspaceKey(ref.Path)
						case "added-runtime":
							tasks = append(tasks, makeTask("in_progress", "runtime", detachedSessionRef(path, "additional")))
						case "busy":
							tasks[1].SourceRefs[0].Activity = protocol.ActivityBusy
						case "review":
							tasks[1].SourceRefs[0].AgentReview = true
						case "cancel":
							cancel()
							return
						default:
							t.Fatal(change)
						}
						store.SetTasks(tasks)
					}
					provider := missingGCProvider{calls: &calls}
					if shape == "unselected-resources" {
						provider.targets = []protocol.CleanupTarget{{Source: "git", Path: filepath.Join(root, "elsewhere"), ProvidesWorkspace: true}}
					}
					if boundary == "preview" {
						provider.hook = mutate
					}
					denied := errors.New("authored cleanup admission denied")
					options := Options{WorkspaceRoot: root, IgnoreRetention: true, GuardExecution: func(_ context.Context, _ protocol.Task, execute func() error) error {
						guarded++
						if boundary == "guard" {
							mutate()
						}
						if change == "denied" {
							return denied
						}
						return execute()
					}}
					result, err := Run(ctx, store, cleanup.New([]integration.CleanupProvider{provider}), nil, time.Now(), options)
					allow := change == "stable" || change == "unchanged-publication" || change == "unrelated-publication"
					if err != nil || calls != 0 || !mutated || guarded != 1 || len(result.Retired) != 0 {
						t.Fatalf("missing-path bypassed final guard: result=%+v err=%v calls=%d guarded=%d mutated=%v", result, err, calls, guarded, mutated)
					}
					if allow {
						if len(result.Deleted) != 1 || len(result.Skipped) != 0 {
							t.Fatalf("stable absence rejected: %+v", result)
						}
						return
					}
					want := "changed"
					if change == "cancel" {
						want = context.Canceled.Error()
					}
					if change == "denied" {
						want = denied.Error()
					}
					if len(result.Deleted) != 0 || len(result.Skipped) != 1 || !strings.Contains(result.Skipped[0].Reason, want) {
						t.Fatalf("stale or denied absence counted as deleted: %+v want=%q", result, want)
					}
				})
			}
		}
	}
}

func TestGCRegisteredMissingRetainsStageBoundary(t *testing.T) {
	for _, stage := range []string{"retire", "expire"} {
		for _, change := range []string{"stable", "cancel", "denied", "reopen"} {
			t.Run(stage+"/"+change, func(t *testing.T) {
				store, root, done, tasks := expiryFixture(t)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				calls, guarded := 0, 0
				now := done.Add(25 * time.Hour)
				if stage == "expire" {
					now = done.Add(ExpiryRetention)
				}
				provider := missingGCProvider{calls: &calls}
				result, err := Run(ctx, store, cleanup.New([]integration.CleanupProvider{provider}), nil, now, Options{WorkspaceRoot: root, GuardExecution: func(_ context.Context, _ protocol.Task, execute func() error) error {
					guarded++
					switch change {
					case "cancel":
						cancel()
					case "denied":
						return errors.New("cleanup admission denied")
					case "reopen":
						tasks[0].Attention, tasks[0].SourceRefs[0].Signal = "in_progress", "in_progress"
						store.SetTasks(tasks)
					}
					return execute()
				}})
				allow := stage == "expire" && change == "stable"
				if err != nil || calls != 0 || len(result.Retired) != 0 || (len(result.Deleted) == 1) != allow || (len(result.Skipped) == 0) != allow {
					t.Fatalf("registered absence stage changed: %+v err=%v calls=%d", result, err, calls)
				}
				wantGuard := 0
				if stage == "expire" {
					wantGuard = 1
				}
				if guarded != wantGuard {
					t.Fatalf("guard calls=%d want=%d", guarded, wantGuard)
				}
			})
		}
	}
}

func TestGCAlreadyMissingRechecksPathInsideGuard(t *testing.T) {
	for _, shape := range []string{"no-resources", "unselected-resources"} {
		for _, entry := range []string{"file", "directory", "dangling-symlink", "unavailable-parent"} {
			t.Run(shape+"/"+entry, func(t *testing.T) {
				store := testStore(t)
				root := t.TempDir()
				parent := filepath.Join(root, "parent")
				if err := os.Mkdir(parent, 0o700); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(parent, "missing")
				store.SetTasks([]protocol.Task{
					makeTask("done", "merged", githubRef("github:pr:acme/app:7", "acme/app", "work")),
					makeTask("in_progress", "worktree", worktreeRef(path, "acme/app", "work")),
				})
				calls, guarded := 0, 0
				provider := missingGCProvider{calls: &calls}
				if shape == "unselected-resources" {
					provider.targets = []protocol.CleanupTarget{{Source: "git", Path: filepath.Join(root, "elsewhere")}}
				}
				result, err := Run(context.Background(), store, cleanup.New([]integration.CleanupProvider{provider}), nil, time.Now(), Options{WorkspaceRoot: root, IgnoreRetention: true, GuardExecution: func(_ context.Context, _ protocol.Task, execute func() error) error {
					guarded++
					switch entry {
					case "file":
						if err := os.WriteFile(path, []byte("preserve"), 0o600); err != nil {
							t.Fatal(err)
						}
					case "directory":
						if err := os.Mkdir(path, 0o700); err != nil {
							t.Fatal(err)
						}
					case "dangling-symlink":
						if err := os.Symlink(filepath.Join(root, "absent"), path); err != nil {
							t.Fatal(err)
						}
					case "unavailable-parent":
						if err := os.Remove(parent); err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(parent, []byte("not a directory"), 0o600); err != nil {
							t.Fatal(err)
						}
					}
					return execute()
				}})
				if err != nil || calls != 0 || guarded != 1 || len(result.Deleted) != 0 || len(result.Retired) != 0 || len(result.Skipped) != 1 || !strings.Contains(result.Skipped[0].Reason, "no longer missing") {
					t.Fatalf("unobserved final absence counted: result=%+v err=%v calls=%d guarded=%d", result, err, calls, guarded)
				}
				preserved := path
				if entry == "unavailable-parent" {
					preserved = parent
				}
				if _, err := os.Lstat(preserved); err != nil {
					t.Fatalf("replacement entry was removed: %v", err)
				}
			})
		}
	}
}
