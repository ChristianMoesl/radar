package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"radar/internal/cleanup"
	"radar/internal/integration"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
	"radar/internal/workspacelease"
)

func TestCreateExistingPausedCleanupMatchesCanonicalBundle(t *testing.T) {
	for _, live := range []bool{false, true} {
		for _, target := range []string{"anchor", "member", "missing-member", "unrelated"} {
			t.Run(fmt.Sprint(target, "/live=", live), func(t *testing.T) {
				root, group := activationFixture(t)
				alias := filepath.Join(t.TempDir(), "alias")
				if err := os.Symlink(group.Path, alias); err != nil {
					t.Fatal(err)
				}
				if target != "anchor" {
					alias = filepath.Join(alias, "member")
				}
				if target == "missing-member" {
					if err := os.Remove(group.Members[0].Path); err != nil {
						t.Fatal(err)
					}
				}
				if target == "unrelated" {
					alias = t.TempDir()
				}
				entered, resume := make(chan struct{}), make(chan struct{})
				var once sync.Once
				unblock := func() { once.Do(func() { close(resume) }) }
				service := cleanup.New([]integration.CleanupProvider{activationCleanup{started: entered, resume: resume}})
				finished, done := make(chan error, 1), make(chan struct{})
				go func() {
					_, err := service.Execute(context.Background(), protocol.CleanupPreview{Targets: []protocol.CleanupTarget{{Source: "fixture", Kind: "workspace", Path: alias}}}, cleanup.ExecuteOptions{Mode: integration.CleanupConfirmed})
					finished <- err
					close(done)
				}()
				defer func() { unblock(); <-done }()
				select {
				case <-entered:
				case err := <-finished:
					t.Fatal("cleanup did not enter provider", err)
				case <-time.After(5 * time.Second):
					t.Fatal("cleanup did not start")
				}
				runner := &fakeRunner{hasSession: live}
				_, err := Create(context.Background(), runner, CreateOptions{WorkspaceRoot: root, TaskLinkingKey: group.NoteKey(), Switch: true})
				if target == "unrelated" {
					if err != nil {
						t.Error("unrelated paused cleanup blocked create-existing", err)
					}
				} else if err == nil || !strings.Contains(err.Error(), "in use by another operation") || len(runner.calls) != 0 {
					t.Errorf("create-existing bypassed canonical cleanup admission: err=%v calls=%+v", err, runner.calls)
				}
				unblock()
				if err := <-finished; err != nil {
					t.Fatal(err)
				}
				assertActivationLeasesReleased(t, group)
			})
		}
	}
}

func TestCreateExistingHoldsBundleThroughSwitch(t *testing.T) {
	for _, live := range []bool{false, true} {
		for _, outcome := range []string{"success", "failure", "cancelled"} {
			t.Run(fmt.Sprint("live=", live, "/", outcome), func(t *testing.T) {
				root, group := sandboxActivationFixture(t)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				base := &fakeRunner{hasSession: live}
				checked := false
				runner := activationRunner{Runner: base, run: func(ctx context.Context, cwd, name string, args ...string) (string, error) {
					if name == "tmux" && args[0] == "switch-client" {
						checked = true
						for _, path := range []string{group.Path, group.Members[0].Path} {
							if release, err := workspacelease.Acquire(path, true); err == nil {
								release()
								t.Error("create-existing dropped canonical bundle admission", path)
							}
						}
						if release, err := workspacelease.AcquireSandbox(group.Sandbox.Name, true); err == nil {
							release()
							t.Error("create-existing dropped shared sandbox admission")
						}
						release, err := workspacelease.Acquire(t.TempDir(), true)
						if err != nil {
							t.Error("unrelated path blocked", err)
						} else {
							release()
						}
						release, err = workspacelease.AcquireSandbox("unrelated-runtime", true)
						if err != nil {
							t.Error("unrelated sandbox blocked", err)
						} else {
							release()
						}
						if outcome == "failure" {
							return "", errors.New("fixture switch failed")
						}
						if outcome == "cancelled" {
							cancel()
							return "", ctx.Err()
						}
					}
					return base.Run(ctx, cwd, name, args...)
				}}
				_, err := Create(ctx, runner, CreateOptions{WorkspaceRoot: root, TaskLinkingKey: group.NoteKey(), Switch: true})
				if !checked || (err != nil) != (outcome != "success") {
					t.Fatal("create-existing switch outcome", checked, err)
				}
				if outcome == "cancelled" && !errors.Is(err, context.Canceled) {
					t.Fatal("lost cancellation", err)
				}
				assertSandboxActivationReleased(t, group)
			})
		}
	}
}

func TestCreateExistingRejectsSharedSandboxCleanupBeforeEffects(t *testing.T) {
	for _, live := range []bool{false, true} {
		t.Run(fmt.Sprint("live=", live), func(t *testing.T) {
			root, group := sandboxActivationFixture(t)
			release, err := workspacelease.AcquireSandbox(group.Sandbox.Name, true)
			if err != nil {
				t.Fatal(err)
			}
			base := &fakeRunner{hasSession: live}
			_, err = Create(context.Background(), base, CreateOptions{WorkspaceRoot: root, TaskLinkingKey: group.NoteKey(), Switch: true})
			release()
			if err == nil || !strings.Contains(err.Error(), "sandbox is in use by another operation") {
				t.Error("create-existing bypassed shared sandbox cleanup", err)
			}
			// Existing planning may observe SBX, but must not provision, prepare panes
			// or switch after the conflicting reusable-name admission.
			assertNotCalledContains(t, base.calls, "tmux", "has-session")
			assertNotCalledContains(t, base.calls, "tmux", "new-session")
			assertNotCalledContains(t, base.calls, "sbx", "create")
			assertNotCalledContains(t, base.calls, "sbx", "exec")
			assertSandboxActivationReleased(t, group)
		})
	}
}

func TestCreateExistingRejectsRegistrationDriftAfterProbe(t *testing.T) {
	for _, live := range []bool{false, true} {
		for _, change := range []string{"session", "member", "removed"} {
			t.Run(fmt.Sprint(change, "/live=", live), func(t *testing.T) {
				root, group := activationFixture(t)
				base := &fakeRunner{hasSession: live}
				changed := false
				runner := activationRunner{Runner: base, run: func(ctx context.Context, cwd, name string, args ...string) (string, error) {
					output, err := base.Run(ctx, cwd, name, args...)
					if !changed && name == "tmux" && args[0] == "has-session" {
						changed = true
						if updateErr := workspacegroup.Update(root, func(registry *workspacegroup.Registry) error {
							switch change {
							case "session":
								registry.Workspaces[0].SessionName = "replacement"
							case "member":
								registry.Workspaces[0].Members = nil
							case "removed":
								registry.Workspaces = nil
							}
							return nil
						}); updateErr != nil {
							t.Error(updateErr)
						}
					}
					return output, err
				}}
				_, err := Create(context.Background(), runner, CreateOptions{WorkspaceRoot: root, TaskLinkingKey: group.NoteKey(), Switch: true})
				if !changed || err == nil || !strings.Contains(err.Error(), "workspace registration changed") {
					t.Error("create-existing accepted registration drift", changed, err)
				}
				assertNotCalledContains(t, base.calls, "tmux", "new-session")
				assertNotCalledContains(t, base.calls, "tmux", "switch-client")
				_, actual, found, lookupErr := RegisteredWorkspace(group.Path, root)
				if lookupErr != nil {
					t.Fatal(lookupErr)
				}
				if change == "removed" && found || change == "session" && (!found || actual.SessionName != "replacement") || change == "member" && (!found || len(actual.Members) != 0) {
					t.Error("create-existing overwrote concurrent registration", actual, found)
				}
				assertActivationLeasesReleased(t, group)
			})
		}
	}
}
