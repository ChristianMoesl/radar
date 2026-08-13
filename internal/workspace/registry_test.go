package workspace

import (
	"context"
	"path/filepath"
	"testing"

	"radar/internal/workspacegroup"
)

func TestRegisterPrimaryWorkspaceBindsExistingGroupWithoutReplacingMembers(t *testing.T) {
	root := t.TempDir()
	primary := filepath.Join(root, "repo", "work")
	secondary := filepath.Join(root, "other", "work")
	group := workspacegroup.Workspace{
		ID: workspacegroup.ID(primary), Name: "work", PrimaryPath: primary,
		Members: []workspacegroup.Member{
			{Repository: filepath.Join(root, "sources", "repo"), Path: primary, Branch: "work", Primary: true},
			{Repository: filepath.Join(root, "sources", "other"), Path: secondary, Branch: "work"},
		},
	}
	if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
		t.Fatal(err)
	}

	plan := WorktreePlan{Root: root, Repo: group.Members[0].Repository, Path: primary, Name: "work", Branch: "work"}
	if err := registerPrimaryWorkspace(context.Background(), unusedRegistryRunner{}, plan, "repo-work", "", sandboxSettings{}, true, "obsidian:task:one"); err != nil {
		t.Fatal(err)
	}

	loaded, err := workspacegroup.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Workspaces) != 1 || len(loaded.Workspaces[0].Members) != 2 || loaded.Workspaces[0].TaskLinkingKey != "obsidian:task:one" {
		t.Fatalf("registry = %+v", loaded)
	}
}

type unusedRegistryRunner struct{}

func (unusedRegistryRunner) LookPath(string) error { return nil }

func (unusedRegistryRunner) Run(context.Context, string, string, ...string) (string, error) {
	return "", nil
}
