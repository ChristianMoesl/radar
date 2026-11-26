package tmux

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"radar/internal/agentservice"
	"radar/internal/cleanup"
	"radar/internal/integration"
	"radar/internal/integration/tmux/sessionlease"
	"radar/internal/integration/workspace"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
	"radar/internal/workspacelease"
)

type retirementOutcomeRunner struct {
	base    *admissionRunner
	armed   bool
	hook    func(string) error
	listing string
	// Keep cached fixture identities available for release probes, while
	// modelling successful native removal independently of command exit.
	removed         map[string]bool
	preserveRuntime bool
}

func (*retirementOutcomeRunner) LookPath(string) error { return nil }
func (r *retirementOutcomeRunner) Run(ctx context.Context, cwd, name string, args ...string) (string, error) {
	output, err := r.base.Run(ctx, cwd, name, args...)
	if err == nil && args[0] == "kill-session" && !r.preserveRuntime {
		if r.removed == nil {
			r.removed = map[string]bool{}
		}
		r.removed[r.base.sessions[args[2]].Key()] = true
	}
	if err == nil && args[0] == "display-message" && args[4] != "#{session_name}" {
		selector := strings.TrimPrefix(strings.TrimSuffix(args[3], ":"), "=")
		if r.removed[r.base.sessions[selector].Key()] {
			output = "12::\n"
		}
	}
	if err == nil && r.armed && r.hook != nil {
		err = r.hook(args[0])
	}
	if args[0] == "list-sessions" && r.listing != "" {
		output = r.listing
	}
	return output, err
}

type retirementOutcomeProvider struct {
	Source
	runner *retirementOutcomeRunner
}

func (p retirementOutcomeProvider) AdmitCleanup(ctx context.Context, targets []protocol.CleanupTarget, mode integration.CleanupMode) (integration.CleanupProvider, func(), error) {
	bound, release, err := admitCleanup(ctx, p.runner, targets, mode, true)
	p.runner.armed = true
	return bound, release, err
}

func executeRetirement(ctx context.Context, runner *retirementOutcomeRunner, target protocol.CleanupTarget, service bool) (protocol.CleanupTarget, error) {
	if service {
		result, err := cleanup.New([]integration.CleanupProvider{retirementOutcomeProvider{runner: runner}}).Execute(ctx, protocol.CleanupPreview{Targets: []protocol.CleanupTarget{target}}, cleanup.ExecuteOptions{Mode: integration.CleanupRetire})
		if len(result.Targets) != 0 {
			return result.Targets[0], err
		}
		return protocol.CleanupTarget{}, err
	}
	bound, release, err := admitCleanup(ctx, runner, []protocol.CleanupTarget{target}, integration.CleanupRetire, false)
	if err != nil {
		return target, err
	}
	defer release()
	runner.armed = true
	return bound.Cleanup(ctx, integration.CleanupRequest{Target: target, Mode: integration.CleanupRetire})
}

func retirementOverlapFixture(t *testing.T) (string, workspacegroup.Workspace, workspacegroup.Workspace, protocol.CleanupTarget, *retirementOutcomeRunner) {
	t.Helper()
	runner := &retirementOutcomeRunner{base: admissionFixture(t)}
	t.Setenv("RADAR_AGENT_ROOT", filepath.Join(t.TempDir(), "agents"))
	root, err := workspace.DefaultRoot()
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "radar", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, fmt.Appendf(nil, "workspace: {root_dir: %q}\n", root), 0600); err != nil {
		t.Fatal(err)
	}
	owner := workspacegroup.Workspace{ID: workspacegroup.ID(filepath.Join(root, "owner")), Name: "owner", Path: filepath.Join(root, "owner"), SessionName: "workspace-alpha", NotePath: filepath.Join(os.Getenv("HOME"), "canonical.md"), Members: []workspacegroup.Member{}}
	other := workspacegroup.Workspace{ID: workspacegroup.ID(filepath.Join(root, "other")), Name: "other", Path: filepath.Join(root, "other"), SessionName: "workspace-beta", Members: []workspacegroup.Member{}}
	for _, dir := range []string{owner.Path, other.Path} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(owner.NotePath, []byte("canonical retained\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{owner, other}}); err != nil {
		t.Fatal(err)
	}
	runner.base.path = owner.Path
	target := protocol.CleanupTarget{Source: "tmux", Kind: "session", ResourceID: owner.SessionName, WorkspaceID: owner.ID, Path: owner.Path, Operation: map[string]string{"session_name": owner.SessionName, "sandbox_name": "", "runtime_id": runner.base.sessions["$7"].Key(), "runtime_retired": "original"}}
	return root, owner, other, target, runner
}

func assertRetirementAdmissionsReleased(t *testing.T, owner workspacegroup.Workspace, runner *retirementOutcomeRunner) {
	t.Helper()
	release, err := workspacelease.Acquire(owner.Path, true)
	if err != nil {
		t.Fatal("owner path admission leaked", err)
	}
	release()
	native := sessionlease.New(true)
	defer native.Close()
	if _, err := native.Admit(context.Background(), runner.base, sessionlease.Request{Selector: "$7", Name: owner.SessionName}); err != nil {
		t.Fatal("native admission leaked", err)
	}
}

func TestNativeRetirementRevalidatesEveryOutcome(t *testing.T) {
	for _, service := range []bool{false, true} {
		for _, phase := range []string{"display-message", "list-sessions", "kill-session"} {
			for _, change := range []string{"owner", "removed-owner", "member", "canonical-note", "sandbox-config", "configured-root", "anchor-replaced", "root-replaced", "cancelled", "provider-error", "unrelated-update"} {
				t.Run(fmt.Sprintf("service=%t/%s/%s", service, phase, change), func(t *testing.T) {
					root, owner, other, target, runner := retirementOverlapFixture(t)
					preview := maps.Clone(target.Operation)
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					failure := errors.New("fixture native provider failure")
					changed := false
					runner.hook = func(command string) (hookErr error) {
						defer func() {
							if hookErr != nil && change != "provider-error" {
								t.Error("fixture mutation failed instead of testing drift", hookErr)
							}
						}()
						if command != phase || changed {
							return nil
						}
						changed = true
						switch change {
						case "cancelled":
							cancel()
							return nil
						case "provider-error":
							return failure
						case "configured-root":
							return os.WriteFile(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "radar", "config.yaml"), fmt.Appendf(nil, "workspace: {root_dir: %q}\n", filepath.Join(root, "different")), 0600)
						case "anchor-replaced":
							if err := os.Rename(owner.Path, owner.Path+".old"); err != nil {
								return err
							}
							return os.Mkdir(owner.Path, 0755)
						case "root-replaced":
							if err := os.Rename(root, root+".old"); err != nil {
								return err
							}
							if err := os.MkdirAll(owner.Path, 0755); err != nil {
								return err
							}
							data, err := os.ReadFile(workspacegroup.Path(root + ".old"))
							if err != nil {
								return err
							}
							return os.WriteFile(workspacegroup.Path(root), data, 0600)
						}
						return workspacegroup.Update(root, func(registry *workspacegroup.Registry) error {
							for i := range registry.Workspaces {
								g := &registry.Workspaces[i]
								if change == "unrelated-update" && g.ID == other.ID {
									g.Model = "unrelated-update"
								}
								if g.ID != owner.ID {
									continue
								}
								switch change {
								case "owner":
									g.Name = "changed"
								case "removed-owner":
									registry.Workspaces = append(registry.Workspaces[:i], registry.Workspaces[i+1:]...)
									return nil
								case "member":
									g.Members = []workspacegroup.Member{{Repository: root, Path: filepath.Join(owner.Path, "member"), Branch: "fixture"}}
								case "canonical-note":
									g.NotePath = filepath.Join(root, "different.md")
								case "sandbox-config":
									g.Sandbox = &workspacegroup.Sandbox{Name: "new-sandbox", Agent: "shell"}
								}
							}
							return nil
						})
					}
					result, err := executeRetirement(ctx, runner, target, service)
					if !changed || (err == nil) != (change == "unrelated-update") {
						t.Fatal("unexpected native outcome", changed, result, err)
					}
					if change == "cancelled" && !errors.Is(err, context.Canceled) {
						t.Fatal("cancellation lost", err)
					}
					if change == "provider-error" && !errors.Is(err, failure) {
						t.Fatal("provider error lost", err)
					}
					if phase != "kill-session" && change != "unrelated-update" && len(runner.base.killed) != 0 {
						t.Fatal("stale authority allowed effects", runner.base.killed)
					}
					if (result.Operation["runtime_retired"] == "true") != (change == "unrelated-update") {
						t.Fatal("retirement stamped partial failure as success", result)
					}
					if !maps.Equal(target.Operation, preview) {
						t.Fatal("preview mutated")
					}
					registry, loadErr := workspacegroup.Load(root)
					if loadErr != nil {
						t.Fatal(loadErr)
					}
					_, found := workspacegroup.FindByID(registry, owner.ID)
					if found == (change == "removed-owner") {
						t.Fatal("owner registration incorrectly changed")
					}
					currentOther, found := workspacegroup.FindByID(registry, other.ID)
					if !found || change == "unrelated-update" && currentOther.Model != "unrelated-update" {
						t.Fatal("unrelated registration lost")
					}
					data, readErr := os.ReadFile(owner.NotePath)
					if readErr != nil || string(data) != "canonical retained\n" {
						t.Fatal("canonical note changed", readErr)
					}
					assertRetirementAdmissionsReleased(t, owner, runner)
				})
			}
		}
	}
}

func TestNativeRetirementDirectAndServiceRefuseActiveOwners(t *testing.T) {
	for _, service := range []bool{false, true} {
		for _, kind := range []string{"anchor", "alias", "durable-agent"} {
			t.Run(fmt.Sprintf("service=%t/%s", service, kind), func(t *testing.T) {
				_, owner, _, target, runner := retirementOverlapFixture(t)
				var release func()
				if kind == "durable-agent" {
					dir := filepath.Join(os.Getenv("RADAR_AGENT_ROOT"), "fixture")
					if err := os.MkdirAll(dir, 0700); err != nil {
						t.Fatal(err)
					}
					data, err := json.Marshal(agentservice.Registration{Version: 1, ID: "fixture", Workspace: protocol.AgentWorkspaceBinding{ID: owner.ID, Path: owner.Path, Mode: "local"}})
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(dir, "registration.json"), data, 0600); err != nil {
						t.Fatal(err)
					}
				} else {
					path := owner.Path
					if kind == "alias" {
						path = filepath.Join(t.TempDir(), "alias")
						if err := os.Symlink(owner.Path, path); err != nil {
							t.Fatal(err)
						}
					}
					var err error
					release, err = workspacelease.Acquire(path, false)
					if err != nil {
						t.Fatal(err)
					}
					defer func() {
						if release != nil {
							release()
						}
					}()
				}
				_, err := executeRetirement(context.Background(), runner, target, service)
				if err == nil || len(runner.base.killed) != 0 {
					t.Fatal("actual owner protection bypassed", err, runner.base.killed)
				}
				if kind == "durable-agent" && !strings.Contains(err.Error(), "associated with agent fixture") {
					t.Fatal("wrong durable-agent refusal", err)
				}
				if release != nil {
					release()
					release = nil
				}
				assertRetirementAdmissionsReleased(t, owner, runner)
			})
		}
	}
}

func TestNativeRetirementBindingRejectsChangedTargetOrMode(t *testing.T) {
	for _, change := range []string{"path", "operation", "mode"} {
		t.Run(change, func(t *testing.T) {
			_, _, _, target, runner := retirementOverlapFixture(t)
			bound, release, err := admitCleanup(context.Background(), runner, []protocol.CleanupTarget{target}, integration.CleanupRetire, true)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			request := integration.CleanupRequest{Target: target, Mode: integration.CleanupRetire}
			request.Target.Operation = maps.Clone(request.Target.Operation)
			switch change {
			case "path":
				request.Target.Path += "-changed"
			case "operation":
				request.Target.Operation["runtime_id"] = "tmux:session:99:34:$7"
			case "mode":
				request.Mode = integration.CleanupConfirmed
			}
			if _, err := bound.Cleanup(context.Background(), request); err == nil || len(runner.base.killed) != 0 {
				t.Fatal("unadmitted request accepted", err, runner.base.killed)
			}
		})
	}
}

func TestNativeRetirementListingRetainsLifetimeAndOwnershipChecks(t *testing.T) {
	for _, service := range []bool{false, true} {
		for _, change := range []string{"malformed", "replacement", "foreign-path", "missing", "aliased-id", "conflicting-created", "duplicate-name", "duplicate-foreign-id", "duplicate-foreign-name", "mixed-server", "missing-foreign-name", "no-server", "no-server-cancelled", "no-server-drift"} {
			t.Run(fmt.Sprintf("service=%t/%s", service, change), func(t *testing.T) {
				root, owner, _, target, runner := retirementOverlapFixture(t)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				switch change {
				case "malformed":
					runner.listing = "malformed listing"
				case "replacement":
					runner.listing = fmt.Sprintf("99\t34\t$7\t%s\t0\t1\t%s", owner.SessionName, owner.Path)
				case "foreign-path":
					runner.base.path = t.TempDir()
				case "missing":
					runner.listing = "\n"
				case "aliased-id", "conflicting-created", "duplicate-name", "duplicate-foreign-id", "duplicate-foreign-name", "mixed-server", "missing-foreign-name":
					runner.listing = fmt.Sprintf("12\t34\t$7\t%s\t0\t1\t%s\n", owner.SessionName, owner.Path)
					switch change {
					case "aliased-id":
						runner.listing += "12\t34\t$7\tprivate-alias\t0\t1\t/private-path\n"
					case "conflicting-created":
						runner.listing += "12\t35\t$7\tprivate-alias\t0\t1\t/private-path\n"
					case "duplicate-name":
						runner.listing += fmt.Sprintf("12\t35\t$8\t%s\t0\t1\t/private-path\n", owner.SessionName)
					case "duplicate-foreign-id":
						runner.listing += "12\t34\t$8\tprivate-other\t0\t1\t/private-path\n12\t35\t$8\tprivate-another\t0\t1\t/private-path\n"
					case "duplicate-foreign-name":
						runner.listing += "12\t34\t$8\tprivate-other\t0\t1\t/private-path\n12\t35\t$9\tprivate-other\t0\t1\t/private-path\n"
					case "mixed-server":
						runner.listing += "99\t35\t$8\tprivate-other\t0\t1\t/private-path\n"
					case "missing-foreign-name":
						runner.listing += "12\t35\t$8\t\t0\t1\t/private-path\n"
					}
				default:
					runner.hook = func(command string) error {
						if command != "list-sessions" {
							return nil
						}
						if change == "no-server-cancelled" {
							cancel()
						}
						if change == "no-server-drift" {
							if err := workspacegroup.Update(root, func(registry *workspacegroup.Registry) error {
								for i := range registry.Workspaces {
									if registry.Workspaces[i].ID == owner.ID {
										registry.Workspaces[i].Name = "changed"
									}
								}
								return nil
							}); err != nil {
								return err
							}
						}
						return errors.New("no server running on fixture socket")
					}
				}
				result, err := executeRetirement(ctx, runner, target, service)
				wantError := change != "missing" && change != "no-server"
				if (err != nil) != wantError || len(runner.base.killed) != 0 || result.Operation["runtime_retired"] == "true" {
					t.Fatal("unsafe listing outcome", result, err, runner.base.killed)
				}
				if err != nil && strings.Contains(err.Error(), "private-") {
					t.Fatal("private listing identity escaped", err)
				}
				if change == "no-server-cancelled" && !errors.Is(err, context.Canceled) {
					t.Fatal("no-server hid cancellation", err)
				}
				assertRetirementAdmissionsReleased(t, owner, runner)
			})
		}
	}
}
