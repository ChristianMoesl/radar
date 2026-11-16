package diagnostics

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"radar/internal/agentservice"
	"radar/internal/protocol"
)

func scheduleFixture(t *testing.T, id, workspace string, count int) (string, []map[string]any) {
	t.Helper()
	dir := filepath.Join(os.Getenv("RADAR_AGENT_ROOT"), id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	reg, _ := json.Marshal(agentservice.Registration{Version: 1, ID: id, Desired: false, Entrypoint: "private-command", Workspace: protocol.AgentWorkspaceBinding{ID: id, Path: workspace, Mode: "local"}})
	if err := os.WriteFile(filepath.Join(dir, "registration.json"), reg, 0600); err != nil {
		t.Fatal(err)
	}
	schedules := []map[string]any{}
	for i := 0; i < count; i++ {
		schedules = append(schedules, map[string]any{"id": fmt.Sprintf("native-%02d", i), "name": fmt.Sprintf("schedule-%02d", i), "enabled": true, "time": "12:00", "timezone": "UTC", "weekdays": []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}, "eligible_after": "2099-01-01T00:00:00Z", "payload": map[string]string{"body": "private-schedule-payload"}, "problem": "private-delivery-error"})
	}
	path := filepath.Join(dir, "schedules.json")
	writeScheduleFixture(t, path, schedules)
	return path, schedules
}

func writeScheduleFixture(t *testing.T, path string, schedules []map[string]any) {
	t.Helper()
	data, _ := json.Marshal(map[string]any{"version": 1, "schedules": schedules})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestAgentSchedulesCompleteScopedPaginationPrivacyAndCheckpointDrift(t *testing.T) {
	store, _, workspace := fixture(t)
	path, schedules := scheduleFixture(t, "stopped", filepath.Join(workspace, "backing"), 25)
	other, _ := scheduleFixture(t, "other", workspace+"-other", 1)
	before, _ := os.ReadFile(path)
	beforeRevision := store.Revision()
	req := protocol.DiagnosticsRequest{Section: "agent_schedules", ParentID: "stopped", Workspace: workspace, Limit: 4}
	seen := map[string]bool{}
	firstCursor := ""
	for {
		page, err := read(store, req, true)
		if err != nil || page.Total != 25 || len(page.Items) > 4 || !page.ResourceRefreshPending || page.Evidence != "fresh_schedule_checkpoints_runtime_errors_unknown" {
			t.Fatalf("page=%+v err=%v", page, err)
		}
		if firstCursor == "" {
			firstCursor = page.NextCursor
		}
		encoded, _ := json.Marshal(page)
		if strings.Contains(string(encoded), "private-") || strings.Contains(string(encoded), "payload") || strings.Contains(string(encoded), "event_id") {
			t.Fatalf("private checkpoint data exposed: %s", encoded)
		}
		for _, item := range page.Items {
			entry := item.AgentSchedule
			if entry == nil || entry.ID != item.ID || entry.AgentID != "stopped" || seen[item.ID] || !entry.Schedule.DeliveryProblem || entry.Schedule.NextDue == "" {
				t.Fatalf("lost schedule evidence: %+v", item)
			}
			seen[item.ID] = true
			selected, err := read(store, protocol.DiagnosticsRequest{Section: "agent_schedules", ID: item.ID, ParentID: "stopped", Workspace: workspace}, true)
			if err != nil || selected.Total != 1 || selected.Items[0].ID != item.ID {
				t.Fatal("opaque ID not selectable in scope")
			}
		}
		if page.Complete {
			if page.NextCursor != "" {
				t.Fatal("terminal cursor")
			}
			break
		}
		req.Cursor = page.NextCursor
	}
	if len(seen) != 25 {
		t.Fatal("lost schedules beyond agent summary")
	}
	all, err := read(store, protocol.DiagnosticsRequest{Section: "agent_schedules"}, false)
	if err != nil || all.Total != 26 {
		t.Fatal("installation inventory incomplete")
	}
	for _, selector := range []protocol.DiagnosticsRequest{
		{Section: "agent_schedules", Workspace: workspace, ParentID: "other"},
		{Section: "agent_schedules", ParentID: "absent"},
	} {
		page, err := read(store, selector, false)
		if err != nil || page.Total != 0 {
			t.Fatal("parent or lexical scope escaped")
		}
	}
	if _, err := read(store, protocol.DiagnosticsRequest{Section: "agent_schedules", State: "done"}, false); err == nil {
		t.Fatal("irrelevant state filter accepted")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) || store.Revision() != beforeRevision {
		t.Fatal("inspection changed checkpoint/state")
	}
	schedules[24]["problem"] = ""
	writeScheduleFixture(t, path, schedules)
	req.Cursor = firstCursor
	if _, err := read(store, req, true); err == nil {
		t.Fatal("hidden checkpoint drift reused cursor")
	}
	if err := os.WriteFile(other, []byte(`{"version":99,"problem":"private-error"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := read(store, protocol.DiagnosticsRequest{Section: "agent_schedules"}, false); err == nil || strings.Contains(err.Error(), "private-") {
		t.Fatal("failed checkpoint became complete partial/empty inventory or exposed errors")
	}
	page, err := read(store, protocol.DiagnosticsRequest{Section: "agent_schedules", ParentID: "stopped"}, false)
	if err != nil || page.Total != 25 {
		t.Fatal("parent filter read unrelated invalid checkpoint")
	}
	page, err = read(store, protocol.DiagnosticsRequest{Section: "agent_schedules", Workspace: workspace}, false)
	if err != nil || page.Total != 25 {
		t.Fatal("workspace scope read unrelated invalid checkpoint")
	}
}
