package agentservice

import (
	"os"
	"path/filepath"
	"radar/internal/protocol"
	"testing"
)

func TestRegistrationPinsStoppedWorkspaceAndRetainsData(t *testing.T) {
	root := t.TempDir()
	t.Setenv("RADAR_AGENT_ROOT", root)
	dir := filepath.Join(root, "fixture")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "workspace")
	reg := Registration{Version: 1, ID: "fixture", Workspace: protocol.AgentWorkspaceBinding{ID: "workspace", Path: path, Mode: "local"}}
	if err := atomicJSON(filepath.Join(dir, "registration.json"), reg); err != nil {
		t.Fatal(err)
	}
	if err := CheckWorkspaceRemoval(path); err == nil {
		t.Fatal("stopped agent's workspace can be removed")
	}
	if err := CheckWorkspaceRemoval(filepath.Dir(path)); err == nil {
		t.Fatal("ancestor removal allowed")
	}
	if err := CheckWorkspaceRemoval(path + "-other"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "registration.json")); err != nil {
		t.Fatal(err)
	}
	if err := CheckWorkspaceRemoval(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatal("durable data directory was removed")
	}
}
