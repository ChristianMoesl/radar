package workspacegroup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testWorkspace(root, name string) Workspace {
	anchor := filepath.Join(root, name)
	return Workspace{
		ID: ID(anchor), Name: name, Path: anchor, Members: []Member{},
	}
}

func TestRegistryRoundTripAndContainingPathLookup(t *testing.T) {
	root := t.TempDir()
	workspace := testWorkspace(root, "work")
	workspace.TaskLinkingKey = "obsidian:task:one"
	workspace.NotePath = filepath.Join(root, "vault", "Tasks", "Work--12345678", "Work.md")
	workspace.Members = []Member{
		{Repository: filepath.Join(root, "sources", "repo"), Path: filepath.Join(workspace.Path, "repo--work"), Branch: "work", SetupScheduled: true},
		{Repository: filepath.Join(root, "sources", "other"), Path: filepath.Join(workspace.Path, "other--work"), Branch: "work"},
	}
	if err := Save(root, Registry{Version: Version, Workspaces: []Workspace{workspace}}); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{workspace.Path, filepath.Join(workspace.Path, "note.md"), filepath.Join(workspace.Members[1].Path, "src", "main.go")} {
		found, ok := FindByContainingPath(loaded, path)
		if !ok || found.ID != workspace.ID {
			t.Fatalf("FindByContainingPath(%q) = %+v, %v", path, found, ok)
		}
	}
	info, err := os.Stat(Path(root))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
}

func TestRegistryAcceptsZeroMembersAndRemoveMemberKeepsWorkspace(t *testing.T) {
	root := t.TempDir()
	workspace := testWorkspace(root, "work")
	member := Member{Repository: filepath.Join(root, "source"), Path: filepath.Join(workspace.Path, "repo--work"), Branch: "work"}
	workspace.Members = []Member{member}
	if err := Save(root, Registry{Version: Version, Workspaces: []Workspace{workspace}}); err != nil {
		t.Fatal(err)
	}
	if err := RemoveMember(root, member.Path); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Workspaces) != 1 || len(loaded.Workspaces[0].Members) != 0 {
		t.Fatalf("registry = %+v", loaded)
	}
}

func TestRegistryRejectsMemberOutsideAnchor(t *testing.T) {
	root := t.TempDir()
	workspace := testWorkspace(root, "work")
	workspace.Members = []Member{{Repository: filepath.Join(root, "source"), Path: filepath.Join(root, "outside"), Branch: "work"}}
	err := Save(root, Registry{Version: Version, Workspaces: []Workspace{workspace}})
	if err == nil || !strings.Contains(err.Error(), "direct child of its anchor") {
		t.Fatalf("error = %v", err)
	}
}

func TestRegistryRejectsTaskLinkingKeyWithoutPrefix(t *testing.T) {
	root := t.TempDir()
	workspace := testWorkspace(root, "work")
	workspace.TaskLinkingKey = "invalid"
	err := Save(root, Registry{Version: Version, Workspaces: []Workspace{workspace}})
	if err == nil || !strings.Contains(err.Error(), "task_linking_key") {
		t.Fatalf("error = %v", err)
	}
}

func TestRegistryRejectsOldVersion(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(Path(root), []byte(`{"version":1,"workspaces":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(root)
	if err == nil || !strings.Contains(err.Error(), "unsupported Radar workspace registry version 1") {
		t.Fatalf("error = %v", err)
	}
}

func TestRegistryNormalizesSandboxPortsAndMounts(t *testing.T) {
	root := t.TempDir()
	workspace := testWorkspace(root, "work")
	workspace.Sandbox = &Sandbox{
		Name: "work", Agent: "shell",
		AdditionalMounts: []SandboxMount{{Path: filepath.Join(root, "z"), ReadOnly: true}, {Path: filepath.Join(root, "a")}},
		Ports:            []SandboxPort{{HostPort: 8080, SandboxPort: 80}, {HostPort: 3000, SandboxPort: 3000}},
	}
	if err := Save(root, Registry{Version: Version, Workspaces: []Workspace{workspace}}); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	ports := loaded.Workspaces[0].Sandbox.Ports
	mounts := loaded.Workspaces[0].Sandbox.AdditionalMounts
	if len(ports) != 2 || ports[0].HostPort != 3000 || ports[1].HostPort != 8080 {
		t.Fatalf("ports = %+v", ports)
	}
	if len(mounts) != 2 || mounts[0].Path != filepath.Join(root, "a") || mounts[1].Path != filepath.Join(root, "z") {
		t.Fatalf("mounts = %+v", mounts)
	}
}

func TestRegistryRejectsDuplicateRepositoryBranchAcrossWorkspaces(t *testing.T) {
	root := t.TempDir()
	first := testWorkspace(root, "one")
	second := testWorkspace(root, "two")
	repository := filepath.Join(root, "source")
	first.Members = []Member{{Repository: repository, Path: filepath.Join(first.Path, "repo--shared"), Branch: "shared"}}
	second.Members = []Member{{Repository: repository, Path: filepath.Join(second.Path, "repo--shared"), Branch: "shared"}}
	err := Save(root, Registry{Version: Version, Workspaces: []Workspace{first, second}})
	if err == nil || !strings.Contains(err.Error(), "duplicate workspace member repository") {
		t.Fatalf("error = %v", err)
	}
}

func TestRegistryRejectsDuplicateTaskLinkingKey(t *testing.T) {
	root := t.TempDir()
	first := testWorkspace(root, "one")
	second := testWorkspace(root, "two")
	first.TaskLinkingKey = "obsidian:task:one"
	second.TaskLinkingKey = first.TaskLinkingKey
	err := Save(root, Registry{Version: Version, Workspaces: []Workspace{first, second}})
	if err == nil || !strings.Contains(err.Error(), "duplicate workspace task_linking_key") {
		t.Fatalf("error = %v", err)
	}
}
