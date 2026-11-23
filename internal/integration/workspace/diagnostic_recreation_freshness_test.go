package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	workspacegroup "radar/internal/integration/workspace/group"
)

type diagnosticRecreationFinalRunner struct {
	base     *recreationRunner
	listings int
	final    func() (string, error, bool)
}

func (r *diagnosticRecreationFinalRunner) LookPath(name string) error { return r.base.LookPath(name) }
func (r *diagnosticRecreationFinalRunner) Run(ctx context.Context, cwd, name string, args ...string) (string, error) {
	if args[0] == "ls" {
		r.listings++
		if r.listings == 2 && r.final != nil {
			if out, err, handled := r.final(); handled {
				r.base.calls = append(r.base.calls, call{cwd: cwd, name: name, args: append([]string(nil), args...)})
				return out, err
			}
		}
	}
	return r.base.Run(ctx, cwd, name, args...)
}

func TestDiagnosticRecreationFinalRuntimeRejectsMetadataWindowDrift(t *testing.T) {
	for _, state := range []string{"running", "stopped"} {
		for _, stage := range []string{"ports", "secret", "policy"} {
			for _, change := range []string{"replacement", "missing", "renamed", "state", "mounts", "readonly"} {
				t.Run(state+"/"+stage+"/"+change, func(t *testing.T) {
					withWorkspaceGOOS(t, "darwin")
					req, groups, base := diagnosticRecreationFixture(t, 1)
					g := groups[0]
					actual := base.live[g.Sandbox.Name]
					actual.Status = state
					base.live[g.Sandbox.Name] = actual
					marker := filepath.Join(g.Path, "retained")
					if err := os.WriteFile(marker, []byte("retained"), 0600); err != nil {
						t.Fatal(err)
					}
					before, err := os.ReadFile(workspacegroup.Path(req.WorkspaceRoot))
					if err != nil {
						t.Fatal(err)
					}
					fired := false
					base.beforeRun = func(args []string) {
						if fired || args[0] != stage {
							return
						}
						fired = true
						changed := base.live[g.Sandbox.Name]
						switch change {
						case "replacement":
							changed.ID = "PRIVATE-replacement"
						case "missing":
							delete(base.live, g.Sandbox.Name)
							return
						case "renamed":
							delete(base.live, g.Sandbox.Name)
							changed.Name = "PRIVATE-renamed"
						case "state":
							if state == "running" {
								changed.Status = "stopped"
							} else {
								changed.Status = "running"
							}
						case "mounts":
							changed.Workspaces = []string{t.TempDir()}
						case "readonly":
							changed.Workspaces = []string{g.Path + ":ro"}
						}
						base.live[changed.Name] = changed
					}
					runner := &diagnosticRecreationFinalRunner{base: base}
					view, err := diagnosticRecreationPreview(context.Background(), runner, req.WorkspaceRoot, g.ID, g.Path)
					if !fired {
						t.Fatal("drift fixture did not run")
					}
					if err == nil || view.ID != "" {
						t.Errorf("ORIGINAL_STALE_RECREATION_PROPOSAL=1 view=%+v err=%v", view, err)
					}
					if err != nil && strings.Contains(err.Error(), "PRIVATE") {
						t.Fatal("private runtime error escaped", err)
					}
					if runner.listings != 2 {
						t.Errorf("missing final independent observation or replay: %d", runner.listings)
					}
					assertNoRecreationMutation(t, base)
					after, readErr := os.ReadFile(workspacegroup.Path(req.WorkspaceRoot))
					if readErr != nil || !reflect.DeepEqual(before, after) {
						t.Fatal("registration changed", readErr)
					}
					data, readErr := os.ReadFile(marker)
					if readErr != nil || string(data) != "retained" {
						t.Fatal("workspace files changed", readErr)
					}
				})
			}
		}
	}
}

func TestDiagnosticRecreationFinalRuntimeUnknownEvidenceFailsClosed(t *testing.T) {
	for _, scenario := range []string{"unavailable", "malformed", "missing identity", "duplicate identity", "duplicate name", "renamed with replacement", "cancelled", "registration drift", "owner drift", "lease drift"} {
		t.Run(scenario, func(t *testing.T) {
			withWorkspaceGOOS(t, "darwin")
			t.Setenv("RADAR_AGENT_ROOT", filepath.Join(t.TempDir(), "agents"))
			t.Setenv("RADAR_SOCKET", filepath.Join(t.TempDir(), "run", "radar.sock"))
			req, groups, base := recreationFixture(t, 1)
			g := groups[0]
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			runner := &diagnosticRecreationFinalRunner{base: base}
			fired := false
			runner.final = func() (string, error, bool) {
				fired = true
				row := base.live[g.Sandbox.Name]
				switch scenario {
				case "unavailable":
					return "", errors.New("PRIVATE-provider"), true
				case "malformed":
					return "PRIVATE-invalid", nil, true
				case "missing identity":
					row.ID = ""
					base.live[row.Name] = row
				case "duplicate identity":
					row.Name = "PRIVATE-alias"
					base.live[row.Name] = row
				case "duplicate name":
					data, _ := json.Marshal([]listedSandbox{row, row})
					return string(data), nil, true
				case "renamed with replacement":
					delete(base.live, row.Name)
					row.Name = "PRIVATE-renamed"
					base.live[row.Name] = row
					row.ID = "PRIVATE-replacement"
					row.Name = g.Sandbox.Name
					base.live[row.Name] = row
				case "cancelled":
					cancel()
				case "registration drift":
					groups[0].Model = "PRIVATE-model"
					saveRecreationGroups(t, req.WorkspaceRoot, groups)
				case "owner drift":
					recreationOwner(t, g.Path, false)
				case "lease drift":
					if err := os.MkdirAll(filepath.Join(filepath.Dir(os.Getenv("RADAR_SOCKET")), "leases"), 0700); err != nil {
						t.Fatal(err)
					}
				}
				return "", nil, false
			}
			view, err := diagnosticRecreationPreview(ctx, runner, req.WorkspaceRoot, g.ID, "")
			if !fired || err == nil || view.ID != "" {
				t.Errorf("unobserved/unsafe evidence accepted: fired=%t view=%+v err=%v", fired, view, err)
			}
			if err != nil && strings.Contains(err.Error(), "PRIVATE") {
				t.Fatal("private final evidence escaped", err)
			}
			if runner.listings != 2 {
				t.Errorf("observation missing or replayed: %d", runner.listings)
			}
			assertNoRecreationMutation(t, base)
		})
	}
}

func TestDiagnosticRecreationFinalRuntimePreservesFiniteNativeDecisions(t *testing.T) {
	for _, scenario := range []string{"running", "stopped", "absent", "blocked ports", "blocked state", "blocked mounts"} {
		t.Run(scenario, func(t *testing.T) {
			withWorkspaceGOOS(t, "darwin")
			req, groups, base := diagnosticRecreationFixture(t, 1)
			g := groups[0]
			row := base.live[g.Sandbox.Name]
			want := "recreate"
			switch scenario {
			case "stopped":
				row.Status = "stopped"
				base.live[row.Name] = row
			case "absent":
				delete(base.live, row.Name)
				want = "skipped"
			case "blocked ports":
				base.ports[row.Name] = []workspacegroup.SandboxPort{{HostPort: 8080, SandboxPort: 80}}
				want = "blocked"
			case "blocked state":
				row.Status = "PRIVATE-unsupported"
				base.live[row.Name] = row
				want = "blocked"
			case "blocked mounts":
				row.Workspaces = []string{t.TempDir()}
				base.live[row.Name] = row
				want = "blocked"
			}
			runner := &diagnosticRecreationFinalRunner{base: base}
			first, err := diagnosticRecreationPreview(context.Background(), runner, req.WorkspaceRoot, g.ID, "")
			if err != nil || first.Status != want || first.ExecutionEligibility != "unknown_fresh_execution_guards_not_run" {
				t.Fatalf("stable native decision changed: %+v %v", first, err)
			}
			if runner.listings != 2 {
				t.Errorf("expected one planner and one independent observation, got %d listings", runner.listings)
			}
			runner.listings = 0
			second, err := diagnosticRecreationPreview(context.Background(), runner, req.WorkspaceRoot, g.ID, "")
			if err != nil || !reflect.DeepEqual(first, second) {
				t.Fatalf("stable proposal changed: %+v %+v %v", first, second, err)
			}
			assertNoRecreationMutation(t, base)
		})
	}
}

func TestDiagnosticRecreationFinalRuntimeAbsenceCannotAdoptNewLifetime(t *testing.T) {
	withWorkspaceGOOS(t, "darwin")
	req, groups, base := diagnosticRecreationFixture(t, 1)
	g := groups[0]
	row := base.live[g.Sandbox.Name]
	delete(base.live, row.Name)
	runner := &diagnosticRecreationFinalRunner{base: base}
	fired := false
	runner.final = func() (string, error, bool) { fired = true; base.live[row.Name] = row; return "", nil, false }
	view, err := diagnosticRecreationPreview(context.Background(), runner, req.WorkspaceRoot, g.ID, "")
	if !fired || err == nil || view.ID != "" {
		t.Errorf("missing-runtime proposal adopted a new lifetime: fired=%t view=%+v err=%v", fired, view, err)
	}
	assertNoRecreationMutation(t, base)
}
