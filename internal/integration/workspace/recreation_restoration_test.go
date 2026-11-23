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
	workspacegroup "radar/internal/integration/workspace/group"
)

type restorationIdentityRunner struct {
	base                        *recreationRunner
	created, prepared, restored bool
	beforeRead                  func() (string, error, bool)
	beforeCommand               func()
	afterCommand                func()
	ignore, commandError        bool
	selectors                   []string
	reads                       int
}

func (r *restorationIdentityRunner) LookPath(name string) error { return r.base.LookPath(name) }
func (r *restorationIdentityRunner) Run(ctx context.Context, cwd, name string, args ...string) (string, error) {
	if r.prepared && args[0] == "ls" {
		r.reads++
		if r.beforeRead != nil {
			if out, err, handled := r.beforeRead(); handled {
				return out, err
			}
		}
	}
	stateCommand := r.prepared && (args[0] == "stop" || (args[0] == "exec" && len(args) == 3 && args[2] == "true"))
	if stateCommand {
		r.selectors = append(r.selectors, args[1])
		r.base.calls = append(r.base.calls, call{cwd: cwd, name: name, args: append([]string(nil), args...)})
		if r.beforeCommand != nil {
			r.beforeCommand()
		}
		if r.commandError {
			return "", errors.New("PRIVATE restoration failure")
		}
		// Model real identity-or-name resolution at dispatch, not a fixture that
		// silently treats an ID as a new name or makes a replacement unmodifiable.
		for key, actual := range r.base.live {
			if actual.ID != args[1] && actual.Name != args[1] {
				continue
			}
			if !r.ignore {
				actual.Status = "running"
				if args[0] == "stop" {
					actual.Status = "stopped"
				}
				r.base.live[key] = actual
			}
			r.restored = true
			if r.afterCommand != nil {
				r.afterCommand()
			}
			return "", nil
		}
		return "", errors.New("PRIVATE runtime not found")
	}
	out, err := r.base.Run(ctx, cwd, name, args...)
	if err == nil && args[0] == "create" {
		r.created = true
	}
	if err == nil && r.created && args[0] == "ports" {
		r.prepared = true
	}
	return out, err
}

func TestRecreationStateRestorationUsesObservedLifetime(t *testing.T) {
	cases := []string{"valid", "reassigned at dispatch", "disappeared at dispatch", "renamed at dispatch", "command failure", "ignored command", "replacement after command", "missing before command", "original before command", "unknown state before command", "readonly before command", "ambiguous before command", "unavailable before command", "cancelled before command", "registration before command", "cancelled during command"}
	for _, state := range []string{"running", "stopped"} {
		for _, route := range []string{"direct", "batch"} {
			for _, scenario := range cases {
				t.Run(state+"/"+route+"/"+scenario, func(t *testing.T) {
					req, groups, fixture := recreationOutcomeFixture(t, state, 1)
					plan, err := PreviewRecreateSandboxes(context.Background(), fixture.base, req)
					if err != nil {
						t.Fatal(err)
					}
					r := &restorationIdentityRunner{base: fixture.base}
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
					// This hook is after readiness/port restore and before any proposed state
					// command. Old production reaches the command without this observation.
					r.beforeRead = func() (string, error, bool) {
						if r.reads != 1 {
							return "", nil, false
						}
						actual := r.base.live["sandbox-0"]
						switch scenario {
						case "missing before command":
							delete(r.base.live, "sandbox-0")
						case "original before command":
							actual.ID = "original-sandbox-0"
							r.base.live[actual.Name] = actual
						case "unknown state before command":
							actual.Status = "unknown"
							r.base.live[actual.Name] = actual
						case "readonly before command":
							actual.Workspaces = []string{groups[0].Path + ":ro"}
							r.base.live[actual.Name] = actual
						case "ambiguous before command":
							duplicate := actual
							duplicate.Name = "unrelated"
							r.base.live[duplicate.Name] = duplicate
						case "unavailable before command":
							return "", errors.New("PRIVATE listing failure"), true
						case "cancelled before command":
							cancel()
						case "registration before command":
							changed := append([]workspacegroup.Workspace(nil), groups...)
							changed[0].Name = "changed"
							saveRecreationGroups(t, req.WorkspaceRoot, changed)
						}
						// Admission must cover the pre-command read, not merely final outcomes.
						if release, err := acquireWorkspaceLeases(context.Background(), groups[0]); err == nil {
							release()
							t.Fatal("admission released before restoration observation")
						}
						return "", nil, false
					}
					var externalBefore listedSandbox
					wrongReplacementMutations := 0
					r.beforeCommand = func() {
						actual := r.base.live["sandbox-0"]
						switch scenario {
						case "reassigned at dispatch", "disappeared at dispatch", "renamed at dispatch":
							delete(r.base.live, actual.Name)
							if scenario != "disappeared at dispatch" {
								actual.Name = "retained-runtime"
								r.base.live[actual.Name] = actual
							}
							if scenario != "renamed at dispatch" {
								externalBefore = actual
								externalBefore.ID, externalBefore.Name = "external-replacement", "sandbox-0"
								externalBefore.Status = "stopped"
								if state == "stopped" {
									externalBefore.Status = "running"
								}
								r.base.live[externalBefore.Name] = externalBefore
							}
						case "ignored command":
							actual.Status = "stopped"
							if state == "stopped" {
								actual.Status = "running"
							}
							r.base.live[actual.Name] = actual
							r.ignore = true
						case "command failure":
							r.commandError = true
						case "cancelled during command":
							cancel()
							r.commandError = true
						}
					}
					r.afterCommand = func() {
						if externalBefore.ID != "" && !reflect.DeepEqual(r.base.live["sandbox-0"], externalBefore) {
							wrongReplacementMutations++
						}
						if scenario == "replacement after command" {
							actual := r.base.live["sandbox-0"]
							actual.ID = "post-command-replacement"
							r.base.live[actual.Name] = actual
						}
					}
					r.base.calls = nil
					var result integration.SandboxRecreateResult
					if route == "direct" {
						err = recreateOneSandbox(ctx, r, nil, req.WorkspaceRoot, groups[0], plan.Targets[0], sandboxReconcilePolicy{forceRecreate: true})
						if (err == nil) != (scenario == "valid") {
							t.Errorf("unexpected result: %v", err)
						}
						if strings.HasPrefix(scenario, "cancelled") && !errors.Is(err, context.Canceled) {
							t.Errorf("cancellation identity lost: %v", err)
						}
						if err != nil && strings.Contains(err.Error(), "PRIVATE") {
							t.Errorf("private error escaped: %v", err)
						}
					} else {
						result, err = recreateSandboxes(ctx, r, nil, req, sandboxReconcilePolicy{})
						if err != nil || len(result.Targets) != 1 || result.OK != (scenario == "valid") || (result.Recreated == 1) != (scenario == "valid") || (result.Failed == 1) != (scenario != "valid") {
							t.Errorf("result=%+v err=%v", result, err)
						}
						if len(result.Targets) == 1 && strings.Contains(result.Targets[0].Reason, "PRIVATE") {
							t.Errorf("private error escaped: %+v", result)
						}
					}
					noCommand := strings.HasSuffix(scenario, "before command")
					if noCommand {
						if len(r.selectors) != 0 {
							t.Errorf("state command preceded usable fresh evidence: %v", r.selectors)
						}
					} else if !reflect.DeepEqual(r.selectors, []string{"new-sandbox-0"}) {
						t.Errorf("state command must address exactly the observed replacement ID: %v", r.selectors)
					}
					if externalBefore.ID != "" && !reflect.DeepEqual(r.base.live["sandbox-0"], externalBefore) {
						t.Errorf("uninspected replacement mutated: actual=%+v expected=%+v", r.base.live["sandbox-0"], externalBefore)
					}
					if wrongReplacementMutations != 0 {
						t.Errorf("WRONG_REPLACEMENT_STATE_MUTATIONS=%d", wrongReplacementMutations)
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
						t.Errorf("unexpected removal/create replay: %d/%d", removes, creates)
					}
					if scenario == "valid" && r.reads != 3 {
						t.Errorf("pre-command plus pinned final observations=%d, want3", r.reads)
					}
					after, readErr := os.ReadFile(workspacegroup.Path(req.WorkspaceRoot))
					if readErr != nil || (scenario != "registration before command" && !reflect.DeepEqual(registryBefore, after)) {
						t.Fatal("registration changed", readErr)
					}
					if data, err := os.ReadFile(marker); err != nil || string(data) != "retained\n" {
						t.Fatal("workspace contents changed", err)
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
