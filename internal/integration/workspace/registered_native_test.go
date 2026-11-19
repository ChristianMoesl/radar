package workspace

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"radar/internal/integration/tmux/sessionlease"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/workspacelease"
)

func testWorkspaceSession(t *testing.T, name string) *workspaceSession {
	t.Helper()
	session := newWorkspaceSession(name)
	t.Cleanup(session.close)
	return session
}

func testPaneSession(t *testing.T, runner Runner, name string) *workspaceSession {
	t.Helper()
	session := testWorkspaceSession(t, name)
	if fake, ok := runner.(*paneGateRunner); ok {
		if fake.native.nativeCreated == nil {
			fake.native.nativeCreated = map[string]string{}
		}
		fake.native.nativeCreated[name] = "$9"
		session.identity = sessionlease.Identity{ServerPID: "12", Created: "34", ID: "$9", Name: name}
		session.selected = true
		return session
	}
	if err := session.admit(context.Background(), runner); err != nil {
		t.Fatal(err)
	}
	return session
}

func registeredRecoveryFixture(t *testing.T) (*layoutNativeRunner, Runner, Workspace) {
	t.Helper()
	root := configureWorkspaceRoot(t)
	created, err := Create(context.Background(), &fakeRunner{}, CreateOptions{Name: "Plan", SessionName: "workspace", WorkspaceRoot: root, NoteAuthor: testNoteAuthor(t), Tmux: layoutAdmissionConfig()})
	if err != nil {
		t.Fatal(err)
	}
	native, runner, _ := standaloneRecoveryFixture(t)
	return native, runner, created
}

func TestRegisteredRecoveryAdmitsBeforeReadiness(t *testing.T) {
	withWorkspaceGOOS(t, "darwin")
	native, base, created := registeredRecoveryFixture(t)
	root, group, _, err := RegisteredWorkspace(created.Path, "")
	if err != nil {
		t.Fatal(err)
	}
	group.Sandbox = &workspacegroup.Sandbox{Name: "fixture-sandbox", Agent: "shell", Mounts: []string{created.Path}, ReadyCommand: []string{"ready"}}
	if err := registerWorkspace(root, group); err != nil {
		t.Fatal(err)
	}
	release, err := workspacelease.AcquireTmux("name:workspace", true)
	if err != nil {
		t.Fatal(err)
	}
	ready := false
	runner := &readyRunner{Runner: base, ready: func(context.Context) (string, error) { ready = true; return "", nil }}
	_, err = OpenRegisteredWorkspace(context.Background(), runner, created.Path, true)
	release()
	if err == nil || !strings.Contains(err.Error(), "tmux session is in use") || ready {
		t.Fatal("registered cleanup conflict reached readiness", ready, err)
	}
	assertNotCalledContains(t, runner.calls, "sbx", "create")
	assertNotCalledContains(t, native.calls, "tmux", "new-session")
	assertLayoutAdmissionReleased(t)
}

func TestRegisteredRecoveryRetainsMissingNameAcrossReadiness(t *testing.T) {
	for _, outcome := range []string{"success", "failure", "cancelled"} {
		t.Run(outcome, func(t *testing.T) {
			withWorkspaceGOOS(t, "darwin")
			native, base, created := registeredRecoveryFixture(t)
			root, group, _, err := RegisteredWorkspace(created.Path, "")
			if err != nil {
				t.Fatal(err)
			}
			group.Sandbox = &workspacegroup.Sandbox{Name: "fixture-sandbox", Agent: "shell", Mounts: []string{created.Path}, ReadyCommand: []string{"ready"}}
			if err := registerWorkspace(root, group); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			checked := false
			runner := &readyRunner{Runner: base, ready: func(ctx context.Context) (string, error) {
				checked = true
				if release, err := workspacelease.AcquireTmux("name:workspace", true); err == nil {
					release()
					t.Error("registered readiness dropped missing-name admission")
				}
				if outcome == "failure" {
					return "", errors.New("fixture readiness failed")
				}
				if outcome == "cancelled" {
					cancel()
					return "", ctx.Err()
				}
				return "", nil
			}}
			_, err = OpenRegisteredWorkspace(ctx, runner, created.Path, true)
			if !checked || (err != nil) != (outcome != "success") {
				t.Fatal("registered readiness outcome", checked, err)
			}
			assertNotCalledContains(t, native.calls, "tmux", "kill-session")
			assertLayoutAdmissionReleased(t)
		})
	}
}

func TestRegisteredRecoveryPinsRuntimeThroughOuterReturn(t *testing.T) {
	for _, route := range []string{"open", "create-existing"} {
		for _, outcome := range []string{"success", "switch-error", "cancelled", "rename-reuse", "replacement"} {
			t.Run(route+"/"+outcome, func(t *testing.T) {
				native, runner, created := registeredRecoveryFixture(t)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				listed, switched := false, false
				native.effect = func(ctx context.Context, args []string) error {
					if args[0] == "select-pane" {
						id := native.sessions["$9"]
						if outcome == "rename-reuse" {
							id.Name = "renamed-runtime"
							replacement := sessionlease.Identity{ServerPID: "12", Created: "99", ID: "$11", Name: "workspace"}
							native.sessions[replacement.Name], native.sessions[replacement.ID] = replacement, replacement
							native.sessions[id.Name], native.sessions[id.ID] = id, id
						}
						if outcome == "replacement" {
							id.Created = "99"
							native.sessions[id.Name], native.sessions[id.ID] = id, id
						}
					}
					if args[0] != "list-panes" && args[0] != "switch-client" {
						return nil
					}
					if args[0] == "list-panes" {
						listed = true
						if args[3] != "$9" {
							t.Error("pane release followed reusable name", args)
						}
					} else {
						switched = true
						if args[2] != "$9" {
							t.Error("switch followed reusable name", args)
						}
					}
					for _, key := range []string{"name:workspace", "runtime:tmux:session:12:34:$9"} {
						if release, err := workspacelease.AcquireTmux(key, true); err == nil {
							release()
							t.Error("registered runtime dropped admission", key)
						}
					}
					if args[0] == "switch-client" {
						if outcome == "switch-error" {
							return errors.New("fixture switch failed")
						}
						if outcome == "cancelled" {
							cancel()
							return ctx.Err()
						}
					}
					return nil
				}
				var err error
				if route == "open" {
					_, err = OpenRegisteredWorkspace(ctx, runner, created.Path, true)
				} else {
					root, group, _, lookupErr := RegisteredWorkspace(created.Path, "")
					if lookupErr != nil {
						t.Fatal(lookupErr)
					}
					_, err = Create(ctx, runner, CreateOptions{WorkspaceRoot: root, TaskLinkingKey: group.NoteKey(), Switch: true})
				}
				wantErr := outcome == "switch-error" || outcome == "cancelled" || outcome == "replacement"
				if (err != nil) != wantErr || switched == (outcome == "replacement") || listed == (outcome == "replacement") {
					t.Fatal("registered recovery outcome", listed, switched, err)
				}
				assertNotCalledContains(t, native.calls, "tmux", "kill-session")
				assertLayoutAdmissionReleased(t)
			})
		}
	}
}

func TestRegisteredRecoveryNativeRenameReuseKeepsOriginal(t *testing.T) {
	for _, sw := range []bool{false, true} {
		t.Run(fmt.Sprint("switch=", sw), func(t *testing.T) {
			_, _, created := registeredRecoveryFixture(t)
			native := newIsolatedPaneTmux(t, created.Path)
			var id, expected string
			switched := false
			runner := layoutNativeHook{isolatedPaneTmux: native, afterCreate: func(output string) error {
				expected = "tmux:session:" + strings.Fields(output)[0]
				id = strings.Split(strings.Fields(output)[0], ":")[2]
				if _, err := native.Run(context.Background(), created.Path, "tmux", "rename-session", "-t", id, "renamed-runtime"); err != nil {
					return err
				}
				_, err := native.Run(context.Background(), created.Path, "tmux", "new-session", "-d", "-s", "workspace", "sleep 120")
				return err
			}, before: func(args []string) error {
				if args[0] == "list-panes" && args[3] != id {
					t.Error("native pane release followed reused name", args)
				}
				if args[0] == "switch-client" {
					switched = true
					if args[2] != id {
						t.Error("native registered switch followed reused name", args)
					}
					for _, key := range []string{"name:workspace", "name:renamed-runtime", "runtime:" + expected} {
						if release, err := workspacelease.AcquireTmux(key, true); err == nil {
							release()
							t.Error("native registered handoff dropped admission", key)
						}
					}
					return errors.New("fixture prevents attaching test client")
				}
				return nil
			}}
			_, err := OpenRegisteredWorkspace(context.Background(), runner, created.Path, sw)
			if switched != sw || (err != nil) != sw {
				t.Fatal("native registered recovery result", switched, err)
			}
			for _, selector := range []string{id, "=workspace", "=renamed-runtime"} {
				if _, err := native.Run(context.Background(), created.Path, "tmux", "has-session", "-t", selector); err != nil {
					t.Fatal("lost runtime", selector, err)
				}
			}
			for _, key := range []string{"name:workspace", "name:renamed-runtime", "runtime:" + expected} {
				release, err := workspacelease.AcquireTmux(key, true)
				if err != nil {
					t.Fatal("native admission leaked", key, err)
				}
				release()
			}
		})
	}
}

func TestRegisteredSessionGateAndSetupEffectsRevalidate(t *testing.T) {
	for _, operation := range []string{"gates", "setup"} {
		for _, outcome := range []string{"success", "failure", "cancelled", "replacement", "rename-reuse"} {
			t.Run(operation+"/"+outcome, func(t *testing.T) {
				native := layoutNativeFixture(t)
				id := sessionlease.Identity{ServerPID: "12", Created: "34", ID: "$9", Name: "workspace"}
				native.sessions[id.Name], native.sessions[id.ID] = id, id
				native.pending = map[string]string{"%1": "radar-pane-first", "%2": "radar-pane-second"}
				session := testWorkspaceSession(t, "workspace")
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if err := session.admit(ctx, native); err != nil {
					t.Fatal(err)
				}
				later := 0
				native.effect = func(ctx context.Context, args []string) error {
					for _, key := range []string{"name:workspace", "runtime:" + id.Key()} {
						if release, err := workspacelease.AcquireTmux(key, true); err == nil {
							release()
							t.Error("effect dropped native ownership", key, args)
						}
					}
					if args[0] == "list-panes" || args[0] == "new-window" {
						if args[0] == "list-panes" && args[3] != id.ID || args[0] == "new-window" && args[2] != id.ID+":" {
							t.Error("effect followed reusable name", args)
						}
						current := id
						if outcome == "replacement" {
							current.Created = "99"
							native.sessions[current.Name], native.sessions[current.ID] = current, current
						}
						if outcome == "rename-reuse" {
							current.Name = "renamed-runtime"
							replacement := sessionlease.Identity{ServerPID: "12", Created: "99", ID: "$11", Name: "workspace"}
							native.sessions[replacement.Name], native.sessions[replacement.ID] = replacement, replacement
							native.sessions[current.Name], native.sessions[current.ID] = current, current
						}
						return nil
					}
					later++
					if outcome == "failure" {
						return errors.New("fixture native effect failed")
					}
					if outcome == "cancelled" {
						cancel()
						return ctx.Err()
					}
					return nil
				}
				var err error
				if operation == "gates" {
					err = releaseWorkspacePanes(ctx, native, "/workspace", session)
				} else {
					err = scheduleMemberSetup(ctx, native, "/workspace", "", "repository", []string{"echo setup"}, session)
				}
				wantErr := outcome == "failure" || outcome == "cancelled" || outcome == "replacement"
				if (err != nil) != wantErr || (later == 0) != (outcome == "replacement") {
					t.Fatal("effect revalidation outcome", later, err)
				}
				session.close()
				assertLayoutAdmissionReleased(t)
			})
		}
	}
}

func TestRegisteredSessionFailureEffectsVerifyOriginalIndependentlyOfCancellation(t *testing.T) {
	for _, outcome := range []string{"original", "rename-reuse", "replacement", "missing", "unidentified", "cleanup-conflict"} {
		t.Run(outcome, func(t *testing.T) {
			native := layoutNativeFixture(t)
			id := sessionlease.Identity{ServerPID: "12", Created: "34", ID: "$9", Name: "workspace"}
			native.sessions[id.Name], native.sessions[id.ID] = id, id
			session := testWorkspaceSession(t, "workspace")
			if err := session.admit(context.Background(), native); err != nil {
				t.Fatal(err)
			}
			session.close()
			switch outcome {
			case "rename-reuse":
				id.Name = "renamed-runtime"
				native.sessions[id.Name], native.sessions[id.ID] = id, id
				native.sessions["workspace"] = sessionlease.Identity{ServerPID: "12", Created: "99", ID: "$11", Name: "workspace"}
			case "replacement":
				id.Created = "99"
				native.sessions[id.Name], native.sessions[id.ID] = id, id
			case "missing":
				delete(native.sessions, id.ID)
			case "unidentified":
				session.identity = sessionlease.Identity{}
			case "cleanup-conflict":
				release, err := workspacelease.AcquireTmux("runtime:"+id.Key(), true)
				if err != nil {
					t.Fatal(err)
				}
				defer release()
			}
			effected := false
			native.effect = func(ctx context.Context, args []string) error {
				if args[0] == "kill-session" {
					effected = true
					if ctx.Err() != nil || args[2] != "$9" {
						t.Error("unsafe failure effect", ctx.Err(), args)
					}
				}
				return nil
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			session.afterFailure(ctx, native, "/workspace", time.Second, "kill-session", "-t", session.identity.ID)
			if effected != (outcome == "original" || outcome == "rename-reuse") {
				t.Fatal("unsafe rollback admission outcome", effected)
			}
		})
	}
}

func TestRegisteredEarlyCreationRetainsNativeOwnershipThroughProvisioningAndSetup(t *testing.T) {
	for _, sandbox := range []bool{false, true} {
		for _, outcome := range []string{"success", "rename-reuse", "replacement"} {
			t.Run(fmt.Sprint("sandbox=", sandbox, "/", outcome), func(t *testing.T) {
				base, options := earlyCreationFixture(t, 1)
				options.Sandbox = sandbox
				base.create, base.ready = nil, nil
				checked, switched, mutated := 0, false, false
				runner := activationRunner{Runner: base, run: func(ctx context.Context, cwd, name string, args ...string) (string, error) {
					if name == "tmux" && args[0] == "switch-client" {
						switched = true
						if args[2] != "$9" {
							t.Error("creation switch followed name", args)
						}
					}
					if name == "sbx" && (args[0] == "create" || args[0] == "exec" || args[0] == "ports") || name == "tmux" && args[0] == "new-window" && strings.Contains(strings.Join(args, " "), "-n setup") {
						if base.fakeRunner.nativeCreated[options.SessionName] != "" || base.fakeRunner.nativeCreated["renamed-runtime"] != "" {
							checked++
							for _, key := range []string{"name:" + options.SessionName, "runtime:tmux:session:12:34:$9"} {
								if release, err := workspacelease.AcquireTmux(key, true); err == nil {
									release()
									t.Error("creation handoff dropped ownership", key, name, args)
								}
							}
						}
					}
					output, err := base.Run(ctx, cwd, name, args...)
					// The final layout effect precedes every early switch/provisioning
					// and the later sandbox-less setup/final-switch handoff.
					if !mutated && name == "tmux" && args[0] == "select-pane" {
						mutated = true
						if outcome == "rename-reuse" {
							base.fakeRunner.nativeCreated["renamed-runtime"] = "$9"
							base.fakeRunner.nativeCreated[options.SessionName] = "$11"
						}
						if outcome == "replacement" {
							base.fakeRunner.nativeCreated[options.SessionName] = "$11"
						}
					}
					return output, err
				}}
				_, err := Create(context.Background(), runner, options)
				if (err != nil) != (outcome == "replacement") || switched == (outcome == "replacement") || checked == 0 && outcome != "replacement" {
					t.Fatal("creation ownership outcome", checked, switched, err)
				}
				for _, key := range []string{"name:" + options.SessionName, "name:renamed-runtime", "runtime:tmux:session:12:34:$9"} {
					release, err := workspacelease.AcquireTmux(key, true)
					if err != nil {
						t.Fatal("creation leaked ownership", key, err)
					}
					release()
				}
			})
		}
	}
}
