package tui

import (
	"bytes"
	"errors"
	tea "github.com/charmbracelet/bubbletea"
	"radar/internal/protocol"
	"strings"
	"testing"
	"time"
)

func TestAgentInteractionRetryRetainsDeliveryAndStaleActionRefreshes(t *testing.T) {
	m := newModel("unused.sock")
	m.mode = "agent"
	m.agent = agentState{id: "fixture", input: true, revision: 4, action: "respond", text: "hello"}
	if cmd := m.submitAgentInteraction(); cmd == nil {
		t.Fatal("no delivery command")
	}
	request := append([]byte(nil), m.agent.request.Params...)
	result, _ := m.agentResult(agentResultMsg{id: "fixture", operation: "interact", err: errors.New("response lost")})
	m = result.(model)
	if m.agent.request == nil || !m.agent.input {
		t.Fatal("ambiguous failure discarded original request")
	}
	m.submitAgentInteraction()
	if !bytes.Equal(m.agent.request.Params, request) {
		t.Fatal("retry changed event identity or content")
	}
	result, refresh := m.agentResult(agentResultMsg{id: "fixture", operation: "interact", err: &protocol.AgentRPCError{Code: -32010, Message: "changed"}})
	m = result.(model)
	if refresh == nil || m.agent.request != nil || m.agent.input {
		t.Fatal("stale decision can be retried without refresh")
	}
}

func TestAgentScheduleInspectionShowsConfigurationAdmissionAndErrors(t *testing.T) {
	m := newModel("unused.sock")
	m.mode, m.width, m.height = "agent", 100, 30
	at := time.Date(2026, 10, 26, 8, 0, 0, 0, time.UTC)
	m.agent = agentState{id: "fixture", status: protocol.AgentStatus{Schedules: []protocol.AgentScheduleStatus{{AgentScheduleConfig: protocol.AgentScheduleConfig{Name: "morning", Time: "09:00", Timezone: "Europe/Vienna", Weekdays: []string{"mon", "fri"}, Payload: []byte(`{"type":"daily.check"}`)}, Enabled: true, NextDue: at, PendingAt: at, LastAdmission: &protocol.AgentScheduleAdmission{ScheduledAt: at, AcceptedAt: at.Add(time.Second), TaskID: 7, CatchUp: true}, LastMissedAt: at.AddDate(0, 0, -3), Error: "admission pending"}}}}
	view := m.agentView()
	if !strings.Contains(view, "Schedules: 1") || !strings.Contains(view, "admission pending") {
		t.Fatal("schedule state hidden in Agents view")
	}
	for index, choice := range m.agentChoices() {
		if choice.operation == "schedules" {
			m.agent.cursor = index
			result, cmd := m.updateAgent(tea.KeyMsg{Type: tea.KeyEnter})
			if cmd != nil {
				t.Fatal("schedule inspection unexpectedly made a remote call")
			}
			detail := result.(model).agent.detail
			for _, want := range []string{"morning · enabled", "mon,fri at 09:00 Europe/Vienna", "Next due:", "Pending admission:", "Last admission:", "admitted", "task 7", "catch-up true", "Last missed/coalesced:", "admission pending", "admission is not completion"} {
				if !strings.Contains(detail, want) {
					t.Fatalf("schedule details missing %q: %s", want, detail)
				}
			}
			return
		}
	}
	t.Fatal("no schedule inspection choice")
}

func TestAgentWorkspaceVisibleAndSummaryCanBeReadWithoutHidingActions(t *testing.T) {
	m := newModel("unused.sock")
	m.mode = "agent"
	m.width = 100
	m.height = 30
	m.agent = agentState{id: "fixture", status: protocol.AgentStatus{Workspace: protocol.AgentWorkspaceBinding{Path: "/workspace/fixture", Mode: "sbx"}, Running: true, Snapshot: &protocol.AgentSnapshot{Presentation: protocol.AgentPresentation{Summary: strings.Repeat("long summary\n", 1000), Actions: []protocol.AgentAction{{ID: "respond", Label: "Respond"}}}}}}
	m.agent.status.Collection = "github"
	m.agent.status.CollectionPending = 3
	m.agent.status.CollectionError = "collection delivery pending"
	view := m.agentView()
	if !strings.Contains(view, "Respond") || !strings.Contains(view, "sbx") || !strings.Contains(view, "/workspace/fixture") {
		t.Fatal("workspace or actions hidden")
	}
	if !strings.Contains(view, "3 pending deliveries") || !strings.Contains(view, "collection delivery pending") {
		t.Fatal("pending collection or delivery failure hidden")
	}
	result, _ := m.updateAgent(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	if result.(model).agent.detail != m.agent.status.Snapshot.Presentation.Summary {
		t.Fatal("full summary unavailable")
	}
}
