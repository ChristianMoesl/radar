package workspacegroup

import (
	"path/filepath"
	"testing"
)

func TestSharedDirectoryValidationAndRegistryRoundTrip(t *testing.T) {
	root := t.TempDir()
	anchor := filepath.Join(root, "work")
	shared := filepath.Join(root, SharedDirectoryParent, ID(anchor))
	for _, path := range []string{"relative", root, filepath.Join(root, SharedDirectoryParent, ID("/other")), shared + "/.."} {
		if err := ValidateSharedDirectory(anchor, path); err == nil {
			t.Fatalf("accepted %q", path)
		}
	}
	group := Workspace{ID: ID(anchor), Name: "work", Path: anchor, Sandbox: &Sandbox{Name: "work", Agent: "shell", SharedDirectory: shared, Mounts: []string{shared}}}
	if err := Save(root, Registry{Version: Version, Workspaces: []Workspace{group}}); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Workspaces[0].Sandbox.SharedDirectory != shared {
		t.Fatal("shared directory was lost")
	}
	group.Sandbox.SharedDirectory = root
	if err := Save(root, Registry{Version: Version, Workspaces: []Workspace{group}}); err == nil {
		t.Fatal("stored unsafe shared directory")
	}
}
