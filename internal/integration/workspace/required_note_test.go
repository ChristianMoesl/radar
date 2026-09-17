package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"radar/internal/config"
	"radar/internal/integration"
	"radar/internal/integration/obsidian"
	"radar/internal/integration/workspace/group"
	"radar/internal/protocol"
)

func TestCreateAutomaticallyPreparesAndPersistsCanonicalNote(t *testing.T) {
	ctx := context.Background()
	root := configureWorkspaceRoot(t)
	author := testNoteAuthor(t)
	options := CreateOptions{Name: "Plan: next steps", WorkspaceRoot: root, TaskLinkingKey: "jira:issue:ABC-123", NoteAuthor: author}
	runner := &fakeRunner{}
	plan, _, err := planCreate(ctx, runner, options)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Note == nil || !plan.Note.Create {
		t.Fatal("preview did not prepare note")
	}
	if _, err := os.Stat(plan.Note.Path); !os.IsNotExist(err) {
		t.Fatal("preview created note")
	}
	options.Note, options.ExpectedPlanID = plan.Note, plan.PlanID
	created, err := Create(ctx, runner, options)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := workspacegroup.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	group := registry.Workspaces[0]
	if group.NoteKey() != plan.Note.LinkingKey || group.TaskLinkingKey != options.TaskLinkingKey {
		t.Fatalf("identity changed: %+v", group)
	}
	if target, err := os.Readlink(filepath.Join(created.Path, "notes.md")); err != nil || target != plan.Note.Path {
		t.Fatalf("notes.md = %q: %v", target, err)
	}
	data, err := os.ReadFile(plan.Note.Path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), "---\n") != 2 || !strings.HasSuffix(string(data), "---\n") {
		t.Fatalf("note has a nonempty body: %s", data)
	}
	// Opening and cleaning up resources never changes lifecycle or deletes the note.
	if _, err := OpenRegisteredWorkspace(ctx, runner, created.Path, false); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(plan.Note.Path)
	if err != nil || string(data) != string(after) {
		t.Fatal("opening changed note")
	}
	if _, err := removeWorkspaceAnchor(root, protocol.CleanupTarget{SourceRefID: "workspace:" + group.ID}); err != nil {
		t.Fatal(err)
	}
	after, err = os.ReadFile(plan.Note.Path)
	if err != nil || string(data) != string(after) {
		t.Fatal("cleanup changed or deleted canonical note")
	}
}

type failingCreationRunner struct {
	*fakeRunner
	fail  bool
	phase string
}

func (r *failingCreationRunner) Run(ctx context.Context, cwd, name string, args ...string) (string, error) {
	if r.fail && ((r.phase == "worktree" && name == "git" && len(args) > 1 && args[0] == "worktree" && args[1] == "add") || (r.phase == "runtime" && name == "tmux" && len(args) > 0 && args[0] == "new-session")) {
		return "", errors.New("injected failure")
	}
	return r.fakeRunner.Run(ctx, cwd, name, args...)
}

func TestCreationFailureRetainsNoteAndRetryDoesNotDuplicateIt(t *testing.T) {
	for _, phase := range []string{"worktree", "runtime"} {
		t.Run(phase, func(t *testing.T) {
			ctx := context.Background()
			root, repo := configureWorkspaceRoot(t), t.TempDir()
			author := testNoteAuthor(t)
			runner := &failingCreationRunner{fakeRunner: &fakeRunner{repo: repo}, fail: true, phase: phase}
			options := CreateOptions{Name: "Retry", WorkspaceRoot: root, NoteAuthor: author, TaskLinkingKey: "jira:issue:ABC-123", Repo: repo, BranchMode: integration.WorkspaceBranchNew, Base: "origin/main"}
			created, err := Create(ctx, runner, options)
			if err == nil || created.Path == "" {
				t.Fatalf("missing partial failure: %+v %v", created, err)
			}
			registry, err := workspacegroup.Load(root)
			if err != nil || len(registry.Workspaces) != 1 || registry.Workspaces[0].NotePath == "" {
				t.Fatalf("association lost: %+v %v", registry, err)
			}
			group := registry.Workspaces[0]
			original, err := os.ReadFile(group.NotePath)
			if err != nil {
				t.Fatal(err)
			}
			runner.fail = false
			// Reopening the partially provisioned workspace preserves identity. Missing
			// members can then be retried through the normal reconciler.
			reopened, err := Create(ctx, runner, options)
			if err != nil || reopened.Path != created.Path {
				t.Fatalf("retry: %+v %v", reopened, err)
			}
			revision, err := workspaceRevision(group, nil)
			if err != nil {
				t.Fatal(err)
			}
			member := DesiredWorkspaceWorktree{Repository: repo, BranchMode: integration.WorkspaceBranchNew, Name: "Retry", Base: "origin/main"}
			if len(group.Members) > 0 {
				member = DesiredWorkspaceWorktree{Repository: repo, BranchMode: integration.WorkspaceBranchExisting, Branch: "Retry"}
			}
			result, err := ApplyReconcileWorkspace(ctx, runner, nil, ReconcileWorkspaceRequest{Workspace: created.Path, WorkspaceRoot: root, Revision: revision, Desired: DesiredWorkspaceDescription{Worktrees: []DesiredWorkspaceWorktree{member}}})
			if err != nil || !result.OK {
				t.Fatalf("resume: %+v %v", result, err)
			}
			notes := author.Collect(ctx, integration.CollectRequest{})
			if !notes.Complete || len(notes.Observations) != 1 || notes.Observations[0].Ref.ID != group.NoteKey() {
				t.Fatalf("duplicate note: %+v", notes)
			}
			after, err := os.ReadFile(group.NotePath)
			if err != nil || string(after) != string(original) {
				t.Fatal("retry replaced note")
			}
		})
	}
}

func TestCreationFailsBeforeProvisioningWhenVaultUnavailable(t *testing.T) {
	root := configureWorkspaceRoot(t)
	runner := &fakeRunner{}
	_, err := Create(context.Background(), runner, CreateOptions{Name: "Plan", WorkspaceRoot: root, NoteAuthor: obsidian.NewSourceAt(filepath.Join(t.TempDir(), "missing"))})
	if err == nil || !strings.Contains(err.Error(), "obsidian vault") {
		t.Fatalf("error=%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "Plan")); !os.IsNotExist(err) {
		t.Fatal("created anchor without vault")
	}
	assertNotCalledContains(t, runner.calls, "tmux", "new-session")
}

func TestOpeningCompletedWorkspaceDoesNotReopenTask(t *testing.T) {
	ctx := context.Background()
	root := configureWorkspaceRoot(t)
	author := testNoteAuthor(t)
	created, err := Create(ctx, &fakeRunner{}, CreateOptions{Name: "Done", WorkspaceRoot: root, NoteAuthor: author})
	if err != nil {
		t.Fatal(err)
	}
	ref := author.Collect(ctx, integration.CollectRequest{}).Observations[0].Ref
	if _, err := author.SetLifecycle(ctx, ref, "done"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(ref.WorkspaceAnchorPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenRegisteredWorkspace(ctx, &fakeRunner{}, created.Path, false); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(ref.WorkspaceAnchorPath)
	if err != nil || string(after) != string(data) {
		t.Fatal("opening reopened task")
	}
}

func addTestWorkspaceNote(t *testing.T, group *workspacegroup.Workspace) {
	t.Helper()
	ctx := context.Background()
	author := testNoteAuthor(t)
	note, err := author.PrepareWorkspaceNote(ctx, group.Name)
	if err != nil {
		t.Fatal(err)
	}
	if err := author.EnsureWorkspaceNote(ctx, note); err != nil {
		t.Fatal(err)
	}
	group.NotePath, group.NoteLinkingKey = note.Path, note.LinkingKey
	if err := ensureNoteLink(group.Path, group.NotePath); err != nil {
		t.Fatal(err)
	}
}

func TestCreateUsesConfiguredRequiredVaultAndPreviewIdentity(t *testing.T) {
	ctx := context.Background()
	root := configureWorkspaceRoot(t)
	vault := t.TempDir()
	if err := os.Mkdir(filepath.Join(vault, ".obsidian"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Obsidian.VaultPath = vault
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path, err := config.Path()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	options := CreateOptions{Name: "Configured vault", WorkspaceRoot: root}
	plan, _, err := planCreate(ctx, &fakeRunner{}, options)
	if err != nil {
		t.Fatal(err)
	}
	public := integrationPlan(plan)
	if public.Note == nil || !strings.HasPrefix(public.Note.Path, vault+string(os.PathSeparator)) {
		t.Fatalf("note=%+v", public.Note)
	}
	options.Note, options.ExpectedPlanID = public.Note, public.PlanID
	created, err := Create(ctx, &fakeRunner{}, options)
	if err != nil {
		t.Fatal(err)
	}
	if target, err := os.Readlink(filepath.Join(created.Path, "notes.md")); err != nil || target != public.Note.Path {
		t.Fatalf("note=%s %v", target, err)
	}
}

func TestCreateRequiresVaultConfiguration(t *testing.T) {
	root := configureWorkspaceRoot(t)
	_, err := Create(context.Background(), &fakeRunner{}, CreateOptions{Name: "No vault", WorkspaceRoot: root})
	if err == nil || !strings.Contains(err.Error(), "obsidian.vault_path is required") {
		t.Fatalf("error=%v", err)
	}
	registry, err := workspacegroup.Load(root)
	if err != nil || len(registry.Workspaces) != 0 {
		t.Fatalf("unexpected registry: %+v %v", registry, err)
	}
}

func TestOpeningWorkspaceFailsWithoutReplacingMissingNoteOrUserFile(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{false: "user file", true: "missing note"}[missing], func(t *testing.T) {
			ctx := context.Background()
			root := configureWorkspaceRoot(t)
			author := testNoteAuthor(t)
			created, err := Create(ctx, &fakeRunner{}, CreateOptions{Name: "Preserve", WorkspaceRoot: root, NoteAuthor: author})
			if err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(created.Path, "notes.md")
			target, err := os.Readlink(link)
			if err != nil {
				t.Fatal(err)
			}
			if missing {
				err = os.Remove(target)
			} else {
				if err = os.Remove(link); err == nil {
					err = os.WriteFile(link, []byte("user content"), 0o644)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			runner := &fakeRunner{}
			if _, err := OpenRegisteredWorkspace(ctx, runner, created.Path, false); err == nil {
				t.Fatal("opened invalid note")
			}
			assertNotCalledContains(t, runner.calls, "tmux", "new-session")
			if missing {
				if _, err := os.Stat(target); !os.IsNotExist(err) {
					t.Fatal("missing note was recreated")
				}
			} else {
				if content, err := os.ReadFile(link); err != nil || string(content) != "user content" {
					t.Fatal("overwrote user file")
				}
			}
		})
	}
}

func TestCreationRetryReusesPreparedNoteWithoutRemoteIdentity(t *testing.T) {
	ctx := context.Background()
	root := configureWorkspaceRoot(t)
	author := testNoteAuthor(t)
	runner := &failingCreationRunner{fakeRunner: &fakeRunner{}, fail: true, phase: "runtime"}
	options := CreateOptions{Name: "Local planning", WorkspaceRoot: root, NoteAuthor: author}
	plan, _, err := planCreate(ctx, runner, options)
	if err != nil {
		t.Fatal(err)
	}
	options.Note, options.ExpectedPlanID = plan.Note, plan.PlanID
	created, err := Create(ctx, runner, options)
	if err == nil || created.Path == "" {
		t.Fatalf("expected runtime failure: %+v %v", created, err)
	}
	runner.fail = false
	options.ExpectedPlanID = "" // Reinspect the now-existing workspace, not the original creation plan.
	retried, err := Create(ctx, runner, options)
	if err != nil || retried.Path != created.Path {
		t.Fatalf("retry=%+v %v", retried, err)
	}
	notes := author.Collect(ctx, integration.CollectRequest{})
	if !notes.Complete || len(notes.Observations) != 1 || notes.Observations[0].Ref.ID != plan.Note.LinkingKey {
		t.Fatalf("note identity lost: %+v", notes)
	}
}
