package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"radar/internal/integration"
	workspacegroup "radar/internal/integration/workspace/group"
)

func TestFeedbackPurposePersistsThroughCreationResourcesAndReconciliation(t *testing.T) {
	root := configureWorkspaceRoot(t)
	originPath := filepath.Join(root, "origin")
	if err := os.MkdirAll(originPath, 0700); err != nil {
		t.Fatal(err)
	}
	origin := workspacegroup.Workspace{ID: workspacegroup.ID(originPath), Name: "Origin", Path: originPath, Members: []workspacegroup.Member{}}
	if err := registerWorkspace(root, origin); err != nil {
		t.Fatal(err)
	}
	options := CreateOptions{Feedback: true, FeedbackOrigin: originPath, Name: "Radar feedback", WorkspaceRoot: root, NoteAuthor: testNoteAuthor(t)}
	runner := &fakeRunner{}
	created, err := Create(context.Background(), runner, options)
	if err != nil {
		t.Fatal(err)
	}
	_, group, found, err := RegisteredWorkspace(created.Path, root)
	if err != nil || !found || !group.Feedback || group.FeedbackOrigin != originPath || group.NotePath == "" || len(group.Members) != 0 {
		t.Fatalf("feedback was not an ordinary note-only workspace: %+v, %v", group, err)
	}
	resources, err := InspectWorkspaceResources(context.Background(), runner, created.Path, root)
	if err != nil || !resources.Feedback || resources.FeedbackOrigin != originPath {
		t.Fatalf("resume lost feedback context: %+v %v", resources, err)
	}
	revision, err := workspaceRevision(group, nil)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PreviewReconcileWorkspace(context.Background(), runner, ReconcileWorkspaceRequest{Workspace: created.Path, WorkspaceRoot: root, Revision: revision, Desired: DesiredWorkspaceDescription{Worktrees: []DesiredWorkspaceWorktree{}}})
	if err != nil || !plan.group.Feedback || plan.group.FeedbackOrigin != originPath {
		t.Fatalf("resource editing lost feedback purpose: %+v %v", plan.group, err)
	}
}

func TestFeedbackPlanningRejectsUnregisteredOriginsAndMissingMCP(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	if _, err := (Source{}).createOptions(integration.ManagedWorkspaceRequest{Feedback: true}); err == nil || !strings.Contains(err.Error(), "linear MCP") {
		t.Fatalf("missing MCP did not disable creation: %v", err)
	}
	root := configureWorkspaceRoot(t)
	for _, feedback := range []bool{false, true} {
		_, _, err := planCreate(context.Background(), &fakeRunner{}, CreateOptions{Feedback: feedback, FeedbackOrigin: t.TempDir(), Name: "Feedback", WorkspaceRoot: root, NoteAuthor: testNoteAuthor(t)})
		if err == nil || !strings.Contains(err.Error(), "feedback origin") {
			t.Fatalf("invalid origin accepted: %v", err)
		}
	}
}

func TestFeedbackUsesOrdinaryEarlySandboxCreation(t *testing.T) {
	runner, options := earlyCreationFixture(t, 0)
	options.Feedback = true
	ctx, _, result := startEarlyCreation(t, runner, options)
	awaitEarlyCreationPhase(t, ctx, runner.create.entered, result)
	_, group, found, err := RegisteredWorkspace(options.Path, options.WorkspaceRoot)
	if err != nil || !found || !group.Feedback || group.Sandbox == nil {
		t.Fatalf("early Pi did not see sandboxed feedback registration: %+v %v", group, err)
	}
	close(runner.create.release)
	awaitEarlyCreationPhase(t, ctx, runner.ready.entered, result)
	close(runner.ready.release)
	if got := awaitEarlyCreationResult(t, result); got.err != nil {
		t.Fatal(got.err)
	}
}

func TestFeedbackPurposeAndOriginParticipateInWorkspaceRevision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace")
	group := workspacegroup.Workspace{ID: workspacegroup.ID(path), Name: "Workspace", Path: path}
	ordinary, err := workspaceRevision(group, nil)
	if err != nil {
		t.Fatal(err)
	}
	group.Feedback = true
	feedback, err := workspaceRevision(group, nil)
	if err != nil || feedback == ordinary {
		t.Fatal("feedback purpose did not invalidate revision", err)
	}
	group.FeedbackOrigin = filepath.Join(filepath.Dir(path), "origin")
	origin, err := workspaceRevision(group, nil)
	if err != nil || origin == feedback {
		t.Fatal("feedback origin did not invalidate revision", err)
	}
}
