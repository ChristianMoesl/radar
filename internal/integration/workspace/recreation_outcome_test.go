package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"radar/internal/integration"
	"radar/internal/integration/workspace/group"
	"radar/internal/workspacelease"
)

type recreationOutcomeRunner struct {
	base         *recreationRunner
	restored     bool
	afterRestore func()
	observe      func([]string) (string, error, bool)
}

func (r *recreationOutcomeRunner) LookPath(name string) error { return r.base.LookPath(name) }
func (r *recreationOutcomeRunner) Run(ctx context.Context, cwd, name string, args ...string) (string, error) {
	if r.restored && r.observe != nil {
		if out, err, handled := r.observe(args); handled {
			return out, err
		}
	}
	out, err := r.base.Run(ctx, cwd, name, args...)
	if err == nil && name == "sbx" && len(args) > 1 && ((args[0] == "stop" && args[1] == "new-sandbox-0") || (args[0] == "exec" && args[1] == "new-sandbox-0")) {
		r.restored = true
		if r.afterRestore != nil {
			r.afterRestore()
		}
	}
	return out, err
}

func recreationOutcomeFixture(t *testing.T, state string, count int) (integration.SandboxRecreateRequest, []workspacegroup.Workspace, *recreationOutcomeRunner) {
	t.Helper()
	withWorkspaceGOOS(t, "darwin")
	home := t.TempDir()
	t.Setenv("RADAR_SOCKET", filepath.Join(home, "run", "radar.sock"))
	req, groups, base := recreationFixture(t, count)
	groups[0].Sandbox.ReadyCommand = []string{"wait-ready"}
	groups[0].Sandbox.Ports = []workspacegroup.SandboxPort{{HostPort: 4001, SandboxPort: 4000}}
	base.ports["sandbox-0"] = append([]workspacegroup.SandboxPort(nil), groups[0].Sandbox.Ports...)
	actual := base.live["sandbox-0"]
	actual.Status = state
	base.live[actual.Name] = actual
	saveRecreationGroups(t, req.WorkspaceRoot, groups)
	for i := range groups {
		_, registered, found, err := RegisteredWorkspace(groups[i].Path, req.WorkspaceRoot)
		if err != nil || !found {
			t.Fatalf("registered fixture unavailable: %v", err)
		}
		groups[i] = registered
	}
	plan, err := PreviewRecreateSandboxes(context.Background(), base, req)
	if err != nil || len(plan.Targets) != count || plan.Targets[0].Status != "recreate" {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	req.ExpectedPlanID = plan.PlanID
	return req, groups, &recreationOutcomeRunner{base: base}
}

func TestSandboxRecreationRequiresObservedReplacement(t *testing.T) {
	cases := []string{"ignored state restore", "original lifetime", "missing runtime", "renamed original", "missing identity", "changed mounts", "readonly anchor", "unknown status", "missing ports", "extra ports", "invalid port exposure", "listing error", "malformed listing", "duplicate name", "duplicate identity", "replacement during ports", "registration drift", "cancelled restore", "cancelled listing", "cancelled ports", "port error", "malformed ports", "registration during final listing", "cancelled final listing", "original during ports", "missing during ports", "duplicate identity during ports", "valid"}
	for _, state := range []string{"running", "stopped"} {
		t.Run(state, func(t *testing.T) {
			for _, route := range []string{"direct", "batch"} {
				t.Run(route, func(t *testing.T) {
					for _, scenario := range cases {
						t.Run(scenario, func(t *testing.T) {
							req, groups, r := recreationOutcomeFixture(t, state, 1)
							registryBefore, err := os.ReadFile(workspacegroup.Path(req.WorkspaceRoot))
							if err != nil {
								t.Fatal(err)
							}
							note := filepath.Join(groups[0].Path, "notes.md")
							if err := os.WriteFile(note, []byte("retained note"), 0600); err != nil {
								t.Fatal(err)
							}
							unrelated := filepath.Join(t.TempDir(), "unrelated")
							if err := os.WriteFile(unrelated, []byte("retained unrelated"), 0600); err != nil {
								t.Fatal(err)
							}
							ctx, cancel := context.WithCancel(context.Background())
							defer cancel()
							observed, portsObserved := 0, 0
							r.afterRestore = func() {
								actual := r.base.live["sandbox-0"]
								switch scenario {
								case "ignored state restore":
									if state == "running" {
										actual.Status = "stopped"
									} else {
										actual.Status = "running"
									}
								case "original lifetime":
									actual.ID = "original-sandbox-0"
								case "missing runtime":
									delete(r.base.live, "sandbox-0")
									return
								case "renamed original":
									original := actual
									original.Name = "renamed-original"
									original.ID = "original-sandbox-0"
									r.base.live[original.Name] = original
								case "missing identity":
									actual.ID = ""
								case "changed mounts":
									actual.Workspaces = append(actual.Workspaces, t.TempDir())
								case "readonly anchor":
									actual.Workspaces = []string{groups[0].Path + ":ro"}
								case "unknown status":
									actual.Status = "unknown"
								case "missing ports":
									r.base.ports["sandbox-0"] = nil
								case "extra ports":
									r.base.ports["sandbox-0"] = append(r.base.ports["sandbox-0"], workspacegroup.SandboxPort{HostPort: 4002, SandboxPort: 4002})
								case "registration drift":
									changed := append([]workspacegroup.Workspace(nil), groups...)
									changed[0].Name = "changed-registration"
									saveRecreationGroups(t, req.WorkspaceRoot, changed)
								case "cancelled restore":
									cancel()
								}
								r.base.live["sandbox-0"] = actual
							}
							r.observe = func(args []string) (string, error, bool) {
								if args[0] == "ls" {
									observed++
									if observed == 2 {
										switch scenario {
										case "registration during final listing":
											changed := append([]workspacegroup.Workspace(nil), groups...)
											changed[0].Name = "changed-registration"
											saveRecreationGroups(t, req.WorkspaceRoot, changed)
										case "cancelled final listing":
											cancel()
										}
									}
									// All original admission stays held through each final observation.
									if release, err := workspacelease.AcquireMutation(groups[0].Path); err == nil {
										release()
										t.Fatal("workspace mutation admission released before outcome")
									}
									if release, err := workspacelease.AcquireSandboxMutation("sandbox-0"); err == nil {
										release()
										t.Fatal("sandbox mutation admission released before outcome")
									}
									switch scenario {
									case "listing error":
										return "", errors.New("PRIVATE provider failure"), true
									case "malformed listing":
										return "PRIVATE invalid JSON", nil, true
									case "duplicate name":
										return `[{"id":"one","name":"sandbox-0"},{"id":"two","name":"sandbox-0"}]`, nil, true
									case "duplicate identity":
										return `[{"id":"same","name":"other"},{"id":"same","name":"unrelated"}]`, nil, true
									case "cancelled listing":
										cancel()
									}
								}
								if args[0] == "ports" && args[1] == "new-sandbox-0" && args[2] == "--json" {
									portsObserved++
									switch scenario {
									case "invalid port exposure":
										return `[{"host_ip":"0.0.0.0","host_port":4001,"sandbox_port":4000,"protocol":"tcp4"}]`, nil, true
									case "replacement during ports":
										actual := r.base.live["sandbox-0"]
										actual.ID = "replacement-during-ports"
										r.base.live[actual.Name] = actual
									case "cancelled ports":
										cancel()
									case "port error":
										return "", errors.New("PRIVATE ports error"), true
									case "malformed ports":
										return "PRIVATE malformed ports", nil, true
									case "original during ports":
										original := r.base.live["sandbox-0"]
										original.Name, original.ID = "renamed-original", "original-sandbox-0"
										r.base.live[original.Name] = original
									case "missing during ports":
										delete(r.base.live, "sandbox-0")
									case "duplicate identity during ports":
										duplicate := r.base.live["sandbox-0"]
										duplicate.Name = "unrelated"
										r.base.live[duplicate.Name] = duplicate
									}
								}
								return "", nil, false
							}
							plan, err := PreviewRecreateSandboxes(context.Background(), r.base, req)
							if err != nil {
								t.Fatal(err)
							}
							var result integration.SandboxRecreateResult
							if route == "direct" {
								err = recreateOneSandbox(ctx, r, nil, req.WorkspaceRoot, groups[0], plan.Targets[0], sandboxReconcilePolicy{forceRecreate: true})
							} else {
								result, err = recreateSandboxes(ctx, r, nil, req, sandboxReconcilePolicy{})
							}
							if !r.restored {
								t.Fatalf("fixture never reached successful native state restore: err=%v result=%+v calls=%+v", err, result, r.base.calls)
							}
							valid := scenario == "valid"
							if route == "direct" {
								if valid && err != nil || !valid && err == nil {
									t.Fatalf("err=%v", err)
								}
								if strings.HasPrefix(scenario, "cancelled") && !errors.Is(err, context.Canceled) {
									t.Fatalf("cancellation identity lost: %v", err)
								}
							} else {
								if err != nil || len(result.Targets) != 1 || result.OK != valid || (valid && (result.Recreated != 1 || result.Failed != 0 || result.Targets[0].Status != "recreated")) || (!valid && (result.Recreated != 0 || result.Failed != 1 || result.Targets[0].Status != "failed")) {
									t.Fatalf("result=%+v err=%v", result, err)
								}
								if strings.Contains(result.Targets[0].Reason, "PRIVATE") {
									t.Fatalf("private error escaped: %+v", result)
								}
							}
							if valid && (observed != 2 || portsObserved != 1) {
								t.Fatalf("outcome coverage: listings=%d ports=%d", observed, portsObserved)
							}
							removes, creates, restores := 0, 0, 0
							for _, c := range r.base.calls {
								switch c.args[0] {
								case "rm":
									removes++
								case "create":
									creates++
								case "stop":
									restores++
								case "exec":
									if c.args[1] == "new-sandbox-0" {
										restores++
									}
								}
							}
							if removes != 1 || creates != 1 || restores != 1 {
								t.Fatalf("unexpected replay: remove=%d create=%d restore=%d", removes, creates, restores)
							}
							for _, path := range []string{note, unrelated} {
								data, err := os.ReadFile(path)
								if err != nil || !strings.HasPrefix(string(data), "retained") {
									t.Fatalf("resource changed: %s %q %v", path, data, err)
								}
							}
							registryAfter, err := os.ReadFile(workspacegroup.Path(req.WorkspaceRoot))
							if err != nil {
								t.Fatal(err)
							}
							if scenario != "registration drift" && scenario != "registration during final listing" && !reflect.DeepEqual(registryBefore, registryAfter) {
								t.Fatal("registry changed on outcome failure")
							}
							release, err := acquireWorkspaceLeases(context.Background(), groups[0])
							if err != nil {
								t.Fatalf("admission not released: %v", err)
							}
							release()
						})
					}
				})
			}
		})
	}
}

func TestSandboxRecreationOutcomeFailurePreservesBatchContinuation(t *testing.T) {
	req, groups, r := recreationOutcomeFixture(t, "stopped", 3)
	r.afterRestore = func() {
		actual := r.base.live["sandbox-0"]
		actual.Status = "running"
		r.base.live[actual.Name] = actual
	}
	before, err := os.ReadFile(workspacegroup.Path(req.WorkspaceRoot))
	if err != nil {
		t.Fatal(err)
	}
	result, err := recreateSandboxes(context.Background(), r, nil, req, sandboxReconcilePolicy{})
	if err != nil || result.OK || result.Failed != 1 || result.Recreated != 2 || len(result.Targets) != 3 || result.Targets[0].Status != "failed" || result.Targets[1].Status != "recreated" || result.Targets[2].Status != "recreated" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	for _, g := range groups {
		if actual, ok := r.base.live[g.Sandbox.Name]; !ok || actual.ID != "new-"+g.Sandbox.Name {
			t.Fatalf("runtime not retained: %+v", actual)
		}
	}
	after, err := os.ReadFile(workspacegroup.Path(req.WorkspaceRoot))
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("registry changed: %v", err)
	}
}
