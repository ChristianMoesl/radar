package diagnostics

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"radar/internal/agentservice"
	"radar/internal/protocol"
)

func TestAgentCollectionBoundedEvidenceAndCursorDrift(t *testing.T) {
	store, _, workspace := fixture(t)
	checkpoint := `{"version":1,"source":"github","seen":{},"pending":[{"source":"private-source","id":"private-event","payload":{"body":"private-body"}}],"problem":"private-error"}`
	paths := map[string]string{}
	for _, id := range []string{"agent-a", "agent-b"} {
		dir := filepath.Join(os.Getenv("RADAR_AGENT_ROOT"), id)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		reg := agentservice.Registration{Version: 1, ID: id, Collection: "github", Workspace: protocol.AgentWorkspaceBinding{ID: id, Path: workspace, Mode: "local"}}
		data, _ := json.Marshal(reg)
		if err := os.WriteFile(filepath.Join(dir, "registration.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
		paths[id] = filepath.Join(dir, "collection.json")
	}
	oversized := checkpoint + strings.Repeat(" ", protocol.AgentMaxFrameBytes)
	if err := os.WriteFile(paths["agent-a"], []byte(oversized), 0600); err != nil {
		t.Fatal(err)
	}
	req := protocol.DiagnosticsRequest{Section: "agents", Workspace: workspace, Limit: 1}
	revision := store.Revision()
	page, err := read(store, req, true)
	if err != nil || page.Total != 2 || len(page.Items) != 1 || page.Complete || page.NextCursor == "" || !page.ResourceRefreshPending {
		t.Fatal("bounded checkpoint failure lost inventory or pagination")
	}
	view := page.Items[0].Agent
	if view == nil || view.ID != "agent-a" || view.CollectionEvidence != "unavailable" || view.Collection != nil || view.OwnershipHold != "registered_owner_even_when_stopped" {
		t.Fatal("oversized collection exposed usable evidence or released stopped owner")
	}
	encoded, _ := json.Marshal(page)
	if strings.Contains(string(encoded), "private-") || strings.Contains(string(encoded), `"payload":`) {
		t.Fatal("private checkpoint data exposed")
	}
	req.Cursor = page.NextCursor
	next, err := read(store, req, true)
	if err != nil || !next.Complete || len(next.Items) != 1 || next.Items[0].ID != "agent-b" || next.Items[0].Agent.CollectionEvidence != "fresh_checkpoint" || next.Items[0].Agent.Collection == nil || next.Items[0].Agent.Collection.Pending != 0 {
		t.Fatal("missing-checkpoint native defaults or complete inventory changed")
	}
	if _, err := os.Stat(paths["agent-b"]); !os.IsNotExist(err) {
		t.Fatal("missing collection checkpoint created")
	}
	if err := os.WriteFile(paths["agent-a"], []byte(checkpoint), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := read(store, req, true); err == nil {
		t.Fatal("changed collection evidence reused old cursor")
	}
	req.Cursor = ""
	page, err = read(store, req, true)
	if err != nil || len(page.Items) != 1 || page.Items[0].Agent.Collection == nil || page.Items[0].Agent.Collection.Pending != 1 || !page.Items[0].Agent.Collection.DeliveryProblem || page.Items[0].Agent.CollectionEvidence != "fresh_checkpoint" || store.Revision() != revision {
		t.Fatal("valid cached counters lost or diagnostic reads changed state")
	}
	data, err := os.ReadFile(paths["agent-a"])
	if err != nil || string(data) != checkpoint {
		t.Fatal("inspection changed private outbox")
	}
}
