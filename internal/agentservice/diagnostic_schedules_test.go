package agentservice

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"radar/internal/protocol"
)

func TestDiagnosticSchedulesShareProductionValidationAndDueEvidence(t *testing.T) {
	t.Setenv("RADAR_AGENT_ROOT", t.TempDir())
	reg := diagnosticRegistration(t, os.Getenv("RADAR_AGENT_ROOT"), "fixture", "/registered/workspace")
	now := time.Date(2026, 11, 16, 11, 0, 0, 0, time.UTC)
	state := scheduleState{Version: 1}
	for i, suffix := range []string{"a", "b"} {
		state.Schedules = append(state.Schedules, calendarSchedule{ID: strings.Repeat("x", 600) + suffix, Enabled: i == 0, EligibleAfter: now, AgentScheduleConfig: protocol.AgentScheduleConfig{Name: "schedule-" + suffix, Time: "12:00", Timezone: "Europe/Vienna", Weekdays: []string{"mon", "tue"}, Payload: json.RawMessage(`{"private":"payload"}`)}})
	}
	if err := atomicJSON(schedulePath(reg), state); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := diagnosticSchedules(reg)
	if err != nil {
		t.Fatal(err)
	}
	production, err := scheduleStatuses(reg, now)
	if err != nil || !reflect.DeepEqual(production, scheduleStateStatuses(checkpoint, now)) {
		t.Fatal("bounded reader changed production validation or due evidence")
	}
	views, err := diagnosticScheduleViews(reg, now)
	if err != nil || len(views) != 2 || views[0].ID == views[1].ID || views[0].Schedule.ID != views[1].Schedule.ID || views[0].Schedule.NextDue != diagnosticTime(production[0].NextDue) || views[1].Schedule.NextDue != "" {
		t.Fatalf("full identity or due evidence lost: %+v %v", views, err)
	}
	state.Schedules[0].Enabled = false
	state.Schedules[0].Payload = json.RawMessage(`{"private":"different"}`)
	if err := atomicJSON(schedulePath(reg), state); err != nil {
		t.Fatal(err)
	}
	changed, err := diagnosticScheduleViews(reg, now.Add(time.Hour))
	if err != nil || changed[0].ID != views[0].ID || changed[0].Schedule.Enabled {
		t.Fatal("mutable config or due time changed stable identity")
	}
	other := reg
	other.ID = "another"
	if err := os.MkdirAll(filepath.Join(os.Getenv("RADAR_AGENT_ROOT"), other.ID), 0700); err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(schedulePath(other), state); err != nil {
		t.Fatal(err)
	}
	otherViews, err := diagnosticScheduleViews(other, now)
	if err != nil || otherViews[0].ID == views[0].ID {
		t.Fatal("schedule identity does not include its owning agent")
	}
}

func TestDiagnosticSchedulesBoundedUnsafeAndInvalidCheckpointsStayUnknown(t *testing.T) {
	for _, kind := range []string{"missing", "malformed", "invalid", "version", "oversized", "directory", "symlink", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			t.Setenv("RADAR_AGENT_ROOT", t.TempDir())
			reg := diagnosticRegistration(t, os.Getenv("RADAR_AGENT_ROOT"), "fixture", "/registered/workspace")
			path := schedulePath(reg)
			var err error
			switch kind {
			case "malformed":
				err = os.WriteFile(path, []byte("{"), 0600)
			case "invalid":
				err = os.WriteFile(path, []byte(`{"version":1,"schedules":[{"id":"one","name":"private-invalid","time":"private-error"}]}`), 0600)
			case "version":
				err = os.WriteFile(path, []byte(`{"version":99}`), 0600)
			case "oversized":
				err = os.WriteFile(path, []byte(strings.Repeat("x", protocol.AgentMaxFrameBytes+1)), 0600)
			case "directory":
				err = os.Mkdir(path, 0700)
			case "symlink":
				err = os.Symlink(filepath.Join(os.Getenv("RADAR_AGENT_ROOT"), "absent"), path)
			case "fifo":
				err = syscall.Mkfifo(path, 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			before := time.Now()
			entries, readErr := DiagnosticAgentSchedules("", reg.ID, "")
			if time.Since(before) > time.Second {
				t.Fatal("unsafe diagnostic checkpoint read blocked")
			}
			if kind == "missing" {
				production, err := loadSchedules(reg)
				if readErr != nil || len(entries) != 0 || err != nil || len(production.Schedules) != 0 {
					t.Fatal("missing schedule checkpoint differs from production semantics")
				}
				return
			}
			if readErr == nil || len(entries) != 0 {
				t.Fatal("unsafe/invalid checkpoint reported complete inventory")
			}
			agents, err := DiagnosticAgents("", reg.ID)
			if err != nil || len(agents) != 1 || agents[0].ScheduleCount != nil || agents[0].SchedulesEvidence != "unavailable" {
				t.Fatal("unknown schedule summary became zero schedules")
			}
		})
	}
}
