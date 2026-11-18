package workspace

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"radar/internal/cleanup"
	"radar/internal/integration"
	"radar/internal/integration/tmux/sessionlease"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
	"radar/internal/workspacelease"
)

type liveNativeRunner struct {
	*fakeRunner
	sessions       map[string]sessionlease.Identity
	nameReads      int
	afterAdmission func()
	switchEffect   func(context.Context, []string) error
}

func (r *liveNativeRunner) Run(ctx context.Context, cwd, name string, args ...string) (string, error) {
	output, err := r.fakeRunner.Run(ctx, cwd, name, args...)
	if err != nil || name != "tmux" {
		return output, err
	}
	if args[0] == "switch-client" && r.switchEffect != nil {
		return "", r.switchEffect(ctx, args)
	}
	if args[0] != "display-message" {
		return output, err
	}
	selector := strings.TrimSuffix(strings.TrimPrefix(args[3], "="), ":")
	id := r.sessions[selector]
	if args[4] == "#{session_name}" {
		result := id.Name + "\n"
		r.nameReads++
		if r.nameReads == 2 && r.afterAdmission != nil {
			r.afterAdmission()
		}
		return result, nil
	}
	if id.ID == "" {
		return "12::\n", nil
	}
	return fmt.Sprintf("%s:%s:%s\n", id.ServerPID, id.Created, id.ID), nil
}

func liveNativeFixture(t *testing.T, registered bool) (*liveNativeRunner, sessionlease.Identity, func(context.Context, bool) (Workspace, error)) {
	t.Helper()
	path, name := t.TempDir(), "workspace-alpha"
	if registered {
		_, g := activationFixture(t)
		path, name = g.Path, g.SessionName
	} else {
		sessionLeasePaths(t, false)
	}
	id := sessionlease.Identity{ServerPID: "12", Created: "34", ID: "$7", Name: name}
	other := sessionlease.Identity{ServerPID: "12", Created: "35", ID: "$8", Name: "unrelated-runtime"}
	r := &liveNativeRunner{fakeRunner: &fakeRunner{hasSession: true}, sessions: map[string]sessionlease.Identity{name: id, id.ID: id, other.Name: other, other.ID: other}}
	activate := func(ctx context.Context, sw bool) (Workspace, error) {
		if registered {
			return OpenRegisteredWorkspace(ctx, r, path, sw)
		}
		return CreateSession(ctx, r, path, name, sw)
	}
	return r, id, activate
}

func assertLiveNativeReleased(t *testing.T, id sessionlease.Identity) {
	t.Helper()
	for _, key := range []string{"name:" + id.Name, "runtime:" + id.Key()} {
		release, err := workspacelease.AcquireTmux(key, true)
		if err != nil {
			t.Fatal("native activation leaked admission", key, err)
		}
		release()
	}
}

func TestLiveActivationRejectsNativeCleanupBeforeSwitch(t *testing.T) {
	for _, registered := range []bool{false, true} {
		for _, keyKind := range []string{"name", "runtime"} {
			t.Run(fmt.Sprint("registered=", registered, "/", keyKind), func(t *testing.T) {
				r, id, activate := liveNativeFixture(t, registered)
				key := "name:" + id.Name
				if keyKind == "runtime" {
					key = "runtime:" + id.Key()
				}
				release, err := workspacelease.AcquireTmux(key, true)
				if err != nil {
					t.Fatal(err)
				}
				_, err = activate(context.Background(), true)
				release()
				if err == nil || !strings.Contains(err.Error(), "tmux session is in use") {
					t.Fatal("live activation bypassed native cleanup", err, r.calls)
				}
				assertNotCalledContains(t, r.calls, "tmux", "switch-client")
				assertLiveNativeReleased(t, id)
			})
		}
	}
}

func TestLiveActivationHoldsNativeAdmissionThroughEverySwitchOutcome(t *testing.T) {
	for _, registered := range []bool{false, true} {
		for _, outcome := range []string{"success", "failure", "cancelled"} {
			t.Run(fmt.Sprint("registered=", registered, "/", outcome), func(t *testing.T) {
				r, id, activate := liveNativeFixture(t, registered)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				entered := false
				r.switchEffect = func(ctx context.Context, args []string) error {
					entered = true
					if args[2] != id.ID {
						t.Error("switch was not pinned to admitted incarnation", args)
					}
					for _, key := range []string{"name:" + id.Name, "runtime:" + id.Key()} {
						if release, err := workspacelease.AcquireTmux(key, true); err == nil {
							release()
							t.Error("cleanup entered active switch", key)
						}
					}
					unrelated := sessionlease.New(true)
					defer unrelated.Close()
					if _, err := unrelated.Admit(ctx, r, sessionlease.Request{Selector: "unrelated-runtime"}); err != nil {
						t.Error("unrelated native cleanup blocked", err)
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
				_, err := activate(ctx, true)
				if !entered || (err != nil) != (outcome != "success") {
					t.Fatal("unexpected switch outcome", entered, err)
				}
				if outcome == "cancelled" && !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				assertLiveNativeReleased(t, id)
			})
		}
	}
}

func TestLiveActivationRevalidatesOriginalIncarnation(t *testing.T) {
	for _, registered := range []bool{false, true} {
		for _, change := range []string{"restart", "replacement", "missing", "cancelled", "rename-and-reuse"} {
			t.Run(fmt.Sprint("registered=", registered, "/", change), func(t *testing.T) {
				r, id, activate := liveNativeFixture(t, registered)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				r.afterAdmission = func() {
					next := id
					switch change {
					case "restart":
						next.ServerPID = "99"
					case "replacement":
						next.Created = "99"
					case "missing":
						delete(r.sessions, id.ID)
						delete(r.sessions, id.Name)
						return
					case "cancelled":
						cancel()
						return
					case "rename-and-reuse":
						next.Name = "renamed-runtime"
						replacement := sessionlease.Identity{ServerPID: "12", Created: "99", ID: "$9", Name: id.Name}
						r.sessions[id.Name], r.sessions[replacement.ID] = replacement, replacement
						r.sessions[next.Name], r.sessions[id.ID] = next, next
						return
					}
					r.sessions[id.ID], r.sessions[id.Name] = next, next
				}
				switched := false
				r.switchEffect = func(_ context.Context, args []string) error {
					switched = true
					if change != "rename-and-reuse" || args[2] != id.ID {
						t.Error("switched changed/replaced runtime", change, args)
					}
					return nil
				}
				_, err := activate(ctx, true)
				if change == "rename-and-reuse" {
					if err != nil || !switched {
						t.Fatal("original renamed runtime was lost", err)
					}
					renamed := id
					renamed.Name = "renamed-runtime"
					assertLiveNativeReleased(t, renamed)
				} else if err == nil || switched {
					t.Fatal("unverified runtime accepted", err, switched)
				}
				assertLiveNativeReleased(t, id)
			})
		}
	}
}

func TestLiveActivationWithoutSwitchStillAdmitsAndReleases(t *testing.T) {
	for _, registered := range []bool{false, true} {
		t.Run(fmt.Sprint("registered=", registered), func(t *testing.T) {
			r, id, activate := liveNativeFixture(t, registered)
			if _, err := activate(context.Background(), false); err != nil {
				t.Fatal(err)
			}
			if r.nameReads != 2 {
				t.Fatal("live no-switch path omitted native admission", r.nameReads)
			}
			assertNotCalledContains(t, r.calls, "tmux", "switch-client")
			assertLiveNativeReleased(t, id)
		})
	}
}

func TestLiveActivationRejectsUnverifiableProbeWithoutRecovery(t *testing.T) {
	for _, registered := range []bool{false, true} {
		for _, change := range []string{"missing", "malformed", "cancelled"} {
			t.Run(fmt.Sprint("registered=", registered, "/", change), func(t *testing.T) {
				r, id, activate := liveNativeFixture(t, registered)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				switch change {
				case "missing":
					delete(r.sessions, id.Name)
				case "malformed":
					bad := id
					bad.ServerPID = "unverified"
					r.sessions[id.Name] = bad
				case "cancelled":
					cancel()
				}
				if _, err := activate(ctx, true); err == nil {
					t.Fatal("unverified live probe accepted")
				}
				assertNotCalledContains(t, r.calls, "tmux", "new-session")
				assertNotCalledContains(t, r.calls, "tmux", "switch-client")
				assertLiveNativeReleased(t, id)
			})
		}
	}
}

// Pause the actual cleanup service while holding the real global note lock
// and exclusive native admission. No VM, tmux removal or provider side effect
// is simulated as successful; only live-navigation overlap is qualified here.
type noteLockedNativeCleanup struct {
	activationCleanup
	runner Runner
	root   string
}

func (p noteLockedNativeCleanup) AdmitCleanup(ctx context.Context, targets []protocol.CleanupTarget, _ integration.CleanupMode) (integration.CleanupProvider, func(), error) {
	admission := sessionlease.New(true)
	for _, target := range targets {
		if _, err := admission.Admit(ctx, p.runner, sessionlease.Request{Selector: target.ResourceID}); err != nil {
			admission.Close()
			return nil, nil, err
		}
	}
	return p, admission.Close, nil
}

func (p noteLockedNativeCleanup) Cleanup(ctx context.Context, req integration.CleanupRequest) (protocol.CleanupTarget, error) {
	err := workspacegroup.WithNoteLock(p.root, func() error {
		close(p.started)
		select {
		case <-p.resume:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	return req.Target, err
}

func TestLiveActivationDuringNoteLockedNativeCleanup(t *testing.T) {
	for _, registered := range []bool{false, true} {
		for _, related := range []bool{false, true} {
			t.Run(fmt.Sprint("registered=", registered, "/related=", related), func(t *testing.T) {
				r, id, activate := liveNativeFixture(t, registered)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				started, resume := make(chan struct{}), make(chan struct{})
				var once sync.Once
				unblock := func() { once.Do(func() { close(resume) }) }
				provider := noteLockedNativeCleanup{activationCleanup: activationCleanup{started: started, resume: resume}, runner: r, root: t.TempDir()}
				service := cleanup.New([]integration.CleanupProvider{provider})
				selector := "unrelated-runtime"
				if related {
					selector = id.ID // Collected alias, not the caller's reusable name.
				}
				cleanupDone := make(chan error, 1)
				go func() {
					_, err := service.Execute(ctx, protocol.CleanupPreview{Targets: []protocol.CleanupTarget{{Source: "fixture", Kind: "session", ResourceID: selector}}}, cleanup.ExecuteOptions{})
					cleanupDone <- err
				}()
				defer func() {
					unblock()
					select {
					case err := <-cleanupDone:
						if err != nil {
							t.Error("cleanup worker failed", err)
						}
					case <-time.After(5 * time.Second):
						t.Error("cleanup worker did not exit")
					}
				}()
				select {
				case <-started:
				case <-time.After(5 * time.Second):
					t.Fatal("cleanup did not acquire note/native locks")
				}
				activationDone := make(chan error, 1)
				go func() { _, err := activate(ctx, true); activationDone <- err }()
				select {
				case err := <-activationDone:
					if (err != nil) != related {
						t.Fatal("live navigation failed actual note/native overlap", err)
					}
					if related && !strings.Contains(err.Error(), "tmux session is in use") {
						t.Fatal("same-runtime rejection was not native contention", err)
					}
				case <-time.After(5 * time.Second):
					unblock()
					select {
					case <-activationDone:
					case <-time.After(5 * time.Second):
						t.Error("activation worker did not exit")
					}
					t.Fatal("live navigation waited for unrelated global note lock")
				}
				if !related {
					assertLiveNativeReleased(t, id)
				}
			})
		}
	}
}

func TestNativeLiveActivationExactNameAndCleanupAdmission(t *testing.T) {
	for _, registered := range []bool{false, true} {
		t.Run(fmt.Sprint("registered=", registered), func(t *testing.T) {
			path, name := t.TempDir(), "workspace-alpha"
			if registered {
				_, group := activationFixture(t)
				path, name = group.Path, group.SessionName
			} else {
				sessionLeasePaths(t, false)
			}
			runner := newIsolatedPaneTmux(t, path)
			ctx := context.Background()
			for _, session := range []string{name + "-longer", name} {
				if _, err := runner.Run(ctx, path, "tmux", "new-session", "-d", "-s", session, "sleep 120"); err != nil {
					t.Fatal(err)
				}
			}
			activate := func() error {
				var result Workspace
				var err error
				if registered {
					result, err = OpenRegisteredWorkspace(ctx, runner, path, false)
				} else {
					result, err = CreateSession(ctx, runner, path, name, false)
				}
				if err == nil && result.SessionName != name {
					t.Error("native exact-name activation changed workspace", result)
				}
				return err
			}
			if err := activate(); err != nil {
				t.Fatal(err)
			}
			active := sessionlease.New(true)
			defer active.Close()
			if _, err := active.Admit(ctx, runner, sessionlease.Request{Selector: "=" + name}); err != nil {
				t.Fatal("native live activation leaked admission", err)
			}
			if err := activate(); err == nil || !strings.Contains(err.Error(), "tmux session is in use") {
				t.Fatal("native cleanup did not protect live activation", err)
			}
			active.Close()
			if err := activate(); err != nil {
				t.Fatal("native activation did not recover after admission release", err)
			}
			if _, err := runner.Run(ctx, path, "tmux", "kill-server"); err != nil {
				t.Fatal(err)
			}
			if _, err := runner.Run(ctx, path, "tmux", "list-sessions"); err == nil {
				t.Fatal("private native fixture server still running")
			}
		})
	}
}

func TestStandaloneLiveAdmissionSpansSandboxReadiness(t *testing.T) {
	for _, outcome := range []string{"success", "failure", "cancelled", "replacement"} {
		t.Run(outcome, func(t *testing.T) {
			withWorkspaceGOOS(t, "darwin")
			r, id, _ := liveNativeFixture(t, false)
			r.sbxListOutput = `[{"name":"shared-sandbox","status":"stopped"}]`
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			checked := false
			runner := &readyRunner{Runner: r, ready: func(ctx context.Context) (string, error) {
				checked = true
				for _, key := range []string{"name:" + id.Name, "runtime:" + id.Key()} {
					if release, err := workspacelease.AcquireTmux(key, true); err == nil {
						release()
						t.Error("native admission released during readiness", key)
					}
				}
				if release, err := workspacelease.AcquireSandbox("shared-sandbox", true); err == nil {
					release()
					t.Error("sandbox admission released during readiness")
				}
				switch outcome {
				case "failure":
					return "", errors.New("fixture readiness failed")
				case "cancelled":
					cancel()
					return "", ctx.Err()
				case "replacement":
					changed := id
					changed.Created = "99"
					r.sessions[id.ID], r.sessions[id.Name] = changed, changed
				}
				return "", nil
			}}
			_, err := CreateSessionWithOptions(ctx, runner, CreateSessionOptions{Path: t.TempDir(), SessionName: id.Name, SandboxName: "shared-sandbox", SandboxKitName: "shell", SandboxReadyCommand: []string{"ready"}, Switch: true})
			if !checked || (err != nil) != (outcome != "success") {
				t.Fatal("unexpected readiness outcome", checked, err)
			}
			if outcome != "success" {
				assertNotCalledContains(t, r.calls, "tmux", "switch-client")
			}
			assertLiveNativeReleased(t, id)
			release, err := workspacelease.AcquireSandbox("shared-sandbox", true)
			if err != nil {
				t.Fatal("sandbox admission leaked", err)
			}
			release()
		})
	}
}
