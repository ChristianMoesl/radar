package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
)

func TestDiagnosticWorkspaceRegistryInspectionDoesNotCreateState(t *testing.T) {
	for _, method := range []string{"inventory", "workspace_policy", "repository_policy"} {
		for _, kind := range []string{"missing_root", "missing_registry", "valid_without_lock"} {
			t.Run(method+"/"+kind, func(t *testing.T) {
				root := configureWorkspaceRoot(t)
				anchor := filepath.Join(root, "registered")
				group := workspacegroup.Workspace{ID: workspacegroup.ID(anchor), Name: "registered", Path: anchor, Members: []workspacegroup.Member{{Path: filepath.Join(anchor, "member"), Repository: t.TempDir(), Branch: "fixture"}}}
				if kind == "missing_registry" {
					if err := os.MkdirAll(root, 0700); err != nil {
						t.Fatal(err)
					}
				}
				if kind == "valid_without_lock" {
					if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
						t.Fatal(err)
					}
					// Only this fixture owns the temporary native coordination file.
					if err := os.Remove(filepath.Join(root, ".radar-workspaces.lock")); err != nil {
						t.Fatal(err)
					}
				}
				source := Source{}
				switch method {
				case "inventory":
					items, err := source.DiagnosticWorkspaceRegistrations()
					want := 0
					if kind == "valid_without_lock" {
						want = 1
					}
					if err != nil || len(items) != want {
						t.Fatal("inventory semantics changed", err)
					}
				case "workspace_policy":
					_, err := source.DiagnosticWorkspacePolicy(group.ID, anchor)
					if (err == nil) != (kind == "valid_without_lock") {
						t.Fatal("exact recipe scope changed", err)
					}
				case "repository_policy":
					member := protocol.DiagnosticMember{Path: group.Members[0].Path, Repository: group.Members[0].Repository}
					id := protocol.DiagnosticWorkspaceResourceID(group.ID, protocol.DiagnosticWorkspaceResource{Kind: "member", Member: &member})
					view, err := source.DiagnosticRepositoryPolicy(context.Background(), group.ID, id, anchor)
					if kind == "valid_without_lock" {
						if err != nil || !view.Available {
							t.Fatal("registered repository defaults changed", err)
						}
					} else if err == nil {
						t.Fatal("missing registration became a repository read")
					}
				}
				if kind == "missing_root" {
					if _, err := os.Lstat(root); !os.IsNotExist(err) {
						t.Fatal("diagnostic inspection created missing workspace root")
					}
				} else {
					entries, err := os.ReadDir(root)
					want := 0
					if kind == "valid_without_lock" {
						want = 1
					}
					if err != nil || len(entries) != want {
						t.Fatal("diagnostic inspection created native coordination state")
					}
				}
			})
		}
	}
}
