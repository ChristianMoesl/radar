package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"radar/internal/cleanup"
	"radar/internal/integration"
	"radar/internal/integration/workspace/group"
	"radar/internal/protocol"
	"radar/internal/workspacelease"
)

func TestRuntimeRetirementRevalidatesRegistrationAndPaths(t *testing.T) {
	for _, failure := range []string{"none", "changed session", "changed sandbox", "wrong path", "symlink anchor", "missing registration", "held lease"} {
		t.Run(failure, func(t *testing.T) {
			root := configureWorkspaceRoot(t)
			anchor := filepath.Join(root, "completed")
			if err := os.MkdirAll(anchor, 0755); err != nil {
				t.Fatal(err)
			}
			valuable := filepath.Join(anchor, "unknown.txt")
			if err := os.WriteFile(valuable, []byte("keep\n"), 0600); err != nil {
				t.Fatal(err)
			}
			group := workspacegroup.Workspace{ID: workspacegroup.ID(anchor), Name: "completed", Path: anchor, SessionName: "session", Sandbox: &workspacegroup.Sandbox{Name: "sandbox", Agent: "shell", Mounts: []string{anchor}}}
			if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
				t.Fatal(err)
			}
			task := protocol.Task{ID: 1, SourceRefs: []protocol.SourceRef{{ID: "workspace:" + group.ID, Source: "workspace", Kind: "workspace", ProvidesWorkspace: true, WorkspaceID: group.ID, Path: anchor}}}
			service := cleanup.New([]integration.CleanupProvider{Source{}})
			preview, err := service.Preview(context.Background(), task, integration.CleanupRetire)
			if err != nil {
				t.Fatal(err)
			}
			if len(preview.Targets) != 1 || len(preview.Targets[0].Safety) != 0 {
				t.Fatalf("local data blocks runtime retirement: %+v", preview)
			}
			switch failure {
			case "changed session", "changed sandbox":
				if err := workspacegroup.Update(root, func(registry *workspacegroup.Registry) error {
					if failure == "changed session" {
						registry.Workspaces[0].SessionName = "replacement"
					} else {
						registry.Workspaces[0].Sandbox.Name = "replacement"
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			case "wrong path":
				preview.Targets[0].Path = t.TempDir()
			case "symlink anchor":
				kept := anchor + "-kept"
				if err := os.Rename(anchor, kept); err != nil {
					t.Fatal(err)
				}
				valuable = filepath.Join(kept, "unknown.txt")
				if err := os.Symlink(t.TempDir(), anchor); err != nil {
					t.Fatal(err)
				}
			case "missing registration":
				if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version}); err != nil {
					t.Fatal(err)
				}
			case "held lease":
				release, err := workspacelease.Acquire(anchor, false)
				if err != nil {
					t.Fatal(err)
				}
				defer release()
			}
			_, err = service.Execute(context.Background(), preview, cleanup.ExecuteOptions{Mode: integration.CleanupRetire})
			if (err != nil) != (failure != "none") {
				t.Fatalf("failure=%s err=%v", failure, err)
			}
			if data, err := os.ReadFile(valuable); err != nil || string(data) != "keep\n" {
				t.Fatalf("runtime stage changed local data: %q %v", data, err)
			}
		})
	}
}
