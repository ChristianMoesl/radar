package diagnostics

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"radar/internal/agentservice"
	"radar/internal/protocol"
)

func TestAgentInventoryScopesPaginationAndCheckpointDrift(t *testing.T) {
	store, _, path := fixture(t)
	for i := 0; i < 23; i++ {
		id := "agent-" + strconv.Itoa(i)
		dir := filepath.Join(os.Getenv("RADAR_AGENT_ROOT"), id)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		workspace := path
		if i == 22 {
			workspace += "-other"
		}
		reg := agentservice.Registration{Version: 1, ID: id, Desired: i%2 == 0, Workspace: protocol.AgentWorkspaceBinding{ID: id, Path: workspace, Mode: "local"}}
		data, _ := json.Marshal(reg)
		if err := os.WriteFile(filepath.Join(dir, "registration.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	req := protocol.DiagnosticsRequest{Section: "agents", Workspace: path, Limit: 5}
	seen := map[string]bool{}
	for {
		page, err := read(store, req, false)
		if err != nil || page.Total != 22 {
			t.Fatalf("page=%+v err=%v", page, err)
		}
		for _, item := range page.Items {
			if item.Agent == nil || item.Agent.Workspace.Path != path || seen[item.ID] {
				t.Fatalf("invalid scoped page: %+v", item)
			}
			seen[item.ID] = true
		}
		if page.Complete {
			break
		}
		req.Cursor = page.NextCursor
	}
	if len(seen) != 22 {
		t.Fatal("incomplete agent inventory")
	}
	page, err := read(store, protocol.DiagnosticsRequest{Section: "agents", ID: "agent-22"}, false)
	if err != nil || page.Total != 1 {
		t.Fatal("installation-wide exact selector failed")
	}
	page, err = read(store, protocol.DiagnosticsRequest{Section: "agents", ID: "agent-22", Workspace: path}, false)
	if err != nil || page.Total != 0 {
		t.Fatal("exact selector escaped scope")
	}
	req.Cursor = ""
	page, err = read(store, req, false)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(protocol.AgentSnapshot{Protocol: protocol.AgentProtocolVersion, InstanceID: "agent-0", Pending: 9})
	if err := os.WriteFile(filepath.Join(os.Getenv("RADAR_AGENT_ROOT"), "agent-0", "snapshot.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	req.Cursor = page.NextCursor
	if _, err := read(store, req, false); err == nil {
		t.Fatal("cursor accepted changed agent checkpoint")
	}
	for _, req := range []protocol.DiagnosticsRequest{{Section: "agents", State: "done"}, {Section: "agents", ParentID: "123"}} {
		if _, err := read(store, req, false); err == nil {
			t.Fatal("irrelevant selector accepted")
		}
	}
	if err := os.WriteFile(filepath.Join(os.Getenv("RADAR_AGENT_ROOT"), "agent-0", "registration.json"), []byte("private-registration-body"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := read(store, protocol.DiagnosticsRequest{Section: "agents"}, false); err == nil || strings.Contains(err.Error(), "private-") {
		t.Fatal("unavailable registration inventory was not sanitized")
	}
}
