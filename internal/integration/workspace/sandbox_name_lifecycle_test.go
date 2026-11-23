package workspace

import (
	"context"
	"reflect"
	"strings"
	"testing"

	workspacegroup "radar/internal/integration/workspace/group"
)

func TestSandboxReconcileNameOnlyBackend(t *testing.T) {
	withWorkspaceGOOS(t, "darwin")
	for _, replaceBeforeRemoval := range []bool{false, true} {
		label := "replaces the mounted runtime"
		if replaceBeforeRemoval {
			label = "refuses identity drift before dispatch"
		}
		t.Run(label, func(t *testing.T) {
			_, groups, r := recreationFixture(t, 1)
			r.namesOnly = true
			group := groups[0]
			name := group.Sandbox.Name
			group.Sandbox.Mounts = append(group.Sandbox.Mounts, t.TempDir())
			if replaceBeforeRemoval {
				reads := 0
				r.beforeRun = func(args []string) {
					if args[0] == "ls" {
						reads++
						if reads == 2 {
							replacement := r.live[name]
							replacement.ID = "unconfirmed-replacement"
							r.live[name] = replacement
						}
					}
				}
			}
			err := reconcileSandboxWithPolicy(context.Background(), r, group, nil, sandboxReconcilePolicy{})
			if replaceBeforeRemoval {
				if err == nil || !strings.Contains(err.Error(), "lifetime changed") {
					t.Fatalf("expected refusal on lifetime drift, got %v", err)
				}
				assertNoRecreationMutation(t, r)
				return
			}
			if err != nil || r.live[name].ID != "new-"+name || !sameMountSet(r.live[name].Workspaces, group.Sandbox.Mounts) {
				t.Fatalf("runtime = %+v, err = %v", r.live[name], err)
			}
			var removals [][]string
			for _, c := range r.calls {
				if c.args[0] == "rm" {
					removals = append(removals, c.args)
				}
			}
			if !reflect.DeepEqual(removals, [][]string{{"rm", "--force", name}}) {
				t.Fatalf("unexpected removal commands: %v", removals)
			}
		})
	}
}

func TestSandboxRecreationNameOnlyBackend(t *testing.T) {
	withWorkspaceGOOS(t, "darwin")
	for _, replaceDuringPortRead := range []bool{false, true} {
		label := "restores ports and stopped state"
		if replaceDuringPortRead {
			label = "refuses port mutation after identity drift"
		}
		t.Run(label, func(t *testing.T) {
			req, groups, r := recreationFixture(t, 1)
			r.namesOnly = true
			group := groups[0]
			name := group.Sandbox.Name
			group.Sandbox.Ports = []workspacegroup.SandboxPort{{HostPort: 4001, SandboxPort: 4000}}
			r.ports[name] = group.Sandbox.Ports
			runtime := r.live[name]
			runtime.Status = "stopped"
			r.live[name] = runtime
			saveRecreationGroups(t, req.WorkspaceRoot, groups)
			req = recreateFixturePlan(t, context.Background(), req, r)
			if replaceDuringPortRead {
				r.beforeRun = func(args []string) {
					if args[0] == "ports" && args[2] == "--json" && r.live[name].ID == "new-"+name {
						replacement := r.live[name]
						replacement.ID = "unconfirmed-replacement"
						r.live[name] = replacement
					}
				}
			}
			result, err := recreateSandboxes(context.Background(), r, nil, req, sandboxReconcilePolicy{})
			if replaceDuringPortRead {
				if err != nil || result.OK || result.Failed != 1 || !strings.Contains(result.Targets[0].Reason, "port restoration failed") {
					t.Fatalf("expected safe stop on lifetime drift, result = %+v, err = %v", result, err)
				}
				for _, c := range r.calls {
					if c.args[0] == "stop" || (c.args[0] == "ports" && c.args[2] != "--json") {
						t.Fatalf("mutated a replacement after identity drift: %v", c.args)
					}
				}
			} else if err != nil || !result.OK || r.live[name].Status != "stopped" || !reflect.DeepEqual(r.ports[name], group.Sandbox.Ports) {
				t.Fatalf("did not restore runtime: result = %+v, err = %v, runtime = %+v, ports = %v", result, err, r.live[name], r.ports[name])
			}
		})
	}
}

func TestSandboxWorkspaceMountsSamePathBinds(t *testing.T) {
	recorded := []string{"/work", "/docs:ro"}
	wire := []string{"/work:/work", "/docs:/docs:ro"}
	if got := sandboxWorkspaceMounts(listedSandbox{Mounts: wire}); !sameMountSet(recorded, got) {
		t.Fatalf("same-path binds not recognized: %v", got)
	}
	for _, extra := range []string{"/work/src:/other/src", "/work/src:/work/../../work", "/work/src:/work/src:ro"} {
		got := sandboxWorkspaceMounts(listedSandbox{Mounts: append(wire, extra)})
		if sameMountSet(recorded, got) {
			t.Fatalf("a remap or access override disappeared from the comparison: %v", got)
		}
	}
}
