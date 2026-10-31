package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"radar/internal/integration"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
)

func cleanupFixtureTargets(group workspacegroup.Workspace) []protocol.CleanupTarget {
	return []protocol.CleanupTarget{{Source: "sbx", Kind: "sandbox", Path: group.Path, WorkspaceID: group.ID}}
}
func TestBundlePreflightRejectsProtectedContentAnywhere(t *testing.T) {
	for _, location := range []string{"member", "unrelated member", "unknown entry"} {
		t.Run(location, func(t *testing.T) {
			_, group, _ := inspectionFixture(t)
			path := group.Members[0].Path
			switch location {
			case "unrelated member":
				path = group.Members[3].Path
			case "unknown entry":
				path = filepath.Join(group.Path, "unknown")
			}
			path = filepath.Join(path, "nested", ".git")
			if err := os.MkdirAll(path, 0700); err != nil {
				t.Fatal(err)
			}
			if err := (Source{}).PreflightCleanup(context.Background(), cleanupFixtureTargets(group), integration.CleanupConfirmed); err == nil || !strings.Contains(err.Error(), "unidentified Git checkout") {
				t.Fatalf("bundle protection skipped %s: %v", location, err)
			}
		})
	}
}
func TestMemberRemovalRevalidatesAfterBundlePreflight(t *testing.T) {
	for _, change := range []string{"nested checkout", "symlink", "canonical note", "branch ownership", "registered repository", "configured root"} {
		t.Run(change, func(t *testing.T) {
			root, group, _ := inspectionFixture(t)
			member := group.Members[0]
			if err := (Source{}).PreflightCleanup(context.Background(), cleanupFixtureTargets(group), integration.CleanupConfirmed); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "nested checkout":
				if err := os.MkdirAll(filepath.Join(member.Path, "nested", ".git"), 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Remove(member.Path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), member.Path); err != nil {
					t.Fatal(err)
				}
			case "canonical note":
				group.NotePath = filepath.Join(member.Path, "notes.md")
			case "branch ownership":
				group.Members[0].Branch = "changed"
			case "registered repository":
				group.Members[0].Repository = t.TempDir()
			case "configured root":
				// A different explicit root is rejected even if it loads the same registry.
				other := t.TempDir()
				if err := workspacegroup.Save(other, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
					t.Fatal(err)
				}
				root = other
			}
			if change != "configured root" {
				if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
					t.Fatal(err)
				}
			}
			if err := ValidateDiscardingWorkspaceMember(root, group.ID, member, integration.CleanupConfirmed); err == nil {
				t.Fatalf("member protection trusted preflight after %s", change)
			}
		})
	}
}
func TestMemberValidationTraversesOnlyItsOwnTree(t *testing.T) {
	root, group, _ := inspectionFixture(t)
	if err := (Source{}).PreflightCleanup(context.Background(), cleanupFixtureTargets(group), integration.CleanupConfirmed); err != nil {
		t.Fatal(err)
	}
	// A later nested checkout in another member cannot be removed with this
	// member. The other member's last-moment check must detect it independently.
	path := filepath.Join(group.Members[3].Path, "nested", ".git")
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := ValidateDiscardingWorkspaceMember(root, group.ID, group.Members[0], integration.CleanupConfirmed); err != nil {
		t.Fatalf("traversed unrelated member: %v", err)
	}
	if err := ValidateDiscardingWorkspaceMember(root, group.ID, group.Members[3], integration.CleanupConfirmed); err == nil {
		t.Fatal("missed protected checkout in removal target")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("protected contents changed")
	}
}
