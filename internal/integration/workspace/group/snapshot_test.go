package workspacegroup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestRegistrationSnapshotMatchesNativePersistenceWithoutIOOrAliasing(t *testing.T) {
	root := t.TempDir()
	anchor := filepath.Join(root, "fixture")
	input := Workspace{
		ID: ID(anchor), Name: " fixture ", Path: anchor, SessionName: " native-session ",
		TaskLinkingKey: "fixture:task:one", Members: nil,
		Sandbox: &Sandbox{Name: " fixture ", Agent: "shell", ReadyCommand: []string{}, Mounts: []string{anchor, anchor}, Ports: []SandboxPort{{HostPort: 8080, SandboxPort: 80}}},
	}
	before, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := RegistrationSnapshot(input)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("snapshot performed filesystem I/O", entries, err)
	}
	after, err := json.Marshal(input)
	if err != nil || string(before) != string(after) {
		t.Fatal("snapshot normalized borrowed input in place", err)
	}
	var independent Workspace
	if err := json.Unmarshal(before, &independent); err != nil {
		t.Fatal(err)
	}
	if err := Save(root, Registry{Version: Version, Workspaces: []Workspace{independent}}); err != nil {
		t.Fatal(err)
	}
	persisted, err := Load(root)
	if err != nil || len(persisted.Workspaces) != 1 || !reflect.DeepEqual(snapshot, persisted.Workspaces[0]) {
		t.Fatal("snapshot differs from native Save/Load canonicalization", snapshot, persisted, err)
	}
	if snapshot.Members == nil || snapshot.Sandbox.ReadyCommand != nil {
		t.Fatal("snapshot ignored native empty/omitempty representation")
	}
	snapshot.Sandbox.Ports[0].HostPort = 9090
	snapshot.Sandbox.Mounts[0] = "/changed"
	if input.Sandbox.Ports[0].HostPort != 8080 || input.Sandbox.Mounts[0] != anchor {
		t.Fatal("snapshot retained borrowed nested slices")
	}
	input.ID = "changed"
	if _, err := RegistrationSnapshot(input); err == nil {
		t.Fatal("snapshot bypassed native registration validation")
	}
}
