package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"radar/internal/protocol"
)

const cleanupMaxWidth = 96

func (m model) cleanupWidth() int {
	return min(cleanupMaxWidth, m.contentWidth())
}

func cleanupWrap(text string, width int) string {
	return ansi.Wrap(text, max(1, width), "")
}

func (m model) cleanupConfirmView(width int) string {
	lines := []string{titleStyle.Render("Clean up local resources?")}
	if m.cleanup.TaskTitle != "" {
		title := m.cleanup.TaskTitle
		if !m.cleanupDetails {
			title = truncateLine(title, width)
		}
		lines = append(lines, title)
	}

	// Risks belong above the resource summary, never behind the details toggle.
	// Keep provider messages intact: an unavailable check is not proof of data loss.
	seen := map[string]bool{}
	for _, target := range m.cleanup.Targets {
		for _, safety := range target.Safety {
			if !safety.BlocksAutomatic {
				continue
			}
			message := cleanupSafetyMessage(safety.Message)
			if message == "" || seen[message] {
				continue
			}
			if len(seen) == 0 {
				lines = append(lines, "")
			}
			seen[message] = true
			lines = append(lines, attentionStyle.Render("⚠ "+message))
		}
	}

	lines = append(lines, "", titleStyle.Render("REMOVE"))
	if len(m.cleanup.Targets) == 0 {
		lines = append(lines, "  No local resources to remove.")
	} else if m.cleanupDetails {
		for i, target := range m.cleanup.Targets {
			if i > 0 {
				lines = append(lines, "")
			}
			lines = append(lines, cleanupTargetDetails(target)...)
		}
	} else {
		lines = append(lines, cleanupResourceSummary(m.cleanup.Targets, width)...)
	}
	lines = append(lines, "", titleStyle.Render("KEEP"), "  Remote resources remain unchanged.")
	return cleanupWrap(strings.Join(lines, "\n"), width)
}

type cleanupResourceGroup struct {
	presentation protocol.CleanupPresentation
	targets      []protocol.CleanupTarget
}

func cleanupResourceSummary(targets []protocol.CleanupTarget, width int) []string {
	var groups []cleanupResourceGroup
	for _, target := range targets {
		index := -1
		for i, group := range groups {
			if group.presentation.Singular == target.Presentation.Singular && group.presentation.Plural == target.Presentation.Plural {
				index = i
				break
			}
		}
		if index == -1 {
			index = len(groups)
			groups = append(groups, cleanupResourceGroup{presentation: target.Presentation})
		}
		groups[index].targets = append(groups[index].targets, target)
	}

	var lines, compact []string
	for _, group := range groups {
		label := group.presentation.Plural
		if len(group.targets) == 1 {
			label = group.presentation.Singular
		}
		count := fmt.Sprintf("%d %s", len(group.targets), label)
		individual := false
		for _, target := range group.targets {
			individual = individual || target.Presentation.Label != "" || len(target.Safety) > 0
		}
		if !individual {
			compact = append(compact, count)
			continue
		}
		lines = append(lines, "  "+count)
		for _, target := range group.targets {
			label := target.Presentation.Label
			if label == "" {
				label = target.Title
			}
			if label == "" {
				label = target.ResourceID
			}
			if target.Presentation.Detail != "" {
				label += " · " + target.Presentation.Detail
			}
			lines = append(lines, "    "+truncateLine(label, max(1, width-4)))
			var notices []string
			for _, safety := range target.Safety {
				if safety.BlocksAutomatic {
					// These short markers identify affected resources; the complete
					// warning is always shown above, even in the compact view.
					marker := "⚠ see warning above"
					switch safety.Kind {
					case "local_changes":
						marker = "⚠ uncommitted changes"
					case "unpublished_data":
						marker = "⚠ remote backup unverified"
					case "safety_check_unavailable":
						marker = "⚠ safety check unavailable"
					}
					notices = append(notices, attentionStyle.Render(marker))
				} else {
					message := safety.Summary
					if message == "" {
						message = safety.Message
					}
					if message != "" {
						notices = append(notices, message)
					}
				}
			}
			if len(notices) > 0 {
				lines = append(lines, "      "+strings.Join(notices, " · "))
			}
		}
	}
	if len(compact) > 0 {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, "  "+strings.Join(compact, " · "))
	}
	return lines
}

func cleanupTargetDetails(target protocol.CleanupTarget) []string {
	label := target.Description
	if label == "" {
		label = target.Title
	}
	if label == "" {
		label = target.Presentation.Singular
	}
	lines := []string{"  " + label}
	for _, field := range []struct{ name, value string }{
		{"Path", target.Path}, {"Branch", target.Branch}, {"Resource", target.ResourceID},
	} {
		if field.value != "" && !strings.Contains(label, field.value) && (field.name != "Resource" || field.value != target.Path) {
			lines = append(lines, "    "+field.name+": "+field.value)
		}
	}
	for _, safety := range target.Safety {
		if safety.Message != "" && !strings.Contains(label, safety.Message) {
			lines = append(lines, "    "+cleanupSafetyMessage(safety.Message))
		}
	}
	return lines
}

func (m model) cleanupFooter(width int) string {
	details := "[d] Show full paths and branch names"
	if m.cleanupDetails {
		details = "[d] Hide details"
	}
	actions := errorStyle.Render("[y] Clean up") + "    [Esc/n] Cancel    [q] Quit"
	if len(m.cleanup.Targets) == 0 {
		actions = "[Esc/n] Cancel    [q] Quit"
	}
	return cleanupWrap(helpStyle.Render(details)+"\n\n"+actions, width)
}

// Keep the action footer reachable even when paths wrap or a task has many members.
func (m model) cleanupViewport() (lines []string, footer string, rows int) {
	width := m.cleanupWidth()
	lines = strings.Split(m.cleanupConfirmView(width), "\n")
	footer = m.cleanupFooter(width)
	rows = len(lines)
	if m.height > 0 {
		rows = max(1, m.height-m.frameHeight()-lipgloss.Height(footer)-2)
	}
	if len(lines) > rows {
		footer = cleanupWrap(helpStyle.Render("↑/↓ Scroll · PgUp/PgDn Page"), width) + "\n" + footer
		if m.height > 0 {
			rows = max(1, m.height-m.frameHeight()-lipgloss.Height(footer)-2)
		}
	}
	return lines, footer, rows
}

func (m *model) scrollCleanup(key string) {
	lines, _, rows := m.cleanupViewport()
	limit := max(0, len(lines)-rows)
	m.cleanupScroll = min(m.cleanupScroll, limit)
	switch key {
	case "j", "down", "ctrl+n":
		m.cleanupScroll++
	case "k", "up", "ctrl+p":
		m.cleanupScroll--
	case "pgdown", "ctrl+d":
		m.cleanupScroll += rows
	case "pgup", "ctrl+u":
		m.cleanupScroll -= rows
	case "home":
		m.cleanupScroll = 0
	case "end":
		m.cleanupScroll = limit
	}
	m.cleanupScroll = max(0, min(m.cleanupScroll, limit))
}

func (m model) cleanupScreen() string {
	lines, footer, rows := m.cleanupViewport()
	offset := max(0, min(m.cleanupScroll, len(lines)-min(rows, len(lines))))
	content := strings.Join(lines[offset:min(len(lines), offset+rows)], "\n") + "\n\n" + footer
	return m.renderFrame(content, m.cleanupWidth())
}
