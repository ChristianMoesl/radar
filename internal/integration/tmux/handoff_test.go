package tmux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"radar/internal/integration"
	"radar/internal/integration/sbx"
	"radar/internal/integration/tmux/sessionlease"
	"radar/internal/integration/workspace"
	"radar/internal/protocol"
	"radar/internal/workspacelease"
)

func TestOpenSessionKeepsOriginalIncarnationAcrossWindowRenameAndNameReuse(t *testing.T) {
	r := openSessionFixture(t)
	original := r.sessions["$7"]
	r.afterWindow = func() {
		if release, err := workspacelease.AcquireTmux("runtime:"+original.Key(), true); err == nil {
			release()
			t.Error("window completion released the operation's runtime admission")
		}
		renamed := original
		renamed.Name = "renamed-runtime"
		for alias, id := range r.sessions {
			if id.ID == original.ID {
				r.sessions[alias] = renamed
			}
		}
		r.sessions[renamed.Name] = renamed
		replacement := sessionlease.Identity{ServerPID: "12", Created: "35", ID: "$11", Name: original.Name}
		r.sessions[replacement.ID], r.sessions[replacement.Name] = replacement, replacement
		// A new name cannot bypass the retained incarnation key in the gap
		// between the window effect and the final client switch.
		probe := sessionlease.New(true)
		defer probe.Close()
		if _, err := probe.Admit(context.Background(), r, sessionlease.Request{Selector: renamed.Name}); err == nil {
			t.Error("cleanup entered renamed runtime between native stages")
		}
	}
	result, err := openSession(context.Background(), r, integration.OpenSessionRequest{Name: original.Name, Window: "shell", Command: "sleep 120", Switch: true})
	if err != nil || result.ID != original.ID || result.Name != "renamed-runtime" || len(r.effects) != 2 || r.effects[0][2] != "$7:" || r.effects[1][2] != "$7" {
		t.Fatal("handoff followed reused name instead of original incarnation", result, err, r.effects)
	}
	assertOpenSessionReleased(t, r, "renamed-runtime")
	assertOpenSessionReleased(t, r, original.Name)
}

func TestOpenSessionRevalidatesAfterExistingWindowBeforeSwitch(t *testing.T) {
	for _, change := range []string{"restart", "replacement", "missing", "cancelled", "switch-error", "switch-cancelled"} {
		t.Run(change, func(t *testing.T) {
			r := openSessionFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			r.afterWindow = func() {
				id := r.sessions["$7"]
				switch change {
				case "restart":
					id.ServerPID = "99"
				case "replacement":
					id.Created = "35"
				case "missing":
					delete(r.sessions, id.Name)
					delete(r.sessions, id.ID)
					return
				case "cancelled":
					cancel()
				}
				r.sessions[id.Name], r.sessions[id.ID] = id, id
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
					t.Fatal("changed existing runtime was switched", args)
				}
				return nil
			}
			if _, err := openSession(ctx, r, integration.OpenSessionRequest{Name: "workspace-alpha", Switch: true}); err == nil {
				t.Fatal("unverified handoff accepted")
			}
			assertOpenSessionReleased(t, r, "workspace-alpha")
		})
	}
}

func TestOpenSessionPausedNativeHandoffExcludesCleanupAndReleases(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprint("existing=", existing), func(t *testing.T) {
			r := openSessionFixture(t)
			name := "missing-runtime"
			if existing {
				name = "workspace-alpha"
			}
			entered, resume, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
			done := make(chan struct{})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			r.effect = func(ctx context.Context, args []string) error {
				if args[0] == "switch-client" {
					return nil
				}
				close(entered)
				select {
				case <-resume:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			go func() {
				defer close(done)
				_, err := openSession(ctx, r, integration.OpenSessionRequest{Name: name, Switch: true})
				finished <- err
			}()
			defer func() {
				cancel()
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Error("paused handoff worker did not exit")
				}
			}()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("native handoff did not pause")
			}
			blocked := sessionlease.New(true)
			if _, err := blocked.Admit(context.Background(), r, sessionlease.Request{Selector: "=" + name, Name: name, AllowMissing: true}); err == nil {
				blocked.Close()
				t.Fatal("cleanup entered paused handoff")
			}
			independent := sessionlease.New(true)
			if _, err := independent.Admit(context.Background(), r, sessionlease.Request{Selector: "workspace-beta"}); err != nil {
				t.Fatal("unrelated runtime blocked", err)
			}
			independent.Close()
			close(resume)
			select {
			case err := <-finished:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("handoff did not resume")
			}
			assertOpenSessionReleased(t, r, name)
		})
	}
}

type handoffMultiplexer struct {
	Source
	runner workspace.Runner
	opens  int
}

func (m *handoffMultiplexer) OpenSession(ctx context.Context, req integration.OpenSessionRequest) (integration.Session, error) {
	m.opens++
	return openSession(ctx, m.runner, req)
}
func (*handoffMultiplexer) Switch(context.Context, integration.SessionTarget) error {
	return errors.New("sandbox action split its admitted native handoff")
}

func TestSandboxActionRetainsBothRuntimeLeasesThroughSingleNativeOperation(t *testing.T) {
	for _, existing := range []bool{false, true} {
		for _, outcome := range []string{"success", "native-error", "cancelled"} {
			t.Run(fmt.Sprint("existing=", existing, "/", outcome), func(t *testing.T) {
				r := openSessionFixture(t)
				// Only LookPath is needed. The actual SBX shell command is passed
				// as data to the deterministic tmux Runner, never executed.
				bin := t.TempDir()
				if err := os.WriteFile(filepath.Join(bin, "sbx"), []byte("#!/bin/sh\nexit 42\n"), 0700); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				name, target := "missing-runtime", "missing-runtime"
				if existing {
					name, target = "workspace-alpha", "$7"
				}
				r.effect = func(ctx context.Context, args []string) error {
					if release, err := workspacelease.AcquireSandbox("shared-runtime", true); err == nil {
						release()
						t.Error("sandbox lease released before native handoff completed")
					}
					probe := sessionlease.New(true)
					_, err := probe.Admit(context.Background(), r, sessionlease.Request{Selector: "=" + name, Name: name, AllowMissing: true})
					probe.Close()
					if err == nil {
						t.Error("tmux lease released during sandbox handoff")
					}
					if args[0] == "switch-client" {
						if outcome == "native-error" {
							return errors.New("fixture native handoff failed")
						}
						if outcome == "cancelled" {
							cancel()
							return ctx.Err()
						}
					}
					return nil
				}
				mux := &handoffMultiplexer{runner: r}
				result, err := (sbx.Source{}).RunAction(ctx, integration.RunActionRequest{ActionID: sbx.OpenShellAction, Ref: protocol.SourceRef{Source: "sbx", Kind: "sandbox", Title: "shared-runtime", Path: t.TempDir()}, Task: protocol.Task{Kind: "session", Metadata: map[string]string{"switch_target": target}}, SwitchClient: true, Multiplexer: mux})
				if (err != nil) != (outcome != "success") || mux.opens != 1 {
					t.Fatal("unexpected sandbox handoff", result, err, mux.opens)
				}
				if outcome == "cancelled" && !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				assertOpenSessionReleased(t, r, name)
				release, err := workspacelease.AcquireSandbox("shared-runtime", true)
				if err != nil {
					t.Fatal("sandbox lease leaked", err)
				}
				release()
			})
		}
	}
}
