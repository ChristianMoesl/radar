package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"radar/internal/cleanup"
	"radar/internal/config"
	"radar/internal/integration"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
	"radar/internal/workspacelease"
)

func sharedCleanupFixture(t *testing.T) (string, workspacegroup.Workspace, workspacegroup.Workspace, protocol.CleanupTarget) {
	t.Helper()
	root, owner, other, target := cleanupRuntimeFixture(t, false)
	path, err := newSharedDirectory(owner.Path)
	if err != nil {
		t.Fatal(err)
	}
	owner.Sandbox.SharedDirectory = path
	owner.Sandbox.Mounts = append(owner.Sandbox.Mounts, path)
	if err := ensureSharedDirectory(owner); err != nil {
		t.Fatal(err)
	}
	writeAnchorFile(t, filepath.Join(path, "clipboard.png"))
	updateSharedCleanupGroup(t, root, owner)
	return root, owner, other, target
}

func updateSharedCleanupGroup(t *testing.T, root string, group workspacegroup.Workspace) {
	t.Helper()
	if err := workspacegroup.Update(root, func(registry *workspacegroup.Registry) error {
		for i := range registry.Workspaces {
			if registry.Workspaces[i].ID == group.ID {
				registry.Workspaces[i] = group
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func sharedCleanupReference(t *testing.T, path string, alias bool) string {
	t.Helper()
	if !alias {
		return path
	}
	link := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(filepath.Dir(path), link); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(link, filepath.Base(path))
}

func assertSharedCleanupReleased(t *testing.T, path string) {
	t.Helper()
	release, err := workspacelease.Acquire(path, true)
	if err != nil {
		t.Fatal("shared-directory deletion admission leaked", err)
	}
	release()
}

func TestSharedCleanupRejectsLatePublicCanonicalNoteBeforeCreation(t *testing.T) {
	for _, alias := range []bool{false, true} {
		t.Run(fmt.Sprint("alias=", alias), func(t *testing.T) {
			root, owner, _, target := sharedCleanupFixture(t)
			note := filepath.Join(owner.Sandbox.SharedDirectory, "Canonical.md")
			writeAnchorFile(t, note)
			selected := sharedCleanupReference(t, note, alias)
			unblock, finished := pauseActualCleanupRuntime(t, target)
			_, err := Create(context.Background(), &fakeRunner{}, CreateOptions{Name: "Late note", WorkspaceRoot: root, NotePath: selected, TaskLinkingKey: "fixture:task:ABC-789", PrepareOnly: true})
			if err == nil || !strings.Contains(err.Error(), "in use by another operation") {
				t.Fatal("public creation bypassed cleanup's separate deletion root", err)
			}
			registry, err := workspacegroup.Load(root)
			if err != nil {
				t.Fatal(err)
			}
			if _, found := workspacegroup.FindByTaskLinkingKey(registry, "fixture:task:ABC-789"); found {
				t.Fatal("rejected creation published a canonical note reference")
			}
			assertAnchorFileKept(t, note)
			unblock()
			if err := <-finished; err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(owner.Sandbox.SharedDirectory); !os.IsNotExist(err) {
				t.Fatal("owner's unreferenced temporary contents were not removed", err)
			}
			assertSharedCleanupReleased(t, owner.Sandbox.SharedDirectory)
		})
	}
}

func TestSharedCleanupStillAllowsBothUnrelatedRegisteredRecoveryEntries(t *testing.T) {
	for _, sandbox := range []bool{false, true} {
		for _, entry := range []string{"open", "create-existing"} {
			t.Run(fmt.Sprint(entry, "/sandbox=", sandbox), func(t *testing.T) {
				root, owner, other, target := sharedCleanupFixture(t)
				if sandbox {
					other.Sandbox = &workspacegroup.Sandbox{Name: "unrelated-runtime", Agent: "shell", Mounts: []string{other.Path}}
					updateSharedCleanupGroup(t, root, other)
				}
				unblock, finished := pauseActualCleanupRuntime(t, target)
				runner := &fakeRunner{}
				activated, done := make(chan error, 1), make(chan struct{})
				go func() {
					activated <- recoverCleanupRuntimeFixture(context.Background(), runner, root, other, other.Path, entry)
					close(done)
				}()
				t.Cleanup(func() { unblock(); <-done })
				select {
				case err := <-activated:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("unrelated recovery waited for shared-directory cleanup")
				}
				assertCalledContains(t, runner.calls, "tmux", "new-session")
				if sandbox {
					assertCalledContains(t, runner.calls, "sbx", "create")
				}
				assertAnchorFileKept(t, filepath.Join(owner.Sandbox.SharedDirectory, "clipboard.png"))
				unblock()
				if err := <-finished; err != nil {
					t.Fatal(err)
				}
				assertSharedCleanupReleased(t, owner.Sandbox.SharedDirectory)
			})
		}
	}
}

type sharedCleanupProvider struct {
	integration.Integration
	call func(context.Context) error
}

func (sharedCleanupProvider) Descriptor() integration.Descriptor {
	return integration.Descriptor{Name: "fixture"}
}
func (sharedCleanupProvider) PreviewCleanup(context.Context, integration.CleanupPreviewRequest) ([]protocol.CleanupTarget, error) {
	return nil, nil
}
func (p sharedCleanupProvider) Cleanup(ctx context.Context, req integration.CleanupRequest) (protocol.CleanupTarget, error) {
	return req.Target, p.call(ctx)
}

func TestSharedCleanupProtectsExistingReferencesBeforeAnyBundleEffects(t *testing.T) {
	for _, mode := range []integration.CleanupMode{integration.CleanupSafe, integration.CleanupConfirmed, integration.CleanupExpired} {
		for _, kind := range []string{"canonical-note", "owner-note", "primary-repository", "additional-mount", "effective-mount", "shared-directory", "configured-mount"} {
			for _, alias := range []bool{false, true} {
				t.Run(fmt.Sprint(mode, "/", kind, "/alias=", alias), func(t *testing.T) {
					root, owner, other, target := sharedCleanupFixture(t)
					reference := sharedCleanupReference(t, filepath.Join(owner.Sandbox.SharedDirectory, "protected"), alias)
					switch kind {
					case "canonical-note":
						other.NotePath = reference
					case "owner-note":
						owner.NotePath = reference
					case "primary-repository":
						other.Members = []workspacegroup.Member{{Path: filepath.Join(other.Path, "member"), Repository: reference, Branch: "fixture"}}
					case "additional-mount":
						other.Sandbox = &workspacegroup.Sandbox{Name: "other-runtime", Agent: "shell", AdditionalMounts: []workspacegroup.SandboxMount{{Path: reference, ReadOnly: true}}}
					case "effective-mount":
						other.Sandbox = &workspacegroup.Sandbox{Name: "other-runtime", Agent: "shell", Mounts: []string{reference + ":ro"}}
					case "shared-directory":
						other.Sandbox = &workspacegroup.Sandbox{Name: "other-runtime", Agent: "shell", SharedDirectory: filepath.Join(reference, workspacegroup.SharedDirectoryParent, other.ID)}
					case "configured-mount":
						cfg, err := config.Load()
						if err != nil {
							t.Fatal(err)
						}
						cfg.SBX.AdditionalMounts = []string{reference + ":ro"}
						data, err := yaml.Marshal(cfg)
						if err != nil {
							t.Fatal(err)
						}
						path, err := config.Path()
						if err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(path, data, 0600); err != nil {
							t.Fatal(err)
						}
					}
					updateSharedCleanupGroup(t, root, owner)
					updateSharedCleanupGroup(t, root, other)
					called := false
					base := &fakeRunner{}
					service := cleanup.New([]integration.CleanupProvider{sharedCleanupProvider{call: func(context.Context) error { called = true; return nil }}, runnerCleanupSource{runner: base}})
					result, err := service.Execute(context.Background(), protocol.CleanupPreview{Targets: []protocol.CleanupTarget{{Source: "fixture"}, target}}, cleanup.ExecuteOptions{Mode: mode})
					if err == nil || !strings.Contains(err.Error(), "shared directory cleanup") || called || len(base.calls) != 0 || len(result.Targets) != 0 {
						t.Fatal("existing protected reference was not rejected before bundle effects", err, called, base.calls, result)
					}
					assertAnchorFileKept(t, filepath.Join(owner.Path, ".pnpm-store", "content"))
					assertAnchorFileKept(t, filepath.Join(owner.Sandbox.SharedDirectory, "clipboard.png"))
					assertSharedCleanupReleased(t, owner.Sandbox.SharedDirectory)
					// Direct Source cleanup must apply the same metadata refusal.
					_, err = (Source{}).cleanupWithRunner(context.Background(), base, integration.CleanupRequest{Target: target, Mode: mode})
					if err == nil || !strings.Contains(err.Error(), "shared directory cleanup") {
						t.Fatal("direct cleanup bypassed reference protection", err)
					}
					assertAnchorFileKept(t, filepath.Join(owner.Path, ".pnpm-store", "content"))
					assertSharedCleanupReleased(t, owner.Sandbox.SharedDirectory)
				})
			}
		}
	}
}

func TestSharedCleanupAdmissionRetainedThroughEarlierProviderAndReleasedEveryOutcome(t *testing.T) {
	for _, outcome := range []string{"success", "error", "cancelled"} {
		t.Run(outcome, func(t *testing.T) {
			_, owner, _, target := sharedCleanupFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			base := &fakeRunner{}
			problem := errors.New("fixture provider failed")
			provider := sharedCleanupProvider{call: func(context.Context) error {
				if release, err := workspacelease.AcquireReferences(context.Background(), filepath.Join(owner.Sandbox.SharedDirectory, "new-note.md")); err == nil {
					release()
					t.Error("shared-directory admission was not held before earlier provider")
				}
				if outcome == "error" {
					return problem
				}
				if outcome == "cancelled" {
					cancel()
				}
				return nil
			}}
			service := cleanup.New([]integration.CleanupProvider{provider, runnerCleanupSource{runner: base}})
			result, err := service.Execute(ctx, protocol.CleanupPreview{Targets: []protocol.CleanupTarget{{Source: "fixture"}, target}}, cleanup.ExecuteOptions{Mode: integration.CleanupSafe})
			switch outcome {
			case "success":
				if err != nil || len(result.Targets) != 2 {
					t.Fatal(result, err)
				}
			case "error":
				if !errors.Is(err, problem) || len(result.Targets) != 0 {
					t.Fatal(result, err)
				}
			case "cancelled":
				if !errors.Is(err, context.Canceled) || len(result.Targets) != 1 {
					t.Fatal(result, err)
				}
			}
			if outcome != "success" {
				assertAnchorFileKept(t, filepath.Join(owner.Path, ".pnpm-store", "content"))
				assertAnchorFileKept(t, filepath.Join(owner.Sandbox.SharedDirectory, "clipboard.png"))
				if len(base.calls) != 0 {
					t.Fatal("later provider ran after failure", base.calls)
				}
			}
			assertSharedCleanupReleased(t, owner.Sandbox.SharedDirectory)
		})
	}
}

func TestSharedCleanupDirectAndBundleRejectActiveReferenceOwner(t *testing.T) {
	for _, direct := range []bool{false, true} {
		t.Run(fmt.Sprint("direct=", direct), func(t *testing.T) {
			_, owner, _, target := sharedCleanupFixture(t)
			release, err := workspacelease.AcquireReferences(context.Background(), filepath.Join(owner.Sandbox.SharedDirectory, "active"))
			if err != nil {
				t.Fatal(err)
			}
			base := &fakeRunner{}
			if direct {
				_, err = (Source{}).cleanupWithRunner(context.Background(), base, integration.CleanupRequest{Target: target})
			} else {
				_, err = cleanup.New([]integration.CleanupProvider{runnerCleanupSource{runner: base}}).Execute(context.Background(), protocol.CleanupPreview{Targets: []protocol.CleanupTarget{target}}, cleanup.ExecuteOptions{})
			}
			release()
			if err == nil || !strings.Contains(err.Error(), "in use by another operation") || len(base.calls) != 0 {
				t.Fatal("cleanup bypassed reference owner", err, base.calls)
			}
			assertAnchorFileKept(t, filepath.Join(owner.Path, ".pnpm-store", "content"))
			assertAnchorFileKept(t, filepath.Join(owner.Sandbox.SharedDirectory, "clipboard.png"))
			assertSharedCleanupReleased(t, owner.Sandbox.SharedDirectory)
		})
	}
}

func TestSharedCleanupBoundRegistrationDriftRefusesNewDeletionRoot(t *testing.T) {
	for _, provisioned := range []bool{false, true} {
		t.Run(fmt.Sprint("provisioned=", provisioned), func(t *testing.T) {
			root, owner, _, target := sharedCleanupFixture(t)
			old := owner.Sandbox.SharedDirectory
			if !provisioned {
				owner.Sandbox.SharedDirectory = ""
				owner.Sandbox.Mounts = []string{owner.Path}
				updateSharedCleanupGroup(t, root, owner)
			}
			base := &fakeRunner{}
			provider := sharedCleanupProvider{call: func(context.Context) error {
				owner.Sandbox.SharedDirectory = filepath.Join(t.TempDir(), workspacegroup.SharedDirectoryParent, owner.ID)
				owner.Sandbox.Mounts = []string{owner.Path, owner.Sandbox.SharedDirectory}
				if err := ensureSharedDirectory(owner); err != nil {
					return err
				}
				writeAnchorFile(t, filepath.Join(owner.Sandbox.SharedDirectory, "new.png"))
				updateSharedCleanupGroup(t, root, owner)
				return nil
			}}
			result, err := cleanup.New([]integration.CleanupProvider{provider, runnerCleanupSource{runner: base}}).Execute(context.Background(), protocol.CleanupPreview{Targets: []protocol.CleanupTarget{{Source: "fixture"}, target}}, cleanup.ExecuteOptions{})
			if err == nil || !strings.Contains(err.Error(), "shared directory changed") || len(result.Targets) != 1 || len(base.calls) != 0 {
				t.Fatal("drift redirected cleanup", result, err, base.calls)
			}
			assertAnchorFileKept(t, filepath.Join(owner.Path, ".pnpm-store", "content"))
			assertAnchorFileKept(t, filepath.Join(old, "clipboard.png"))
			assertAnchorFileKept(t, filepath.Join(owner.Sandbox.SharedDirectory, "new.png"))
			assertSharedCleanupReleased(t, old)
			assertSharedCleanupReleased(t, owner.Sandbox.SharedDirectory)
		})
	}
}

func TestSharedCleanupFinalReferenceGuardRejectsLateMetadataBypass(t *testing.T) {
	root, owner, other, target := sharedCleanupFixture(t)
	unblock, finished := pauseActualCleanupRuntime(t, target)
	other.NotePath = filepath.Join(owner.Sandbox.SharedDirectory, "clipboard.png")
	// Deliberately bypass managed reference admission to qualify the fresh
	// final metadata guard too; this is not a supported public writer.
	updateSharedCleanupGroup(t, root, other)
	unblock()
	if err := <-finished; err == nil || !strings.Contains(err.Error(), "shared directory cleanup") {
		t.Fatal("final reference guard trusted old admission metadata", err)
	}
	assertAnchorFileKept(t, other.NotePath)
	assertAnchorFileKept(t, filepath.Join(owner.Path, ".pnpm-store", "content"))
	registry, err := workspacegroup.Load(root)
	if _, found := workspacegroup.FindByID(registry, owner.ID); err != nil || !found {
		t.Fatal("final owner reference disappeared", err)
	}
	assertSharedCleanupReleased(t, owner.Sandbox.SharedDirectory)
}

func TestSharedCleanupAdmissionDeduplicatesAndUnwindsPartialSets(t *testing.T) {
	root, first, _, _ := sharedCleanupFixture(t)
	second := sharedDirectoryFixture(t, root, "second-owner")
	groups := []workspacegroup.Workspace{first, first, second}
	paths := []string{first.Sandbox.SharedDirectory, second.Sandbox.SharedDirectory}
	sort.Strings(paths)
	held, err := workspacelease.Acquire(paths[1], false)
	if err != nil {
		t.Fatal(err)
	}
	_, release, err := acquireSharedDirectoryCleanup(context.Background(), root, groups)
	if release != nil {
		release()
	}
	held()
	if err == nil || !strings.Contains(err.Error(), "in use") {
		t.Fatal("later conflict was ignored", err)
	}
	for _, path := range paths {
		assertSharedCleanupReleased(t, path)
	}
	_, release, err = acquireSharedDirectoryCleanup(context.Background(), root, groups)
	if err != nil {
		t.Fatal("duplicate owner self-conflicted", err)
	}
	release()
	for _, path := range paths {
		assertSharedCleanupReleased(t, path)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, release, err := acquireSharedDirectoryCleanup(ctx, root, groups); !errors.Is(err, context.Canceled) {
		if release != nil {
			release()
		}
		t.Fatal("cancelled admission accepted", err)
	}
	for _, path := range paths {
		assertSharedCleanupReleased(t, path)
	}
}

// Err is queried synchronously, as in the reference-admission cancellation
// tests. Exercise cancellation before, during and after ownership.
type cancelSharedCleanupAdmission struct {
	context.Context
	remaining int
}

func (c *cancelSharedCleanupAdmission) Err() error {
	c.remaining--
	if c.remaining <= 0 {
		return context.Canceled
	}
	return nil
}

func TestSharedCleanupCancellationAndInvalidResolutionReleaseAllResources(t *testing.T) {
	root, first, _, _ := sharedCleanupFixture(t)
	second := sharedDirectoryFixture(t, root, "second-owner")
	for cancelAt := 1; cancelAt <= 7; cancelAt++ {
		ctx := &cancelSharedCleanupAdmission{Context: context.Background(), remaining: cancelAt}
		_, release, err := acquireSharedDirectoryCleanup(ctx, root, []workspacegroup.Workspace{first, second})
		if err == nil {
			release()
		} else if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		assertSharedCleanupReleased(t, first.Sandbox.SharedDirectory)
		assertSharedCleanupReleased(t, second.Sandbox.SharedDirectory)
	}
	alias := filepath.Join(t.TempDir(), workspacegroup.SharedDirectoryParent)
	if err := os.Symlink(filepath.Dir(first.Sandbox.SharedDirectory), alias); err != nil {
		t.Fatal(err)
	}
	duplicate := first
	sandbox := *first.Sandbox
	duplicate.Sandbox = &sandbox
	duplicate.Sandbox.SharedDirectory = filepath.Join(alias, first.ID)
	_, release, err := acquireSharedDirectoryCleanup(context.Background(), root, []workspacegroup.Workspace{first, duplicate})
	if err != nil {
		t.Fatal("physical alias self-conflicted", err)
	}
	release()
	assertSharedCleanupReleased(t, first.Sandbox.SharedDirectory)
	dangling := filepath.Join(t.TempDir(), "dangling")
	if err := os.Symlink(filepath.Join(t.TempDir(), "absent"), dangling); err != nil {
		t.Fatal(err)
	}
	second.Sandbox.SharedDirectory = filepath.Join(dangling, workspacegroup.SharedDirectoryParent, second.ID)
	if _, release, err := acquireSharedDirectoryCleanup(context.Background(), root, []workspacegroup.Workspace{first, second}); err == nil {
		release()
		t.Fatal("unresolved shared identity accepted")
	}
	assertSharedCleanupReleased(t, first.Sandbox.SharedDirectory)
}

func TestSharedCleanupUnprovisionedAndRetireDoNotInventDeletionResources(t *testing.T) {
	root, original, _, target := sharedCleanupFixture(t)
	path := original.Sandbox.SharedDirectory
	for _, mode := range []integration.CleanupMode{integration.CleanupSafe, integration.CleanupRetire} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			owner := original
			sandbox := *original.Sandbox
			owner.Sandbox = &sandbox
			if mode == integration.CleanupSafe {
				owner.Sandbox.SharedDirectory = ""
				owner.Sandbox.Mounts = []string{owner.Path}
			}
			updateSharedCleanupGroup(t, root, owner)
			bound, release, err := (Source{}).admitCleanupWithRunner(context.Background(), &fakeRunner{}, []protocol.CleanupTarget{target}, mode)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			if bound == nil {
				t.Fatal("missing bound source")
			}
			references, err := workspacelease.AcquireReferences(context.Background(), filepath.Join(path, "keep"))
			if err != nil {
				t.Fatal("nondeleting mode guessed a temporary resource", err)
			}
			references()
			assertAnchorFileKept(t, filepath.Join(path, "clipboard.png"))
		})
	}
}
