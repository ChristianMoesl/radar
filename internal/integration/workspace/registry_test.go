package workspace

import (
	"path/filepath"
	"strings"
	"testing"

	"radar/internal/integration/workspace/group"
)

func TestRegisterWorkspaceUpdatesExistingAnchorWithoutReplacingMembers(t *testing.T) {
	root := t.TempDir()
	anchor := filepath.Join(root, "work")
	group := workspacegroup.Workspace{
		ID: workspacegroup.ID(anchor), Name: "work", Path: anchor,
		Members: []workspacegroup.Member{
			{Repository: filepath.Join(root, "sources", "repo"), Path: filepath.Join(anchor, "repo--work"), Branch: "work"},
			{Repository: filepath.Join(root, "sources", "other"), Path: filepath.Join(anchor, "other--work"), Branch: "work"},
		},
	}
	if err := registerWorkspace(root, group); err != nil {
		t.Fatal(err)
	}
	group.TaskLinkingKey = "obsidian:task:one"
	if err := registerWorkspace(root, group); err != nil {
		t.Fatal(err)
	}
	loaded, err := workspacegroup.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Workspaces) != 1 || len(loaded.Workspaces[0].Members) != 2 || loaded.Workspaces[0].TaskLinkingKey != group.TaskLinkingKey {
		t.Fatalf("registry = %+v", loaded)
	}
}

func TestRegisterWorkspaceRejectsDifferentTaskLink(t *testing.T) {
	root := t.TempDir()
	anchor := filepath.Join(root, "work")
	group := workspacegroup.Workspace{ID: workspacegroup.ID(anchor), Name: "work", Path: anchor, TaskLinkingKey: "obsidian:task:one", Members: []workspacegroup.Member{}}
	if err := registerWorkspace(root, group); err != nil {
		t.Fatal(err)
	}
	group.TaskLinkingKey = "obsidian:task:two"
	err := registerWorkspace(root, group)
	if err == nil || !strings.Contains(err.Error(), "already linked to another task") {
		t.Fatalf("registerWorkspace() error = %v", err)
	}
}
