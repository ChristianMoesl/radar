package workspace

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	workspacegroup "radar/internal/integration/workspace/group"
)

func TestNativeCreationAdmissionFailureReleasesLocksBeforeEffects(t *testing.T) {
	for _, mode := range []string{"denied", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			root := configureWorkspaceRoot(t)
			options := CreateOptions{Name: "fixture", WorkspaceRoot: root, PrepareOnly: true, NoteAuthor: testNoteAuthor(t)}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			denied := errors.New("fixture admission denied")
			var captured ReconcileWorkspacePlan
			calls := 0
			runner := &fakeRunner{}
			_, err := createWithAdmission(ctx, runner, options, func(plan ReconcileWorkspacePlan) error {
				calls++
				captured = plan
				(&agentPlanningNoteAuthor{t: t, root: root, checkLock: true}).assertNoteLock()
				if mode == "denied" {
					return denied
				}
				cancel()
				return nil
			})
			want := denied
			if mode == "cancelled" {
				want = context.Canceled
			}
			if !errors.Is(err, want) || calls != 1 || captured.group.Path == "" || captured.Note == nil {
				t.Fatal("native admission result or full plan lost", calls, captured, err)
			}
			if _, err := os.Lstat(captured.group.Path); !os.IsNotExist(err) {
				t.Fatal("rejected admission created its anchor", err)
			}
			if _, err := os.Lstat(captured.Note.Path); !os.IsNotExist(err) {
				t.Fatal("rejected admission created its note", err)
			}
			registry, err := workspacegroup.Load(root)
			if err != nil || len(registry.Workspaces) != 0 {
				t.Fatal("rejected admission registered resources", registry, err)
			}
			// A later independent operation must be able to reacquire both the
			// creation/note locks and native bundle admission after either error.
			if _, err := Create(context.Background(), runner, options); err != nil {
				t.Fatal("failed native admission leaked locks or leases", err)
			}
		})
	}
}

func TestNativeCreationAdmissionRetainsFinalRegistrationCheck(t *testing.T) {
	root, registered := activationFixture(t)
	runner := &fakeRunner{}
	_, err := createWithAdmission(context.Background(), runner, CreateOptions{Path: registered.Path, WorkspaceRoot: root, PrepareOnly: true}, func(plan ReconcileWorkspacePlan) error {
		if plan.current.ID != registered.ID || plan.group.Path != registered.Path || !plan.registered || !plan.prepareOnly {
			t.Fatal("native admission did not retain full current/desired plan", plan)
		}
		return workspacegroup.Update(root, func(registry *workspacegroup.Registry) error {
			registry.Workspaces[0].SessionName = "concurrent-replacement"
			return nil
		})
	})
	if err == nil || !strings.Contains(err.Error(), "workspace registration changed") {
		t.Fatal("acceptance bypassed native final registration revalidation", err)
	}
	_, actual, found, err := RegisteredWorkspace(registered.Path, root)
	if err != nil || !found || actual.SessionName != "concurrent-replacement" {
		t.Fatal("native execution overwrote concurrent registration", actual, found, err)
	}
	assertNotCalledContains(t, runner.calls, "tmux", "new-session")
	assertNotCalledContains(t, runner.calls, "sbx", "create")
	assertActivationLeasesReleased(t, registered)
}

func TestAgentWorkspaceExactPlanStillRejectsNoChangeRecovery(t *testing.T) {
	options, request, runner := agentWorkspaceFixture(t)
	binding, original, err := prepareAgentWorkspace(context.Background(), runner, options, request, true)
	if err != nil {
		t.Fatal(err)
	}
	request.Expected, request.Path, request.PlanID = &binding, binding.Path, original.PlanID
	calls := len(runner.calls)
	_, _, err = prepareAgentWorkspace(context.Background(), runner, options, request, false)
	if err == nil || !strings.Contains(err.Error(), "agent workspace plan changed") {
		t.Fatal("generic no-change creation recovery bypassed exact agent acceptance", err)
	}
	for _, call := range runner.calls[calls:] {
		if call.name == "sbx" && len(call.args) > 0 && (call.args[0] == "create" || call.args[0] == "rm" || call.args[0] == "exec") {
			t.Fatal("stale no-change agent plan entered runtime effects", call)
		}
	}
}
