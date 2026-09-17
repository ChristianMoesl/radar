package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"radar/internal/integration"
	"radar/internal/integration/obsidian"
	"radar/internal/integration/workspace/group"
)

func TestAttachNotePreservesWorkspaceAndOriginalTaskIdentity(t *testing.T) {
	root := configureWorkspaceRoot(t)
	anchor := filepath.Join(root, "investigate")
	if err := os.MkdirAll(anchor, 0o755); err != nil {
		t.Fatal(err)
	}
	initial := workspacegroup.Workspace{ID: workspacegroup.ID(anchor), Name: "Investigate", Path: anchor, SessionName: "investigate", TaskLinkingKey: "jira:issue:ABC-123", Members: []workspacegroup.Member{}}
	if err := registerWorkspace(root, initial); err != nil {
		t.Fatal(err)
	}
	vault := t.TempDir()
	if err := os.Mkdir(filepath.Join(vault, ".obsidian"), 0o755); err != nil {
		t.Fatal(err)
	}
	author := obsidian.NewSourceAt(vault)
	note, err := author.PrepareWorkspaceNote(context.Background(), "Investigation: notes")
	if err != nil {
		t.Fatal(err)
	}
	revision, err := workspaceRevision(initial, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := ReconcileWorkspaceRequest{Workspace: anchor, WorkspaceRoot: root, Revision: revision, NoteAuthor: author, Desired: DesiredWorkspaceDescription{Note: &note, Worktrees: []DesiredWorkspaceWorktree{}}}
	runner := &fakeRunner{}
	plan, err := PreviewReconcileWorkspace(context.Background(), runner, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) != 1 || plan.Changes[0].Resource != "note" || len(plan.Warnings) == 0 {
		t.Fatalf("plan = %+v", plan)
	}
	if _, err := os.Lstat(note.Path); !os.IsNotExist(err) {
		t.Fatal("preview created note")
	}
	// A different display title with the same sanitized path still needs a new
	// confirmation; otherwise the original title was lost from the plan hash.
	changedNote := note
	changedNote.Title = "Investigation- notes"
	changedRequest := request
	changedRequest.Desired.Note = &changedNote
	changedPlan, err := PreviewReconcileWorkspace(context.Background(), runner, changedRequest)
	if err != nil || changedPlan.PlanID == plan.PlanID {
		t.Fatalf("title change did not invalidate confirmation: %v", err)
	}
	request.ExpectedPlanID = "wrong"
	rejected, err := ApplyReconcileWorkspace(context.Background(), runner, nil, request)
	if err != nil || !rejected.ReconfirmRequired {
		t.Fatalf("stale plan = %+v, %v", rejected, err)
	}
	if _, err := os.Lstat(note.Path); !os.IsNotExist(err) {
		t.Fatal("rejected confirmation created note")
	}
	request.ExpectedPlanID = plan.PlanID
	result, err := ApplyReconcileWorkspace(context.Background(), runner, nil, request)
	if err != nil || !result.OK || !result.NoteAdded {
		t.Fatalf("result = %+v, %v", result, err)
	}
	registry, err := workspacegroup.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	got := registry.Workspaces[0]
	if got.TaskLinkingKey != initial.TaskLinkingKey || got.NoteKey() != note.LinkingKey || got.Path != anchor || got.SessionName != initial.SessionName {
		t.Fatalf("identity changed: %+v", got)
	}
	if taskPiSessionID(got.SessionName, got.TaskLinkingKey) != taskPiSessionID(initial.SessionName, initial.TaskLinkingKey) {
		t.Fatal("Pi identity changed")
	}
	if target, err := os.Readlink(filepath.Join(anchor, "notes.md")); err != nil || target != note.Path {
		t.Fatalf("link = %q, %v", target, err)
	}
	notes := author.Collect(context.Background(), integration.CollectRequest{})
	if !notes.Complete || len(notes.Observations) != 1 || notes.Observations[0].Ref.Title != "Investigation: notes" {
		t.Fatalf("note title lost during reconciliation: %+v", notes)
	}
	collected := (Source{}).Collect(context.Background(), integration.CollectRequest{})
	if len(collected.Observations) != 1 || !contains(collected.Observations[0].Ref.LinkingKeys, initial.TaskLinkingKey) || !contains(collected.Observations[0].Ref.LinkingKeys, note.LinkingKey) {
		t.Fatalf("links = %+v", collected)
	}
	// Null is preserve, never detach.
	request.Revision, request.ExpectedPlanID, request.Desired.Note = result.Revision, "", nil
	retained, err := PreviewReconcileWorkspace(context.Background(), runner, request)
	if err != nil || retained.group.NotePath != note.Path {
		t.Fatalf("null note = %+v, %v", retained, err)
	}
	// A different identity cannot replace the note.
	replacement := note
	replacement.LinkingKey = "obsidian:task:different"
	request.Desired.Note = &replacement
	if _, err := PreviewReconcileWorkspace(context.Background(), runner, request); err == nil {
		t.Fatal("note replacement accepted")
	}
	// Secondary note identity also survives canonical filename changes.
	renamed := filepath.Join(filepath.Dir(note.Path), "Renamed.md")
	if err := os.Rename(note.Path, renamed); err != nil {
		t.Fatal(err)
	}
	refreshed, err := RefreshWorkspaceNote(root, got)
	if err != nil || refreshed.NotePath != renamed {
		t.Fatalf("rename = %+v, %v", refreshed, err)
	}
}

func TestAttachNoteRejectsOccupiedManagedPath(t *testing.T) {
	anchor := t.TempDir()
	canonical := filepath.Join(t.TempDir(), "Plan.md")
	if err := os.WriteFile(canonical, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(anchor, "notes.md"), []byte("user content"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := validateWorkspaceNoteAddition(anchor, DesiredWorkspaceNote{Path: canonical, LinkingKey: "obsidian:task:one"})
	if err == nil || !strings.Contains(err.Error(), "occupied") {
		t.Fatalf("error = %v", err)
	}
}

func TestCreatePlanUsesReconcilerForEmptyAndMultipleMembers(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	initRepository(t, ctx, repo)
	root := t.TempDir()
	options := CreateOptions{NoteAuthor: testNoteAuthor(t), Name: "Plan", WorkspaceRoot: root, Worktrees: []DesiredWorkspaceWorktree{
		{Repository: repo, BranchMode: integration.WorkspaceBranchNew, Name: "one", Base: "HEAD"},
		{Repository: repo, BranchMode: integration.WorkspaceBranchNew, Name: "two", Base: "HEAD"},
	}}
	runner := &fakeRunner{repo: repo}
	plan, _, err := planCreate(ctx, runner, options)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.additions) != 2 || !plan.create {
		t.Fatalf("plan = %+v", plan)
	}
	if _, err := os.Stat(plan.group.Path); !os.IsNotExist(err) {
		t.Fatal("preview created anchor")
	}
	options.ExpectedPlanID = "old"
	if _, err := Create(ctx, runner, options); err == nil {
		t.Fatal("stale creation accepted")
	}
	if _, err := os.Stat(plan.group.Path); !os.IsNotExist(err) {
		t.Fatal("stale plan created anchor")
	}
	options.Note = plan.Note
	options.ExpectedPlanID = plan.PlanID
	created, err := Create(ctx, runner, options)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := workspacegroup.Load(root)
	if err != nil || len(registry.Workspaces) != 1 || len(registry.Workspaces[0].Members) != 2 {
		t.Fatalf("registry = %+v, %v", registry, err)
	}
	if created.Path != plan.group.Path {
		t.Fatal("creation changed anchor")
	}
	empty, err := Create(ctx, runner, CreateOptions{NoteAuthor: testNoteAuthor(t), Name: "Empty", WorkspaceRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(empty.Path); err != nil || len(entries) != 1 || entries[0].Name() != "notes.md" {
		t.Fatalf("empty workspace = %+v, %v", entries, err)
	}
}

func TestNoteAttachmentChangesSandboxMountPlan(t *testing.T) {
	root := t.TempDir()
	anchor := filepath.Join(root, "plan")
	if err := os.Mkdir(anchor, 0o755); err != nil {
		t.Fatal(err)
	}
	note := filepath.Join(t.TempDir(), "Plan.md")
	if err := os.WriteFile(note, []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
	initial := workspacegroup.Workspace{ID: workspacegroup.ID(anchor), Name: "Plan", Path: anchor, Members: []workspacegroup.Member{}, Sandbox: &workspacegroup.Sandbox{Name: "plan", Agent: "shell", Mounts: []string{anchor}}}
	if err := registerWorkspace(root, initial); err != nil {
		t.Fatal(err)
	}
	revision, _ := workspaceRevision(initial, nil)
	request := ReconcileWorkspaceRequest{Workspace: anchor, WorkspaceRoot: root, Revision: revision, Desired: DesiredWorkspaceDescription{Note: &DesiredWorkspaceNote{Path: note, LinkingKey: "obsidian:task:one"}, Sandbox: &DesiredWorkspaceSandbox{}}}
	plan, err := PreviewReconcileWorkspace(context.Background(), &fakeRunner{}, request)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(plan.group.Sandbox.Mounts, filepath.Dir(note)) || !contains(plan.group.Sandbox.Mounts, plan.group.Sandbox.SharedDirectory) || len(plan.group.Sandbox.Mounts) != 3 {
		t.Fatalf("mounts = %v", plan.group.Sandbox.Mounts)
	}
	if !strings.Contains(strings.Join(plan.Warnings, " "), "interrupts processes") {
		t.Fatal("missing sandbox warning")
	}
}

func TestPendingSetupCanBeRetriedWithoutAddingAnotherMember(t *testing.T) {
	root, repo := t.TempDir(), t.TempDir()
	anchor := filepath.Join(root, "plan")
	member := filepath.Join(anchor, "app--feature")
	if err := os.MkdirAll(member, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".radar.json"), []byte(`{"setup":["echo setup"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	group := workspacegroup.Workspace{ID: workspacegroup.ID(anchor), Name: "Plan", Path: anchor, SessionName: "plan", Members: []workspacegroup.Member{{Repository: repo, Path: member, Branch: "feature"}}}
	addTestWorkspaceNote(t, &group)
	if err := registerWorkspace(root, group); err != nil {
		t.Fatal(err)
	}
	revision, _ := workspaceRevision(group, nil)
	request := ReconcileWorkspaceRequest{Workspace: anchor, WorkspaceRoot: root, Revision: revision, Desired: DesiredWorkspaceDescription{Worktrees: []DesiredWorkspaceWorktree{{Repository: repo, BranchMode: integration.WorkspaceBranchExisting, Branch: "feature"}}}}
	runner := &fakeRunner{repo: repo}
	plan, err := PreviewReconcileWorkspace(context.Background(), runner, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.additions) != 0 || !plan.startSession || len(plan.Changes) != 2 {
		t.Fatalf("plan = %+v", plan)
	}
	request.ExpectedPlanID = plan.PlanID
	result, err := ApplyReconcileWorkspace(context.Background(), runner, nil, request)
	if err != nil || !result.OK || result.WorktreesAdded != 0 {
		t.Fatalf("result = %+v, %v", result, err)
	}
	registry, err := workspacegroup.Load(root)
	if err != nil || !registry.Workspaces[0].Members[0].SetupScheduled {
		t.Fatal("setup was not persisted")
	}
}
