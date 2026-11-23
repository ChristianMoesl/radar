package sbx

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"radar/internal/cleanup"
	"radar/internal/integration"
	"radar/internal/integration/workspace"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
	"radar/internal/workspacelease"
)

type admittedFixtureSource struct {
	Source
	runner workspace.Runner
}

func (s admittedFixtureSource) Cleanup(ctx context.Context, req integration.CleanupRequest) (protocol.CleanupTarget, error) {
	return cleanupWithRunner(ctx, s.runner, req, false)
}
func (s admittedFixtureSource) AdmitCleanup(_ context.Context, targets []protocol.CleanupTarget, mode integration.CleanupMode) (integration.CleanupProvider, func(), error) {
	return admitSandboxCleanup(s.runner, targets, mode), func() {}, nil
}

type admissionFixtureRunner struct {
	owner       workspacegroup.Workspace
	calls       []string
	removed     bool
	stopped     bool
	hook        func(string) error
	replacement bool
}

func (*admissionFixtureRunner) LookPath(string) error { return nil }
func (r *admissionFixtureRunner) Run(_ context.Context, _ string, name string, args ...string) (string, error) {
	command := name + " " + strings.Join(args, " ")
	r.calls = append(r.calls, command)
	if r.hook != nil {
		if err := r.hook(command); err != nil {
			return "", err
		}
	}
	if len(args) > 0 && args[0] == "ls" {
		if r.removed && !r.replacement {
			return "[]", nil
		}
		id := "lifetime-1"
		if r.removed {
			id = "replacement"
		}
		status := "running"
		if r.stopped {
			status = "stopped"
		}
		return fmt.Sprintf(`[{"id":%q,"name":%q,"status":%q,"workspaces":[%q]}]`, id, r.owner.Sandbox.Name, status, r.owner.Path), nil
	}
	if len(args) > 0 && args[0] == "rm" {
		r.removed = true
	}
	if len(args) > 0 && args[0] == "stop" {
		r.stopped = true
	}
	return "", nil
}

func runtimeAdmissionFixture(t *testing.T) (string, workspacegroup.Workspace, workspacegroup.Workspace, protocol.CleanupTarget) {
	t.Helper()
	home := t.TempDir()
	root := filepath.Join(home, "workspaces")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("RADAR_SOCKET", filepath.Join(home, "run", "radar.sock"))
	path := filepath.Join(home, "config", "radar", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, fmt.Appendf(nil, "workspace: {root_dir: %q}\n", root), 0600); err != nil {
		t.Fatal(err)
	}
	owner := workspacegroup.Workspace{ID: workspacegroup.ID(filepath.Join(root, "owner")), Name: "owner", Path: filepath.Join(root, "owner"), SessionName: "owner-session", NotePath: filepath.Join(home, "canonical.md"), Sandbox: &workspacegroup.Sandbox{Name: "owner-sandbox", Agent: "shell"}, Members: []workspacegroup.Member{}}
	other := workspacegroup.Workspace{ID: workspacegroup.ID(filepath.Join(root, "other")), Name: "other", Path: filepath.Join(root, "other"), Members: []workspacegroup.Member{}}
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
	target := protocol.CleanupTarget{Source: "sbx", Kind: "sandbox", ResourceID: owner.Sandbox.Name, WorkspaceID: owner.ID, Path: owner.Path, Operation: map[string]string{"session_name": owner.SessionName, "sandbox_name": owner.Sandbox.Name, "runtime_id": "lifetime-1"}}
	return root, owner, other, target
}

func assertRuntimeAdmissionReleased(t *testing.T, owner workspacegroup.Workspace) {
	t.Helper()
	for _, admit := range []func() (func(), error){func() (func(), error) { return workspacelease.Acquire(owner.Path, true) }, func() (func(), error) { return workspacelease.AcquireSandbox(owner.Sandbox.Name, true) }} {
		release, err := admit()
		if err != nil {
			t.Fatal("admission leaked", err)
		}
		release()
	}
}

func TestRegisteredSandboxIORevalidatesEveryOutcome(t *testing.T) {
	for _, service := range []bool{false, true} {
		for _, mode := range []integration.CleanupMode{integration.CleanupSafe, integration.CleanupConfirmed, integration.CleanupExpired, integration.CleanupRetire} {
			cases := []string{"owner", "removed-owner", "member", "canonical-note", "sandbox-config", "configured-root", "anchor-replaced", "root-replaced", "cancelled", "lookup-error", "unrelated-update"}
			if mode != integration.CleanupRetire {
				cases = append(cases, "removal-error", "absence-error", "post-removal-drift", "post-removal-cancel", "replacement")
			} else {
				cases = append(cases, "stop-error", "post-stop-drift")
			}
			for _, change := range cases {
				t.Run(fmt.Sprintf("service=%t/%v/%s", service, mode, change), func(t *testing.T) {
					root, owner, other, target := runtimeAdmissionFixture(t)
					originalOperation := maps.Clone(target.Operation)
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					failure := errors.New("fixture provider failure")
					runner := &admissionFixtureRunner{owner: owner, replacement: change == "replacement"}
					changed := false
					mutate := func() error {
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
								case "owner", "post-removal-drift", "post-stop-drift":
									g.Name = "changed"
								case "removed-owner":
									registry.Workspaces = append(registry.Workspaces[:i], registry.Workspaces[i+1:]...)
									return nil
								case "member":
									g.Members = []workspacegroup.Member{{Repository: filepath.Join(root, "repository"), Path: filepath.Join(owner.Path, "member"), Branch: "fixture"}}
								case "canonical-note":
									g.NotePath = filepath.Join(root, "new-canonical.md")
								case "sandbox-config":
									g.Sandbox.Agent = "different"
								}
							}
							return nil
						})
					}
					runner.hook = func(command string) error {
						if command == "sbx ls --json" && !changed {
							changed = true
							switch change {
							case "cancelled":
								cancel()
							case "lookup-error":
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
							case "post-removal-drift", "post-removal-cancel", "post-stop-drift":
								return nil
							default:
								return mutate()
							}
						}
						if command == "sbx rm --force lifetime-1" {
							if change == "removal-error" {
								return failure
							}
							if change == "post-removal-cancel" {
								cancel()
							}
							if change == "post-removal-drift" {
								return mutate()
							}
						}
						if command == "sbx stop lifetime-1" {
							if change == "stop-error" {
								return failure
							}
							if change == "post-stop-drift" {
								return mutate()
							}
						}
						if runner.removed && command == "sbx ls --json" && change == "absence-error" {
							return failure
						}
						return nil
					}
					var err error
					if service {
						_, err = cleanup.New([]integration.CleanupProvider{admittedFixtureSource{runner: runner}}).Execute(ctx, protocol.CleanupPreview{Targets: []protocol.CleanupTarget{target}}, cleanup.ExecuteOptions{Mode: mode})
					} else {
						_, err = cleanupWithRunner(ctx, runner, integration.CleanupRequest{Target: target, Mode: mode}, false)
					}
					if !maps.Equal(target.Operation, originalOperation) {
						t.Fatal("cleanup mutated the caller's preview operation")
					}
					if !changed {
						t.Fatal("actual runtime lookup not reached", err)
					}
					if (err == nil) != (change == "unrelated-update") {
						t.Fatal("unexpected outcome", change, err)
					}
					if strings.Contains(change, "cancel") && !errors.Is(err, context.Canceled) {
						t.Fatal("cancellation lost", err)
					}
					if strings.Contains(change, "error") && !errors.Is(err, failure) {
						t.Fatal("provider error lost", err)
					}
					log := strings.Join(runner.calls, "\n")
					partial := strings.HasPrefix(change, "post-") || change == "absence-error" || change == "replacement"
					if !partial && change != "unrelated-update" && (strings.Contains(log, "sbx rm") || strings.Contains(log, "sbx stop")) && change != "removal-error" && change != "stop-error" {
						t.Fatal("stale authority allowed effects", log)
					}
					registry, loadErr := workspacegroup.Load(root)
					if loadErr != nil {
						t.Fatal(loadErr)
					}
					_, found := workspacegroup.FindByID(registry, owner.ID)
					if found == (change == "removed-owner") {
						t.Fatal("final owner reference incorrectly changed")
					}
					actualOther, found := workspacegroup.FindByID(registry, other.ID)
					if !found {
						t.Fatal("unrelated owner lost")
					}
					if change == "unrelated-update" && actualOther.Model != "unrelated-update" {
						t.Fatal("unrelated metadata overwritten")
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

func TestDirectRegisteredSandboxAdmissionRejectsActiveOwners(t *testing.T) {
	for _, kind := range []string{"anchor", "alias", "sandbox"} {
		for _, mode := range []integration.CleanupMode{integration.CleanupSafe, integration.CleanupConfirmed, integration.CleanupExpired, integration.CleanupRetire} {
			t.Run(fmt.Sprintf("%s/%v", kind, mode), func(t *testing.T) {
				_, owner, _, target := runtimeAdmissionFixture(t)
				path := owner.Path
				if kind == "alias" {
					path = filepath.Join(t.TempDir(), "alias")
					if err := os.Symlink(owner.Path, path); err != nil {
						t.Fatal(err)
					}
				}
				var release func()
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
				runner := &admissionFixtureRunner{owner: owner}
				_, err = cleanupWithRunner(context.Background(), runner, integration.CleanupRequest{Target: target, Mode: mode}, false)
				if err == nil || !strings.Contains(err.Error(), "in use by another operation") || len(runner.calls) != 0 {
					t.Fatal("active resource admission bypassed", err, runner.calls)
				}
				release()
				release = nil
				assertRuntimeAdmissionReleased(t, owner)
			})
		}
	}
}

func TestSandboxServiceBindingRejectsDifferentTargetOrMode(t *testing.T) {
	for _, change := range []string{"target", "operation", "mode"} {
		t.Run(change, func(t *testing.T) {
			_, owner, _, target := runtimeAdmissionFixture(t)
			runner := &admissionFixtureRunner{owner: owner}
			bound := admitSandboxCleanup(runner, []protocol.CleanupTarget{target}, integration.CleanupConfirmed)
			changed := target
			changed.Operation = maps.Clone(target.Operation)
			mode := integration.CleanupConfirmed
			switch change {
			case "target":
				changed.Path += "-different"
			case "operation":
				changed.Operation["runtime_id"] = "replacement"
			case "mode":
				mode = integration.CleanupRetire
			}
			_, err := bound.Cleanup(context.Background(), integration.CleanupRequest{Target: changed, Mode: mode})
			if err == nil || len(runner.calls) != 0 {
				t.Fatal("binding allowed unadmitted target", err, runner.calls)
			}
		})
	}
}

func TestRegisteredSandboxRejectsNonSandboxTarget(t *testing.T) {
	_, owner, _, target := runtimeAdmissionFixture(t)
	target.Source, target.Kind, target.ResourceID = "workspace", "workspace", owner.ID
	runner := &admissionFixtureRunner{owner: owner}
	if _, err := cleanupRegisteredSandbox(context.Background(), runner, target, true); err == nil || len(runner.calls) != 0 {
		t.Fatal("non-sandbox target reached runtime I/O", err, runner.calls)
	}
}
