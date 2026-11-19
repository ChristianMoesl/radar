package workspace

import (
	"bytes"
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
)

func TestAnchorFilesystemDeletionAllowsUnrelatedRegisteredRecovery(t *testing.T) {
	testAnchorFilesystemRecovery(t, false)
}

func TestAnchorFilesystemBoundDeletionAllowsUnrelatedRegisteredRecovery(t *testing.T) {
	testAnchorFilesystemRecovery(t, true)
}

// Exercise both the public Service bundle and direct Source's real pipeline,
// including provider admission, final unregister and post-cleanup note handling.
type filesystemCleanupSource struct {
	Source
	runner    Runner
	admission *sharedDirectoryCleanupAdmission
	removeAll func(*os.Root, string) error
}

func (s filesystemCleanupSource) Cleanup(ctx context.Context, req integration.CleanupRequest) (protocol.CleanupTarget, error) {
	return s.Source.cleanupWithFilesystem(ctx, s.runner, req, s.admission, s.removeAll)
}

func (s filesystemCleanupSource) AdmitCleanup(ctx context.Context, targets []protocol.CleanupTarget, mode integration.CleanupMode) (integration.CleanupProvider, func(), error) {
	provider, release, err := s.Source.admitCleanupWithRunner(ctx, s.runner, targets, mode)
	if err != nil {
		return nil, nil, err
	}
	s.admission = provider.(*admittedWorkspaceCleanup).admission
	return s, release, nil
}

func testAnchorFilesystemRecovery(t *testing.T, bound bool) {
	t.Helper()
	for _, sandbox := range []bool{false, true} {
		for _, entry := range []string{"open", "create-existing"} {
			t.Run(fmt.Sprint(entry, "/sandbox=", sandbox), func(t *testing.T) {
				root, owner, other, target := cleanupRuntimeFixture(t, sandbox)
				t.Setenv("RADAR_AGENT_ROOT", filepath.Join(t.TempDir(), "agents"))
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				entered, resume, cleanupDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
				cleanupResult := make(chan error, 1)
				var once sync.Once
				unblock := func() { once.Do(func() { close(resume) }) }
				removeAll := func(directory *os.Root, name string) error {
					// Execute the real rooted deletion before pausing: this is not
					// another runtime/provider lookup or a generic fixture provider.
					if err := directory.RemoveAll(name); err != nil {
						return err
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
					defer close(cleanupDone)
					source := filesystemCleanupSource{runner: &fakeRunner{}, removeAll: removeAll}
					var err error
					if bound {
						_, err = cleanup.New([]integration.CleanupProvider{source}).Execute(ctx, protocol.CleanupPreview{Targets: []protocol.CleanupTarget{target}}, cleanup.ExecuteOptions{})
					} else {
						_, err = source.Cleanup(ctx, integration.CleanupRequest{Target: target})
					}
					cleanupResult <- err
				}()
				t.Cleanup(func() { cancel(); unblock(); <-cleanupDone })
				select {
				case <-entered:
				case err := <-cleanupResult:
					t.Fatal("cleanup failed before rooted filesystem deletion", err)
				case <-time.After(5 * time.Second):
					t.Fatal("cleanup did not reach rooted filesystem deletion")
				}
				assertAnchorRegistered(t, root, owner.ID)
				if _, err := os.Stat(filepath.Join(owner.Path, ".pnpm-store")); !os.IsNotExist(err) {
					t.Fatal("actual disposable entry deletion has not occurred", err)
				}
				base := &fakeRunner{}
				activated, activationDone := make(chan error, 1), make(chan struct{})
				go func() {
					defer close(activationDone)
					activated <- recoverCleanupRuntimeFixture(ctx, base, root, other, other.Path, entry)
				}()
				t.Cleanup(func() { cancel(); unblock(); <-activationDone })
				select {
				case err := <-activated:
					if err != nil {
						t.Fatal("unrelated recovery failed", err)
					}
				case <-time.After(5 * time.Second):
					unblock()
					<-activationDone
					t.Fatal("unrelated registered recovery waited for actual filesystem deletion")
				}
				assertCalledContains(t, base.calls, "tmux", "new-session")
				assertCalledContains(t, base.calls, "tmux", "switch-client")
				if sandbox {
					assertCalledContains(t, base.calls, "sbx", "create")
				}
				assertAnchorRegistered(t, root, owner.ID)
				assertAnchorRegistered(t, root, other.ID)
				for _, note := range []string{owner.NotePath, other.NotePath} {
					if _, err := os.Stat(note); err != nil {
						t.Fatal("canonical note changed during deletion", err)
					}
				}
				unblock()
				if err := <-cleanupResult; err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(owner.Path); !os.IsNotExist(err) {
					t.Fatal("cleanup anchor remains", err)
				}
				assertAnchorRegistered(t, root, other.ID)
				assertDirectAnchorAdmissionReleased(t, owner.Path)
			})
		}
	}
}

func TestAnchorFilesystemDeletionRetainsActualResourceConflicts(t *testing.T) {
	for _, conflict := range []string{"anchor", "alias", "shared-sandbox"} {
		for _, entry := range []string{"open", "create-existing"} {
			t.Run(conflict+"/"+entry, func(t *testing.T) {
				root, owner, other, target := cleanupRuntimeFixture(t, true)
				t.Setenv("RADAR_AGENT_ROOT", filepath.Join(t.TempDir(), "agents"))
				selected := owner
				if conflict == "alias" {
					target.Path = sharedCleanupReference(t, owner.Path, true)
				}
				if conflict == "shared-sandbox" {
					other.Sandbox.Name = owner.Sandbox.Name
					updateSharedCleanupGroup(t, root, other)
					selected = other
				}
				checked := false
				removeAll := func(directory *os.Root, name string) error {
					if err := directory.RemoveAll(name); err != nil {
						return err
					}
					checked = true
					base := &fakeRunner{}
					err := recoverCleanupRuntimeFixture(context.Background(), base, root, selected, selected.Path, entry)
					if err == nil || !strings.Contains(err.Error(), "in use by another operation") {
						t.Fatal("actual filesystem phase lost resource admission before effects", err, base.calls)
					}
					// Create may discover runtimes read-only before admission;
					// neither recovery entry may mutate or switch the resource.
					assertNotCalledContains(t, base.calls, "tmux", "new-session")
					assertNotCalledContains(t, base.calls, "tmux", "switch-client")
					assertNotCalledContains(t, base.calls, "sbx", "create")
					assertNotCalledContains(t, base.calls, "sbx", "rm")
					assertNotCalledContains(t, base.calls, "sbx", "stop")
					assertAnchorRegistered(t, root, owner.ID)
					return nil
				}
				_, err := (Source{}).cleanupWithFilesystem(context.Background(), &fakeRunner{}, integration.CleanupRequest{Target: target}, nil, removeAll)
				if err != nil || !checked {
					t.Fatal("filesystem conflict boundary not qualified", checked, err)
				}
				assertAnchorRegistered(t, root, other.ID)
				assertDirectAnchorAdmissionReleased(t, owner.Path)
			})
		}
	}
}

func TestAnchorFilesystemDeletionPreservesPartialAndChangedAuthority(t *testing.T) {
	for _, mode := range []integration.CleanupMode{integration.CleanupSafe, integration.CleanupConfirmed, integration.CleanupExpired} {
		for _, change := range []string{"owner", "owner-removed", "disposal-config", "configured-root", "anchor-reference", "shared-reference", "cancelled", "deletion-error", "late-entry", "anchor-replaced", "unrelated-update"} {
			t.Run(fmt.Sprint(mode, "/", change), func(t *testing.T) {
				root, owner, task := disposableAnchorFixture(t)
				t.Setenv("RADAR_SOCKET", filepath.Join(t.TempDir(), "radar.sock"))
				t.Setenv("RADAR_AGENT_ROOT", filepath.Join(t.TempDir(), "agents"))
				configureDisposableEntries(t, ".pnpm-store", "scratch.log")
				writeAnchorFile(t, filepath.Join(owner.Path, "scratch.log"))
				owner.NotePath = filepath.Join(t.TempDir(), "Canonical.md")
				writeAnchorFile(t, owner.NotePath)
				if err := ensureNoteLink(owner.Path, owner.NotePath); err != nil {
					t.Fatal(err)
				}
				owner.Sandbox = &workspacegroup.Sandbox{Name: "cleanup-runtime", Agent: "shell", Mounts: []string{owner.Path}, SharedDirectory: filepath.Join(t.TempDir(), workspacegroup.SharedDirectoryParent, owner.ID)}
				if err := ensureSharedDirectory(owner); err != nil {
					t.Fatal(err)
				}
				sharedFile := filepath.Join(owner.Sandbox.SharedDirectory, "shared-content")
				writeAnchorFile(t, sharedFile)
				saveAnchorFixture(t, root, owner)
				target := previewAnchor(t, task, mode)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				calls := 0
				var expectedRegistry []byte
				removeAll := func(directory *os.Root, name string) error {
					calls++
					if err := directory.RemoveAll(name); err != nil {
						return err
					}
					if calls != 1 {
						return nil
					}
					// Deliberate raw metadata changes test fresh validation, not a
					// public writer bypass: public writers must first take leases.
					if err := workspacegroup.Update(root, func(registry *workspacegroup.Registry) error {
						switch change {
						case "owner":
							registry.Workspaces[0].SessionName = "replacement-session"
						case "owner-removed":
							registry.Workspaces = nil
						case "anchor-reference", "shared-reference", "unrelated-update":
							path := filepath.Join(root, "other")
							other := workspacegroup.Workspace{ID: workspacegroup.ID(path), Name: "other", Path: path, SessionName: "keep-this-update"}
							if change == "anchor-reference" {
								other.NotePath = filepath.Join(owner.Path, "scratch.log")
							} else if change == "shared-reference" {
								other.NotePath = sharedFile
							}
							registry.Workspaces = append(registry.Workspaces, other)
						}
						return nil
					}); err != nil {
						return err
					}
					switch change {
					case "disposal-config":
						configureDisposableEntries(t)
					case "configured-root":
						path := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "radar", "config.yaml")
						if err := os.WriteFile(path, []byte(fmt.Sprintf("workspace: {root_dir: %q}\n", t.TempDir())), 0600); err != nil {
							return err
						}
					case "cancelled":
						cancel()
					case "late-entry":
						writeAnchorFile(t, filepath.Join(owner.Path, "not-previewed"))
					case "anchor-replaced":
						if err := os.Rename(owner.Path, owner.Path+"-detached"); err != nil {
							return err
						}
						writeAnchorFile(t, filepath.Join(owner.Path, "replacement-data"))
					}
					var err error
					expectedRegistry, err = os.ReadFile(workspacegroup.Path(root))
					if err != nil {
						return err
					}
					if change == "deletion-error" {
						return errors.New("fixture error after partial deletion")
					}
					return nil
				}
				_, err := (Source{}).cleanupWithFilesystem(ctx, &fakeRunner{}, integration.CleanupRequest{Target: target, Mode: mode}, nil, removeAll)
				if change == "unrelated-update" {
					if err != nil || calls != 2 {
						t.Fatal("unrelated metadata prevented successful deletion", calls, err)
					}
					registry, err := workspacegroup.Load(root)
					if err != nil || len(registry.Workspaces) != 1 || registry.Workspaces[0].SessionName != "keep-this-update" {
						t.Fatal("final unregister overwrote unrelated metadata", registry, err)
					}
				} else {
					if err == nil || calls == 0 || expectedRegistry == nil {
						t.Fatal("partial deletion bypassed changed authority", calls, err)
					}
					if change == "cancelled" && !errors.Is(err, context.Canceled) {
						t.Fatal("cancellation was lost", err)
					}
					after, readErr := os.ReadFile(workspacegroup.Path(root))
					if readErr != nil || !bytes.Equal(after, expectedRegistry) {
						t.Fatal("failure removed or overwrote current registration", readErr)
					}
					assertAnchorFileKept(t, sharedFile)
					if change == "late-entry" {
						assertAnchorFileKept(t, filepath.Join(owner.Path, "not-previewed"))
					} else if change == "anchor-replaced" {
						assertAnchorFileKept(t, filepath.Join(owner.Path, "replacement-data"))
						assertAnchorFileKept(t, filepath.Join(owner.Path+"-detached", "scratch.log"))
					} else {
						assertAnchorFileKept(t, filepath.Join(owner.Path, "scratch.log"))
					}
				}
				assertAnchorFileKept(t, owner.NotePath)
				assertDirectAnchorAdmissionReleased(t, owner.Path)
				assertDirectAnchorAdmissionReleased(t, owner.Sandbox.SharedDirectory)
			})
		}
	}
}
