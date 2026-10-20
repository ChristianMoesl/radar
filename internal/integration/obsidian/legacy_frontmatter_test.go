package obsidian

import (
	"testing"
)

const bindingTestHeader = "---\nradar-id: 12345678-1234-4234-8234-123456789abc\nradar-title: Plan\nradar-state: open\nradar-priority: normal\nradar-created-at: 2026-08-01T10:00:00Z\nradar-completed-at:\n"

func TestPreferenceFrontmatterValidation(t *testing.T) {
	for _, fields := range []string{
		"", "radar-muted: true\n", "radar-muted: false\n", "radar-muted: TRUE\n",
		"radar-source-refs: []\n",
		"radar-source-refs:\n  - source: jira\n    kind: issue\n    id: jira:issue:ABC-123\n    work_item: true\n",
		"radar-source-refs:\n- {source: tmux, kind: session, id: 'tmux:session:a#b', key: 'tmux:$7:1234', work_item: false}\n",
	} {
		if _, err := parseNote(bindingTestHeader + fields + "---\nbody"); err != nil {
			t.Errorf("valid fields %q: %v", fields, err)
		}
	}
	for _, fields := range []string{
		"radar-muted:\n", "radar-muted: null\n", "radar-muted: 'true'\n", "radar-muted: 1\n", "radar-muted: []\n", "radar-muted: false\nradar-muted: true\n",
		"radar-source-refs:\n", "radar-source-refs: {}\n", "radar-source-refs: true\n", "radar-source-refs: [jira:issue:ABC-123]\n",
		"radar-source-refs: [{source: jira, kind: issue}]\n",
		"radar-source-refs: [{source: jira, kind: issue, id: null}]\n",
		"radar-source-refs: [{source: jira, kind: issue, id: 123}]\n",
		"radar-source-refs: [{source: jira, kind: issue, id: ' ABC-123'}]\n",
		"radar-source-refs: [{source: jira, kind: issue, id: 'ABC-123', key: ''}]\n",
		"radar-source-refs: [{source: 'jira:issue', kind: issue, id: ABC-123}]\n",
		"radar-source-refs: [{source: jira, kind: 'issue:pr', id: ABC-123}]\n",
		"radar-source-refs: [{source: jira, kind: issue, id: \"a\\nb\"}]\n",
		"radar-source-refs: [{source: jira, kind: issue, id: ABC-123, work_item: 'true'}]\n",
		"radar-source-refs: [{source: jira, kind: issue, id: ABC-123, work_item: null}]\n",
		"radar-source-refs: [{source: jira, kind: issue, id: ABC-123, status: done}]\n",
		"radar-source-refs: [{source: jira, kind: issue, id: ABC-123, id: ABC-456}]\n",
		"radar-source-refs: [{source: jira, kind: issue, id: ABC-123}, {source: jira, kind: issue, id: ABC-123}]\n",
		"radar-source-refs: [{source: tmux, kind: session, id: old, key: same}, {source: tmux, kind: session, id: new, key: same}]\n",
		"user-binding: &binding {source: jira, kind: issue, id: ABC-123}\nradar-source-refs: [*binding]\n",
		"user-muted: &muted true\nradar-muted: *muted\n",
	} {
		if _, err := parseNote(bindingTestHeader + fields + "---\n"); err == nil {
			t.Errorf("invalid fields accepted: %q", fields)
		}
	}
}
