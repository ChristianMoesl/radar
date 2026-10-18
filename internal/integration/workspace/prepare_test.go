package workspace

import (
	"context"
	"radar/internal/integration"
	"strings"
	"testing"
)

func TestPrepareOnlyCreatesResourcesWithoutStartingPi(t *testing.T) {
	root := configureWorkspaceRoot(t)
	runner := &fakeRunner{}
	options := CreateOptions{Name: "Prepare review", WorkspaceRoot: root, PrepareOnly: true, TaskLinkingKey: "agent:fixture:review", NoteAuthor: testNoteAuthor(t)}
	plan, _, err := planCreate(context.Background(), runner, options)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range plan.Changes {
		if change.Resource == "session" {
			t.Fatal("preparation plans an interactive session")
		}
	}
	options.Note = plan.Note
	options.ExpectedPlanID = plan.PlanID
	created, err := Create(context.Background(), runner, options)
	if err != nil {
		t.Fatal(err)
	}
	if created.Path == "" {
		t.Fatal("workspace was not prepared")
	}
	replayed, err := Create(context.Background(), runner, options)
	if err != nil || replayed.Path != created.Path {
		t.Fatalf("uncertain creation did not recover the same workspace: %+v %v", replayed, err)
	}
	for _, call := range runner.calls {
		if strings.Contains(strings.Join(call.args, " "), "new-session") || strings.Contains(strings.Join(call.args, " "), "send-keys") {
			t.Fatalf("preparation launched interactive commands: %+v", call)
		}
	}
}

func TestPrepareOnlyCannotReuseAnOperationToBypassAChangedPlan(t *testing.T) {
	root := configureWorkspaceRoot(t)
	runner := &fakeRunner{}
	options := CreateOptions{Name: "fixture", WorkspaceRoot: root, PrepareOnly: true, TaskLinkingKey: "agent:fixture", NoteAuthor: testNoteAuthor(t)}
	original, _, err := planCreate(context.Background(), runner, options)
	if err != nil {
		t.Fatal(err)
	}
	options.Note = original.Note
	options.ExpectedPlanID = original.PlanID
	if _, err := Create(context.Background(), runner, options); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	runner.repo = repo
	options.Repo = repo
	options.BranchMode = integration.WorkspaceBranchNew
	options.Base = "main"
	options.Name = "unapproved-member"
	if _, err := Create(context.Background(), runner, options); err == nil || !strings.Contains(err.Error(), "plan changed") {
		t.Fatalf("changed intent reused accepted operation: %v", err)
	}
}
