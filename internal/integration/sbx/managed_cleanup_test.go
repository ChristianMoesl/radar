package sbx

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
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
	"radar/internal/workspacelease"
)

type managedSandboxRunner struct {
	*admissionFixtureRunner
	lookup       func() error
	after        func() error
	removalError error
}

func (r *managedSandboxRunner) LookPath(string) error {
	if r.lookup != nil {
		return r.lookup()
	}
	return nil
}
func (r *managedSandboxRunner) Run(ctx context.Context, cwd, name string, args ...string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	output, err := r.admissionFixtureRunner.Run(ctx, cwd, name, args...)
	if err != nil {
		return output, err
	}
	if r.removalError != nil {
		return "", r.removalError
	}
	if r.after != nil {
		return output, r.after()
	}
	return output, nil
}
func managedSandboxFixture(t *testing.T) (string, workspacegroup.Workspace, workspacegroup.Workspace, protocol.CleanupTarget, *managedSandboxRunner) {
	t.Helper()
	root, owner, other, target := runtimeAdmissionFixture(t)
	t.Setenv("RADAR_AGENT_ROOT", filepath.Join(t.TempDir(), "agents"))
	target.WorkspaceID, target.Path = "", ""
	target.Operation = map[string]string{"fixture": "must remain unchanged"}
	return root, owner, other, target, &managedSandboxRunner{admissionFixtureRunner: &admissionFixtureRunner{owner: owner}}
}
func executeManagedSandbox(ctx context.Context, runner *managedSandboxRunner, target protocol.CleanupTarget, service bool, mode integration.CleanupMode) (protocol.CleanupResult, error) {
	if service {
		return cleanup.New([]integration.CleanupProvider{admittedFixtureSource{runner: runner}}).Execute(ctx, protocol.CleanupPreview{Targets: []protocol.CleanupTarget{target}}, cleanup.ExecuteOptions{Mode: mode})
	}
	result, err := cleanupWithRunner(ctx, runner, integration.CleanupRequest{Target: target, Mode: mode}, false)
	if err != nil {
		return protocol.CleanupResult{}, err
	}
	return protocol.CleanupResult{Targets: []protocol.CleanupTarget{result}}, nil
}
func registerManagedSandboxAgent(t *testing.T, owner workspacegroup.Workspace) {
	t.Helper()
	dir := filepath.Join(os.Getenv("RADAR_AGENT_ROOT"), "fixture-agent")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(agentservice.Registration{Version: 1, ID: "fixture-agent", Workspace: protocol.AgentWorkspaceBinding{ID: owner.ID, Path: owner.Path, Mode: "local"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "registration.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestManagedSandboxRevalidatesEveryOutcome(t *testing.T) {
	for _, service := range []bool{false, true} {
		for _, mode := range []integration.CleanupMode{integration.CleanupSafe, integration.CleanupConfirmed, integration.CleanupExpired} {
			for _, phase := range []string{"lookup", "removal"} {
				for _, change := range []string{"owner", "removed-owner", "member", "canonical-note", "sandbox-config", "configured-root", "anchor-replaced", "root-replaced", "cancelled", "agent-appeared", "shared-name", "unrelated-update"} {
					t.Run(fmt.Sprintf("service=%t/mode=%d/%s/%s", service, mode, phase, change), func(t *testing.T) {
						root, owner, other, target, runner := managedSandboxFixture(t)
						original := maps.Clone(target.Operation)
						ctx, cancel := context.WithCancel(context.Background())
						defer cancel()
						changed := false
						mutate := func() (hookErr error) {
							defer func() {
								if hookErr != nil {
									t.Error("fixture mutation failed instead of testing drift", hookErr)
								}
							}()
							if changed {
								return nil
							}
							changed = true
							switch change {
							case "cancelled":
								cancel()
								return nil
							case "agent-appeared":
								registerManagedSandboxAgent(t, owner)
								return nil
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
									if g.ID == other.ID {
										if change == "unrelated-update" {
											g.Model = "unrelated-update"
										}
										if change == "shared-name" {
											g.Sandbox = &workspacegroup.Sandbox{Name: owner.Sandbox.Name, Agent: "shell"}
										}
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
										g.Sandbox.Agent = "changed"
									}
								}
								return nil
							})
						}
						if phase == "lookup" {
							runner.lookup = mutate
						} else {
							runner.after = mutate
						}
						result, err := executeManagedSandbox(ctx, runner, target, service, mode)
						if !changed || (err == nil) != (change == "unrelated-update") {
							t.Fatal("unexpected managed SBX outcome", changed, result, err)
						}
						if change == "cancelled" && !errors.Is(err, context.Canceled) {
							t.Fatal("cancellation lost", err)
						}
						if phase == "lookup" && change != "unrelated-update" && len(runner.calls) != 0 {
							t.Fatal("stale authority allowed native effects", runner.calls)
						}
						if err != nil && len(result.Targets) != 0 {
							t.Fatal("partial native outcome claimed complete", result)
						}
						if !maps.Equal(target.Operation, original) {
							t.Fatal("caller preview mutated")
						}
						registry, loadErr := workspacegroup.Load(root)
						if loadErr != nil {
							t.Fatal(loadErr)
						}
						_, found := workspacegroup.FindByID(registry, owner.ID)
						if found == (change == "removed-owner") {
							t.Fatal("owner registration incorrectly changed")
						}
						actualOther, found := workspacegroup.FindByID(registry, other.ID)
						if !found || change == "unrelated-update" && actualOther.Model != "unrelated-update" {
							t.Fatal("unrelated metadata lost")
						}
						data, readErr := os.ReadFile(owner.NotePath)
						if readErr != nil || string(data) != "canonical retained\n" {
							t.Fatal("canonical note changed", readErr)
						}
						assertRuntimeAdmissionReleased(t, owner)
					})
				}
			}
		}
	}
}

func TestManagedSandboxRejectsActualOwnerConflicts(t *testing.T) {
	for _, service := range []bool{false, true} {
		for _, mode := range []integration.CleanupMode{integration.CleanupSafe, integration.CleanupConfirmed, integration.CleanupExpired} {
			for _, kind := range []string{"anchor", "alias", "sandbox", "durable-agent"} {
				t.Run(fmt.Sprintf("service=%t/mode=%d/%s", service, mode, kind), func(t *testing.T) {
					_, owner, _, target, runner := managedSandboxFixture(t)
					var release func()
					if kind == "durable-agent" {
						registerManagedSandboxAgent(t, owner)
					} else {
						path := owner.Path
						if kind == "alias" {
							path = filepath.Join(t.TempDir(), "alias")
							if err := os.Symlink(owner.Path, path); err != nil {
								t.Fatal(err)
							}
						}
						var err error
						if kind == "sandbox" {
							release, err = workspacelease.AcquireSandbox(owner.Sandbox.Name, false)
						} else {
							release, err = workspacelease.Acquire(path, false)
						}
						if err != nil {
							t.Fatal(err)
						}
						defer func() {
							if release != nil {
								release()
							}
						}()
					}
					result, err := executeManagedSandbox(context.Background(), runner, target, service, mode)
					want := "in use by another operation"
					if kind == "durable-agent" {
						want = "associated with agent fixture-agent"
					}
					if err == nil || !strings.Contains(err.Error(), want) || len(runner.calls) != 0 || len(result.Targets) != 0 {
						t.Fatal("actual owner conflict bypassed", result, err, runner.calls)
					}
					if release != nil {
						release()
						release = nil
					}
					assertRuntimeAdmissionReleased(t, owner)
				})
			}
		}
	}
}

type managedSandboxProbe struct{ run func() error }

func (managedSandboxProbe) Descriptor() integration.Descriptor {
	return integration.Descriptor{Name: "sbx-probe"}
}
func (managedSandboxProbe) Collect(context.Context, integration.CollectRequest) integration.CollectResult {
	return integration.CollectResult{}
}
func (managedSandboxProbe) PreviewCleanup(context.Context, integration.CleanupPreviewRequest) ([]protocol.CleanupTarget, error) {
	return nil, nil
}
func (p managedSandboxProbe) Cleanup(_ context.Context, req integration.CleanupRequest) (protocol.CleanupTarget, error) {
	return req.Target, p.run()
}

func TestManagedSandboxRetainsFullAndDiscoveredBundleThroughLaterOutcomes(t *testing.T) {
	for _, full := range []bool{false, true} {
		for _, mode := range []integration.CleanupMode{integration.CleanupSafe, integration.CleanupConfirmed, integration.CleanupExpired} {
			for _, outcome := range []string{"success", "provider-error", "cancelled"} {
				t.Run(fmt.Sprintf("full=%t/mode=%d/%s", full, mode, outcome), func(t *testing.T) {
					_, owner, _, target, runner := managedSandboxFixture(t)
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					failure := errors.New("fixture later provider failure")
					later := protocol.CleanupTarget{Source: "sbx-probe"}
					if full {
						later.Path = filepath.Join(t.TempDir(), "alias")
						if err := os.Symlink(owner.Path, later.Path); err != nil {
							t.Fatal(err)
						}
					}
					calls := 0
					probe := managedSandboxProbe{run: func() error {
						calls++
						for _, admit := range []func() (func(), error){func() (func(), error) { return workspacelease.Acquire(owner.Path, false) }, func() (func(), error) { return workspacelease.AcquireSandbox(owner.Sandbox.Name, false) }} {
							if release, err := admit(); err == nil {
								release()
								t.Fatal("actual owner/native admission dropped before later provider")
							}
						}
						if outcome == "provider-error" {
							return failure
						}
						if outcome == "cancelled" {
							cancel()
							return ctx.Err()
						}
						return nil
					}}
					result, err := cleanup.New([]integration.CleanupProvider{admittedFixtureSource{runner: runner}, probe}).Execute(ctx, protocol.CleanupPreview{Targets: []protocol.CleanupTarget{target, later}}, cleanup.ExecuteOptions{Mode: mode})
					if (err != nil) != (outcome != "success") || calls != 1 || !runner.removed {
						t.Fatal("unexpected Service lifetime", result, err, calls, runner.calls)
					}
					if outcome == "provider-error" && !errors.Is(err, failure) {
						t.Fatal("provider cause lost", err)
					}
					if outcome == "cancelled" && !errors.Is(err, context.Canceled) {
						t.Fatal("cancellation lost", err)
					}
					want := 2
					if outcome != "success" {
						want = 1
					}
					if len(result.Targets) != want {
						t.Fatal("incorrect partial completion", result)
					}
					if strings.Join(runner.calls, "\n") != "sbx rm --force "+owner.Sandbox.Name {
						t.Fatal("ordinary name-addressed command changed", runner.calls)
					}
					assertRuntimeAdmissionReleased(t, owner)
				})
			}
		}
	}
}

func TestManagedSandboxMissingAndFailureOutcomes(t *testing.T) {
	for _, service := range []bool{false, true} {
		for _, mode := range []integration.CleanupMode{integration.CleanupSafe, integration.CleanupConfirmed, integration.CleanupExpired} {
			for _, change := range []string{"missing-anchor", "anchor-appears", "missing-runtime", "missing-runtime-drift", "missing-runtime-cancelled", "provider-error", "lookup-error", "pre-cancelled"} {
				t.Run(fmt.Sprintf("service=%t/mode=%d/%s", service, mode, change), func(t *testing.T) {
					root, owner, _, target, runner := managedSandboxFixture(t)
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					failure := errors.New("fixture native provider failure")
					switch change {
					case "missing-anchor", "anchor-appears":
						if err := os.Remove(owner.Path); err != nil {
							t.Fatal(err)
						}
					case "pre-cancelled":
						cancel()
					case "lookup-error":
						runner.lookup = func() error { return failure }
					case "provider-error":
						runner.removalError = failure
					case "missing-runtime", "missing-runtime-drift", "missing-runtime-cancelled":
						runner.after = func() error {
							if change == "missing-runtime-drift" {
								if err := workspacegroup.Update(root, func(registry *workspacegroup.Registry) error {
									for i := range registry.Workspaces {
										if registry.Workspaces[i].ID == owner.ID {
											registry.Workspaces[i].Name = "changed"
										}
									}
									return nil
								}); err != nil {
									t.Fatal("fixture mutation failed", err)
								}
							}
							if change == "missing-runtime-cancelled" {
								cancel()
							}
							return errors.New("sandbox not found")
						}
					}
					if change == "anchor-appears" {
						runner.lookup = func() error {
							if err := os.Mkdir(owner.Path, 0755); err != nil {
								t.Fatal("fixture mutation failed", err)
							}
							return nil
						}
					}
					result, err := executeManagedSandbox(ctx, runner, target, service, mode)
					success := change == "missing-anchor" || change == "missing-runtime"
					if (err == nil) != success || success && len(result.Targets) != 1 || !success && len(result.Targets) != 0 {
						t.Fatal("unsafe missing/failure outcome", change, result, err)
					}
					if strings.Contains(change, "cancelled") && !errors.Is(err, context.Canceled) {
						t.Fatal("cancellation lost", err)
					}
					if change == "provider-error" && !errors.Is(err, failure) {
						t.Fatal("provider cause lost", err)
					}
					if (change == "pre-cancelled" || change == "lookup-error" || change == "anchor-appears") && len(runner.calls) != 0 {
						t.Fatal("unsafe early native effects", runner.calls)
					}
					assertRuntimeAdmissionReleased(t, owner)
				})
			}
		}
	}
}
