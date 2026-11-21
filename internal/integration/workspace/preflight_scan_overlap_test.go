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

	"go.yaml.in/yaml/v3"

	"radar/internal/cleanup"
	"radar/internal/config"
	"radar/internal/integration"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
	"radar/internal/workspacelease"
)

// Service calls preflight on the original provider, not its admitted wrapper.
// Override only its private scan, retaining real admission and later cleanup.
type scanCleanupSource struct {
	runnerCleanupSource
	scan func(*cleanupAnchor, workspacegroup.Registry, workspacegroup.Workspace, []string, integration.CleanupMode) (anchorEntries, error)
}

func (s scanCleanupSource) PreflightCleanup(ctx context.Context, targets []protocol.CleanupTarget, mode integration.CleanupMode) error {
	return s.Source.preflightCleanupWithScan(ctx, targets, mode, s.scan)
}

func TestPreflightScanAllowsUnrelatedRegisteredRecovery(t *testing.T) {
	for _, mode := range []integration.CleanupMode{integration.CleanupSafe, integration.CleanupConfirmed, integration.CleanupExpired} {
		for _, service := range []bool{false, true} {
			for _, sandbox := range []bool{false, true} {
				for _, entry := range []string{"open", "create-existing"} {
					t.Run(fmt.Sprint(mode, "/service=", service, "/", entry, "/sandbox=", sandbox), func(t *testing.T) {
						root, owner, other, target := cleanupRuntimeFixture(t, sandbox)
						t.Setenv("RADAR_AGENT_ROOT", filepath.Join(t.TempDir(), "agents"))
						assertNotes := sharedFilesystemNotesUnchanged(t, owner.NotePath, other.NotePath)
						ctx, cancel := context.WithCancel(context.Background())
						entered, resume, preflightDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
						result := make(chan error, 1)
						var once sync.Once
						unblock := func() { once.Do(func() { close(resume) }) }
						scan := func(a *cleanupAnchor, registry workspacegroup.Registry, group workspacegroup.Workspace, disposable []string, mode integration.CleanupMode) (anchorEntries, error) {
							entries, err := a.entries(registry, group, disposable, mode)
							if err != nil {
								return entries, err
							}
							close(entered) // Actual rooted classification/traversal has run.
							select {
							case <-resume:
								return entries, ctx.Err()
							case <-ctx.Done():
								return entries, ctx.Err()
							}
						}
						go func() {
							defer close(preflightDone)
							source := scanCleanupSource{runnerCleanupSource: runnerCleanupSource{runner: &fakeRunner{}}, scan: scan}
							var err error
							if service {
								_, err = cleanup.New([]integration.CleanupProvider{source}).Execute(ctx, protocol.CleanupPreview{Targets: []protocol.CleanupTarget{target}}, cleanup.ExecuteOptions{Mode: mode})
							} else {
								err = source.PreflightCleanup(ctx, []protocol.CleanupTarget{target}, mode)
							}
							result <- err
						}()
						t.Cleanup(func() { cancel(); unblock(); <-preflightDone })
						select {
						case <-entered:
						case err := <-result:
							t.Fatal("preflight failed before actual scan boundary", err)
						case <-time.After(5 * time.Second):
							t.Fatal("actual preflight scan not reached")
						}
						assertAnchorRegistered(t, root, owner.ID)
						assertAnchorFileKept(t, filepath.Join(owner.Path, ".pnpm-store", "content"))
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
							t.Fatal("unrelated recovery waited for actual preflight scan transaction")
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
						assertAnchorRegistered(t, root, other.ID)
						assertNotes()
						assertDirectAnchorAdmissionReleased(t, owner.Path)
					})
				}
			}
		}
	}
}

func TestPreflightScanRetainsServiceResourceConflicts(t *testing.T) {
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
					other.Sandbox.Mounts = append(other.Sandbox.Mounts, sharedCleanupReference(t, owner.Sandbox.SharedDirectory, conflict == "shared-path-alias"))
					selected, current = other, other.Path
				}
				stop := errors.New("fixture stops after conflict proof")
				checked := false
				scan := func(a *cleanupAnchor, registry workspacegroup.Registry, group workspacegroup.Workspace, disposable []string, mode integration.CleanupMode) (anchorEntries, error) {
					entries, err := a.entries(registry, group, disposable, mode)
					if err != nil {
						return entries, err
					}
					checked = true
					if strings.HasPrefix(conflict, "shared-path") {
						// Raw bypass tests the retained admission, not public-writer permission.
						updateSharedCleanupGroup(t, root, other)
					}
					base := &fakeRunner{}
					err = recoverCleanupRuntimeFixture(context.Background(), base, root, selected, current, entry)
					if err == nil {
						t.Fatal("actual preflight scan lost resource conflict", base.calls)
					}
					for _, call := range base.calls {
						if call.name == "tmux" || (call.name == "sbx" && len(call.args) > 0 && call.args[0] != "ls") {
							t.Fatal("conflicting recovery mutated or switched", call)
						}
					}
					return entries, stop
				}
				source := scanCleanupSource{runnerCleanupSource: runnerCleanupSource{runner: &fakeRunner{}}, scan: scan}
				// The actual reusable SBX name is admitted before provider preflight.
				// Register its capability too; the failure-only stub must remain
				// unreachable because the real scan stops the bundle first.
				targets := []protocol.CleanupTarget{{Source: "sbx", Kind: "sandbox", ResourceID: owner.Sandbox.Name, Path: owner.Path, WorkspaceID: owner.ID}, target}
				result, err := cleanup.New([]integration.CleanupProvider{source, failedSandboxCleanup{}}).Execute(context.Background(), protocol.CleanupPreview{Targets: targets}, cleanup.ExecuteOptions{})
				if !checked || !errors.Is(err, stop) || len(result.Targets) != 0 {
					t.Fatal("conflict proof did not run at actual preflight boundary", checked, result, err)
				}
				assertAnchorRegistered(t, root, owner.ID)
				assertAnchorFileKept(t, filepath.Join(owner.Path, ".pnpm-store", "content"))
				assertAnchorFileKept(t, filepath.Join(owner.Sandbox.SharedDirectory, "a-content"))
				assertDirectAnchorAdmissionReleased(t, owner.Path)
				assertDirectAnchorAdmissionReleased(t, owner.Sandbox.SharedDirectory)
				release, err := workspacelease.AcquireSandboxMutation(owner.Sandbox.Name)
				if err != nil {
					t.Fatal("preflight failure leaked reusable runtime admission", err)
				}
				release()
			})
		}
	}
}

func TestPreflightScanRevalidatesBeforeBundleEffects(t *testing.T) {
	for _, mode := range []integration.CleanupMode{integration.CleanupSafe, integration.CleanupConfirmed, integration.CleanupExpired} {
		for _, change := range []string{"owner", "owner-removed", "member", "disposal-config", "configured-root", "canonical-reference", "configured-reference", "cancelled", "scan-error", "anchor-replaced", "root-replaced", "unrelated-update"} {
			t.Run(fmt.Sprint(mode, "/", change), func(t *testing.T) {
				root, owner, other, target := cleanupRuntimeFixture(t, false)
				t.Setenv("RADAR_AGENT_ROOT", filepath.Join(t.TempDir(), "agents"))
				assertNotes := sharedFilesystemNotesUnchanged(t, owner.NotePath, other.NotePath)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				stop := errors.New("fixture scan failure")
				providerReached := errors.New("fixture reached first provider")
				var expectedRegistry []byte
				var pinned *cleanupAnchor
				scans := 0
				scan := func(a *cleanupAnchor, registry workspacegroup.Registry, group workspacegroup.Workspace, disposable []string, mode integration.CleanupMode) (anchorEntries, error) {
					entries, err := a.entries(registry, group, disposable, mode)
					if err != nil {
						return entries, err
					}
					scans++
					pinned = a
					switch change {
					case "owner":
						owner.SessionName = "replacement-session"
						updateSharedCleanupGroup(t, root, owner)
					case "owner-removed":
						if err := workspacegroup.Update(root, func(registry *workspacegroup.Registry) error {
							registry.Workspaces = []workspacegroup.Workspace{other}
							return nil
						}); err != nil {
							return entries, err
						}
					case "member":
						owner.Members = []workspacegroup.Member{{Path: filepath.Join(owner.Path, "new-member"), Repository: t.TempDir(), Branch: "new-branch"}}
						updateSharedCleanupGroup(t, root, owner)
					case "disposal-config":
						configureDisposableEntries(t)
					case "configured-root":
						path := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "radar", "config.yaml")
						if err := os.WriteFile(path, []byte(fmt.Sprintf("workspace: {root_dir: %q}\n", t.TempDir())), 0600); err != nil {
							return entries, err
						}
					case "canonical-reference":
						other.NotePath = filepath.Join(owner.Path, ".pnpm-store", "content")
						updateSharedCleanupGroup(t, root, other)
					case "configured-reference":
						cfg, err := config.Load()
						if err != nil {
							return entries, err
						}
						cfg.SBX.AdditionalMounts = []string{filepath.Join(owner.Path, ".pnpm-store") + ":ro"}
						data, err := yaml.Marshal(cfg)
						if err != nil {
							return entries, err
						}
						path, err := config.Path()
						if err != nil {
							return entries, err
						}
						if err := os.WriteFile(path, data, 0600); err != nil {
							return entries, err
						}
					case "cancelled":
						cancel()
					case "anchor-replaced":
						if err := os.Rename(owner.Path, owner.Path+"-detached"); err != nil {
							return entries, err
						}
						writeAnchorFile(t, filepath.Join(owner.Path, "replacement-data"))
					case "root-replaced":
						if err := os.Rename(root, root+"-detached"); err != nil {
							return entries, err
						}
						writeAnchorFile(t, filepath.Join(owner.Path, "replacement-data"))
						if err := workspacegroup.Save(root, registry); err != nil {
							return entries, err
						}
					case "unrelated-update":
						other.SessionName = "keep-unrelated-update"
						updateSharedCleanupGroup(t, root, other)
					}
					expectedRegistry, err = os.ReadFile(workspacegroup.Path(root))
					if err != nil {
						return entries, err
					}
					if change == "scan-error" {
						return entries, stop
					}
					return entries, nil
				}
				called := false
				first := sharedCleanupProvider{call: func(context.Context) error { called = true; return providerReached }}
				source := scanCleanupSource{runnerCleanupSource: runnerCleanupSource{runner: &fakeRunner{}}, scan: scan}
				targets := []protocol.CleanupTarget{{Source: "fixture"}, target}
				result, err := cleanup.New([]integration.CleanupProvider{first, source}).Execute(ctx, protocol.CleanupPreview{Targets: targets}, cleanup.ExecuteOptions{Mode: mode})
				if scans != 1 || expectedRegistry == nil || len(result.Targets) != 0 {
					t.Fatal("actual preflight boundary not exercised", scans, result, err)
				}
				if change == "unrelated-update" {
					if !called || !errors.Is(err, providerReached) {
						t.Fatal("unrelated update blocked fresh preflight", called, err)
					}
				} else if err == nil || called {
					t.Fatal("changed authority allowed bundle effects", called, err)
				}
				if change == "scan-error" && !errors.Is(err, stop) {
					t.Fatal("scan error lost", err)
				}
				if change == "cancelled" && !errors.Is(err, context.Canceled) {
					t.Fatal("cancellation lost", err)
				}
				after, readErr := os.ReadFile(workspacegroup.Path(root))
				if readErr != nil || !bytes.Equal(after, expectedRegistry) {
					t.Fatal("read-only preflight overwrote current registration", readErr)
				}
				if _, err := pinned.parent.Stat("."); err == nil {
					t.Fatal("preflight leaked pinned parent handle")
				}
				if pinned.anchor != nil {
					if _, err := pinned.anchor.Stat("."); err == nil {
						t.Fatal("preflight leaked pinned anchor handle")
					}
				}
				assertNotes()
				assertDirectAnchorAdmissionReleased(t, owner.Path)
			})
		}
	}
}

func TestPreflightScanRefusesRetargetedSafeAlias(t *testing.T) {
	root, owner, _, target := cleanupRuntimeFixture(t, false)
	t.Setenv("RADAR_AGENT_ROOT", filepath.Join(t.TempDir(), "agents"))
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(owner.Path, alias); err != nil {
		t.Fatal(err)
	}
	target.Path = alias
	other := t.TempDir()
	called := false
	scan := func(a *cleanupAnchor, registry workspacegroup.Registry, group workspacegroup.Workspace, disposable []string, mode integration.CleanupMode) (anchorEntries, error) {
		entries, err := a.entries(registry, group, disposable, mode)
		if err != nil {
			return entries, err
		}
		called = true
		if err := os.Remove(alias); err != nil {
			return entries, err
		}
		return entries, os.Symlink(other, alias)
	}
	err := (Source{}).preflightCleanupWithScan(context.Background(), []protocol.CleanupTarget{target}, integration.CleanupSafe, scan)
	if !called || err == nil || !strings.Contains(err.Error(), "no longer matches") {
		t.Fatal("preflight accepted a retargeted admitted alias", called, err)
	}
	assertAnchorRegistered(t, root, owner.ID)
	assertAnchorFileKept(t, filepath.Join(owner.Path, ".pnpm-store", "content"))
}

func TestPreflightScanDeduplicatesAndPreservesMissingAnchors(t *testing.T) {
	for _, mode := range []integration.CleanupMode{integration.CleanupSafe, integration.CleanupConfirmed, integration.CleanupExpired} {
		for _, missing := range []bool{false, true} {
			t.Run(fmt.Sprint(mode, "/missing=", missing), func(t *testing.T) {
				root, owner, _, target := cleanupRuntimeFixture(t, false)
				if missing {
					if err := os.RemoveAll(owner.Path); err != nil {
						t.Fatal(err)
					}
				}
				var pinned *cleanupAnchor
				scans := 0
				scan := func(a *cleanupAnchor, registry workspacegroup.Registry, group workspacegroup.Workspace, disposable []string, mode integration.CleanupMode) (anchorEntries, error) {
					scans++
					pinned = a
					return a.entries(registry, group, disposable, mode)
				}
				err := (Source{}).preflightCleanupWithScan(context.Background(), []protocol.CleanupTarget{target, target}, mode, scan)
				if err != nil || scans != 1 {
					t.Fatal("duplicate target rescanned or missing anchor was not idempotent", scans, err)
				}
				if _, err := pinned.parent.Stat("."); err == nil {
					t.Fatal("successful preflight leaked parent handle")
				}
				if _, err := os.Stat(owner.Path); missing != os.IsNotExist(err) {
					t.Fatal("read-only preflight changed missing anchor state", err)
				}
				assertAnchorRegistered(t, root, owner.ID)
			})
		}
	}
}
