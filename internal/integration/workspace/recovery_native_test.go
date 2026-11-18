package workspace

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	sessionlayout "radar/internal/integration/tmux/layout"
	"radar/internal/integration/tmux/sessionlease"
	"radar/internal/workspacelease"
)

func standaloneRecoveryFixture(t *testing.T) (*layoutNativeRunner, Runner, CreateSessionOptions) {
	t.Helper()
	path, _ := sessionLeasePaths(t, false)
	native := layoutNativeFixture(t)
	base := &fakeRunner{}
	runner := activationRunner{Runner: base, run: func(ctx context.Context, cwd, name string, args ...string) (string, error) {
		if name != "tmux" {
			return base.Run(ctx, cwd, name, args...)
		}
		if args[0] == "has-session" {
			if native.sessions["workspace"].ID == "" {
				return "", errors.New("missing")
			}
			return "", nil
		}
		return native.Run(ctx, cwd, name, args...)
	}}
	return native, runner, CreateSessionOptions{Path: path, SessionName: "workspace", Switch: true, Tmux: layoutAdmissionConfig()}
}

func TestStandaloneRecoveryAdmitsMissingNameBeforeSandboxReadiness(t *testing.T) {
	withWorkspaceGOOS(t, "darwin")
	native, base, options := standaloneRecoveryFixture(t)
	options.Sandbox, options.SandboxName, options.SandboxReadyCommand = true, "fixture-sandbox", []string{"ready"}
	release, err := workspacelease.AcquireTmux("name:workspace", true)
	if err != nil {
		t.Fatal(err)
	}
	ready := false
	runner := &readyRunner{Runner: base, ready: func(context.Context) (string, error) { ready = true; return "", nil }}
	_, err = CreateSessionWithOptions(context.Background(), runner, options)
	release()
	if err == nil || !strings.Contains(err.Error(), "tmux session is in use") || ready {
		t.Fatal("missing-name cleanup did not block before readiness", ready, err)
	}
	assertNotCalledContains(t, runner.calls, "sbx", "create")
	assertNotCalledContains(t, native.calls, "tmux", "new-session")
	assertLayoutAdmissionReleased(t)
}

func TestStandaloneRecoveryHoldsMissingNameAcrossReadiness(t *testing.T) {
	for _, outcome := range []string{"success", "failure", "cancelled"} {
		t.Run(outcome, func(t *testing.T) {
			withWorkspaceGOOS(t, "darwin")
			_, base, options := standaloneRecoveryFixture(t)
			options.Sandbox, options.SandboxName, options.SandboxReadyCommand = true, "fixture-sandbox", []string{"ready"}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			checked := false
			runner := &readyRunner{Runner: base, ready: func(ctx context.Context) (string, error) {
				checked = true
				if release, err := workspacelease.AcquireTmux("name:workspace", true); err == nil {
					release()
					t.Error("missing native name unprotected during readiness")
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
			_, err := CreateSessionWithOptions(ctx, runner, options)
			if !checked || (err != nil) != (outcome != "success") {
				t.Fatal("readiness outcome", checked, err)
			}
			assertLayoutAdmissionReleased(t)
		})
	}
}

func TestStandaloneRecoveryRetainsCreationThroughEverySwitchOutcome(t *testing.T) {
	for _, outcome := range []string{"success", "failure", "cancelled", "rename-reuse", "replacement"} {
		t.Run(outcome, func(t *testing.T) {
			native, runner, options := standaloneRecoveryFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switched := false
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
				if args[0] != "switch-client" {
					return nil
				}
				switched = true
				if args[2] != "$9" {
					t.Error("recovery followed reusable name", args)
				}
				for _, key := range []string{"name:workspace", "runtime:tmux:session:12:34:$9"} {
					if release, err := workspacelease.AcquireTmux(key, true); err == nil {
						release()
						t.Error("creation handoff dropped admission", key)
					}
				}
				if outcome == "failure" {
					return errors.New("fixture switch failed")
				}
				if outcome == "cancelled" {
					cancel()
					return ctx.Err()
				}
				return nil
			}
			_, err := CreateSessionWithOptions(ctx, runner, options)
			wantErr := outcome == "failure" || outcome == "cancelled" || outcome == "replacement"
			if (err != nil) != wantErr || switched == (outcome == "replacement") {
				t.Fatal("recovery switch outcome", switched, err)
			}
			assertNotCalledContains(t, native.calls, "tmux", "kill-session")
			assertLayoutAdmissionReleased(t)
		})
	}
}

func TestStandaloneRecoveryNativeRenameReuseKeepsOriginal(t *testing.T) {
	for _, sw := range []bool{false, true} {
		t.Run(fmt.Sprint("switch=", sw), func(t *testing.T) {
			path, _ := sessionLeasePaths(t, false)
			native := newIsolatedPaneTmux(t, path)
			var id, expected string
			switched := false
			runner := layoutNativeHook{isolatedPaneTmux: native, afterCreate: func(output string) error {
				expected = "tmux:session:" + strings.Fields(output)[0]
				id = strings.Split(strings.Fields(output)[0], ":")[2]
				if _, err := native.Run(context.Background(), path, "tmux", "rename-session", "-t", id, "renamed-runtime"); err != nil {
					return err
				}
				_, err := native.Run(context.Background(), path, "tmux", "new-session", "-d", "-s", "workspace", "sleep 120")
				return err
			}, before: func(args []string) error {
				if args[0] == "switch-client" {
					switched = true
					if args[2] != id {
						t.Error("native recovery followed reused name", args)
					}
					for _, key := range []string{"name:workspace", "name:renamed-runtime", "runtime:" + expected} {
						if release, err := workspacelease.AcquireTmux(key, true); err == nil {
							release()
							t.Error("native handoff released admission", key)
						}
					}
					return errors.New("fixture prevents attaching a test client")
				}
				return nil
			}}
			cfg := sessionlayout.Config{Windows: []sessionlayout.Window{{Name: "pi", Panes: []sessionlayout.Pane{{Command: "sleep 120 # " + sessionlayout.PiArgsPlaceholder}}}}}
			_, err := CreateSessionWithOptions(context.Background(), runner, CreateSessionOptions{Path: path, SessionName: "workspace", Switch: sw, Tmux: cfg})
			if switched != sw || (err != nil) != sw {
				t.Fatal("native recovery result", switched, err)
			}
			for _, selector := range []string{id, "=workspace", "=renamed-runtime"} {
				if _, err := native.Run(context.Background(), path, "tmux", "has-session", "-t", selector); err != nil {
					t.Fatal("native recovery lost a runtime", selector, err)
				}
			}
			for _, key := range []string{"name:workspace", "name:renamed-runtime", "runtime:" + expected} {
				release, err := workspacelease.AcquireTmux(key, true)
				if err != nil {
					t.Fatal("native recovery leaked admission", key, err)
				}
				release()
			}
			assertLayoutAdmissionReleased(t)
		})
	}
}
