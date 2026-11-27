package workspace

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"

	"radar/internal/integration"
	workspacegroup "radar/internal/integration/workspace/group"
)

func TestAgentCompletionReceivesCompleteNativeRegistration(t *testing.T) {
	options, _, runner := agentWorkspaceFixture(t)
	options.Name, options.TaskLinkingKey = "agent-fixture", "radar.agent:fixture"
	calls := 0
	_, err := createWithAdmission(context.Background(), runner, options, nil, func(ctx context.Context, plan ReconcileWorkspacePlan) error {
		calls++
		_, actual, found, err := RegisteredWorkspace(plan.group.Path, plan.root)
		if err != nil || !found {
			t.Fatal(found, err)
		}
		expected, err := workspacegroup.RegistrationSnapshot(plan.group)
		if err != nil || !reflect.DeepEqual(expected, actual) {
			t.Fatal("native completion lost full applied registration", err)
		}
		return nil
	})
	if err != nil || calls != 1 {
		t.Fatal("native successful completion did not run exactly once", calls, err)
	}
}

func TestNativeCreationCompletionSkipsPartialResourceFailure(t *testing.T) {
	options, _, runner := agentWorkspaceFixture(t)
	options.Name, options.TaskLinkingKey = "agent-fixture", "radar.agent:fixture"
	runner.failCreate = SandboxName("workspace", options.Name)
	calls := 0
	created, err := createWithAdmission(context.Background(), runner, options, nil, func(context.Context, ReconcileWorkspacePlan) error {
		calls++
		return errors.New("completion must not replace the native resource error")
	})
	if err == nil || calls != 0 || created.Path == "" {
		t.Fatal("partial native failure ran completion or lost its accepted result", created, calls, err)
	}
	_, registered, found, err := RegisteredWorkspace(created.Path, options.WorkspaceRoot)
	if err != nil || !found {
		t.Fatal("native failure rolled back accepted registration", found, err)
	}
	if _, err := os.Stat(registered.NotePath); err != nil {
		t.Fatal("native failure discarded accepted note", err)
	}
	// Ordinary no-completion Create can recover without leaked native locks.
	runner.failCreate = ""
	options.Path = created.Path
	if _, err := Create(context.Background(), runner, options); err != nil {
		t.Fatal("skipped completion leaked admission", err)
	}
}

type panickingCreationNoteAuthor struct {
	integration.WorkspaceNoteAuthor
}

func (panickingCreationNoteAuthor) EnsureWorkspaceNote(context.Context, DesiredWorkspaceNote) error {
	panic("fixture native effect panic")
}

func TestNativeCreationCompletionNeverRunsDuringPanicUnwinding(t *testing.T) {
	options, _, runner := agentWorkspaceFixture(t)
	options.Name, options.TaskLinkingKey = "agent-fixture", "radar.agent:fixture"
	options.NoteAuthor = panickingCreationNoteAuthor{options.NoteAuthor}
	calls := 0
	var captured ReconcileWorkspacePlan
	func() {
		defer func() {
			if got := recover(); got != "fixture native effect panic" {
				t.Fatal("native panic was lost or replaced", got)
			}
		}()
		_, _ = createWithAdmission(context.Background(), runner, options, func(plan ReconcileWorkspacePlan) error {
			captured = plan
			return nil
		}, func(context.Context, ReconcileWorkspacePlan) error {
			calls++
			return nil
		})
	}()
	if calls != 0 || captured.group.Path == "" {
		t.Fatal("native completion ran while unwinding failed effects", calls)
	}
	release, err := acquireWorkspaceLeases(context.Background(), captured.current, captured.group)
	if err != nil {
		t.Fatal("native panic leaked bundle admission", err)
	}
	release()
	if err := workspacegroup.WithNoteLock(options.WorkspaceRoot, func() error { return nil }); err != nil {
		t.Fatal("native panic leaked note lock", err)
	}
}
