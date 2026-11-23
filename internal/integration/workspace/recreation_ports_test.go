package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	workspacegroup "radar/internal/integration/workspace/group"
)

// Model identity-or-name dispatch, including external actors that ignore Radar's
// admission. Port changes on a replacement are observable, not just argv checks.
type recreationPortRunner struct {
	base                                           *recreationRunner
	ready                                          bool
	afterReady                                     func()
	listing                                        func() (string, error, bool)
	beforeRead, afterRead, beforeWrite, afterWrite func()
	reads, writes, wrongMutations                  int
	portSelectors, writeSelectors, stateSelectors  []string
	rejectRead, rejectWrite, ignoreWrite           bool
}

func (r *recreationPortRunner) LookPath(name string) error { return r.base.LookPath(name) }
func (r *recreationPortRunner) Run(ctx context.Context, cwd, name string, args ...string) (string, error) {
	if r.ready && args[0] == "ls" && r.listing != nil {
		if out, err, handled := r.listing(); handled {
			return out, err
		}
	}
	if r.ready && args[0] == "ports" {
		r.base.calls = append(r.base.calls, call{cwd: cwd, name: name, args: append([]string(nil), args...)})
		r.portSelectors = append(r.portSelectors, args[1])
		read := args[2] == "--json"
		if read {
			r.reads++
			if r.beforeRead != nil {
				r.beforeRead()
			}
			if r.rejectRead {
				return "", errors.New("PRIVATE port-read rejection")
			}
		} else {
			r.writes++
			r.writeSelectors = append(r.writeSelectors, args[1])
			if r.beforeWrite != nil {
				r.beforeWrite()
			}
			if r.rejectWrite {
				return "", errors.New("PRIVATE port-write rejection")
			}
		}
		key := ""
		for name, actual := range r.base.live {
			if actual.ID == args[1] || actual.Name == args[1] {
				key = name
				break
			}
		}
		if key == "" {
			return "", errors.New("PRIVATE runtime not found")
		}
		if read {
			rows := []map[string]any{}
			for _, p := range r.base.ports[key] {
				rows = append(rows, map[string]any{"host_ip": "127.0.0.1", "host_port": p.HostPort, "sandbox_port": p.SandboxPort, "protocol": "tcp4"})
			}
			out, err := json.Marshal(rows)
			if r.afterRead != nil {
				r.afterRead()
			}
			return string(out), err
		}
		port, _, err := parseTestPortSpec(args[3])
		if err != nil {
			return "", err
		}
		if !r.ignoreWrite {
			if r.base.live[key].ID != "new-sandbox-0" {
				r.wrongMutations++
			}
			switch args[2] {
			case "--publish":
				r.base.ports[key] = append(r.base.ports[key], port)
			case "--unpublish":
				next := []workspacegroup.SandboxPort{}
				for _, p := range r.base.ports[key] {
					if p != port {
						next = append(next, p)
					}
				}
				r.base.ports[key] = next
			default:
				return "", fmt.Errorf("unexpected ports action %s", args[2])
			}
		}
		if r.afterWrite != nil {
			r.afterWrite()
		}
		return "", nil
	}
	if r.ready && (args[0] == "stop" || (args[0] == "exec" && len(args) == 3 && args[2] == "true")) {
		r.stateSelectors = append(r.stateSelectors, args[1])
	}
	out, err := r.base.Run(ctx, cwd, name, args...)
	if err == nil && args[0] == "exec" && args[len(args)-1] == "wait-ready" {
		r.ready = true
		if r.afterReady != nil {
			r.afterReady()
		}
	}
	return out, err
}

func TestRecreationPortsUseObservedLifetime(t *testing.T) {
	scenarios := []string{"valid", "valid unpublish", "reassigned at publish", "renamed at publish", "missing at publish", "unpublish reassigned", "replacement during port read", "replacement after port read", "replacement after publish", "read rejection", "write rejection", "ignored write", "cancelled read", "cancelled write", "missing before ports", "original before ports", "unknown state before ports", "readonly before ports", "ambiguous before ports", "unavailable before ports", "registration before ports", "registration during port read", "cancelled before ports"}
	for _, state := range []string{"running", "stopped"} {
		for _, route := range []string{"direct", "batch"} {
			for _, scenario := range scenarios {
				t.Run(state+"/"+route+"/"+scenario, func(t *testing.T) {
					req, groups, fixture := recreationOutcomeFixture(t, state, 1)
					plan, err := PreviewRecreateSandboxes(context.Background(), fixture.base, req)
					if err != nil {
						t.Fatal(err)
					}
					r := &recreationPortRunner{base: fixture.base}
					marker := filepath.Join(groups[0].Path, "retained")
					if err := os.WriteFile(marker, []byte("retained\n"), 0600); err != nil {
						t.Fatal(err)
					}
					registryBefore, err := os.ReadFile(workspacegroup.Path(req.WorkspaceRoot))
					if err != nil {
						t.Fatal(err)
					}
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					externalPorts := []workspacegroup.SandboxPort{{HostPort: 9001, SandboxPort: 9000}}
					replace := func(keepOriginal bool) {
						actual := r.base.live["sandbox-0"]
						delete(r.base.live, "sandbox-0")
						if keepOriginal {
							actual.Name = "retained-runtime"
							r.base.live[actual.Name] = actual
							r.base.ports[actual.Name] = append([]workspacegroup.SandboxPort(nil), r.base.ports["sandbox-0"]...)
						}
						actual.ID, actual.Name = "external-replacement", "sandbox-0"
						r.base.live[actual.Name] = actual
						r.base.ports[actual.Name] = append([]workspacegroup.SandboxPort(nil), externalPorts...)
					}
					changeRegistration := func() {
						changed := append([]workspacegroup.Workspace(nil), groups...)
						changed[0].Name = "changed"
						saveRecreationGroups(t, req.WorkspaceRoot, changed)
					}
					r.afterReady = func() {
						actual := r.base.live["sandbox-0"]
						switch scenario {
						case "valid unpublish", "unpublish reassigned":
							r.base.ports[actual.Name] = []workspacegroup.SandboxPort{{HostPort: 8081, SandboxPort: 8081}}
						case "missing before ports":
							delete(r.base.live, actual.Name)
						case "original before ports":
							actual.ID = "original-sandbox-0"
							r.base.live[actual.Name] = actual
						case "unknown state before ports":
							actual.Status = "unknown"
							r.base.live[actual.Name] = actual
						case "readonly before ports":
							actual.Workspaces = []string{groups[0].Path + ":ro"}
							r.base.live[actual.Name] = actual
						case "ambiguous before ports":
							actual.Name = "unrelated"
							r.base.live[actual.Name] = actual
						case "registration before ports":
							changeRegistration()
						case "cancelled before ports":
							cancel()
						}
					}
					r.listing = func() (string, error, bool) {
						if release, err := acquireWorkspaceLeases(context.Background(), groups[0]); err == nil {
							release()
							t.Fatal("admission released before restoration observation")
						}
						if scenario == "unavailable before ports" {
							return "", errors.New("PRIVATE listing error"), true
						}
						return "", nil, false
					}
					r.beforeRead = func() {
						if r.reads != 1 {
							return
						}
						switch scenario {
						case "replacement during port read":
							replace(false)
						case "read rejection":
							r.rejectRead = true
						case "cancelled read":
							cancel()
							r.rejectRead = true
						case "registration during port read":
							changeRegistration()
						}
					}
					r.afterRead = func() {
						if r.reads == 1 && scenario == "replacement after port read" {
							replace(false)
						}
					}
					r.beforeWrite = func() {
						if r.writes != 1 {
							return
						}
						switch scenario {
						case "reassigned at publish", "unpublish reassigned":
							replace(true)
						case "renamed at publish":
							actual := r.base.live["sandbox-0"]
							delete(r.base.live, actual.Name)
							actual.Name = "retained-runtime"
							r.base.live[actual.Name] = actual
							r.base.ports[actual.Name] = r.base.ports["sandbox-0"]
							delete(r.base.ports, "sandbox-0")
						case "missing at publish":
							delete(r.base.live, "sandbox-0")
						case "write rejection":
							r.rejectWrite = true
						case "cancelled write":
							cancel()
							r.rejectWrite = true
						case "ignored write":
							r.ignoreWrite = true
						}
					}
					r.afterWrite = func() {
						if r.writes == 1 && scenario == "replacement after publish" {
							replace(false)
							r.base.ports["sandbox-0"] = append([]workspacegroup.SandboxPort(nil), groups[0].Sandbox.Ports...)
						}
					}
					r.base.calls = nil
					valid := strings.HasPrefix(scenario, "valid")
					if route == "direct" {
						err = recreateOneSandbox(ctx, r, nil, req.WorkspaceRoot, groups[0], plan.Targets[0], sandboxReconcilePolicy{forceRecreate: true})
						if (err == nil) != valid {
							t.Errorf("unexpected result: %v", err)
						}
						if strings.HasPrefix(scenario, "cancelled") && !errors.Is(err, context.Canceled) {
							t.Errorf("cancellation lost: %v", err)
						}
						if err != nil && strings.Contains(err.Error(), "PRIVATE") {
							t.Errorf("private error escaped: %v", err)
						}
					} else {
						result, err := recreateSandboxes(ctx, r, nil, req, sandboxReconcilePolicy{})
						if err != nil || len(result.Targets) != 1 || result.OK != valid || (result.Recreated == 1) != valid || (result.Failed == 1) == valid {
							t.Errorf("result=%+v err=%v", result, err)
						}
						if len(result.Targets) == 1 && strings.Contains(result.Targets[0].Reason, "PRIVATE") {
							t.Errorf("private error escaped: %+v", result)
						}
					}
					noPorts := strings.HasSuffix(scenario, "before ports")
					if noPorts {
						if len(r.portSelectors) != 0 {
							t.Errorf("ports preceded usable identity/registration/context: %v", r.portSelectors)
						}
					} else {
						if len(r.portSelectors) == 0 || r.portSelectors[0] != "new-sandbox-0" {
							t.Errorf("first ports read must use observedID: %v", r.portSelectors)
						}
						for _, selector := range r.writeSelectors {
							if selector != "new-sandbox-0" {
								t.Errorf("port mutation used unobserved selector: %v", r.writeSelectors)
							}
						}
					}
					if r.wrongMutations != 0 {
						t.Errorf("WRONG_REPLACEMENT_PORT_MUTATIONS=%d", r.wrongMutations)
					}
					if actual, ok := r.base.live["sandbox-0"]; ok && actual.ID == "external-replacement" && scenario != "replacement after publish" && !reflect.DeepEqual(r.base.ports[actual.Name], externalPorts) {
						t.Errorf("external replacement ports changed: %v", r.base.ports[actual.Name])
					}
					if scenario == "replacement after publish" && len(r.stateSelectors) != 0 {
						t.Errorf("state restore adopted a post-port replacement: %v", r.stateSelectors)
					}
					if valid && !reflect.DeepEqual(r.stateSelectors, []string{"new-sandbox-0"}) {
						t.Errorf("state restore lost pinnedID: %v", r.stateSelectors)
					}
					removes, creates := 0, 0
					for _, c := range r.base.calls {
						if c.args[0] == "rm" {
							removes++
						}
						if c.args[0] == "create" {
							creates++
						}
					}
					if removes != 1 || creates != 1 {
						t.Errorf("unexpected remove/create replay: %d/%d", removes, creates)
					}
					if valid && r.writes != 1 && scenario == "valid" {
						t.Errorf("unexpected write replay: %d", r.writes)
					}
					after, readErr := os.ReadFile(workspacegroup.Path(req.WorkspaceRoot))
					if readErr != nil || (!strings.HasPrefix(scenario, "registration") && !reflect.DeepEqual(after, registryBefore)) {
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
