package workspacegroup

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestRegistrySandboxReadyCommandStructuralRoundTrip(t *testing.T) {
	root := t.TempDir()
	workspace := testWorkspace(root, "ABC-123")
	argv := []string{"/nonexistent/on/host", " argument with spaces ", "", "$(literal)"}
	workspace.Sandbox = &Sandbox{Name: "ABC-123", Agent: "shell", ReadyCommand: argv}
	if err := Save(root, Registry{Version: Version, Workspaces: []Workspace{workspace}}); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(root)
	if err != nil || !reflect.DeepEqual(loaded.Workspaces[0].Sandbox.ReadyCommand, argv) {
		t.Fatalf("registry = %+v, %v", loaded, err)
	}
	if err := RemoveWorkspace(root, workspace.ID); err != nil {
		t.Fatal(err)
	}
}

func TestRegistryVersionTwoWithoutReadyCommandIsUnchanged(t *testing.T) {
	root := t.TempDir()
	workspace := testWorkspace(root, "ABC-123")
	workspace.Sandbox = &Sandbox{Name: "ABC-123", Agent: "shell"}
	if err := Save(root, Registry{Version: 2, Workspaces: []Workspace{workspace}}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(Path(root))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(before), `"ready_command"`) {
		t.Fatalf("unexpected field: %s", before)
	}
	loaded, err := Load(root)
	if err != nil || loaded.Workspaces[0].Sandbox.ReadyCommand != nil {
		t.Fatalf("registry = %+v, %v", loaded, err)
	}
	if err := Save(root, loaded); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(Path(root))
	if err != nil || string(before) != string(after) {
		t.Fatalf("old registry rewritten: %s, %v", after, err)
	}
}

func TestRegistryRejectsMalformedReadyCommandOnSaveAndLoad(t *testing.T) {
	for _, argv := range [][]string{{""}, {"  "}, {"sentinel-secret\x00"}, {"ready", "sentinel-secret\x00"}} {
		root := t.TempDir()
		workspace := testWorkspace(root, "ABC-123")
		workspace.Sandbox = &Sandbox{Name: "ABC-123", Agent: "shell", ReadyCommand: argv}
		registry := Registry{Version: Version, Workspaces: []Workspace{workspace}}
		if err := Save(root, registry); err == nil || !strings.Contains(err.Error(), "ready_command") || strings.Contains(err.Error(), "sentinel-secret") {
			t.Fatalf("Save = %v", err)
		}
		data, err := json.Marshal(registry)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(Path(root), data, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(root); err == nil || !strings.Contains(err.Error(), "ready_command") || strings.Contains(err.Error(), "sentinel-secret") {
			t.Fatalf("Load = %v", err)
		}
	}
}
