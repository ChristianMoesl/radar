package workspace

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
)

type diagnosticRecreationTrackingRunner struct {
	*recreationRunner
	touches int
}

func (r *diagnosticRecreationTrackingRunner) LookPath(name string) error {
	r.touches++
	return r.recreationRunner.LookPath(name)
}

func (r *diagnosticRecreationTrackingRunner) Run(ctx context.Context, cwd, executable string, args ...string) (string, error) {
	r.touches++
	return r.recreationRunner.Run(ctx, cwd, executable, args...)
}

func TestDiagnosticRecreationNativePlannerParityPrivacyAndNoMutation(t *testing.T) {
	for _, scenario := range []string{"running", "stopped", "absent", "secrets", "policies", "mounts", "unknown_state", "env_file", "metadata_error"} {
		t.Run(scenario, func(t *testing.T) {
			withWorkspaceGOOS(t, "darwin")
			req, groups, runner := recreationFixture(t, 2)
			g := groups[0]
			live := runner.live[g.Sandbox.Name]
			switch scenario {
			case "stopped":
				live.Status = "stopped"
				runner.live[g.Sandbox.Name] = live
			case "absent":
				delete(runner.live, g.Sandbox.Name)
			case "secrets":
				runner.secrets[g.Sandbox.Name] = `{"secrets":["PRIVATE-value"],"custom_secrets":[],"env_only_count":0}`
			case "policies":
				runner.policies[g.Sandbox.Name] = `{"rules":[{"name":"PRIVATE-policy","scope":"sandbox:PRIVATE"}]}`
			case "mounts":
				live.Workspaces = []string{"/PRIVATE-mount"}
				runner.live[g.Sandbox.Name] = live
			case "unknown_state":
				live.Status = "PRIVATE-state"
				runner.live[g.Sandbox.Name] = live
			case "env_file":
				groups[0].Sandbox.EnvFile = "/PRIVATE-missing-env"
				saveRecreationGroups(t, req.WorkspaceRoot, groups)
			case "metadata_error":
				runner.secretFailure = true
			}
			req.Workspace = g.Path
			native, err := PreviewRecreateSandboxes(context.Background(), runner, req)
			if err != nil || len(native.Targets) != 1 {
				t.Fatalf("native: %+v %v", native, err)
			}
			runner.calls = nil
			before, _ := os.ReadFile(workspacegroup.Path(req.WorkspaceRoot))
			liveBefore, _ := json.Marshal(runner.live)
			view, err := diagnosticRecreationPreview(context.Background(), runner, req.WorkspaceRoot, g.ID, g.Path)
			if err != nil || !protocol.ValidDiagnosticRecreationPreview(view, g.ID) || view.Status != native.Targets[0].Status {
				t.Fatalf("not native parity: %+v %v", view, err)
			}
			raw, _ := json.Marshal(view)
			for _, private := range []string{"PRIVATE", g.Path, g.Sandbox.Name, live.ID, native.PlanID} {
				if private != "" && strings.Contains(string(raw), private) {
					t.Fatalf("private native evidence leaked: %s", raw)
				}
			}
			listing := 0
			for _, c := range runner.calls {
				if c.args[0] == "ls" {
					listing++
				}
				if (c.args[0] == "secret" || c.args[0] == "policy" || c.args[0] == "ports") && strings.Contains(strings.Join(c.args, " "), groups[1].Sandbox.Name) {
					t.Fatal("unrelated workspace metadata read")
				}
			}
			if listing != 1 {
				t.Fatalf("native planner repeated: %d", listing)
			}
			assertNoRecreationMutation(t, runner)
			after, _ := os.ReadFile(workspacegroup.Path(req.WorkspaceRoot))
			liveAfter, _ := json.Marshal(runner.live)
			if string(before) != string(after) || string(liveBefore) != string(liveAfter) {
				t.Fatal("preview mutated registration or runtimes")
			}
		})
	}
}

func TestDiagnosticRecreationRejectsScopeBeforeRunner(t *testing.T) {
	for _, scenario := range []string{"missing", "empty", "wrong_scope", "nested_scope", "relative_scope", "no_sandbox", "cancelled", "oversize_registration"} {
		t.Run(scenario, func(t *testing.T) {
			withWorkspaceGOOS(t, "darwin")
			req, groups, runner := recreationFixture(t, 1)
			id, scope := groups[0].ID, groups[0].Path
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch scenario {
			case "missing":
				id = "PRIVATE-missing"
			case "empty":
				id = ""
			case "wrong_scope":
				scope += "-other"
			case "nested_scope":
				scope += "/nested"
			case "relative_scope":
				scope = "relative"
			case "no_sandbox":
				groups[0].Sandbox = nil
				saveRecreationGroups(t, req.WorkspaceRoot, groups)
			case "cancelled":
				cancel()
			case "oversize_registration":
				groups[0].Model = strings.Repeat("x", 1024*1024)
				saveRecreationGroups(t, req.WorkspaceRoot, groups)
			}
			tracked := &diagnosticRecreationTrackingRunner{recreationRunner: runner}
			view, err := diagnosticRecreationPreview(ctx, tracked, req.WorkspaceRoot, id, scope)
			if err == nil || view.ID != "" || tracked.touches != 0 || strings.Contains(err.Error(), "PRIVATE") {
				t.Fatalf("unsafe scope admission: %+v %v calls=%+v", view, err, runner.calls)
			}
		})
	}
}

func TestDiagnosticRecreationFullPrivateRegistrationDriftAndCancellation(t *testing.T) {
	for _, scenario := range []string{"recipe_drift", "other_group_drift", "cancel_ports", "cancel_policy"} {
		t.Run(scenario, func(t *testing.T) {
			withWorkspaceGOOS(t, "darwin")
			req, groups, runner := recreationFixture(t, 2)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			changed := false
			runner.beforeRun = func(args []string) {
				if changed || (args[0] != "ports" && args[0] != "policy") {
					return
				}
				if scenario == "cancel_policy" && args[0] != "policy" {
					return
				}
				changed = true
				switch scenario {
				case "recipe_drift":
					groups[0].Model = "PRIVATE-new-model"
					saveRecreationGroups(t, req.WorkspaceRoot, groups)
				case "other_group_drift":
					groups[1].Thinking = "PRIVATE-other-recipe"
					saveRecreationGroups(t, req.WorkspaceRoot, groups)
				default:
					cancel()
				}
			}
			view, err := diagnosticRecreationPreview(ctx, runner, req.WorkspaceRoot, groups[0].ID, "")
			if !changed || err == nil || view.ID != "" || strings.Contains(err.Error(), "PRIVATE") {
				t.Fatalf("drift/cancellation accepted: %+v %v", view, err)
			}
			assertNoRecreationMutation(t, runner)
		})
	}
}

func TestDiagnosticRecreationPreviewIdentityBindsPrivateRecipe(t *testing.T) {
	withWorkspaceGOOS(t, "darwin")
	req, groups, runner := recreationFixture(t, 1)
	read := func() protocol.DiagnosticRecreationPreview {
		v, err := diagnosticRecreationPreview(context.Background(), runner, req.WorkspaceRoot, groups[0].ID, "")
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	before, again := read(), read()
	if !reflect.DeepEqual(before, again) {
		t.Fatal("unchanged proposal unstable")
	}
	groups[0].Model = "PRIVATE-recipe-change"
	saveRecreationGroups(t, req.WorkspaceRoot, groups)
	after := read()
	if before.PreviewID == after.PreviewID {
		t.Fatal("private recipe drift did not change display identity")
	}
	after.PreviewID = before.PreviewID
	if !reflect.DeepEqual(before, after) {
		t.Fatal("private recipe exposed in public shape")
	}
	assertNoRecreationMutation(t, runner)
}
