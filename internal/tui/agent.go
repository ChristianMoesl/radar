package tui

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"radar/internal/client"
	"radar/internal/protocol"
)

type agentState struct {
	generation   uint64
	conversation int
	id           string
	status       protocol.AgentStatus
	cursor       int
	text         string
	input        bool
	busy         bool
	request      *protocol.AgentRequest
	revision     int
	action       string
	detail       string
	scroll       int
}
type agentResultMsg struct {
	id        string
	operation string
	data      json.RawMessage
	err       error
}
type agentTickMsg struct {
	id         string
	generation uint64
}
type agentChoice struct {
	label     string
	operation string
	action    string
	text      bool
	params    any
	workspace string
}

func taskAgentID(task protocol.Task) string {
	for _, ref := range task.SourceRefs {
		if ref.AgentID != "" {
			return ref.AgentID
		}
	}
	return ""
}

func (m model) openAgent(id string) (tea.Model, tea.Cmd) {
	m.mode = "agent"
	m.agent = agentState{id: id, busy: true, generation: uint64(time.Now().UnixNano())}
	m.err = nil
	return m, tea.Batch(m.agentCall(protocol.AgentRequest{Operation: "inspect", ID: id}), m.agentTick())
}

func (m model) agentCall(request protocol.AgentRequest) tea.Cmd {
	return func() tea.Msg {
		response, err := client.CallRequest(m.socketPath, protocol.Request{Method: "agent", Agent: &request})
		if err == nil && !response.OK {
			err = fmt.Errorf("%s", response.Error)
			if response.AgentErrorCode != 0 {
				err = &protocol.AgentRPCError{Code: response.AgentErrorCode, Message: response.Error}
			}
		}
		return agentResultMsg{id: request.ID, operation: request.Operation, data: response.AgentResult, err: err}
	}
}

func (m model) agentTick() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return agentTickMsg{id: m.agent.id, generation: m.agent.generation} })
}

func (m model) agentChoices() []agentChoice {
	var choices []agentChoice
	s := m.agent.status
	if s.Workspace.Path != "" {
		choices = append(choices, agentChoice{label: "Open associated workspace", operation: "open", workspace: s.Workspace.Path})
	}
	if s.Running {
		if s.Snapshot != nil {
			p := s.Snapshot.Presentation
			for _, a := range p.Actions {
				choices = append(choices, agentChoice{label: a.Label, operation: "interact", action: a.ID, text: a.Text})
			}
			if p.Message {
				choices = append(choices, agentChoice{label: "Send a message", operation: "interact", text: true})
			}
			for _, c := range p.Conversations {
				choices = append(choices, agentChoice{label: "Conversation: " + c.Label, operation: "conversation", params: map[string]int{"id": c.ID}})
			}
			for _, w := range p.Workspaces {
				if w.Path == s.Workspace.Path {
					continue
				}
				choices = append(choices, agentChoice{label: "Open workspace: " + w.Label, operation: "open", workspace: w.Path})
			}
		}
		choices = append(choices, agentChoice{label: "Restart agent", operation: "restart"}, agentChoice{label: "Stop agent", operation: "stop"})
	} else {
		choices = append(choices, agentChoice{label: "Start agent", operation: "start"})
	}
	choices = append(choices, agentChoice{label: "View logs", operation: "logs"})
	return choices
}

func (m model) updateAgent(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "ctrl+c" {
		return m, tea.Quit
	}
	if m.agent.input {
		if m.agent.request != nil && msg.String() != "esc" && msg.String() != "enter" {
			return m, nil
		}
		switch msg.String() {
		case "esc":
			m.agent.input = false
			m.agent.request = nil
			m.agent.text = ""
			m.err = nil
			return m, nil
		case "enter":
			if !m.agent.busy {
				return m, m.submitAgentInteraction()
			}
		case "backspace", "ctrl+h":
			m.agent.text = dropLastRune(m.agent.text)
		default:
			if value, ok := textInputValue(msg); ok {
				m.agent.text += value
			}
		}
		return m, nil
	}
	if m.agent.detail != "" {
		switch msg.String() {
		case "esc", "backspace":
			m.agent.detail = ""
			m.agent.conversation = 0
			m.agent.scroll = 0
			return m, nil
		case "down", "j":
			m.agent.scroll++
		case "up", "k":
			m.agent.scroll = max(0, m.agent.scroll-1)
		}
		return m, nil
	}
	choices := m.agentChoices()
	switch msg.String() {
	case "s":
		if m.agent.status.Snapshot != nil {
			m.agent.detail = m.agent.status.Snapshot.Presentation.Summary
			m.agent.scroll = 0
		}
		return m, nil
	case "esc", "backspace":
		m.mode = ""
		m.agent = agentState{}
		m.err = nil
		return m, nil
	case "q", "ctrl+c":
		return m, tea.Quit
	case "down", "j":
		m.agent.cursor = min(m.agent.cursor+1, len(choices)-1)
	case "up", "k":
		m.agent.cursor = max(0, m.agent.cursor-1)
	case "enter":
		if m.agent.busy || len(choices) == 0 {
			return m, nil
		}
		m.agent.cursor = max(0, min(m.agent.cursor, len(choices)-1))
		choice := choices[m.agent.cursor]
		if choice.operation == "open" {
			task := protocol.Task{Title: choice.label}
			return m, m.startOperation(task, "session", "Opening workspace…", "Workspace open failed", taskAction(m.openRegisteredWorkspace(protocol.SourceRef{Path: choice.workspace})))
		}
		if choice.operation == "interact" {
			m.agent.action = choice.action
			m.agent.revision = m.agent.status.Snapshot.Presentation.Revision
			m.agent.text = ""
			m.agent.input = true
			m.agent.request = nil
			if !choice.text {
				return m, m.submitAgentInteraction()
			}
			return m, nil
		}
		params, _ := json.Marshal(choice.params)
		if choice.operation == "conversation" {
			m.agent.conversation = choice.params.(map[string]int)["id"]
		}
		m.agent.busy = true
		m.err = nil
		return m, m.agentCall(protocol.AgentRequest{Operation: choice.operation, ID: m.agent.id, Params: params})
	case "r":
		if !m.agent.busy {
			m.agent.busy = true
			return m, m.agentCall(protocol.AgentRequest{Operation: "inspect", ID: m.agent.id})
		}
	}
	return m, nil
}

func (m *model) submitAgentInteraction() tea.Cmd {
	if m.agent.request == nil {
		id := make([]byte, 16)
		if _, err := rand.Read(id); err != nil {
			m.err = err
			return nil
		}
		params, _ := json.Marshal(map[string]any{"eventId": hex.EncodeToString(id), "revision": m.agent.revision, "action": m.agent.action, "text": m.agent.text})
		m.agent.request = &protocol.AgentRequest{Operation: "interact", ID: m.agent.id, Params: params}
	}
	m.agent.busy = true
	m.err = nil
	return m.agentCall(*m.agent.request)
}

func (m model) agentResult(msg agentResultMsg) (tea.Model, tea.Cmd) {
	if m.mode != "agent" || m.agent.id != msg.id {
		return m, nil
	}
	m.agent.busy = false
	if msg.err != nil {
		m.err = msg.err
		var rpcErr *protocol.AgentRPCError
		if errors.As(msg.err, &rpcErr) && rpcErr.Code == -32010 {
			m.agent.input = false
			m.agent.request = nil
			m.message = "The interaction changed. Current choices have been refreshed."
			return m, m.agentCall(protocol.AgentRequest{Operation: "inspect", ID: m.agent.id})
		}
		return m, nil
	}
	m.err = nil
	if msg.operation == "inspect" {
		if err := json.Unmarshal(msg.data, &m.agent.status); err != nil {
			m.err = err
		}
		m.agent.cursor = max(0, min(m.agent.cursor, len(m.agentChoices())-1))
		return m, nil
	}
	if msg.operation == "conversation" {
		m.agent.detail = conversationText(msg.data)
		return m, nil
	}
	if msg.operation == "logs" {
		var value struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(msg.data, &value); err != nil {
			m.err = err
			return m, nil
		}
		file, err := os.Open(value.Path)
		if err != nil {
			m.err = err
			return m, nil
		}
		defer file.Close()
		if info, err := file.Stat(); err == nil && info.Size() > 65536 {
			_, _ = file.Seek(-65536, io.SeekEnd)
		}
		data, err := io.ReadAll(io.LimitReader(file, 65536))
		if err != nil {
			m.err = err
			return m, nil
		}
		m.agent.detail = string(data)
		if m.agent.detail == "" {
			m.agent.detail = "No log output yet"
		}
		m.agent.scroll = 0
		return m, nil
	}
	m.agent.input = false
	m.agent.request = nil
	m.agent.busy = true
	return m, m.agentCall(protocol.AgentRequest{Operation: "inspect", ID: m.agent.id})
}

func conversationText(data json.RawMessage) string {
	var value struct {
		Entries []struct {
			Kind  string `json:"kind"`
			Model []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"model"`
		} `json:"entries"`
	}
	if json.Unmarshal(data, &value) != nil {
		return "Conversation unavailable"
	}
	var lines []string
	for _, entry := range value.Entries {
		for _, message := range entry.Model {
			var plain string
			if json.Unmarshal(message.Content, &plain) == nil {
				lines = append(lines, message.Role+": "+plain)
				continue
			}
			var blocks []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			_ = json.Unmarshal(message.Content, &blocks)
			for _, block := range blocks {
				if block.Type == "text" {
					lines = append(lines, message.Role+": "+block.Text)
				}
			}
		}
	}
	if len(lines) == 0 {
		return "No messages yet"
	}
	return strings.Join(lines, "\n\n")
}

func (m model) agentView() string {
	width := m.contentWidth()
	s := m.agent.status
	title, summary := m.agent.id, "Stopped"
	if s.Running {
		summary = "Idle"
	}
	var lines []string
	if s.Snapshot != nil {
		p := s.Snapshot.Presentation
		if p.Title != "" {
			title = p.Title
		}
		if p.Summary != "" {
			summary = p.Summary
		}
		lines = append(lines, fmt.Sprintf("Pending work: %d", s.Snapshot.Pending))
	}
	summaryLines := strings.Split(ansi.Wrap(ansi.Strip(summary), width, ""), "\n")
	limit := max(2, min(6, m.height/5))
	if len(summaryLines) > limit {
		summaryLines = append(summaryLines[:limit], "… press s for the full summary")
	}
	lines = append([]string{titleStyle.Render(ansi.Strip(title)), "", strings.Join(summaryLines, "\n")}, lines...)
	lines = append(lines, fmt.Sprintf("Workspace: %s · %s", ansi.Strip(s.Workspace.Path), ansi.Strip(s.Workspace.Mode)))
	if s.Collection != "" {
		lines = append(lines, fmt.Sprintf("Collection: %s · %d pending deliveries", ansi.Strip(s.Collection), s.CollectionPending))
	}
	if s.CollectionError != "" {
		lines = append(lines, attentionStyle.Render(ansi.Strip(s.CollectionError)))
	}
	if s.Error != "" {
		lines = append(lines, attentionStyle.Render(ansi.Strip(s.Error)))
	}
	if m.agent.detail != "" {
		body := strings.Split(ansi.Wrap(ansi.Strip(m.agent.detail), width, ""), "\n")
		offset := min(m.agent.scroll, max(0, len(body)-1))
		rows := max(1, m.height-10)
		lines = append(lines, "", strings.Join(body[offset:min(len(body), offset+rows)], "\n"), "", helpStyle.Render("↑/↓ scroll · esc back"))
	} else if m.agent.input {
		lines = append(lines, "", "> "+ansi.Strip(m.agent.text), "", helpStyle.Render("enter send / retry · esc cancel"))
	} else {
		lines = append(lines, "")
		choices := m.agentChoices()
		count := max(3, m.height-22)
		start := max(0, m.agent.cursor-count+1)
		for i := start; i < min(len(choices), start+count); i++ {
			choice := choices[i]
			prefix := "  "
			if i == m.agent.cursor {
				prefix = "› "
			}
			lines = append(lines, prefix+ansi.Strip(choice.label))
		}
		if s.Snapshot != nil {
			var recent []struct {
				Kind    string `json:"kind"`
				Status  string `json:"status"`
				Outcome struct {
					Status string `json:"status"`
					Error  struct {
						Message string `json:"message"`
					} `json:"error"`
				} `json:"outcome"`
			}
			_ = json.Unmarshal(s.Snapshot.Recent, &recent)
			lines = append(lines, "", titleStyle.Render("Recent work"))
			for _, task := range recent[:min(5, len(recent))] {
				label := task.Status
				if task.Outcome.Status != "" {
					label = task.Outcome.Status
				}
				lines = append(lines, "  "+ansi.Strip(task.Kind)+" · "+label)
				if task.Outcome.Error.Message != "" {
					lines = append(lines, "    "+ansi.Strip(task.Outcome.Error.Message))
				}
			}
		}
		lines = append(lines, "", helpStyle.Render("↑/↓ choose · enter select · s summary · r refresh · esc back"))
	}
	if m.agent.busy {
		lines = append(lines, subtleStyle.Render("Working…"))
	}
	if m.message != "" {
		lines = append(lines, subtleStyle.Render(ansi.Strip(m.message)))
	}
	if m.err != nil {
		lines = append(lines, attentionStyle.Render(ansi.Strip(m.err.Error())))
	}
	return m.renderFrame(strings.Join(lines, "\n"), width)
}
