package diagnostics

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"radar/internal/agentservice"
	"radar/internal/protocol"
)

func TestAgentRegistrationInventoryFailureDoesNotPublishPartialPage(t *testing.T) {
	store, _, path := fixture(t)
	file := ""
	for _, id := range []string{"owner-a", "owner-z"} {
		dir := filepath.Join(os.Getenv("RADAR_AGENT_ROOT"), id)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		reg := agentservice.Registration{Version: 1, ID: id, Entrypoint: "private-command", Workspace: protocol.AgentWorkspaceBinding{ID: "registered", Path: path, Mode: "local"}}
		data, _ := json.Marshal(reg)
		file = filepath.Join(dir, "registration.json")
		if err := os.WriteFile(file, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	revision := store.Revision()
	req := protocol.DiagnosticsRequest{Section: "agents", Workspace: path, Limit: 1}
	first, err := read(store, req, true)
	if err != nil || first.Total != 2 || first.NextCursor == "" {
		t.Fatal("valid registration pagination lost", err)
	}
	req.Cursor = first.NextCursor
	data, _ := os.ReadFile(file)
	oversized := append(data, bytes.Repeat([]byte(" "), 64*1024)...)
	if err := os.WriteFile(file, oversized, 0600); err != nil {
		t.Fatal(err)
	}
	for _, request := range []protocol.DiagnosticsRequest{req, {Section: "agents", Workspace: path}, {Section: "agent_schedules", Workspace: path}} {
		page, err := read(store, request, true)
		if err == nil || len(page.Items) != 0 || strings.Contains(err.Error(), "private-") {
			t.Error("unsafe registration produced usable, partial or private diagnostic inventory")
		}
	}
	if store.Revision() != revision {
		t.Fatal("registration refusal mutated store")
	}
	after, _ := os.ReadFile(file)
	if !bytes.Equal(oversized, after) {
		t.Fatal("registration refusal rewrote source")
	}
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	recovered, err := read(store, protocol.DiagnosticsRequest{Section: "agents", Workspace: path, Limit: 1}, false)
	if err != nil || recovered.Total != 2 {
		t.Fatal("valid replacement registration remained unavailable", err)
	}
}
