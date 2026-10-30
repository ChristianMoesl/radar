package main

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"radar/internal/protocol"
	"radar/internal/version"
)

func TestAgentScheduleCLIConfigurationLifecycleAndInspection(t *testing.T) {
	dir, err := os.MkdirTemp("", "ras-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "s")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	requests := make(chan protocol.AgentRequest, 4)
	at := time.Date(2026, 10, 26, 8, 0, 0, 0, time.UTC)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			var req protocol.Request
			_ = json.NewDecoder(conn).Decode(&req)
			response := protocol.Response{OK: true, Version: version.Current()}
			if req.Method == "agent" && req.Agent != nil {
				requests <- *req.Agent
				var config protocol.AgentScheduleConfig
				_ = json.Unmarshal(req.Agent.Params, &config)
				response.AgentResult, _ = json.Marshal(protocol.AgentStatus{ID: req.Agent.ID, Schedules: []protocol.AgentScheduleStatus{{AgentScheduleConfig: config, Enabled: true, NextDue: at, LastAdmission: &protocol.AgentScheduleAdmission{ScheduledAt: at, AcceptedAt: at.Add(time.Second), TaskID: 7, CatchUp: true}, LastMissedAt: at.AddDate(0, 0, -3), Error: "fixture delivery problem"}}})
			}
			_ = json.NewEncoder(conn).Encode(response)
			_ = conn.Close()
		}
	}()
	payload := filepath.Join(dir, "payload.json")
	if err := os.WriteFile(payload, []byte(`{"type":"daily.check"}`), 0600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := outputCLI(t, []string{"agent", "schedule", "set", "fixture", "morning", "--time", "09:00", "--timezone", "Europe/Vienna", "--weekdays", "mon,tue,wed,thu,fri", "--payload", payload}, "", "RADAR_SOCKET="+path)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	for _, want := range []string{"morning · enabled", "09:00 Europe/Vienna", "Next due:", "Last admission: scheduled", "admitted", "task 7", "catch-up true", "Last missed/coalesced:", "fixture delivery problem", `{"type":"daily.check"}`} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("inspection missing %q: %s", want, stdout)
		}
	}
	req := <-requests
	var config protocol.AgentScheduleConfig
	if err := json.Unmarshal(req.Params, &config); err != nil {
		t.Fatal(err)
	}
	if req.Operation != "schedule.set" || req.ID != "fixture" || config.Name != "morning" || len(config.Weekdays) != 5 || config.Time != "09:00" || config.Timezone != "Europe/Vienna" || string(config.Payload) != `{"type":"daily.check"}` {
		t.Fatalf("configuration changed: %+v %+v", req, config)
	}
	for _, operation := range []string{"pause", "resume", "remove"} {
		stdout, stderr, code := outputCLI(t, []string{"agent", "schedule", operation, "--json", "fixture", "morning"}, "", "RADAR_SOCKET="+path)
		if code != 0 || !json.Valid([]byte(stdout)) {
			t.Fatalf("%s exit %d: %s %s", operation, code, stdout, stderr)
		}
		req := <-requests
		if req.Operation != "schedule."+operation || req.ID != "fixture" {
			t.Fatalf("lifecycle request changed: %+v", req)
		}
	}
}

func TestAgentScheduleCLIRejectsIncompleteConfiguration(t *testing.T) {
	for _, args := range [][]string{
		{"agent", "schedule"},
		{"agent", "schedule", "set", "fixture", "morning", "--time", "09:00"},
		{"agent", "schedule", "pause", "fixture", "morning", "--time", "09:00"},
		{"agent", "schedule", "remove", "fixture"},
		{"agent", "schedule", "unknown", "fixture", "morning"},
	} {
		stdout, stderr, code := outputCLI(t, args, "")
		if code == 0 || stdout != "" || stderr == "" {
			t.Fatalf("invalid configuration accepted: %v: %d %s %s", args, code, stdout, stderr)
		}
	}
}
