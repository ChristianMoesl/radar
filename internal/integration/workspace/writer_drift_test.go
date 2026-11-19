package workspace

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/workspacelease"
)

func TestWorkspaceWriterReconcileRejectsProbeTimeRegistrationDrift(t *testing.T) {
	for _, change := range []string{"session", "member", "removed"} {
		t.Run(change, func(t *testing.T) {
			root, group := activationFixture(t)
			base := &fakeRunner{}
			changed := false
			var expected workspacegroup.Registry
			runner := activationRunner{Runner: base, run: func(ctx context.Context, cwd, name string, args ...string) (string, error) {
				output, err := base.Run(ctx, cwd, name, args...)
				if name == "tmux" && args[0] == "has-session" {
					changed = true
					if updateErr := workspacegroup.Update(root, func(registry *workspacegroup.Registry) error {
						switch change {
						case "session":
							registry.Workspaces[0].SessionName = "replacement"
						case "member":
							registry.Workspaces[0].Members = nil
						case "removed":
							registry.Workspaces = nil
						}
						return nil
					}); updateErr != nil {
						t.Error(updateErr)
					}
					var loadErr error
					expected, loadErr = workspacegroup.Load(root)
					if loadErr != nil {
						t.Error(loadErr)
					}
				}
				return output, err
			}}
			if _, err := ApplyReconcileWorkspace(context.Background(), runner, nil, writerRequest(t, root, group)); !changed || err == nil || !strings.Contains(err.Error(), "registration changed") {
				t.Error("reconciliation overwrote probe-time registration drift", changed, err)
			}
			actual, err := workspacegroup.Load(root)
			if err != nil || !reflect.DeepEqual(expected, actual) {
				t.Error("concurrent registration was overwritten", actual, err)
			}
			assertNotCalledContains(t, base.calls, "tmux", "new-session")
			assertActivationLeasesReleased(t, group)
		})
	}
}

func TestWorkspaceWriterRecreationRejectsValidationDriftAndKeepsIndependentResults(t *testing.T) {
	withWorkspaceGOOS(t, "darwin")
	for _, conflict := range []string{"path-mutation", "sandbox-mutation", "registration"} {
		t.Run(conflict, func(t *testing.T) {
			t.Setenv("RADAR_SOCKET", filepath.Join(t.TempDir(), "radar.sock"))
			req, groups, runner := recreationFixture(t, 2)
			for i := range groups {
				groups[i].Members = []workspacegroup.Member{{Path: filepath.Join(groups[i].Path, "member"), Repository: t.TempDir(), Branch: "fixture"}}
			}
			saveRecreationGroups(t, req.WorkspaceRoot, groups)
			req = recreateFixturePlan(t, context.Background(), req, runner)
			var release func()
			var err error
			switch conflict {
			case "path-mutation":
				release, err = workspacelease.AcquireMutation(groups[0].Members[0].Path)
			case "sandbox-mutation":
				release, err = workspacelease.AcquireSandboxMutation(groups[0].Sandbox.Name)
			case "registration":
				checks := 0
				runner.beforeRun = func(args []string) {
					if args[0] != "secret" || args[3] != groups[0].Sandbox.Name {
						return
					}
					checks++
					if checks == 2 { // execution validation, after its admission
						if updateErr := workspacegroup.Update(req.WorkspaceRoot, func(registry *workspacegroup.Registry) error {
							for i := range registry.Workspaces {
								if registry.Workspaces[i].ID == groups[0].ID {
									registry.Workspaces[i].Members = nil
								}
							}
							return nil
						}); updateErr != nil {
							t.Error(updateErr)
						}
					}
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			result, err := recreateSandboxes(context.Background(), runner, nil, req, sandboxReconcilePolicy{})
			if release != nil {
				release()
			}
			if err != nil || result.OK || result.Failed != 1 || result.Recreated != 1 || len(result.Targets) != 2 || result.Targets[0].Status != "failed" || result.Targets[1].Status != "recreated" {
				t.Fatal("per-target recreation result", result, err)
			}
			for _, call := range runner.calls {
				if call.args[0] == "rm" && call.args[2] == groups[0].Sandbox.Name {
					t.Error("conflicting/stale recreation deleted its sandbox")
				}
			}
			for _, group := range groups {
				assertSandboxActivationReleased(t, group)
			}
			if conflict == "registration" {
				registry, err := workspacegroup.Load(req.WorkspaceRoot)
				actual, found := workspacegroup.FindByID(registry, groups[0].ID)
				if err != nil || !found || len(actual.Members) != 0 {
					t.Error("recreation overwrote registration drift", registry, err)
				}
			}
			if _, err := os.Stat(groups[1].Path); err != nil {
				t.Fatal("independent anchor disappeared", err)
			}
		})
	}
}
