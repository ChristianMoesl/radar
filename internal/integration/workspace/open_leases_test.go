package workspace

import (
	"context"
	"errors"
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

func activationFixture(t *testing.T) (string, workspacegroup.Workspace) {
	t.Helper()
	root := configureWorkspaceRoot(t)
	t.Setenv("RADAR_SOCKET", filepath.Join(t.TempDir(), "radar.sock"))
	created, err := Create(context.Background(), &fakeRunner{}, CreateOptions{Name: "Activation", WorkspaceRoot: root, NoteAuthor: testNoteAuthor(t)})
	if err != nil {
		t.Fatal(err)
	}
	_, group, found, err := RegisteredWorkspace(created.Path, root)
	if err != nil || !found {
		t.Fatalf("registration = %+v, %v", group, err)
	}
	member := filepath.Join(group.Path, "member")
	if err := os.MkdirAll(member, 0755); err != nil {
		t.Fatal(err)
	}
	group.Members = []workspacegroup.Member{{Path: member, Repository: t.TempDir(), Branch: "fixture"}}
	if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
		t.Fatal(err)
	}
	return root, group
}

func assertActivationLeasesReleased(t *testing.T, group workspacegroup.Workspace) {
	t.Helper()
	for _, path := range []string{group.Path, group.Members[0].Path} {
		release, err := workspacelease.Acquire(path, true)
		if err != nil {
			t.Fatalf("lease leaked for %s: %v", path, err)
		}
		release()
	}
}

func TestOpenWorkspaceRejectsMatchingCleanupLeasesBeforeRuntimeCalls(t *testing.T) {
	for _, live := range []bool{false, true} {
		for _, target := range []string{"anchor", "member"} {
			t.Run(target+"/live="+map[bool]string{false: "false", true: "true"}[live], func(t *testing.T) {
				_, group := activationFixture(t)
				path := group.Path
				if target == "member" {
					path = group.Members[0].Path
				}
				release, err := workspacelease.Acquire(path, true)
				if err != nil {
					t.Fatal(err)
				}
				defer func() {
					if release != nil {
						release()
					}
				}()
				runner := &fakeRunner{hasSession: live}
				// Nested/member selectors must protect the registered bundle, not
				// create a different lock identity from the user-supplied path.
				_, err = OpenRegisteredWorkspace(context.Background(), runner, filepath.Join(group.Members[0].Path, "nested"), true)
				if err == nil || !strings.Contains(err.Error(), "in use by another operation") || len(runner.calls) != 0 {
					t.Fatalf("activation bypassed cleanup: err=%v calls=%+v", err, runner.calls)
				}
				release()
				release = nil
				assertActivationLeasesReleased(t, group)
			})
		}
	}
}

type activationRunner struct {
	Runner
	run func(context.Context, string, string, ...string) (string, error)
}

func (r activationRunner) Run(ctx context.Context, cwd, name string, args ...string) (string, error) {
	return r.run(ctx, cwd, name, args...)
}

func TestOpenWorkspaceHoldsBundleLeasesOnBothPathsAndReleasesEveryOutcome(t *testing.T) {
	for _, live := range []bool{false, true} {
		for _, outcome := range []string{"success", "failure", "cancelled"} {
			t.Run(outcome+"/live="+map[bool]string{false: "false", true: "true"}[live], func(t *testing.T) {
				_, group := activationFixture(t)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				entered, resume := make(chan struct{}), make(chan struct{})
				base := &fakeRunner{hasSession: live}
				runner := activationRunner{Runner: base, run: func(ctx context.Context, cwd, name string, args ...string) (string, error) {
					if name == "tmux" && args[0] == "switch-client" {
						close(entered)
						<-resume
						if outcome == "failure" {
							return "", errors.New("fixture switch failed")
						}
						if ctx.Err() != nil {
							return "", ctx.Err()
						}
					}
					return base.Run(ctx, cwd, name, args...)
				}}
				finished, done := make(chan error, 1), make(chan struct{})
				var once sync.Once
				unblock := func() { once.Do(func() { close(resume) }) }
				go func() { _, err := OpenRegisteredWorkspace(ctx, runner, group.Path, true); finished <- err; close(done) }()
				defer func() { unblock(); <-done }()
				select {
				case <-entered:
				case err := <-finished:
					t.Fatalf("activation returned before switch: %v", err)
				case <-time.After(5 * time.Second):
					t.Fatal("activation did not reach switch")
				}
				for _, path := range []string{group.Path, group.Members[0].Path} {
					if release, err := workspacelease.Acquire(path, true); err == nil {
						release()
						unblock()
						<-finished
						t.Fatalf("cleanup acquired active target %s", path)
					}
				}
				// Unrelated path leases remain independently admissible. This is
				// not a claim about recovery's existing global note lock.
				release, err := workspacelease.Acquire(t.TempDir(), true)
				if err != nil {
					t.Fatal(err)
				}
				release()
				if outcome == "cancelled" {
					cancel()
				}
				unblock()
				if err := <-finished; (err != nil) != (outcome != "success") {
					t.Fatalf("outcome=%s err=%v", outcome, err)
				}
				assertActivationLeasesReleased(t, group)
			})
		}
	}
}

func TestOpenWorkspaceRejectsRegistrationDriftBeforeSwitchOrRecovery(t *testing.T) {
	for _, live := range []bool{false, true} {
		t.Run(map[bool]string{false: "recovery", true: "live"}[live], func(t *testing.T) {
			root, group := activationFixture(t)
			base := &fakeRunner{hasSession: live}
			runner := activationRunner{Runner: base, run: func(ctx context.Context, cwd, name string, args ...string) (string, error) {
				output, err := base.Run(ctx, cwd, name, args...)
				if name == "tmux" && args[0] == "has-session" {
					if updateErr := workspacegroup.Update(root, func(registry *workspacegroup.Registry) error {
						registry.Workspaces[0].SessionName = "changed"
						return nil
					}); updateErr != nil {
						return "", updateErr
					}
				}
				return output, err
			}}
			_, err := OpenRegisteredWorkspace(context.Background(), runner, group.Path, true)
			if err == nil || !strings.Contains(err.Error(), "registration changed") {
				t.Fatalf("changed registration accepted: %v", err)
			}
			assertNotCalledContains(t, base.calls, "tmux", "switch-client")
			assertNotCalledContains(t, base.calls, "tmux", "new-session")
			assertActivationLeasesReleased(t, group)
		})
	}
}

func TestOpenWorkspacePreCancelledDoesNotCallRuntimeOrRetainLeases(t *testing.T) {
	_, group := activationFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runner := &fakeRunner{hasSession: true}
	if _, err := OpenRegisteredWorkspace(ctx, runner, group.Path, true); !errors.Is(err, context.Canceled) || len(runner.calls) != 0 {
		t.Fatalf("cancelled activation ran: %v %+v", err, runner.calls)
	}
	assertActivationLeasesReleased(t, group)
}

func TestOpenWorkspaceRechecksRegistrationAndCancellationUnderNoteLock(t *testing.T) {
	for _, change := range []string{"session", "member", "removed", "cancelled"} {
		t.Run(change, func(t *testing.T) {
			root, group := activationFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			runner := &fakeRunner{}
			err := workspacegroup.WithNoteLock(root, func() error {
				if change == "cancelled" {
					cancel()
				} else if err := workspacegroup.Update(root, func(registry *workspacegroup.Registry) error {
					switch change {
					case "session":
						registry.Workspaces[0].SessionName = "replacement"
					case "member":
						registry.Workspaces[0].Members = nil
					case "removed":
						registry.Workspaces = nil
					}
					return nil
				}); err != nil {
					return err
				}
				_, err := openRegisteredWorkspaceAt(ctx, runner, root, group.Path, group, true, testWorkspaceSession(t, group.SessionName))
				return err
			})
			if err == nil || len(runner.calls) != 0 {
				t.Fatalf("stale or cancelled recovery ran: %v %+v", err, runner.calls)
			}
			if change == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost cancellation: %v", err)
			}
		})
	}
}

func TestOpenWorkspaceLeasesBlockCleanupBeforeProbeOrRecreation(t *testing.T) {
	for _, live := range []bool{false, true} {
		t.Run(map[bool]string{false: "recovery", true: "live"}[live], func(t *testing.T) {
			_, group := activationFixture(t)
			base := &fakeRunner{hasSession: live}
			paused := false
			runner := activationRunner{Runner: base, run: func(ctx context.Context, cwd, name string, args ...string) (string, error) {
				phase := "new-session"
				if live {
					phase = "has-session"
				}
				if name == "tmux" && args[0] == phase {
					paused = true
					for _, path := range []string{group.Path, group.Members[0].Path} {
						started := make(chan struct{})
						resume := make(chan struct{})
						close(resume) // No test deadlock if a regression dispatches the provider.
						service := cleanup.New([]integration.CleanupProvider{activationCleanup{started: started, resume: resume}})
						preview := protocol.CleanupPreview{Targets: []protocol.CleanupTarget{{Source: "fixture", Kind: "workspace", Path: path}}}
						_, err := service.Execute(ctx, preview, cleanup.ExecuteOptions{Mode: integration.CleanupConfirmed})
						if err == nil || !strings.Contains(err.Error(), "in use by another operation") {
							t.Fatalf("cleanup admitted during %s: %v", phase, err)
						}
						select {
						case <-started:
							t.Fatal("conflicting cleanup reached provider")
						default:
						}
					}
				}
				return base.Run(ctx, cwd, name, args...)
			}}
			if _, err := OpenRegisteredWorkspace(context.Background(), runner, group.Path, true); err != nil || !paused {
				t.Fatalf("activation=%v phase reached=%v", err, paused)
			}
			assertActivationLeasesReleased(t, group)
		})
	}
}

func TestOpenWorkspaceCleanupServiceBlocksActivationUntilReturn(t *testing.T) {
	for _, live := range []bool{false, true} {
		t.Run(map[bool]string{false: "recovery", true: "live"}[live], func(t *testing.T) {
			_, group := activationFixture(t)
			started, resume := make(chan struct{}), make(chan struct{})
			provider := activationCleanup{started: started, resume: resume}
			service := cleanup.New([]integration.CleanupProvider{provider})
			preview := protocol.CleanupPreview{Targets: []protocol.CleanupTarget{{Source: "fixture", Kind: "workspace", Path: group.Path}}}
			finished, done := make(chan error, 1), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(resume) }) }
			go func() {
				_, err := service.Execute(context.Background(), preview, cleanup.ExecuteOptions{Mode: integration.CleanupConfirmed})
				finished <- err
				close(done)
			}()
			defer func() { unblock(); <-done }()
			select {
			case <-started:
			case err := <-finished:
				t.Fatalf("cleanup failed admission: %v", err)
			case <-time.After(5 * time.Second):
				t.Fatal("cleanup did not start")
			}
			runner := &fakeRunner{hasSession: live}
			_, err := OpenRegisteredWorkspace(context.Background(), runner, group.Path, true)
			unblock()
			cleanupErr := <-finished
			if err == nil || !strings.Contains(err.Error(), "in use by another operation") || len(runner.calls) != 0 || cleanupErr != nil {
				t.Fatalf("activation=%v calls=%+v cleanup=%v", err, runner.calls, cleanupErr)
			}
			if _, err := OpenRegisteredWorkspace(context.Background(), runner, group.Path, true); err != nil {
				t.Fatalf("activation did not recover after cleanup return: %v", err)
			}
			assertActivationLeasesReleased(t, group)
		})
	}
}

type activationCleanup struct {
	integration.Integration
	started chan struct{}
	resume  chan struct{}
}

func (activationCleanup) Descriptor() integration.Descriptor {
	return integration.Descriptor{Name: "fixture"}
}
func (activationCleanup) PreviewCleanup(context.Context, integration.CleanupPreviewRequest) ([]protocol.CleanupTarget, error) {
	return nil, nil
}
func (p activationCleanup) Cleanup(_ context.Context, req integration.CleanupRequest) (protocol.CleanupTarget, error) {
	close(p.started)
	<-p.resume
	return req.Target, nil
}
