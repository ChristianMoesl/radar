package workspace

import (
	"context"
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

// Matching ancestor admission is a prerequisite to narrowing note transactions,
// not a claim that missing-runtime recovery already overlaps note-held cleanup.
func TestWorkspaceProtectedReferencesRejectActualCleanupAncestors(t *testing.T) {
	for _, kind := range []string{"canonical-note", "primary-repository", "additional-mount", "effective-mount"} {
		for _, alias := range []bool{false, true} {
			t.Run(kind+map[bool]string{false: "/physical", true: "/alias"}[alias], func(t *testing.T) {
				base := t.TempDir()
				t.Setenv("RADAR_SOCKET", filepath.Join(base, "radar.sock"))
				target, other := filepath.Join(base, "cleanup"), filepath.Join(base, "other")
				if err := os.MkdirAll(target, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(other, 0700); err != nil {
					t.Fatal(err)
				}
				reference := filepath.Join(target, "protected")
				if alias {
					link := filepath.Join(base, "alias")
					if err := os.Symlink(target, link); err != nil {
						t.Fatal(err)
					}
					reference = filepath.Join(link, "protected")
				}
				group := workspacegroup.Workspace{ID: workspacegroup.ID(other), Name: "Other", Path: other}
				switch kind {
				case "canonical-note":
					group.NotePath = filepath.Join(reference, "note.md")
				case "primary-repository":
					group.Members = []workspacegroup.Member{{Path: filepath.Join(other, "member"), Repository: reference, Branch: "fixture"}}
				case "additional-mount":
					group.Sandbox = &workspacegroup.Sandbox{Name: "other-runtime", AdditionalMounts: []workspacegroup.SandboxMount{{Path: reference, ReadOnly: true}}}
				case "effective-mount":
					group.Sandbox = &workspacegroup.Sandbox{Name: "other-runtime", Mounts: []string{reference + ":ro"}}
				}
				entered, resume := make(chan struct{}), make(chan struct{})
				done := make(chan error, 1)
				var once sync.Once
				unblock := func() { once.Do(func() { close(resume) }) }
				service := cleanup.New([]integration.CleanupProvider{activationCleanup{started: entered, resume: resume}})
				go func() {
					_, err := service.Execute(context.Background(), protocol.CleanupPreview{Targets: []protocol.CleanupTarget{{Source: "fixture", Kind: "workspace", Path: target}}}, cleanup.ExecuteOptions{Mode: integration.CleanupConfirmed})
					done <- err
				}()
				defer func() {
					unblock()
					if err := <-done; err != nil {
						t.Error(err)
					}
				}()
				select {
				case <-entered:
				case <-time.After(5 * time.Second):
					t.Fatal("cleanup did not enter admitted provider")
				}
				release, err := acquireWorkspaceLeases(context.Background(), group)
				if err == nil {
					release()
					t.Error("cleanup-held ancestor did not exclude a new " + kind + " reference")
				} else if !strings.Contains(err.Error(), "in use") {
					t.Fatal("unexpected error", err)
				}
			})
		}
	}
}

func TestWorkspaceProtectedReferencesRejectBothRegisteredEntriesBeforeEffects(t *testing.T) {
	for _, entry := range []string{"registered-live", "registered-missing", "create-existing"} {
		for _, kind := range []string{"canonical-note", "primary-repository", "additional-mount", "effective-mount"} {
			t.Run(entry+"/"+kind, func(t *testing.T) {
				root, group := sandboxActivationFixture(t)
				target := filepath.Join(t.TempDir(), "cleanup")
				reference := filepath.Join(target, "protected")
				switch kind {
				case "canonical-note":
					group.NotePath = filepath.Join(reference, "note.md")
				case "primary-repository":
					group.Members[0].Repository = reference
				case "additional-mount":
					group.Sandbox.AdditionalMounts = []workspacegroup.SandboxMount{{Path: reference, ReadOnly: true}}
				case "effective-mount":
					group.Sandbox.Mounts = append(group.Sandbox.Mounts, reference+":ro")
				}
				if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
					t.Fatal(err)
				}
				release, err := workspacelease.Acquire(target, true)
				if err != nil {
					t.Fatal(err)
				}
				runner := &fakeRunner{hasSession: entry == "registered-live"}
				if entry == "create-existing" {
					_, err = Create(context.Background(), runner, CreateOptions{WorkspaceRoot: root, TaskLinkingKey: group.NoteKey(), Switch: true})
				} else {
					_, err = OpenRegisteredWorkspace(context.Background(), runner, group.Path, true)
				}
				release()
				if err == nil || !strings.Contains(err.Error(), "in use") {
					t.Fatal("registered entry ignored reference ancestor deletion", err)
				}
				for _, args := range []struct{ name, command string }{{"tmux", "has-session"}, {"tmux", "new-session"}, {"tmux", "switch-client"}, {"sbx", "create"}, {"sbx", "exec"}} {
					assertNotCalledContains(t, runner.calls, args.name, args.command)
				}
				assertSandboxActivationReleased(t, group)
			})
		}
	}
}
