package tmux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"radar/internal/cleanup"
	"radar/internal/integration"
	"radar/internal/integration/tmux/sessionlease"
	"radar/internal/operationlock"
	"radar/internal/protocol"
	"radar/internal/workspacelease"
)

type openSessionRunner struct {
	*admissionRunner
	effects       [][]string
	effect        func(context.Context, []string) error
	createdOutput string
	afterCreate   func()
	afterWindow   func()
}

func (r *openSessionRunner) Run(ctx context.Context, cwd, name string, args ...string) (string, error) {
	if args[0] != "new-session" && args[0] != "new-window" && args[0] != "switch-client" {
		return r.admissionRunner.Run(ctx, cwd, name, args...)
	}
	r.effects = append(r.effects, append([]string(nil), args...))
	if r.effect != nil {
		if err := r.effect(ctx, args); err != nil {
			return "", err
		}
	}
	if args[0] == "new-session" {
		id := sessionlease.Identity{ServerPID: "12", Created: "35", ID: "$9", Name: args[3]}
		r.sessions[id.Name], r.sessions[id.ID] = id, id
		if r.afterCreate != nil {
			r.afterCreate()
		}
		if r.createdOutput != "" {
			return r.createdOutput, nil
		}
		return "12:35:$9\n", nil
	}
	if args[0] == "new-window" && r.afterWindow != nil {
		r.afterWindow()
	}
	return "", nil
}

func openSessionFixture(t *testing.T) *openSessionRunner {
	return &openSessionRunner{admissionRunner: admissionFixture(t)}
}

func assertOpenSessionReleased(t *testing.T, r *openSessionRunner, name string) {
	t.Helper()
	probe := sessionlease.New(true)
	defer probe.Close()
	if _, err := probe.Admit(context.Background(), r, sessionlease.Request{Selector: "=" + name, Name: name, AllowMissing: true}); err != nil {
		t.Fatal("consumer leaked name/runtime admission", err)
	}
	update, err := operationlock.Acquire(true)
	if err != nil {
		t.Fatal("consumer leaked update admission", err)
	}
	update()
}

func TestOpenSessionRejectCleanupAliasesBeforeEffects(t *testing.T) {
	for _, route := range []string{"ensure-live", "ensure-missing", "window"} {
		selectors := []string{"workspace-alpha", "=workspace-alpha", "workspace-alph", "workspace-alpha*", "$7"}
		if route == "ensure-missing" {
			selectors = []string{"=missing-runtime"}
		}
		for _, selector := range selectors {
			t.Run(route+"/"+selector, func(t *testing.T) {
				r := openSessionFixture(t)
				name := "workspace-alpha"
				if route == "ensure-missing" {
					name, selector = "missing-runtime", "=missing-runtime"
				}
				active := sessionlease.New(true)
				defer active.Close()
				if _, err := active.Admit(context.Background(), r, sessionlease.Request{Selector: selector, Name: name, AllowMissing: route == "ensure-missing"}); err != nil {
					t.Fatal(err)
				}
				var err error
				if route == "window" {
					_, err = openSession(context.Background(), r, integration.OpenSessionRequest{Name: name, Command: "sleep 120"})
				} else {
					_, err = openSession(context.Background(), r, integration.OpenSessionRequest{Name: name, Switch: true})
				}
				if err == nil || !strings.Contains(err.Error(), "tmux session is in use") || len(r.effects) != 0 {
					t.Fatal("consumer bypassed cleanup admission", err, r.effects)
				}
			})
		}
	}
}

func TestOpenSessionHoldAdmissionThroughEveryNativeOutcome(t *testing.T) {
	for _, route := range []string{"ensure-live", "ensure-missing", "window"} {
		for _, outcome := range []string{"success", "failure", "cancelled"} {
			t.Run(route+"/"+outcome, func(t *testing.T) {
				r := openSessionFixture(t)
				name := "workspace-alpha"
				if route == "ensure-missing" {
					name = "missing-runtime"
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				r.effect = func(ctx context.Context, args []string) error {
					probe := sessionlease.New(true)
					_, err := probe.Admit(context.Background(), r, sessionlease.Request{Selector: "=" + name, Name: name, AllowMissing: true})
					probe.Close()
					if err == nil {
						t.Error("cleanup entered active native consumer", args)
					}
					if runtime.GOOS == "darwin" {
						if release, err := operationlock.Acquire(true); err == nil {
							release()
							t.Error("update entered active native consumer")
						}
					}
					if args[0] != "new-session" {
						want := "$7"
						if route == "ensure-missing" {
							want = "$9"
						}
						if release, err := workspacelease.AcquireTmux("runtime:"+r.sessions[want].Key(), true); err == nil {
							release()
							t.Error("native consumer omitted incarnation admission")
						}
						if args[0] == "new-window" {
							want += ":"
						}
						if args[2] != want {
							t.Errorf("dispatch not pinned to admitted runtime: %v", args)
						}
					}
					// Test errors at creation as well as after a created runtime has
					// joined admission and is handed off to native switching.
					if outcome == "failure" {
						return errors.New("fixture native failure")
					}
					if outcome == "cancelled" {
						cancel()
						return ctx.Err()
					}
					return nil
				}
				var err error
				if route == "window" {
					_, err = openSession(ctx, r, integration.OpenSessionRequest{Name: name, Window: "shell", Path: "/unrelated/path", Command: "sleep 120"})
				} else {
					var session integration.Session
					session, err = openSession(ctx, r, integration.OpenSessionRequest{Name: name, Switch: true})
					if err == nil && (session.Created != (route == "ensure-missing") || session.ID == "") {
						t.Fatal(session)
					}
				}
				if (err != nil) != (outcome != "success") {
					t.Fatal("unexpected native outcome", err)
				}
				if outcome == "cancelled" && !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				assertOpenSessionReleased(t, r, name)
			})
		}
	}
}

func TestOpenSessionAcceptCollectedIDsButNeverRecreateMissingIDs(t *testing.T) {
	for _, route := range []string{"ensure", "window"} {
		for _, state := range []string{"live", "missing", "cleanup-held"} {
			t.Run(route+"/"+state, func(t *testing.T) {
				r := openSessionFixture(t)
				if state == "missing" {
					delete(r.sessions, "$7")
				}
				if state == "cleanup-held" {
					active := sessionlease.New(true)
					defer active.Close()
					if _, err := active.Admit(context.Background(), r, sessionlease.Request{Selector: "workspace-alpha"}); err != nil {
						t.Fatal(err)
					}
				}
				var err error
				if route == "ensure" {
					var result integration.Session
					result, err = openSession(context.Background(), r, integration.OpenSessionRequest{Name: "$7", Switch: true})
					if err == nil && (result.Created || result.ID != "$7") {
						t.Fatal("collected ID was not reused", result)
					}
				} else {
					_, err = openSession(context.Background(), r, integration.OpenSessionRequest{Name: "$7"})
				}
				if (err == nil) != (state == "live") || state != "live" && len(r.effects) != 0 {
					t.Fatal("collected ID selected or created another runtime", err, r.effects)
				}
			})
		}
	}
}

func TestOpenSessionCreationRevalidatesBeforeSwitch(t *testing.T) {
	for _, change := range []string{"restart", "replacement", "missing", "malformed-output", "cancelled", "switch-error", "switch-cancelled"} {
		t.Run(change, func(t *testing.T) {
			r := openSessionFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			r.afterCreate = func() {
				id := r.sessions["$9"]
				switch change {
				case "restart":
					id.ServerPID = "99"
				case "replacement":
					id.Created = "36"
				case "missing":
					delete(r.sessions, "missing-runtime")
					delete(r.sessions, "$9")
					return
				case "cancelled":
					cancel()
				}
				r.sessions[id.Name], r.sessions[id.ID] = id, id
			}
			if change == "malformed-output" {
				r.createdOutput = "not a runtime identity"
			}
			r.effect = func(ctx context.Context, args []string) error {
				if args[0] == "switch-client" {
					if change == "switch-error" {
						return errors.New("fixture switch failed")
					}
					if change == "switch-cancelled" {
						cancel()
						return ctx.Err()
					}
					t.Fatal("replaced/unverifiable runtime was switched", args)
				}
				return nil
			}
			if _, err := openSession(ctx, r, integration.OpenSessionRequest{Name: "missing-runtime", Switch: true}); err == nil {
				t.Fatal("unsafe created runtime accepted")
			}
			assertOpenSessionReleased(t, r, "missing-runtime")
		})
	}
}

func TestOpenSessionRejectBeforeEffectsAndRelease(t *testing.T) {
	for _, route := range []string{"ensure", "window"} {
		invalidInputs := []string{"empty", "colon", "control", "pre-cancelled"}
		if runtime.GOOS == "darwin" {
			invalidInputs = append(invalidInputs, "update")
		}
		for _, invalid := range invalidInputs {
			t.Run(route+"/"+invalid, func(t *testing.T) {
				r := openSessionFixture(t)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				name := "workspace-alpha"
				switch invalid {
				case "empty":
					name = " "
				case "colon":
					name = "bad:name"
				case "control":
					name = "bad\nname"
				case "pre-cancelled":
					cancel()
				case "update":
					release, err := operationlock.Acquire(true)
					if err != nil {
						t.Fatal(err)
					}
					defer release()
				}
				var err error
				if route == "ensure" {
					_, err = openSession(ctx, r, integration.OpenSessionRequest{Name: name, Command: "sleep 120"})
				} else {
					_, err = openSession(ctx, r, integration.OpenSessionRequest{Name: name, Command: "sleep 120"})
				}
				if err == nil || len(r.effects) != 0 {
					t.Fatal("invalid consumer performed native effect", err, r.effects)
				}
			})
		}
	}
}

func TestOpenSessionRemainAvailableDuringUnrelatedCleanup(t *testing.T) {
	for _, route := range []string{"ensure-live", "ensure-missing", "window"} {
		t.Run(route, func(t *testing.T) {
			r := openSessionFixture(t)
			effects := 0
			provider := effectProvider{calls: &effects, run: func() error {
				if route == "window" {
					_, err := openSession(context.Background(), r, integration.OpenSessionRequest{Name: "workspace-beta"})
					return err
				}
				name := "workspace-beta"
				if route == "ensure-missing" {
					name = "missing-runtime"
				}
				_, err := openSession(context.Background(), r, integration.OpenSessionRequest{Name: name, Switch: true})
				return err
			}}
			// Native alpha cleanup is completed but its bundle remains held while
			// the later provider is paused inside the actual cleanup service.
			service := cleanup.New([]integration.CleanupProvider{fixtureAdmissionProvider{runner: r.admissionRunner}, provider})
			preview := protocol.CleanupPreview{Targets: []protocol.CleanupTarget{sessionTarget(r.admissionRunner, "workspace-alpha"), {Source: "fixture", ResourceID: "paused"}}}
			if _, err := service.Execute(context.Background(), preview, cleanup.ExecuteOptions{}); err != nil || effects != 1 {
				t.Fatal(err, effects)
			}
		})
	}
}

func TestNativeOpenSessionUseExactNameAndCreationIdentity(t *testing.T) {
	binary, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	setupHome(t)
	t.Setenv("RADAR_SOCKET", filepath.Join(t.TempDir(), "radar.sock"))
	// Match the existing native fixture convention; macOS sockets need short
	// paths. This is a private server, never the ambient/default tmux server.
	dir, err := os.MkdirTemp("/tmp", "rte-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	runner := privateTmuxRunner{binary: binary, socket: filepath.Join(dir, "s")}
	defer func() { _, _ = runner.Run(context.Background(), "", "tmux", "kill-server") }()
	for _, name := range []string{"workspace-alphabet", "workspace-alpha"} {
		result, err := openSession(context.Background(), runner, integration.OpenSessionRequest{Name: name, Window: "shell", Command: "sleep 120"})
		if err != nil || !result.Created || result.ID == "" {
			t.Fatal("native creation/incarnation capture failed", result, err)
		}
		reused, err := openSession(context.Background(), runner, integration.OpenSessionRequest{Name: name, Command: "sleep 120"})
		if err != nil || reused.Created || reused.ID != result.ID {
			t.Fatal("native exact reuse failed", reused, result, err)
		}
		byID, err := openSession(context.Background(), runner, integration.OpenSessionRequest{Name: result.ID, Command: "sleep 120"})
		if err != nil || byID.Created || byID.ID != result.ID {
			t.Fatal("native collected-ID reuse failed", byID, result, err)
		}
		target := name
		if name == "workspace-alpha" {
			target = result.ID
		}
		if _, err := openSession(context.Background(), runner, integration.OpenSessionRequest{Name: target, Window: "another", Command: "sleep 120"}); err != nil {
			t.Fatal(err)
		}
		output, err := runner.Run(context.Background(), "", "tmux", "display-message", "-p", "-t", result.ID+":", "#{session_name}:#{session_windows}")
		if err != nil || strings.TrimSpace(output) != fmt.Sprint(name, ":4") {
			t.Fatal("native window went to wrong session", output, err)
		}
	}
	if _, err := runner.Run(context.Background(), "", "tmux", "kill-server"); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background(), "", "tmux", "list-sessions"); err == nil {
		t.Fatal("private fixture server still running")
	}
}
