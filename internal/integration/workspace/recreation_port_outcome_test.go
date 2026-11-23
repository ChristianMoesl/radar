package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	workspacegroup "radar/internal/integration/workspace/group"
)

// A temporary name reassignment can disappear between the two identity reads.
// Model actual ID-or-name port selection, not just the command's argv.
func TestRecreationPortOutcomeUsesObservedLifetime(t *testing.T) {
	scenarios := []string{"valid", "ABA mismatched observed ports", "ABA correct observed ports", "temporary rename", "ID rejection", "ID malformed", "ID unsafe exposure", "ID cancellation", "replacement during read", "registration during read", "provider error", "cancelled read"}
	for _, state := range []string{"running", "stopped"} {
		for _, route := range []string{"direct", "batch"} {
			for _, scenario := range scenarios {
				t.Run(state+"/"+route+"/"+scenario, func(t *testing.T) {
					req, groups, r := recreationOutcomeFixture(t, state, 1)
					plan, err := PreviewRecreateSandboxes(context.Background(), r.base, req)
					if err != nil {
						t.Fatal(err)
					}
					before, err := os.ReadFile(workspacegroup.Path(req.WorkspaceRoot))
					if err != nil {
						t.Fatal(err)
					}
					marker := filepath.Join(groups[0].Path, "retained-outcome-note")
					if err := os.WriteFile(marker, []byte("retained\n"), 0600); err != nil {
						t.Fatal(err)
					}
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					selectors, sampled := []string{}, []string{}
					listings := 0
					r.afterRestore = func() {
						if scenario == "ABA mismatched observed ports" {
							r.base.ports["sandbox-0"] = nil
						}
					}
					r.observe = func(args []string) (string, error, bool) {
						if args[0] != "ls" && args[0] != "ports" {
							return "", nil, false
						}
						if release, err := acquireWorkspaceLeases(context.Background(), groups[0]); err == nil {
							release()
							t.Fatal("admission released before final evidence")
						}
						if args[0] == "ls" {
							listings++
							return "", nil, false
						}
						if len(args) != 3 || args[2] != "--json" {
							t.Fatalf("unexpected final port mutation: %v", args)
						}
						selectors = append(selectors, args[1])
						if args[1] == "new-sandbox-0" {
							switch scenario {
							case "ID rejection":
								return "", errors.New("PRIVATE unsupported ID selector"), true
							case "ID malformed":
								return "PRIVATE malformed ports", nil, true
							case "ID unsafe exposure":
								return `[{"host_ip":"0.0.0.0","host_port":4001,"sandbox_port":4000,"protocol":"tcp4"}]`, nil, true
							case "ID cancellation":
								cancel()
								return "", errors.New("PRIVATE cancelled ID read"), true
							}
						}
						switch scenario {
						case "provider error":
							return "", errors.New("PRIVATE provider error"), true
						case "cancelled read":
							cancel()
							return "", errors.New("PRIVATE cancelled read"), true
						case "replacement during read":
							actual := r.base.live["sandbox-0"]
							actual.ID = "external-runtime"
							r.base.live[actual.Name] = actual
						case "registration during read":
							changed := append([]workspacegroup.Workspace(nil), groups...)
							changed[0].Name = "changed-registration"
							saveRecreationGroups(t, req.WorkspaceRoot, changed)
						}
						if strings.HasPrefix(scenario, "ABA ") || scenario == "temporary rename" {
							actual := r.base.live["sandbox-0"]
							ports := append([]workspacegroup.SandboxPort(nil), r.base.ports[actual.Name]...)
							delete(r.base.live, actual.Name)
							delete(r.base.ports, actual.Name)
							actual.Name = "temporarily-renamed"
							r.base.live[actual.Name], r.base.ports[actual.Name] = actual, ports
							if scenario != "temporary rename" {
								external := actual
								external.Name, external.ID = "sandbox-0", "external-runtime"
								r.base.live[external.Name] = external
								r.base.ports[external.Name] = nil
								if scenario == "ABA mismatched observed ports" {
									r.base.ports[external.Name] = append([]workspacegroup.SandboxPort(nil), groups[0].Sandbox.Ports...)
								}
							}
							for _, runtime := range r.base.live {
								if runtime.ID == args[1] || runtime.Name == args[1] {
									sampled = append(sampled, runtime.ID)
								}
							}
							out, readErr := r.base.Run(ctx, "", "sbx", args...)
							delete(r.base.live, "temporarily-renamed")
							delete(r.base.ports, "temporarily-renamed")
							delete(r.base.live, "sandbox-0")
							delete(r.base.ports, "sandbox-0")
							actual.Name = "sandbox-0"
							r.base.live[actual.Name], r.base.ports[actual.Name] = actual, ports
							return out, readErr, true
						}
						return "", nil, false
					}
					r.base.calls = nil
					valid := scenario == "valid" || scenario == "ABA correct observed ports" || scenario == "temporary rename"
					accepted := false
					if route == "direct" {
						err = recreateOneSandbox(ctx, r, nil, req.WorkspaceRoot, groups[0], plan.Targets[0], sandboxReconcilePolicy{forceRecreate: true})
						accepted = err == nil
						if (scenario == "ID cancellation" || scenario == "cancelled read") && !errors.Is(err, context.Canceled) {
							t.Errorf("cancellation identity lost: %v", err)
						}
						if err != nil && strings.Contains(err.Error(), "PRIVATE") {
							t.Errorf("private error escaped: %v", err)
						}
					} else {
						result, batchErr := recreateSandboxes(ctx, r, nil, req, sandboxReconcilePolicy{})
						accepted = result.OK
						if batchErr != nil || len(result.Targets) != 1 || (result.Recreated == 1) != valid || (result.Failed == 1) == valid || len(result.Targets) == 1 && strings.Contains(result.Targets[0].Reason, "PRIVATE") {
							t.Errorf("result=%+v err=%v", result, batchErr)
						}
					}
					if accepted != valid {
						t.Errorf("accepted=%v expected=%v err=%v", accepted, valid, err)
					}
					if scenario == "ABA mismatched observed ports" && accepted {
						t.Errorf("WRONG_LIFETIME_PORT_OUTCOME_ACCEPTED=1 sampled=%v", sampled)
					}
					if !reflect.DeepEqual(selectors, []string{"new-sandbox-0"}) {
						t.Errorf("final port evidence must read exactly one observedID, no fallback/replay: %v", selectors)
					}
					for _, id := range sampled {
						if id != "new-sandbox-0" {
							t.Errorf("final port evidence sampled another lifetime: %s", id)
						}
					}
					if !r.restored || valid && listings != 2 {
						t.Errorf("restored=%v final identity reads=%d", r.restored, listings)
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
							if len(c.args) == 3 && c.args[1] == "new-sandbox-0" && c.args[2] == "true" {
								restores++
							}
						}
					}
					if removes != 1 || creates != 1 || restores != 1 {
						t.Errorf("unexpected mutation replay: %d/%d/%d", removes, creates, restores)
					}
					after, readErr := os.ReadFile(workspacegroup.Path(req.WorkspaceRoot))
					if readErr != nil || scenario != "registration during read" && !reflect.DeepEqual(before, after) {
						t.Fatal("registration changed", readErr)
					}
					if data, err := os.ReadFile(marker); err != nil || string(data) != "retained\n" {
						t.Fatal("workspace changed", err)
					}
					release, err := acquireWorkspaceLeases(context.Background(), groups[0])
					if err != nil {
						t.Fatal("admission not released", err)
					}
					release()
				})
			}
		}
	}
}
