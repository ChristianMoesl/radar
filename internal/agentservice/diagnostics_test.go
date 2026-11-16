package agentservice

import (
	"bytes"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"radar/internal/protocol"
)

func diagnosticRegistration(t *testing.T, root, id, workspace string) Registration {
	t.Helper()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	reg := Registration{Version: 1, ID: id, Entrypoint: "private-definition-command", Workspace: protocol.AgentWorkspaceBinding{ID: "workspace-" + id, Path: workspace, Mode: "local"}}
	if err := atomicJSON(filepath.Join(dir, "registration.json"), reg); err != nil {
		t.Fatal(err)
	}
	return reg
}

func TestDiagnosticAgentsKeepStoppedOwnersAndProjectOnlyBoundedCheckpointEvidence(t *testing.T) {
	root := agentRuntimeTestDir(t)
	t.Setenv("RADAR_AGENT_ROOT", filepath.Join(root, "agents"))
	t.Setenv("RADAR_SOCKET", filepath.Join(root, "radar.sock"))
	workspace := filepath.Join(root, "workspace")
	reg := diagnosticRegistration(t, os.Getenv("RADAR_AGENT_ROOT"), "stopped", workspace)
	reg.Collection = "github"
	dir, _ := instanceDir(reg.ID)
	if err := atomicJSON(filepath.Join(dir, "registration.json"), reg); err != nil {
		t.Fatal(err)
	}
	diagnosticRegistration(t, os.Getenv("RADAR_AGENT_ROOT"), "unrelated", workspace+"-other")
	snapshot := protocol.AgentSnapshot{Protocol: protocol.AgentProtocolVersion, InstanceID: reg.ID, DefinitionVersion: 4, PID: 987654, Pending: 7, Problem: "private-provider-body", Presentation: protocol.AgentPresentation{Attention: true, Revision: 9, Title: "private-title", Summary: "private-summary", Actions: []protocol.AgentAction{{ID: "private-action", Payload: json.RawMessage(`{"secret":"private-payload"}`)}}, Conversations: []protocol.AgentConversation{{ID: 1, Label: "private-conversation"}}}, Tasks: json.RawMessage(`[{"body":"private-task"}]`), Recent: json.RawMessage(`["private-event"]`)}
	if err := atomicJSON(filepath.Join(dir, "snapshot.json"), snapshot); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ingress.token"), []byte("private-token"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(collectionPath(reg), collectionState{Version: 1, Source: "github", Seen: map[string]collectionRevision{}, Pending: []collectionEvent{{Source: "private-source", ID: "private-event", Payload: json.RawMessage(`{"body":"private-event-body"}`)}}, Problem: "private-delivery-error"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	schedules := scheduleState{Version: 1}
	for i := 0; i < protocol.DiagnosticChildLimit+3; i++ {
		id := "schedule-" + strconv.Itoa(i)
		schedules.Schedules = append(schedules.Schedules, calendarSchedule{ID: id, Enabled: true, EligibleAfter: now, AgentScheduleConfig: protocol.AgentScheduleConfig{Name: id, Time: "12:00", Timezone: "UTC", Weekdays: []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}, Payload: json.RawMessage(`{"body":"private-schedule-payload"}`)}, Pending: &schedulePending{ScheduledAt: now, Event: scheduledEvent{Source: "radar.schedule", ID: scheduleEventID(id, now), Payload: json.RawMessage(`{"body":"private-pending-payload"}`)}}, LastAdmission: &protocol.AgentScheduleAdmission{TaskID: 3, ScheduledAt: now, AcceptedAt: now, EventID: scheduleEventID(id, now)}, Problem: "private-schedule-error"})
	}
	if err := atomicJSON(schedulePath(reg), schedules); err != nil {
		t.Fatal(err)
	}
	before := map[string][]byte{}
	for _, name := range []string{"registration.json", "snapshot.json", "ingress.token", "collection.json", "schedules.json"} {
		before[name], _ = os.ReadFile(filepath.Join(dir, name))
	}
	path, _ := socketPath(reg.ID)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	items, err := DiagnosticAgents(workspace, "")
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	view := items[0]
	if view.ID != reg.ID || !view.Registered || view.Desired || view.Workspace.Path != workspace || view.OwnershipHold != "registered_owner_even_when_stopped" || view.RunnerEvidence != "unknown_not_probed" || view.LeaseEvidence != "unknown_not_inspected" {
		t.Fatalf("registration/runtime claims incorrect: %+v", view)
	}
	if view.SnapshotEvidence != "cached_runner_snapshot" || view.SnapshotSavedAt == "" || view.Snapshot == nil || view.Snapshot.Pending != 7 || view.Snapshot.Revision != 9 || !view.Snapshot.Attention || !view.Snapshot.Problem || view.Snapshot.DefinitionVersion != 4 {
		t.Fatalf("snapshot projection lost: %+v", view)
	}
	if view.CollectionEvidence != "fresh_checkpoint" || view.Collection == nil || view.Collection.Pending != 1 || !view.Collection.DeliveryProblem {
		t.Fatalf("collection evidence lost: %+v", view)
	}
	if view.ScheduleCount == nil || *view.ScheduleCount != 19 || len(view.Schedules) != protocol.DiagnosticChildLimit || !view.Truncated || view.SchedulesEvidence != "fresh_checkpoint_runtime_errors_unknown" || !view.Schedules[0].DeliveryProblem || view.Schedules[0].PendingAt == "" || view.Schedules[0].NextDue == "" || view.Schedules[0].LastAdmission.TaskID != 3 {
		t.Fatalf("schedule evidence lost or unbounded: %+v", view)
	}
	encoded, _ := json.Marshal(view)
	if strings.Contains(string(encoded), "private-") || strings.Contains(string(encoded), "987654") || strings.Contains(string(encoded), "event_id") {
		t.Fatalf("private payload exposed: %s", encoded)
	}
	for name, data := range before {
		after, _ := os.ReadFile(filepath.Join(dir, name))
		if !bytes.Equal(data, after) {
			t.Fatalf("inspection changed %s", name)
		}
	}
	_ = listener.SetDeadline(time.Now().Add(20 * time.Millisecond))
	if conn, err := listener.Accept(); err == nil {
		conn.Close()
		t.Fatal("inspection contacted the runner")
	}
	owners, err := WorkspaceRemovalOwners([]string{workspace})
	if err != nil || len(owners[workspace]) != 1 || owners[workspace][0] != view.ID {
		t.Fatal("diagnostics disagree with production ownership guard")
	}
}

func TestDiagnosticAgentMissingInvalidOrUnsupportedEvidenceIsNotHealthy(t *testing.T) {
	for _, test := range []struct{ name, content, evidence string }{
		{"missing", "", "missing_no_runtime_conclusion"},
		{"malformed", "{", "invalid_or_incompatible"},
		{"identity", `{"protocol":1,"instanceId":"other","pending":1}`, "invalid_or_incompatible"},
		{"version", `{"protocol":99,"instanceId":"fixture","pending":1}`, "invalid_or_incompatible"},
		{"negative", `{"protocol":1,"instanceId":"fixture","pending":-1}`, "invalid_or_incompatible"},
		{"oversized", strings.Repeat("x", protocol.AgentMaxFrameBytes+1), "unavailable"},
		{"directory", "", "unavailable"},
		{"symlink", "", "unavailable"},
		{"fifo", "", "unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("RADAR_AGENT_ROOT", root)
			reg := diagnosticRegistration(t, root, "fixture", filepath.Join(root, "workspace"))
			path := filepath.Join(root, reg.ID, "snapshot.json")
			var err error
			switch test.name {
			case "directory":
				err = os.Mkdir(path, 0700)
			case "symlink":
				err = os.Symlink(filepath.Join(root, "missing-target"), path)
			case "fifo":
				err = syscall.Mkfifo(path, 0600)
			default:
				if test.content != "" {
					err = os.WriteFile(path, []byte(test.content), 0600)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			// Malformed independent checkpoints must not be reported as no work.
			reg.Collection = "github"
			if err := atomicJSON(filepath.Join(root, reg.ID, "registration.json"), reg); err != nil {
				t.Fatal(err)
			}
			for _, file := range []string{collectionPath(reg), schedulePath(reg)} {
				if err := os.WriteFile(file, []byte(`{"version":99,"problem":"private-error"}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			items, err := DiagnosticAgents("", "fixture")
			if err != nil || len(items) != 1 {
				t.Fatalf("registration lost: %+v %v", items, err)
			}
			item := items[0]
			if item.SnapshotEvidence != test.evidence || item.Snapshot != nil || item.CollectionEvidence != "unavailable" || item.Collection != nil || item.SchedulesEvidence != "unavailable" || item.ScheduleCount != nil || item.RunnerEvidence != "unknown_not_probed" {
				t.Fatalf("unsupported data became healthy evidence: %+v", item)
			}
		})
	}
}
