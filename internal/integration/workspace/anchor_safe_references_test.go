package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"radar/internal/cleanup"
	"radar/internal/config"
	"radar/internal/integration"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
	"radar/internal/workspacelease"
)

func TestSafeAnchorProtectsPubliclyRegisteredCanonicalNotes(t *testing.T) {
	for _, alias := range []bool{false, true} {
		t.Run(fmt.Sprint("alias=", alias), func(t *testing.T) {
			root, owner, task := disposableAnchorFixture(t)
			configureDisposableEntries(t, ".pnpm-store")
			target := previewDisposableAnchor(t, task)
			note := filepath.Join(owner.Path, ".pnpm-store", "Canonical.md")
			writeAnchorFile(t, note)
			created, err := Create(context.Background(), &fakeRunner{}, CreateOptions{
				Name: "Protected note", WorkspaceRoot: root, NotePath: sharedCleanupReference(t, note, alias),
				TaskLinkingKey: "fixture:task:ABC-789", PrepareOnly: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := (Source{}).PreviewCleanup(context.Background(), integration.CleanupPreviewRequest{Task: task}); err == nil || !strings.Contains(err.Error(), "canonical note") {
				t.Fatal("preview accepted another registered canonical note", err)
			}
			collected := (Source{}).Collect(context.Background(), integration.CollectRequest{})
			foundIssue := false
			for _, observation := range collected.Observations {
				if observation.Ref.WorkspaceID == owner.ID {
					foundIssue = strings.Contains(strings.Join(observation.Ref.CleanupIssues, " "), "canonical note")
				}
			}
			if !foundIssue {
				t.Fatal("collection omitted the protected note issue", collected)
			}
			service := cleanup.New([]integration.CleanupProvider{runnerCleanupSource{runner: &fakeRunner{}}})
			result, err := service.Execute(context.Background(), protocol.CleanupPreview{Targets: []protocol.CleanupTarget{target}}, cleanup.ExecuteOptions{Mode: integration.CleanupSafe})
			if err == nil || !strings.Contains(err.Error(), "canonical note") || len(result.Targets) != 0 {
				t.Fatal("stale preview deleted another registered note", result, err)
			}
			assertAnchorFileKept(t, note)
			assertAnchorRegistered(t, root, owner.ID)
			assertAnchorRegistered(t, root, workspacegroup.ID(created.Path))
		})
	}
}

func TestAnchorReferencesRejectEveryModeBeforeBundleEffects(t *testing.T) {
	for _, mode := range []integration.CleanupMode{integration.CleanupSafe, integration.CleanupConfirmed, integration.CleanupExpired} {
		for _, kind := range []string{"canonical-note", "owner-note", "registered-workspace", "primary-repository", "additional-mount", "effective-mount", "shared-directory", "configured-mount"} {
			for _, alias := range []bool{false, true} {
				t.Run(fmt.Sprint(mode, "/", kind, "/alias=", alias), func(t *testing.T) {
					root, owner, task := disposableAnchorFixture(t)
					configureDisposableEntries(t, ".pnpm-store")
					target := previewAnchor(t, task, mode)
					path := filepath.Join(owner.Path, ".pnpm-store", "protected")
					writeAnchorFile(t, filepath.Join(path, "content"))
					reference := sharedCleanupReference(t, path, alias)
					other := workspacegroup.Workspace{Path: filepath.Join(root, "other"), Name: "Other"}
					other.ID = workspacegroup.ID(other.Path)
					switch kind {
					case "canonical-note":
						other.NotePath = filepath.Join(reference, "content")
					case "owner-note":
						owner.NotePath = filepath.Join(reference, "content")
					case "registered-workspace":
						other.Path, other.ID = reference, workspacegroup.ID(reference)
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
						configPath, err := config.Path()
						if err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(configPath, data, 0600); err != nil {
							t.Fatal(err)
						}
					}
					if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{owner, other}}); err != nil {
						t.Fatal(err)
					}
					called := false
					base := &fakeRunner{}
					service := cleanup.New([]integration.CleanupProvider{sharedCleanupProvider{call: func(context.Context) error { called = true; return nil }}, runnerCleanupSource{runner: base}})
					result, err := service.Execute(context.Background(), protocol.CleanupPreview{Targets: []protocol.CleanupTarget{{Source: "fixture"}, target}}, cleanup.ExecuteOptions{Mode: mode})
					if err == nil || !strings.Contains(err.Error(), "workspace anchor contains") || called || len(base.calls) != 0 || len(result.Targets) != 0 {
						t.Fatal("protected reference accepted before bundle effects", result, err, called, base.calls)
					}
					// Direct callers must apply the same fresh final guard.
					if _, err := (Source{}).cleanupWithRunner(context.Background(), base, integration.CleanupRequest{Target: target, Mode: mode}); err == nil || !strings.Contains(err.Error(), "workspace anchor contains") {
						t.Fatal("direct cleanup bypassed protected reference", err)
					}
					assertAnchorFileKept(t, filepath.Join(path, "content"))
					assertAnchorFileKept(t, filepath.Join(owner.Path, ".pnpm-store", "v10", "content"))
					assertAnchorRegistered(t, root, owner.ID)
					assertAnchorRegistered(t, root, other.ID)
					release, err := workspacelease.Acquire(owner.Path, true)
					if err != nil {
						t.Fatal("refusal leaked anchor admission", err)
					}
					release()
				})
			}
		}
	}
}

func TestSafeAnchorFinalGuardRechecksLateRegisteredReference(t *testing.T) {
	for _, alias := range []bool{false, true} {
		t.Run(fmt.Sprint("alias=", alias), func(t *testing.T) {
			root, owner, other, target := cleanupRuntimeFixture(t, false)
			path := filepath.Join(owner.Path, ".pnpm-store", "content")
			unblock, finished := pauseActualCleanupRuntime(t, target)
			// Deliberately bypass public reference admission to exercise the
			// final recheck, not claim raw registry writes are admitted writers.
			other.NotePath = sharedCleanupReference(t, path, alias)
			updateSharedCleanupGroup(t, root, other)
			unblock()
			if err := <-finished; err == nil || !strings.Contains(err.Error(), "canonical note") {
				t.Fatal("late registered reference was deleted", err)
			}
			assertAnchorFileKept(t, path)
			assertAnchorRegistered(t, root, owner.ID)
			assertAnchorRegistered(t, root, other.ID)
			release, err := workspacelease.AcquireSandboxMutation(owner.Sandbox.Name)
			if err != nil {
				t.Fatal("final refusal leaked runtime reservation", err)
			}
			release()
		})
	}
}

func TestSafeAnchorReferencesAllowExternalSymlinkTargetsAndPrefixSiblings(t *testing.T) {
	root, owner, task := disposableAnchorFixture(t)
	configureDisposableEntries(t, ".pnpm-store")
	external := t.TempDir()
	note := filepath.Join(external, "Canonical.md")
	writeAnchorFile(t, note)
	if err := os.RemoveAll(filepath.Join(owner.Path, ".pnpm-store")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(owner.Path, ".pnpm-store")); err != nil {
		t.Fatal(err)
	}
	other := workspacegroup.Workspace{Path: owner.Path + "-sibling", Name: "Other", NotePath: note}
	other.ID = workspacegroup.ID(other.Path)
	if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{owner, other}}); err != nil {
		t.Fatal(err)
	}
	target := previewDisposableAnchor(t, task)
	service := cleanup.New([]integration.CleanupProvider{runnerCleanupSource{runner: &fakeRunner{}}})
	if _, err := service.Execute(context.Background(), protocol.CleanupPreview{Targets: []protocol.CleanupTarget{target}}, cleanup.ExecuteOptions{Mode: integration.CleanupSafe}); err != nil {
		t.Fatal("unrelated references blocked safe unlink", err)
	}
	assertAnchorFileKept(t, note)
	assertAnchorRegistered(t, root, other.ID)
	if _, err := os.Lstat(owner.Path); !os.IsNotExist(err) {
		t.Fatal("owner anchor remains", err)
	}
}

func TestSafeAnchorPreflightDoesNotExpandResourceOnlyCleanup(t *testing.T) {
	root, owner, _ := disposableAnchorFixture(t)
	// The unconfigured directory would block anchor deletion, but no anchor
	// deletion was requested by this resource-only bundle.
	called := false
	service := cleanup.New([]integration.CleanupProvider{sharedCleanupProvider{call: func(context.Context) error { called = true; return nil }}, Source{}})
	result, err := service.Execute(context.Background(), protocol.CleanupPreview{Targets: []protocol.CleanupTarget{{Source: "fixture", Path: owner.Path, WorkspaceID: owner.ID}}}, cleanup.ExecuteOptions{Mode: integration.CleanupSafe})
	if err != nil || !called || len(result.Targets) != 1 {
		t.Fatal("anchor preflight expanded resource-only cleanup", result, err, called)
	}
	assertAnchorFileKept(t, filepath.Join(owner.Path, ".pnpm-store", "v10", "content"))
	assertAnchorRegistered(t, root, owner.ID)
}

func TestSafeAnchorPreflightBindsCanonicalAdmittedTarget(t *testing.T) {
	for _, change := range []string{"alias", "other-path", "wrong-workspace-id"} {
		t.Run(change, func(t *testing.T) {
			root, owner, task := disposableAnchorFixture(t)
			configureDisposableEntries(t, ".pnpm-store")
			target := previewDisposableAnchor(t, task)
			switch change {
			case "alias":
				target.Path = filepath.Join(t.TempDir(), "alias")
				if err := os.Symlink(owner.Path, target.Path); err != nil {
					t.Fatal(err)
				}
			case "other-path":
				target.Path = t.TempDir()
			case "wrong-workspace-id":
				target.WorkspaceID = "changed"
			}
			called := false
			service := cleanup.New([]integration.CleanupProvider{sharedCleanupProvider{call: func(context.Context) error { called = true; return nil }}, runnerCleanupSource{runner: &fakeRunner{}}})
			result, err := service.Execute(context.Background(), protocol.CleanupPreview{Targets: []protocol.CleanupTarget{{Source: "fixture"}, target}}, cleanup.ExecuteOptions{Mode: integration.CleanupSafe})
			if change == "alias" {
				if err != nil || !called || len(result.Targets) != 2 {
					t.Fatal("canonical alias did not preserve admitted cleanup", result, err, called)
				}
				if _, err := os.Stat(owner.Path); !os.IsNotExist(err) {
					t.Fatal("alias cleanup left the owner anchor", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "no longer matches") || called || len(result.Targets) != 0 {
				t.Fatal("cleanup escaped its admitted canonical owner", result, err, called)
			}
			assertAnchorFileKept(t, filepath.Join(owner.Path, ".pnpm-store", "v10", "content"))
			assertAnchorRegistered(t, root, owner.ID)
		})
	}
}
