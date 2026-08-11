package workspacegroup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegistryRoundTripAndLookup(t *testing.T) {
	root := t.TempDir()
	primary := filepath.Join(root, "repo", "work")
	secondary := filepath.Join(root, "other", "work")
	registry := Registry{Version: Version, Workspaces: []Workspace{{
		ID: ID(primary), Name: "work", PrimaryPath: primary,
		Members: []Member{
			{Repository: filepath.Join(root, "sources", "repo"), Path: primary, Branch: "work", Primary: true, SetupScheduled: true},
			{Repository: filepath.Join(root, "sources", "other"), Path: secondary, Branch: "work"},
		},
	}}}
	if err := Save(root, registry); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	workspace, ok := FindByMemberPath(loaded, secondary)
	if !ok || workspace.ID != ID(primary) {
		t.Fatalf("lookup = %+v, %v", workspace, ok)
	}
	info, err := os.Stat(Path(root))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
}

func TestRemoveMemberPreservesGroupUntilPrimaryAndMembersAreGone(t *testing.T) {
	root := t.TempDir()
	primary := filepath.Join(root, "repo", "work")
	secondary := filepath.Join(root, "other", "work")
	for _, path := range []string{primary, secondary} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	workspace := Workspace{ID: ID(primary), Name: "work", PrimaryPath: primary, Members: []Member{
		{Repository: filepath.Join(root, "sources", "repo"), Path: primary, Branch: "work", Primary: true},
		{Repository: filepath.Join(root, "sources", "other"), Path: secondary, Branch: "other"},
	}}
	if err := Save(root, Registry{Version: Version, Workspaces: []Workspace{workspace}}); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(primary); err != nil {
		t.Fatal(err)
	}
	if err := RemoveMember(root, primary); err != nil {
		t.Fatal(err)
	}
	registry, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(registry.Workspaces) != 1 || len(registry.Workspaces[0].Members) != 2 {
		t.Fatalf("registry after primary cleanup = %+v", registry)
	}
	if err := os.RemoveAll(secondary); err != nil {
		t.Fatal(err)
	}
	if err := RemoveMember(root, secondary); err != nil {
		t.Fatal(err)
	}
	registry, err = Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(registry.Workspaces) != 0 {
		t.Fatalf("registry after full cleanup = %+v", registry)
	}
}

func TestRegistryRejectsUnsupportedVersion(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(Path(root), []byte(`{"version":2,"workspaces":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(root)
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("error = %v", err)
	}
}

func TestRegistryNormalizesSandboxPorts(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "repo", "work")
	repository := filepath.Join(root, "source")
	registry := Registry{Version: Version, Workspaces: []Workspace{{
		ID: "one", Name: "one", PrimaryPath: path,
		Sandbox: &Sandbox{Name: "one", Agent: "shell", AdditionalMounts: []SandboxMount{{Path: filepath.Join(root, "z"), ReadOnly: true}, {Path: filepath.Join(root, "a")}}, Ports: []SandboxPort{{HostPort: 8080, SandboxPort: 80}, {HostPort: 3000, SandboxPort: 3000}}},
		Members: []Member{{Repository: repository, Path: path, Branch: "one", Primary: true}},
	}}}
	if err := Save(root, registry); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	ports := loaded.Workspaces[0].Sandbox.Ports
	if len(ports) != 2 || ports[0].HostPort != 3000 || ports[1].HostPort != 8080 {
		t.Fatalf("ports = %+v", ports)
	}
	mounts := loaded.Workspaces[0].Sandbox.AdditionalMounts
	if len(mounts) != 2 || mounts[0].Path != filepath.Join(root, "a") || mounts[1].Path != filepath.Join(root, "z") || !mounts[1].ReadOnly {
		t.Fatalf("additional mounts = %+v", mounts)
	}
}

func TestRegistryRejectsDuplicateSandboxAdditionalMounts(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "repo", "work")
	mount := filepath.Join(root, "shared")
	err := Save(root, Registry{Version: Version, Workspaces: []Workspace{{
		ID: "one", Name: "one", PrimaryPath: path,
		Sandbox: &Sandbox{Name: "one", Agent: "shell", AdditionalMounts: []SandboxMount{{Path: mount, ReadOnly: true}, {Path: mount}}},
		Members: []Member{{Repository: filepath.Join(root, "source"), Path: path, Branch: "one", Primary: true}},
	}}})
	if err == nil || !strings.Contains(err.Error(), "duplicate sandbox additional mount") {
		t.Fatalf("error = %v", err)
	}
}

func TestRegistryRejectsDuplicateSandboxHostPorts(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "repo", "work")
	err := Save(root, Registry{Version: Version, Workspaces: []Workspace{{
		ID: "one", Name: "one", PrimaryPath: path,
		Sandbox: &Sandbox{Name: "one", Agent: "shell", Ports: []SandboxPort{{HostPort: 3000, SandboxPort: 3000}, {HostPort: 3000, SandboxPort: 4000}}},
		Members: []Member{{Repository: filepath.Join(root, "source"), Path: path, Branch: "one", Primary: true}},
	}}})
	if err == nil || !strings.Contains(err.Error(), "duplicate sandbox host port") {
		t.Fatalf("error = %v", err)
	}
}

func TestRegistryRejectsDuplicateMembers(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "repo", "work")
	repository := filepath.Join(root, "source")
	err := Save(root, Registry{Version: Version, Workspaces: []Workspace{
		{ID: "one", Name: "one", PrimaryPath: path, Members: []Member{{Repository: repository, Path: path, Branch: "one", Primary: true}}},
		{ID: "two", Name: "two", PrimaryPath: path, Members: []Member{{Repository: repository, Path: path, Branch: "two", Primary: true}}},
	}})
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("error = %v", err)
	}
}
