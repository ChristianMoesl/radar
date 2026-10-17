package tui

import (
	"bytes"
	"errors"
	tea "github.com/charmbracelet/bubbletea"
	"radar/internal/protocol"
	"strings"
	"testing"
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

func TestAgentWorkspaceVisibleAndSummaryCanBeReadWithoutHidingActions(t *testing.T) {
	m := newModel("unused.sock")
	m.mode = "agent"
	m.width = 100
	m.height = 30
	m.agent = agentState{id: "fixture", status: protocol.AgentStatus{Workspace: protocol.AgentWorkspaceBinding{Path: "/workspace/fixture", Mode: "sbx"}, Running: true, Snapshot: &protocol.AgentSnapshot{Presentation: protocol.AgentPresentation{Summary: strings.Repeat("long summary\n", 1000), Actions: []protocol.AgentAction{{ID: "respond", Label: "Respond"}}}}}}
	view := m.agentView()
	if !strings.Contains(view, "Respond") || !strings.Contains(view, "sbx") || !strings.Contains(view, "/workspace/fixture") {
		t.Fatal("workspace or actions hidden")
	}
	result, _ := m.updateAgent(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	if result.(model).agent.detail != m.agent.status.Snapshot.Presentation.Summary {
		t.Fatal("full summary unavailable")
	}
}
