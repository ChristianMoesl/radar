package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"radar/internal/cleanup"
	"radar/internal/protocol"
)

// Keep the inspected identity separate from the overview's fallback selection.
// A missing task must never silently turn into another task's details.
type detailState struct {
	task      protocol.Task
	available bool
	scroll    int
}

func (m model) updateDetail(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "backspace":
		m.mode = ""
		m.detail = detailState{}
		return m, nil
	case "q", "ctrl+c":
		return m, tea.Quit
	case "j", "down", "ctrl+n", "k", "up", "ctrl+p", "pgdown", "ctrl+d", "pgup", "ctrl+u", "home", "g", "end", "G":
		lines, _, _, rows := m.detailViewport()
		limit := max(0, len(lines)-rows)
		m.detail.scroll = max(0, min(m.detail.scroll, limit))
		switch msg.String() {
		case "j", "down", "ctrl+n":
			m.detail.scroll++
		case "k", "up", "ctrl+p":
			m.detail.scroll--
		case "pgdown", "ctrl+d":
			m.detail.scroll += rows
		case "pgup", "ctrl+u":
			m.detail.scroll -= rows
		case "home", "g":
			m.detail.scroll = 0
		case "end", "G":
			m.detail.scroll = limit
		}
		m.detail.scroll = max(0, min(m.detail.scroll, limit))
	}
	// Inspect is read-only: no key falls through to overview actions.
	return m, nil
}

func (m *model) clampDetailScroll() {
	if m.mode != "detail" {
		return
	}
	lines, _, _, rows := m.detailViewport()
	m.detail.scroll = max(0, min(m.detail.scroll, len(lines)-rows))
}

// Wrap before slicing so offsets count terminal rows, not logical fields.
// The title, position indicator, and help stay outside the scrollable body.
func (m model) detailViewport() (lines []string, title, footer string, rows int) {
	width := m.contentWidth()
	title = titleStyle.Render(truncateLine("Inspect · "+strings.Join(strings.Fields(m.detail.task.Title), " "), width))
	body := ansi.Wrap(subtleStyle.Render("Task no longer available."), width, "")
	if m.detail.available {
		body = taskDetailView(m.detail.task, width)
	}
	lines = strings.Split(body, "\n")
	footer = ansi.Wrap(helpStyle.Render("↑/k/ctrl+p ↓/j/ctrl+n scroll • PgUp/PgDn/ctrl+u/d page • g/Home G/End top/bottom • esc/backspace back • q quit"), width, "")
	rows = len(lines)
	if m.height > 0 {
		// Two section separators plus one position-indicator row.
		rows = max(1, m.height-m.frameHeight()-lipgloss.Height(title)-lipgloss.Height(footer)-3)
	}
	return lines, title, footer, rows
}

func (m model) detailScreen() string {
	lines, title, footer, rows := m.detailViewport()
	offset := max(0, min(m.detail.scroll, len(lines)-min(rows, len(lines))))
	end := min(len(lines), offset+rows)
	body := strings.Join(lines[offset:end], "\n")
	body = lipgloss.NewStyle().Height(rows).Render(body)
	position := helpStyle.Render(fmt.Sprintf("%d–%d / %d", offset+1, end, len(lines)))
	return m.renderFrame(title+"\n\n"+body+"\n\n"+position+"\n"+footer, m.contentWidth())
}

func taskDetailView(task protocol.Task, width int) string {
	var lines []string
	appendDetailLine := func(label string, value string) {
		if value != "" {
			lines = append(lines, fmt.Sprintf("%-10s %s", label, value))
		}
	}
	if issues := cleanup.Unresolved(task); len(issues) > 0 {
		lines = append(lines, titleStyle.Render("Unresolved"))
		for _, issue := range issues {
			label := issue.Ref.Title
			if label == "" {
				label = issue.Ref.ID
			}
			if issue.Ref.Path != "" {
				label = shortenPath(issue.Ref.Path)
			}
			resource := issue.Ref.Source
			if issue.Ref.Kind != resource {
				resource += " " + issue.Ref.Kind
			}
			lines = append(lines, "  "+strings.TrimSpace(resource)+" · "+label, "    "+issue.Message)
		}
		lines = append(lines, "")
	}
	// The fixed heading is abbreviated; retain the full title in the body.
	appendDetailLine("Title", task.Title)
	appendDetailLine("Status", task.Attention)
	if activity := taskActivity(task); activity != protocol.ActivityIdle {
		appendDetailLine("Activity", activity.String())
	}
	appendDetailLine("Reason", displayTaskReason(task))
	appendDetailLine("Repo", task.Repo)
	appendDetailLine("URL", task.URL)
	if len(task.Metadata) > 0 {
		lines = append(lines, "", titleStyle.Render("Metadata"))
		keys := make([]string, 0, len(task.Metadata))
		for key := range task.Metadata {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			appendDetailLine(key, shortenPath(task.Metadata[key]))
		}
	}
	if len(task.SourceRefs) > 0 {
		lines = append(lines, "", titleStyle.Render("Source refs"))
		for _, ref := range task.SourceRefs {
			line := "  " + sourceRefLabel(ref)
			if ref.URL != "" {
				line += "  " + subtleStyle.Render(ref.URL)
			}
			lines = append(lines, line)
			appendRefDetail := func(label string, value string) {
				if value != "" {
					lines = append(lines, subtleStyle.Render(fmt.Sprintf("    %-8s %s", label, value)))
				}
			}
			appendRefDetail("source", ref.Source)
			appendRefDetail("kind", ref.Kind)
			appendRefDetail("role", string(ref.Role))
			appendRefDetail("status", ref.Status)
			appendRefDetail("repo", ref.Repo)
			appendRefDetail("path", shortenPath(ref.Path))
			appendRefDetail("branch", ref.Branch)
			metadataKeys := make([]string, 0, len(ref.Metadata))
			for key := range ref.Metadata {
				metadataKeys = append(metadataKeys, key)
			}
			sort.Strings(metadataKeys)
			for _, key := range metadataKeys {
				appendRefDetail(key, shortenPath(ref.Metadata[key]))
			}
		}
	}
	return ansi.Wrap(strings.Join(lines, "\n"), max(1, width), "")
}
