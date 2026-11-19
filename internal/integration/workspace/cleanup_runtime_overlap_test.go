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

// Inject only the command runner, retaining Source's actual preflight, cleanup,
// archive and Service admission rather than pausing a generic fixture provider.
type runnerCleanupSource struct {
	Source
	runner Runner
}

func (s runnerCleanupSource) Cleanup(ctx context.Context, req integration.CleanupRequest) (protocol.CleanupTarget, error) {
	return s.Source.cleanupWithRunner(ctx, s.runner, req)
}

func cleanupRuntimeFixture(t *testing.T, sandbox bool) (string, workspacegroup.Workspace, workspacegroup.Workspace, protocol.CleanupTarget) {
	t.Helper()
	withWorkspaceGOOS(t, "darwin")
	root, author, _, owner := archiveWorkspaceFixture(t)
	t.Setenv("RADAR_SOCKET", filepath.Join(t.TempDir(), "radar.sock"))
	owner.SessionName = "cleanup-session"
	owner.Sandbox = &workspacegroup.Sandbox{Name: "cleanup-runtime", Agent: "shell", Mounts: []string{owner.Path}}
	saveAnchorFixture(t, root, owner)
	configureDisposableEntries(t, ".pnpm-store")
	writeAnchorFile(t, filepath.Join(owner.Path, ".pnpm-store", "content"))
	created, err := Create(context.Background(), &fakeRunner{}, CreateOptions{Name: "Unrelated", WorkspaceRoot: root, NoteAuthor: author})
	if err != nil {
		t.Fatal(err)
	}
	_, other, found, err := RegisteredWorkspace(created.Path, root)
	if err != nil || !found {
		t.Fatal("missing unrelated registration", err)
	}
	if sandbox {
		other.Sandbox = &workspacegroup.Sandbox{Name: "unrelated-runtime", Agent: "shell", Mounts: []string{other.Path}}
		if err := workspacegroup.Update(root, func(registry *workspacegroup.Registry) error {
			for i := range registry.Workspaces {
				if registry.Workspaces[i].ID == other.ID {
					registry.Workspaces[i] = other
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	targets, err := (Source{}).PreviewCleanup(context.Background(), integration.CleanupPreviewRequest{Task: protocol.Task{SourceRefs: []protocol.SourceRef{{ID: "workspace:" + owner.ID, Source: "workspace", Kind: "workspace"}}}})
	if err != nil || len(targets) != 1 {
		t.Fatal("cleanup preview", targets, err)
	}
	return root, owner, other, targets[0]
}

func pauseActualCleanupRuntime(t *testing.T, target protocol.CleanupTarget) (func(), <-chan error) {
	t.Helper()
	entered, resume, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	finished := make(chan error, 1)
	var once sync.Once
	unblock := func() { once.Do(func() { close(resume) }) }
	base := &fakeRunner{}
	runner := activationRunner{Runner: base, run: func(ctx context.Context, cwd, name string, args ...string) (string, error) {
		if name == "sbx" && len(args) == 2 && args[0] == "ls" && args[1] == "--json" {
			close(entered)
			<-resume
		}
		return base.Run(ctx, cwd, name, args...)
	}}
	service := cleanup.New([]integration.CleanupProvider{runnerCleanupSource{runner: runner}})
	go func() {
		_, err := service.Execute(context.Background(), protocol.CleanupPreview{Targets: []protocol.CleanupTarget{target}}, cleanup.ExecuteOptions{Mode: integration.CleanupSafe})
		finished <- err
		close(done)
	}()
	t.Cleanup(func() { unblock(); <-done })
	select {
	case <-entered:
	case err := <-finished:
		t.Fatal("actual cleanup did not reach SBX absence lookup", err)
	case <-time.After(5 * time.Second):
		t.Fatal("actual cleanup did not reach SBX absence lookup")
	}
	return unblock, finished
}

func recoverCleanupRuntimeFixture(ctx context.Context, runner Runner, root string, group workspacegroup.Workspace, current, entry string) error {
	if entry == "open" {
		_, err := OpenRegisteredWorkspace(ctx, runner, current, true)
		return err
	}
	_, err := Create(ctx, runner, CreateOptions{WorkspaceRoot: root, TaskLinkingKey: group.NoteKey(), Switch: true})
	return err
}

func TestCleanupRuntimeLookupAllowsUnrelatedMissingRuntimeRecovery(t *testing.T) {
	for _, sandbox := range []bool{false, true} {
		for _, entry := range []string{"open", "create-existing"} {
			t.Run(fmt.Sprint(entry, "/sandbox=", sandbox), func(t *testing.T) {
				root, owner, other, target := cleanupRuntimeFixture(t, sandbox)
				unblock, cleanupFinished := pauseActualCleanupRuntime(t, target)
				base := &fakeRunner{} // Both native session and optional SBX are absent.
				activated, done := make(chan error, 1), make(chan struct{})
				go func() {
					activated <- recoverCleanupRuntimeFixture(context.Background(), base, root, other, other.Path, entry)
					close(done)
				}()
				t.Cleanup(func() { unblock(); <-done })
				select {
				case err := <-activated:
					if err != nil {
						t.Fatal("unrelated missing-runtime recovery failed", err)
					}
				case <-time.After(5 * time.Second):
					unblock()
					<-done
					t.Fatal("unrelated missing-runtime recovery waited for paused actual cleanup")
				}
				assertCalledContains(t, base.calls, "tmux", "new-session")
				assertCalledContains(t, base.calls, "tmux", "switch-client")
				if sandbox {
					assertCalledContains(t, base.calls, "sbx", "create")
				}
				registry, err := workspacegroup.Load(root)
				if _, found := workspacegroup.FindByID(registry, owner.ID); err != nil || !found {
					t.Fatal("last cleanup reference disappeared during runtime lookup", err)
				}
				assertAnchorFileKept(t, filepath.Join(owner.Path, ".pnpm-store", "content"))
				unblock()
				if err := <-cleanupFinished; err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(owner.Path); !os.IsNotExist(err) {
					t.Fatal("cleanup anchor remains", err)
				}
				for _, note := range []string{owner.NotePath, other.NotePath} {
					if _, err := os.Stat(note); err != nil {
						t.Fatal("canonical note changed", err)
					}
				}
				release, err := workspacelease.AcquireSandboxMutation(owner.Sandbox.Name)
				if err != nil {
					t.Fatal("successful cleanup leaked runtime reservation", err)
				}
				release()
			})
		}
	}
}

func TestCleanupRuntimeLookupRetainsCanonicalAndReusableNameConflicts(t *testing.T) {
	for _, conflict := range []string{"anchor", "alias", "shared-sandbox"} {
		for _, entry := range []string{"open", "create-existing"} {
			t.Run(conflict+"/"+entry, func(t *testing.T) {
				root, owner, other, target := cleanupRuntimeFixture(t, true)
				selected, current := owner, owner.Path
				if conflict == "alias" {
					alias := filepath.Join(t.TempDir(), "alias")
					if err := os.Symlink(owner.Path, alias); err != nil {
						t.Fatal(err)
					}
					// Resolve cleanup admission through the physical alias, while
					// activation uses the supported registered selector.
					target.Path = alias
				}
				if conflict == "shared-sandbox" {
					other.Sandbox.Name = owner.Sandbox.Name
					if err := workspacegroup.Update(root, func(registry *workspacegroup.Registry) error {
						for i := range registry.Workspaces {
							if registry.Workspaces[i].ID == other.ID {
								registry.Workspaces[i] = other
							}
						}
						return nil
					}); err != nil {
						t.Fatal(err)
					}
					selected, current = other, other.Path
				}
				// This preview intentionally contains only the workspace target:
				// the absent runtime does not supply an SBX deletion-name lease.
				unblock, cleanupFinished := pauseActualCleanupRuntime(t, target)
				base := &fakeRunner{}
				finished, done := make(chan error, 1), make(chan struct{})
				go func() {
					finished <- recoverCleanupRuntimeFixture(context.Background(), base, root, selected, current, entry)
					close(done)
				}()
				t.Cleanup(func() { unblock(); <-done })
				select {
				case err := <-finished:
					if err == nil || !strings.Contains(err.Error(), "in use by another operation") {
						t.Fatal("matching writer bypassed actual cleanup", err)
					}
				case <-time.After(5 * time.Second):
					unblock()
					<-done
					t.Fatal("conflicting writer waited instead of reporting admission conflict")
				}
				assertNotCalledContains(t, base.calls, "tmux", "new-session")
				assertNotCalledContains(t, base.calls, "tmux", "switch-client")
				assertNotCalledContains(t, base.calls, "sbx", "create")
				assertAnchorFileKept(t, filepath.Join(owner.Path, ".pnpm-store", "content"))
				unblock()
				if err := <-cleanupFinished; err != nil {
					t.Fatal(err)
				}
				release, err := workspacelease.AcquireSandboxMutation(owner.Sandbox.Name)
				if err != nil {
					t.Fatal("cleanup leaked runtime reservation", err)
				}
				release()
			})
		}
	}
}

func TestCleanupRuntimeLookupRevalidatesBeforeEffectsAndReleasesReservation(t *testing.T) {
	for _, mode := range []integration.CleanupMode{integration.CleanupSafe, integration.CleanupConfirmed, integration.CleanupExpired} {
		for _, change := range []string{"session", "sandbox", "note", "member", "removed", "cancelled", "lookup-error", "unknown-entry", "configured-root"} {
			t.Run(fmt.Sprint(mode, "/", change), func(t *testing.T) {
				root, owner, task := disposableAnchorFixture(t)
				t.Setenv("RADAR_SOCKET", filepath.Join(t.TempDir(), "radar.sock"))
				configureDisposableEntries(t, ".pnpm-store")
				owner.Sandbox = &workspacegroup.Sandbox{Name: "cleanup-runtime", Agent: "shell", Mounts: []string{owner.Path}}
				saveAnchorFixture(t, root, owner)
				targets, err := (Source{}).PreviewCleanup(context.Background(), integration.CleanupPreviewRequest{Task: task, Mode: mode})
				if err != nil || len(targets) != 1 {
					t.Fatal(targets, err)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				failure := errors.New("fixture lookup failure")
				base, observed := &fakeRunner{}, false
				var expectedRegistry []byte
				runner := activationRunner{Runner: base, run: func(ctx context.Context, cwd, name string, args ...string) (string, error) {
					if name == "sbx" && len(args) == 2 && args[0] == "ls" {
						observed = true
						if err := workspacegroup.Update(root, func(registry *workspacegroup.Registry) error {
							group := &registry.Workspaces[0]
							switch change {
							case "session":
								group.SessionName = "replacement"
							case "sandbox":
								group.Sandbox.Name = "replacement"
							case "note":
								group.NotePath = filepath.Join(t.TempDir(), "canonical.md")
								writeAnchorFile(t, group.NotePath)
							case "member":
								group.Members = []workspacegroup.Member{{Repository: t.TempDir(), Path: filepath.Join(group.Path, "member"), Branch: "fixture"}}
							case "removed":
								registry.Workspaces = nil
							}
							return nil
						}); err != nil {
							return "", err
						}
						if change == "cancelled" {
							cancel()
						}
						if change == "unknown-entry" {
							writeAnchorFile(t, filepath.Join(owner.Path, "not-previewed"))
						}
						if change == "configured-root" {
							path := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "radar", "config.yaml")
							if err := os.WriteFile(path, []byte(fmt.Sprintf("workspace: {root_dir: %q}\n", t.TempDir())), 0o600); err != nil {
								return "", err
							}
						}
						expectedRegistry, err = os.ReadFile(workspacegroup.Path(root))
						if err != nil {
							return "", err
						}
						if change == "lookup-error" {
							return "", failure
						}
					}
					return base.Run(ctx, cwd, name, args...)
				}}
				_, err = (Source{}).cleanupWithRunner(ctx, runner, integration.CleanupRequest{Target: targets[0], Mode: mode})
				if !observed || err == nil {
					t.Fatal("stale or failed runtime lookup allowed cleanup", observed, err)
				}
				if change == "cancelled" && !errors.Is(err, context.Canceled) || change == "lookup-error" && !errors.Is(err, failure) {
					t.Fatal("lost runtime lookup failure", err)
				}
				actualRegistry, readErr := os.ReadFile(workspacegroup.Path(root))
				if readErr != nil || string(actualRegistry) != string(expectedRegistry) {
					t.Fatal("cleanup overwrote changed registration", readErr)
				}
				assertAnchorFileKept(t, filepath.Join(owner.Path, ".pnpm-store", "v10", "content"))
				release, err := workspacelease.AcquireSandboxMutation(owner.Sandbox.Name)
				if err != nil {
					t.Fatal("runtime reservation leaked on error", err)
				}
				release()
			})
		}
	}
}

func TestCleanupRuntimeLookupRejectsExistingWriterBeforeProviderIO(t *testing.T) {
	root, owner, task := disposableAnchorFixture(t)
	t.Setenv("RADAR_SOCKET", filepath.Join(t.TempDir(), "radar.sock"))
	configureDisposableEntries(t, ".pnpm-store")
	owner.Sandbox = &workspacegroup.Sandbox{Name: "cleanup-runtime", Agent: "shell", Mounts: []string{owner.Path}}
	saveAnchorFixture(t, root, owner)
	target := previewDisposableAnchor(t, task)
	release, err := workspacelease.AcquireSandboxMutation(owner.Sandbox.Name)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	base := &fakeRunner{}
	_, err = (Source{}).cleanupWithRunner(context.Background(), base, integration.CleanupRequest{Target: target})
	if err == nil || !strings.Contains(err.Error(), "in use by another operation") || len(base.calls) != 0 {
		t.Fatal("cleanup bypassed runtime writer", err, base.calls)
	}
	assertAnchorFileKept(t, filepath.Join(owner.Path, ".pnpm-store", "v10", "content"))
}
