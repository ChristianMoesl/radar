package workspace

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"radar/internal/integration"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
)

func TestDiagnosticRecreationCapturesSamePrivateNativePlan(t *testing.T) {
	for _, scenario := range []string{"running", "stopped", "absent", "blocked"} {
		for _, scoped := range []bool{false, true} {
			t.Run(scenario+map[bool]string{false: "/installation", true: "/exact_scope"}[scoped], func(t *testing.T) {
				withWorkspaceGOOS(t, "darwin")
				req, groups, runner := diagnosticRecreationFixture(t, 2)
				g := groups[0]
				actual := runner.live[g.Sandbox.Name]
				switch scenario {
				case "stopped":
					actual.Status = "stopped"
					runner.live[g.Sandbox.Name] = actual
				case "absent":
					delete(runner.live, g.Sandbox.Name)
				case "blocked":
					runner.secrets[g.Sandbox.Name] = `{"secrets":["PRIVATE-secret"],"custom_secrets":[],"env_only_count":0}`
				}
				req.Workspace = g.Path
				native, err := PreviewRecreateSandboxes(context.Background(), runner, req)
				if err != nil || len(native.Targets) != 1 {
					t.Fatalf("native preview: %+v %v", native, err)
				}
				runner.calls = nil
				before, err := os.ReadFile(workspacegroup.Path(req.WorkspaceRoot))
				if err != nil {
					t.Fatal(err)
				}
				scope := ""
				if scoped {
					scope = g.Path
				}
				view, private, err := captureDiagnosticRecreationPreview(context.Background(), runner, req.WorkspaceRoot, g.ID, scope)
				want := integration.SandboxRecreateRequest{WorkspaceRoot: req.WorkspaceRoot, Workspace: g.Path, ExpectedPlanID: native.PlanID}
				if err != nil || private != want || !protocol.ValidDiagnosticRecreationPreview(view, g.ID) || view.Status != native.Targets[0].Status {
					t.Fatalf("capture did not bind the same exact native plan: view=%+v private=%+v err=%v", view, private, err)
				}
				listings := 0
				for _, c := range runner.calls {
					if c.args[0] == "ls" {
						listings++
					}
					if (c.args[0] == "secret" || c.args[0] == "policy" || c.args[0] == "ports") && strings.Contains(strings.Join(c.args, " "), groups[1].Sandbox.Name) {
						t.Fatal("capture selected unrelated workspace metadata")
					}
				}
				if listings != 2 {
					t.Fatalf("capture replanned or lost independent runtime observation: %d listings", listings)
				}
				raw, err := json.Marshal(view)
				if err != nil {
					t.Fatal(err)
				}
				for _, secret := range []string{"PRIVATE", private.ExpectedPlanID, private.WorkspaceRoot, private.Workspace, g.Sandbox.Name, actual.ID} {
					if secret != "" && strings.Contains(string(raw), secret) {
						t.Fatalf("private native binding entered public view: %s", raw)
					}
				}
				public, err := diagnosticRecreationPreview(context.Background(), runner, req.WorkspaceRoot, g.ID, scope)
				if err != nil || !reflect.DeepEqual(public, view) {
					t.Fatalf("public projection changed: %+v %v", public, err)
				}
				after, err := os.ReadFile(workspacegroup.Path(req.WorkspaceRoot))
				if err != nil || !reflect.DeepEqual(before, after) {
					t.Fatal("capture changed registration", err)
				}
				assertNoRecreationMutation(t, runner)
			})
		}
	}
}

func TestDiagnosticRecreationFailedCaptureWithholdsPrivateBinding(t *testing.T) {
	for _, scenario := range []string{"wrong_parent", "wrong_scope", "cancel_before", "cancel_final", "runtime_drift", "registration_drift", "owner_drift", "lease_drift"} {
		t.Run(scenario, func(t *testing.T) {
			withWorkspaceGOOS(t, "darwin")
			req, groups, base := diagnosticRecreationFixture(t, 1)
			g := groups[0]
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			id, scope := g.ID, g.Path
			switch scenario {
			case "wrong_parent":
				id = "PRIVATE-other"
			case "wrong_scope":
				scope += "-other"
			case "cancel_before":
				cancel()
			}
			runner := &diagnosticRecreationFinalRunner{base: base}
			runner.final = func() (string, error, bool) {
				switch scenario {
				case "cancel_final":
					cancel()
				case "runtime_drift":
					actual := base.live[g.Sandbox.Name]
					actual.ID = "PRIVATE-replacement"
					base.live[g.Sandbox.Name] = actual
				case "registration_drift":
					groups[0].Model = "PRIVATE-new-model"
					saveRecreationGroups(t, req.WorkspaceRoot, groups)
				case "owner_drift":
					recreationOwner(t, g.Path, false)
				case "lease_drift":
					release, err := acquireWorkspaceLeases(ctx, groups[0])
					if err != nil {
						t.Fatal(err)
					}
					release()
				}
				return "", nil, false
			}
			view, private, err := captureDiagnosticRecreationPreview(ctx, runner, req.WorkspaceRoot, id, scope)
			if err == nil || view != (protocol.DiagnosticRecreationPreview{}) || private != (integration.SandboxRecreateRequest{}) || strings.Contains(err.Error(), "PRIVATE") {
				t.Fatalf("rejected capture exposed a partial binding: view=%+v private=%+v err=%v", view, private, err)
			}
			if (scenario == "wrong_parent" || scenario == "wrong_scope" || scenario == "cancel_before") && runner.listings != 0 {
				t.Fatal("invalid capture reached native runtime")
			}
			assertNoRecreationMutation(t, base)
		})
	}
}

func TestDiagnosticRecreationPrivateBindingRetainsNativeConfirmation(t *testing.T) {
	for _, namesOnly := range []bool{false, true} {
		for _, changed := range []bool{false, true} {
			name := map[bool]string{false: "identity_backend", true: "name_only_backend"}[namesOnly] + map[bool]string{false: "/confirmed", true: "/changed_recipe"}[changed]
			t.Run(name, func(t *testing.T) {
				withWorkspaceGOOS(t, "darwin")
				req, groups, runner := diagnosticRecreationFixture(t, 2)
				g := groups[0]
				runner.namesOnly = namesOnly
				view, private, err := captureDiagnosticRecreationPreview(context.Background(), runner, req.WorkspaceRoot, g.ID, "")
				if err != nil || view.Status != "recreate" || private.Workspace != g.Path || private.ExpectedPlanID == "" || private.ExpectedPlanID == view.PreviewID {
					t.Fatalf("missing private exact native binding: %+v %+v %v", view, private, err)
				}
				originalOther := runner.live[groups[1].Sandbox.Name]
				runner.calls = nil
				if changed {
					groups[0].Model = "changed-private-recipe"
					saveRecreationGroups(t, req.WorkspaceRoot, groups)
				}
				result, err := recreateSandboxes(context.Background(), runner, nil, private, sandboxReconcilePolicy{})
				if changed {
					if err == nil || !strings.Contains(err.Error(), "plan changed") || result.Recreated != 0 {
						t.Fatalf("private binding bypassed native changed-plan refusal: %+v %v", result, err)
					}
					assertNoRecreationMutation(t, runner)
				} else if err != nil || !result.OK || result.Recreated != 1 || result.Failed != 0 || len(result.Targets) != 1 || result.Targets[0].WorkspaceID != g.ID {
					t.Fatalf("private binding lost exact native execution: %+v %v", result, err)
				}
				if !reflect.DeepEqual(runner.live[groups[1].Sandbox.Name], originalOther) {
					t.Fatal("private binding became an installation-wide execution")
				}
			})
		}
	}
}
