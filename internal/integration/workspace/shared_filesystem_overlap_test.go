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

// Keep the real Source and Service admission; inject only shared-root entry I/O.
type sharedFilesystemCleanupSource struct{ filesystemCleanupSource }

func (s sharedFilesystemCleanupSource) Cleanup(ctx context.Context, req integration.CleanupRequest) (protocol.CleanupTarget, error) {
	return s.Source.cleanupWithFilesystems(ctx, s.runner, req, s.admission, (*os.Root).RemoveAll, s.removeAll)
}

func (s sharedFilesystemCleanupSource) AdmitCleanup(ctx context.Context, targets []protocol.CleanupTarget, mode integration.CleanupMode) (integration.CleanupProvider, func(), error) {
	provider, release, err := s.Source.admitCleanupWithRunner(ctx, s.runner, targets, mode)
	if err != nil {
		return nil, nil, err
	}
	s.admission = provider.(*admittedWorkspaceCleanup).admission
	return s, release, nil
}

func sharedFilesystemFixture(t *testing.T, sandbox bool) (string, workspacegroup.Workspace, workspacegroup.Workspace, protocol.CleanupTarget) {
	t.Helper()
	root, owner, other, _ := cleanupRuntimeFixture(t, sandbox)
	t.Setenv("RADAR_AGENT_ROOT", filepath.Join(t.TempDir(), "agents"))
	owner.Sandbox.SharedDirectory = filepath.Join(t.TempDir(), workspacegroup.SharedDirectoryParent, owner.ID)
	if err := ensureSharedDirectory(owner); err != nil {
		t.Fatal(err)
	}
	writeAnchorFile(t, filepath.Join(owner.Sandbox.SharedDirectory, "a-content"))
	// Preserve the unrelated registration: Save of a single-owner fixture would
	// turn open into an unknown-workspace failure rather than recovery proof.
	updateSharedCleanupGroup(t, root, owner)
	targets, err := (Source{}).PreviewCleanup(context.Background(), integration.CleanupPreviewRequest{Task: protocol.Task{SourceRefs: []protocol.SourceRef{{ID: "workspace:" + owner.ID, Source: "workspace", Kind: "workspace"}}}})
	if err != nil || len(targets) != 1 {
		t.Fatal("shared filesystem preview", targets, err)
	}
	return root, owner, other, targets[0]
}

func TestSharedFilesystemDeletionAllowsUnrelatedRegisteredRecovery(t *testing.T) {
	for _, bound := range []bool{false, true} {
		for _, sandbox := range []bool{false, true} {
			for _, entry := range []string{"open", "create-existing"} {
				t.Run(fmt.Sprint("bound=", bound, "/", entry, "/sandbox=", sandbox), func(t *testing.T) {
					root, owner, other, target := sharedFilesystemFixture(t, sandbox)
					assertNotes := sharedFilesystemNotesUnchanged(t, owner.NotePath, other.NotePath)
					ctx, cancel := context.WithCancel(context.Background())
					entered, resume, cleanupDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
					result := make(chan error, 1)
					var once sync.Once
					unblock := func() { once.Do(func() { close(resume) }) }
					removeAll := func(directory *os.Root, name string) error {
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
						source := sharedFilesystemCleanupSource{filesystemCleanupSource{runner: &fakeRunner{}, removeAll: removeAll}}
						var err error
						if bound {
							_, err = cleanup.New([]integration.CleanupProvider{source}).Execute(ctx, protocol.CleanupPreview{Targets: []protocol.CleanupTarget{target}}, cleanup.ExecuteOptions{})
						} else {
							_, err = source.Cleanup(ctx, integration.CleanupRequest{Target: target})
						}
						result <- err
					}()
					t.Cleanup(func() { cancel(); unblock(); <-cleanupDone })
					select {
					case <-entered:
					case err := <-result:
						t.Fatal("cleanup failed before shared filesystem deletion", err)
					case <-time.After(5 * time.Second):
						t.Fatal("shared filesystem phase not reached")
					}
					assertAnchorRegistered(t, root, owner.ID)
					if _, err := os.Stat(filepath.Join(owner.Sandbox.SharedDirectory, "a-content")); !os.IsNotExist(err) {
						t.Fatal("actual shared entry deletion did not occur", err)
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
						t.Fatal("unrelated recovery waited for actual shared filesystem deletion")
					}
					assertCalledContains(t, base.calls, "tmux", "new-session")
					assertCalledContains(t, base.calls, "tmux", "switch-client")
					if sandbox {
						assertCalledContains(t, base.calls, "sbx", "create")
					}
					assertAnchorRegistered(t, root, owner.ID)
					assertAnchorRegistered(t, root, other.ID)
					assertNotes()
					unblock()
					if err := <-result; err != nil {
						t.Fatal(err)
					}
					if _, err := os.Stat(owner.Sandbox.SharedDirectory); !os.IsNotExist(err) {
						t.Fatal("shared root remains", err)
					}
					assertAnchorRegistered(t, root, other.ID)
					assertDirectAnchorAdmissionReleased(t, owner.Path)
					assertDirectAnchorAdmissionReleased(t, owner.Sandbox.SharedDirectory)
				})
			}
		}
	}
}

func TestSharedFilesystemDeletionRetainsActualResourceConflicts(t *testing.T) {
	for _, conflict := range []string{"anchor", "alias", "shared-sandbox", "shared-path", "shared-path-alias"} {
		for _, entry := range []string{"open", "create-existing"} {
			t.Run(conflict+"/"+entry, func(t *testing.T) {
				root, owner, other, target := sharedFilesystemFixture(t, true)
				selected, current := owner, owner.Path
				switch conflict {
				case "alias":
					target.Path = sharedCleanupReference(t, owner.Path, true)
				case "shared-sandbox":
					other.Sandbox.Name = owner.Sandbox.Name
					updateSharedCleanupGroup(t, root, other)
					selected, current = other, other.Path
				case "shared-path", "shared-path-alias":
					path := sharedCleanupReference(t, owner.Sandbox.SharedDirectory, conflict == "shared-path-alias")
					other.Sandbox.Mounts = append(other.Sandbox.Mounts, path)
					selected, current = other, other.Path
				}
				checked := false
				removeAll := func(directory *os.Root, name string) error {
					if err := directory.RemoveAll(name); err != nil {
						return err
					}
					checked = true
					if strings.HasPrefix(conflict, "shared-path") {
						// Public writers cannot publish this reference while cleanup
						// owns the root. Deliberately bypass to test both admission
						// and the fresh final protected-reference refusal.
						updateSharedCleanupGroup(t, root, other)
					}
					base := &fakeRunner{}
					err := recoverCleanupRuntimeFixture(context.Background(), base, root, selected, current, entry)
					if err == nil || !strings.Contains(err.Error(), "in use by another operation") {
						t.Fatal("shared filesystem phase lost resource admission", err, base.calls)
					}
					assertNotCalledContains(t, base.calls, "tmux", "new-session")
					assertNotCalledContains(t, base.calls, "tmux", "switch-client")
					assertNotCalledContains(t, base.calls, "sbx", "create")
					assertNotCalledContains(t, base.calls, "sbx", "rm")
					assertNotCalledContains(t, base.calls, "sbx", "stop")
					assertAnchorRegistered(t, root, owner.ID)
					return nil
				}
				_, err := (Source{}).cleanupWithFilesystems(context.Background(), &fakeRunner{}, integration.CleanupRequest{Target: target}, nil, (*os.Root).RemoveAll, removeAll)
				if !checked || (strings.HasPrefix(conflict, "shared-path") && err == nil) || (!strings.HasPrefix(conflict, "shared-path") && err != nil) {
					t.Fatal("shared filesystem conflict boundary not qualified", checked, err)
				}
				if strings.HasPrefix(conflict, "shared-path") {
					assertAnchorRegistered(t, root, owner.ID)
				}
				assertAnchorRegistered(t, root, other.ID)
				assertDirectAnchorAdmissionReleased(t, owner.Path)
				assertDirectAnchorAdmissionReleased(t, owner.Sandbox.SharedDirectory)
			})
		}
	}
}

func TestSharedFilesystemDeletionPreservesPartialAndChangedAuthority(t *testing.T) {
	for _, mode := range []integration.CleanupMode{integration.CleanupSafe, integration.CleanupConfirmed, integration.CleanupExpired} {
		for _, change := range []string{"owner", "owner-removed", "shared-path", "disposal-config", "configured-root", "anchor-reference", "shared-reference", "cancelled", "deletion-error", "child-replaced", "parent-replaced", "late-entry", "unrelated-update"} {
			t.Run(fmt.Sprint(mode, "/", change), func(t *testing.T) {
				root, owner, other, _ := sharedFilesystemFixture(t, false)
				assertNotes := sharedFilesystemNotesUnchanged(t, owner.NotePath, other.NotePath)
				shared := owner.Sandbox.SharedDirectory
				writeAnchorFile(t, filepath.Join(shared, "b-remaining"))
				targets, err := (Source{}).PreviewCleanup(context.Background(), integration.CleanupPreviewRequest{Task: protocol.Task{SourceRefs: []protocol.SourceRef{{ID: "workspace:" + owner.ID, Source: "workspace", Kind: "workspace"}}}, Mode: mode})
				if err != nil || len(targets) != 1 {
					t.Fatal(targets, err)
				}
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
					// Raw metadata changes deliberately bypass public-writer admission
					// to exercise fresh decisions, not to claim an allowed bypass.
					if err := workspacegroup.Update(root, func(registry *workspacegroup.Registry) error {
						for i := range registry.Workspaces {
							current := &registry.Workspaces[i]
							if current.ID == owner.ID {
								switch change {
								case "owner":
									current.SessionName = "replacement-session"
								case "shared-path":
									current.Sandbox.SharedDirectory = filepath.Join(t.TempDir(), workspacegroup.SharedDirectoryParent, owner.ID)
								}
							} else if current.ID == other.ID {
								switch change {
								case "unrelated-update":
									current.SessionName = "keep-this-update"
								case "shared-reference":
									current.NotePath = filepath.Join(shared, "b-remaining")
								case "anchor-reference":
									current.NotePath = filepath.Join(owner.Path, "late-note.md")
								}
							}
						}
						if change == "owner-removed" {
							registry.Workspaces = []workspacegroup.Workspace{other}
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
						if err := os.WriteFile(path, []byte(fmt.Sprintf("workspace: {root_dir: %q}\n", t.TempDir())), 0o600); err != nil {
							return err
						}
					case "cancelled":
						cancel()
					case "child-replaced":
						if err := os.Rename(shared, shared+"-detached"); err != nil {
							return err
						}
						writeAnchorFile(t, filepath.Join(shared, "replacement-data"))
					case "parent-replaced":
						if err := os.Rename(filepath.Dir(shared), filepath.Dir(shared)+"-detached"); err != nil {
							return err
						}
						writeAnchorFile(t, filepath.Join(shared, "replacement-data"))
					case "late-entry":
						writeAnchorFile(t, filepath.Join(shared, "late-content"))
					}
					var err error
					expectedRegistry, err = os.ReadFile(workspacegroup.Path(root))
					if err != nil {
						return err
					}
					if change == "deletion-error" {
						return errors.New("fixture error after partial shared deletion")
					}
					return nil
				}
				_, err = (Source{}).cleanupWithFilesystems(ctx, &fakeRunner{}, integration.CleanupRequest{Target: targets[0], Mode: mode}, nil, (*os.Root).RemoveAll, removeAll)
				if change == "unrelated-update" {
					if err != nil || calls != 2 {
						t.Fatal("unrelated metadata prevented shared deletion", calls, err)
					}
					registry, loadErr := workspacegroup.Load(root)
					if loadErr != nil || len(registry.Workspaces) != 1 || registry.Workspaces[0].SessionName != "keep-this-update" {
						t.Fatal("final unregister overwrote unrelated metadata", registry, loadErr)
					}
				} else {
					if err == nil || calls == 0 || expectedRegistry == nil {
						t.Fatal("partial shared deletion bypassed changed authority", calls, err)
					}
					if change == "cancelled" && !errors.Is(err, context.Canceled) {
						t.Fatal("cancellation lost", err)
					}
					after, readErr := os.ReadFile(workspacegroup.Path(root))
					if readErr != nil || !bytes.Equal(after, expectedRegistry) {
						t.Fatal("failure overwrote current registration", readErr)
					}
					switch change {
					case "child-replaced":
						assertAnchorFileKept(t, filepath.Join(shared, "replacement-data"))
						assertAnchorFileKept(t, filepath.Join(shared+"-detached", "b-remaining"))
					case "parent-replaced":
						assertAnchorFileKept(t, filepath.Join(shared, "replacement-data"))
						assertAnchorFileKept(t, filepath.Join(filepath.Dir(shared)+"-detached", owner.ID, "b-remaining"))
					case "late-entry":
						assertAnchorFileKept(t, filepath.Join(shared, "late-content"))
					default:
						assertAnchorFileKept(t, filepath.Join(shared, "b-remaining"))
					}
				}
				assertNotes()
				assertDirectAnchorAdmissionReleased(t, owner.Path)
				assertDirectAnchorAdmissionReleased(t, shared)
			})
		}
	}
}

func TestPinnedSharedDirectoryAllowsHostTempAliasAndMissingRoot(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprint("missing=", missing), func(t *testing.T) {
			root := t.TempDir()
			if missing {
				root = filepath.Join(root, "not-created")
			} else {
				alias := filepath.Join(t.TempDir(), "temp-alias")
				if err := os.Symlink(root, alias); err != nil {
					t.Fatal(err)
				}
				root = alias
			}
			t.Setenv(hostTempDirEnv, root)
			group := sharedDirectoryFixture(t, t.TempDir(), "cleanup")
			if !missing {
				if err := ensureSharedDirectory(group); err != nil {
					t.Fatal(err)
				}
				writeAnchorFile(t, filepath.Join(group.Sandbox.SharedDirectory, "content"))
			}
			if err := removeSharedDirectory(group); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(group.Sandbox.SharedDirectory); !os.IsNotExist(err) {
				t.Fatal("shared child remains or was created", err)
			}
			if missing {
				if _, err := os.Stat(root); !os.IsNotExist(err) {
					t.Fatal("missing temp root was created", err)
				}
			} else if _, err := os.Stat(filepath.Dir(group.Sandbox.SharedDirectory)); err != nil {
				t.Fatal("shared parent was removed", err)
			}
		})
	}
}

func sharedFilesystemNotesUnchanged(t *testing.T, paths ...string) func() {
	t.Helper()
	before := map[string][]byte{}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		before[path] = data
	}
	return func() {
		t.Helper()
		for path, data := range before {
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(data, after) {
				t.Fatal("canonical note changed during shared cleanup", path, err)
			}
		}
	}
}

func TestSharedFilesystemRefusesRetargetedAdmittedTempAlias(t *testing.T) {
	root, owner, _, target := sharedFilesystemFixture(t, false)
	original, replacement := t.TempDir(), t.TempDir()
	alias := filepath.Join(t.TempDir(), "host-temp")
	if err := os.Symlink(original, alias); err != nil {
		t.Fatal(err)
	}
	owner.Sandbox.SharedDirectory = filepath.Join(alias, workspacegroup.SharedDirectoryParent, owner.ID)
	if err := ensureSharedDirectory(owner); err != nil {
		t.Fatal(err)
	}
	writeAnchorFile(t, filepath.Join(owner.Sandbox.SharedDirectory, "original-content"))
	replacementFile := filepath.Join(replacement, workspacegroup.SharedDirectoryParent, owner.ID, "replacement-content")
	writeAnchorFile(t, replacementFile)
	updateSharedCleanupGroup(t, root, owner)
	retargeted := false
	removeAnchorEntry := func(directory *os.Root, name string) error {
		if err := directory.RemoveAll(name); err != nil {
			return err
		}
		if err := os.Remove(alias); err != nil {
			return err
		}
		if err := os.Symlink(replacement, alias); err != nil {
			return err
		}
		retargeted = true
		return nil
	}
	_, err := (Source{}).cleanupWithFilesystems(context.Background(), &fakeRunner{}, integration.CleanupRequest{Target: target}, nil, removeAnchorEntry, (*os.Root).RemoveAll)
	if !retargeted || err == nil {
		t.Fatal("cleanup followed a different shared root than the admitted identity", retargeted, err)
	}
	assertAnchorRegistered(t, root, owner.ID)
	assertAnchorFileKept(t, replacementFile)
	assertAnchorFileKept(t, filepath.Join(original, workspacegroup.SharedDirectoryParent, owner.ID, "original-content"))
	assertDirectAnchorAdmissionReleased(t, owner.Path)
	assertDirectAnchorAdmissionReleased(t, filepath.Join(original, workspacegroup.SharedDirectoryParent, owner.ID))
}
