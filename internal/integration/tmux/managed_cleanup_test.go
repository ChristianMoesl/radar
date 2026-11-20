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
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
	"radar/internal/workspacelease"
)

func managedSessionFixture(t *testing.T) (string, workspacegroup.Workspace, workspacegroup.Workspace, protocol.CleanupTarget, *retirementOutcomeRunner) {
	t.Helper()
	root, owner, other, target, runner := retirementOverlapFixture(t)
	target.SourceRefID = target.Operation["runtime_id"]
	target.Title, target.ResourceID = owner.SessionName, "$7"
	target.Path, target.WorkspaceID = "", ""
	return root, owner, other, target, runner
}

func executeManagedSession(ctx context.Context, runner *retirementOutcomeRunner, target protocol.CleanupTarget, service bool) (protocol.CleanupTarget, error) {
	if service {
		result, err := cleanup.New([]integration.CleanupProvider{retirementOutcomeProvider{runner: runner}}).Execute(ctx, protocol.CleanupPreview{Targets: []protocol.CleanupTarget{target}}, cleanup.ExecuteOptions{})
		if len(result.Targets) != 0 {
			return result.Targets[0], err
		}
		return protocol.CleanupTarget{}, err
	}
	bound, release, err := admitCleanup(ctx, runner, []protocol.CleanupTarget{target}, integration.CleanupSafe, false)
	if err != nil {
		return target, err
	}
	defer release()
	runner.armed = true
	return bound.Cleanup(ctx, integration.CleanupRequest{Target: target})
}

func TestManagedSessionRevalidatesEveryOutcome(t *testing.T) {
	for _, service := range []bool{false, true} {
		for _, phase := range []string{"display-message", "kill-session"} {
			for _, change := range []string{"owner", "removed-owner", "member", "canonical-note", "sandbox-config", "configured-root", "anchor-replaced", "root-replaced", "cancelled", "provider-error", "unrelated-update"} {
				t.Run(fmt.Sprintf("service=%t/%s/%s", service, phase, change), func(t *testing.T) {
					root, owner, other, target, runner := managedSessionFixture(t)
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
					result, err := executeManagedSession(ctx, runner, target, service)
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
					assertManagedSessionAdmissionsReleased(t, owner, runner)
				})
			}
		}
	}
}

func TestManagedSessionDirectAndServiceRefuseActiveOwners(t *testing.T) {
	for _, service := range []bool{false, true} {
		for _, kind := range []string{"anchor", "alias", "durable-agent"} {
			t.Run(fmt.Sprintf("service=%t/%s", service, kind), func(t *testing.T) {
				_, owner, _, target, runner := managedSessionFixture(t)
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
				_, err := executeManagedSession(context.Background(), runner, target, service)
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
				assertManagedSessionAdmissionsReleased(t, owner, runner)
			})
		}
	}
}

type managedSessionProbe struct {
	Source
	run func() error
}

func (managedSessionProbe) Descriptor() integration.Descriptor {
	return integration.Descriptor{Name: "managed-probe"}
}
func (p managedSessionProbe) AdmitCleanup(context.Context, []protocol.CleanupTarget, integration.CleanupMode) (integration.CleanupProvider, func(), error) {
	return p, func() {}, nil
}
func (p managedSessionProbe) Cleanup(_ context.Context, req integration.CleanupRequest) (protocol.CleanupTarget, error) {
	return req.Target, p.run()
}

func TestManagedSessionRetainsFullAndDiscoveredBundleThroughLaterOutcomes(t *testing.T) {
	for _, full := range []bool{false, true} {
		for _, outcome := range []string{"success", "provider-error", "cancelled"} {
			t.Run(fmt.Sprintf("full=%t/%s", full, outcome), func(t *testing.T) {
				_, owner, _, target, runner := managedSessionFixture(t)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				failure := errors.New("fixture later provider failure")
				later := protocol.CleanupTarget{Source: "managed-probe"}
				if full {
					later.Path = filepath.Join(t.TempDir(), "owner-alias")
					if err := os.Symlink(owner.Path, later.Path); err != nil {
						t.Fatal(err)
					}
				}
				calls := 0
				probe := managedSessionProbe{run: func() error {
					calls++
					if release, err := workspacelease.Acquire(owner.Path, false); err == nil {
						release()
						t.Fatal("actual owner admission released before later provider")
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
				result, err := cleanup.New([]integration.CleanupProvider{retirementOutcomeProvider{runner: runner}, probe}).Execute(ctx, protocol.CleanupPreview{Targets: []protocol.CleanupTarget{target, later}}, cleanup.ExecuteOptions{})
				if (err != nil) != (outcome != "success") || calls != 1 || len(runner.base.killed) != 1 {
					t.Fatal("unexpected Service lifetime outcome", result, err, calls, runner.base.killed)
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
				assertManagedSessionAdmissionsReleased(t, owner, runner)
			})
		}
	}
}

func TestManagedSessionAlreadyAbsentRetainsFreshOwnerChecks(t *testing.T) {
	for _, service := range []bool{false, true} {
		for _, nameOnly := range []bool{false, true} {
			t.Run(fmt.Sprintf("service=%t/name-only=%t", service, nameOnly), func(t *testing.T) {
				_, owner, _, target, runner := managedSessionFixture(t)
				for alias, identity := range runner.base.sessions {
					if identity.ID == "$7" {
						delete(runner.base.sessions, alias)
					}
				}
				if nameOnly {
					target.ResourceID, target.Title, target.SourceRefID = owner.SessionName, "", ""
				}
				if _, err := executeManagedSession(context.Background(), runner, target, service); err != nil {
					t.Fatal("ordinary already-absent session cleanup changed", err)
				}
				if len(runner.base.killed) != 0 {
					t.Fatal("missing session caused native effects", runner.base.killed)
				}
				assertManagedSessionAdmissionsReleased(t, owner, runner)
			})
		}
	}
}

func TestManagedSessionMissingAnchorAndNativeLifetimeOutcomes(t *testing.T) {
	for _, service := range []bool{false, true} {
		for _, change := range []string{"missing-anchor", "missing-anchor-appears", "missing-runtime", "replacement", "wrong-name", "shared-name", "agent-appeared", "pre-cancelled"} {
			t.Run(fmt.Sprintf("service=%t/%s", service, change), func(t *testing.T) {
				root, owner, other, target, runner := managedSessionFixture(t)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if strings.HasPrefix(change, "missing-anchor") {
					if err := os.Remove(owner.Path); err != nil {
						t.Fatal(err)
					}
				}
				if change == "pre-cancelled" {
					cancel()
				}
				if change == "shared-name" {
					if err := workspacegroup.Update(root, func(registry *workspacegroup.Registry) error {
						for i := range registry.Workspaces {
							if registry.Workspaces[i].ID == other.ID {
								registry.Workspaces[i].SessionName = owner.SessionName
							}
						}
						return nil
					}); err != nil {
						t.Fatal(err)
					}
				}
				changed := false
				runner.hook = func(command string) error {
					if command != "display-message" || changed {
						return nil
					}
					changed = true
					switch change {
					case "missing-anchor-appears":
						if err := os.Mkdir(owner.Path, 0755); err != nil {
							t.Fatal("fixture mutation failed", err)
						}
					case "missing-runtime":
						for alias, identity := range runner.base.sessions {
							if identity.ID == "$7" {
								delete(runner.base.sessions, alias)
							}
						}
					case "replacement":
						identity := runner.base.sessions["$7"]
						identity.Created = "99"
						runner.base.sessions["$7"] = identity
					case "wrong-name":
						identity := runner.base.sessions["$7"]
						identity.Name = other.SessionName
						runner.base.sessions["$7"] = identity
					case "agent-appeared":
						dir := filepath.Join(os.Getenv("RADAR_AGENT_ROOT"), "late-agent")
						if err := os.MkdirAll(dir, 0700); err != nil {
							t.Fatal(err)
						}
						data, err := json.Marshal(agentservice.Registration{Version: 1, ID: "late-agent", Workspace: protocol.AgentWorkspaceBinding{ID: owner.ID, Path: owner.Path, Mode: "local"}})
						if err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(filepath.Join(dir, "registration.json"), data, 0600); err != nil {
							t.Fatal("fixture mutation failed", err)
						}
					}
					return nil
				}
				_, err := executeManagedSession(ctx, runner, target, service)
				if (err == nil) != (change == "missing-anchor") {
					t.Fatal("unsafe managed session outcome", change, err)
				}
				if change != "missing-anchor" && len(runner.base.killed) != 0 {
					t.Fatal("unsafe native effects", runner.base.killed)
				}
				if change == "pre-cancelled" && !errors.Is(err, context.Canceled) {
					t.Fatal("cancellation hidden", err)
				}
				assertManagedSessionAdmissionsReleased(t, owner, runner)
			})
		}
	}
}

// Probe the real keys directly: an intentionally absent/replaced runtime is not
// a provider fixture failure and must not be mistaken for a lease leak.
func assertManagedSessionAdmissionsReleased(t *testing.T, owner workspacegroup.Workspace, _ *retirementOutcomeRunner) {
	t.Helper()
	release, err := workspacelease.Acquire(owner.Path, true)
	if err != nil {
		t.Fatal("actual owner admission leaked", err)
	}
	release()
	for _, key := range []string{"name:" + owner.SessionName, "runtime:tmux:session:12:34:$7", "runtime:tmux:session:12:99:$7"} {
		release, err := workspacelease.AcquireTmux(key, true)
		if err != nil {
			t.Fatal("native admission leaked", key, err)
		}
		release()
	}
}
