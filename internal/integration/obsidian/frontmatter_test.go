package obsidian

import (
	"reflect"
	"strings"
	"testing"

	"radar/internal/protocol"
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

func TestSurgicalWriterMultilineBindingsCRLFAndMultipleOptionalInsertions(t *testing.T) {
	bindings := []protocol.SourceBinding{{Source: "tmux", Kind: "session", ID: "tmux:session:name#1", Key: "tmux:$7:created", WorkItem: false}, {Source: "github", Kind: "pr", ID: "github:pr:acme/app:7", WorkItem: true}}
	for _, newline := range []string{"\n", "\r\n"} {
		for _, existing := range []bool{false, true} {
			t.Run(strings.ReplaceAll(newline, "\r", "CR")+map[bool]string{false: "insert", true: "replace"}[existing], func(t *testing.T) {
				prefs := ""
				if existing {
					prefs = "radar-muted: false # keep preference comment\nradar-source-refs: # keep binding comment\n  - source: jira\n    kind: issue\n    id: jira:issue:ABC-123\n    work_item: true\n# user separator\n\n"
				}
				original := strings.ReplaceAll(bindingTestHeader+prefs+"user: &user\n  multiline: |-\n    radar-muted: false\n    radar-source-refs: not a managed field\n  id: {nested: unchanged}\nother: *user\n# trailing user comment\n---\n", "\n", newline) + "\r\nBody: keep mixed newline bytes.\nNo final newline"
				current, err := parseNote(original)
				if err != nil {
					t.Fatal(err)
				}
				content, updated, err := updateNoteContent(current, map[string]string{
					"radar-muted": "true", "radar-source-refs": encodeBindings(bindings),
					"radar-completion-baseline": "pending", "radar-priority": "urgent",
				})
				if err != nil {
					t.Fatal(err)
				}
				if !updated.Muted || !reflect.DeepEqual(updated.Bindings, bindings) || updated.Priority != "urgent" || updated.CompletionBaseline != "pending" {
					t.Fatalf("updated fields = %+v", updated)
				}
				unknown := original[strings.Index(original, "user: &user"):strings.LastIndex(original, "---"+newline)]
				body := original[strings.LastIndex(original, "---"+newline):]
				if !strings.Contains(content, unknown) || !strings.HasSuffix(content, body) {
					t.Fatalf("unknown YAML/body changed:\n%s", content)
				}
				if existing && (!strings.Contains(content, "# keep preference comment"+newline) || !strings.Contains(content, "# keep binding comment"+newline) || !strings.Contains(content, "# user separator"+newline+newline)) {
					t.Fatalf("comments/whitespace changed:\n%s", content)
				}
				frontmatter := content[:strings.Index(content, "---"+newline+"\r\nBody")]
				if newline == "\r\n" && strings.Contains(strings.ReplaceAll(frontmatter, "\r\n", ""), "\n") {
					t.Fatal("writer introduced LF into CRLF frontmatter")
				}
			})
		}
	}
}

func TestSurgicalWriterReplacesUnindentedAndFlowBindingSequences(t *testing.T) {
	for _, field := range []string{
		"radar-source-refs:\n- source: jira\n  kind: issue\n  id: jira:issue:ABC-123\n  work_item: true\n",
		"radar-source-refs: [{source: jira, kind: issue, id: jira:issue:ABC-123, work_item: true}] # preserve\n",
	} {
		original := bindingTestHeader + field + "unknown: >-\n  user text\n\n# user comment\n---\nBody"
		current, err := parseNote(original)
		if err != nil {
			t.Fatal(err)
		}
		updated, parsed, err := updateNoteContent(current, map[string]string{"radar-source-refs": encodeBindings([]protocol.SourceBinding{{Source: "git", Kind: "worktree", ID: "git:/path", Key: "git:/path:created"}}), "radar-muted": "true"})
		if err != nil || !parsed.Muted || len(parsed.Bindings) != 1 || !strings.Contains(updated, "unknown: >-\n  user text\n\n# user comment\n") || !strings.HasSuffix(updated, "---\nBody") {
			t.Fatalf("replaced sequence = %s, %v", updated, err)
		}
	}
}

func TestBindingKeyDistinctLifetimesValidate(t *testing.T) {
	bindings := []protocol.SourceBinding{{Source: "git", Kind: "worktree", ID: "git:/path", Key: "git:/path:old"}, {Source: "git", Kind: "worktree", ID: "git:/path", Key: "git:/path:new"}}
	current, err := parseNote(bindingTestHeader + "radar-source-refs:" + encodeBindings(bindings) + "\n---\n")
	if err != nil || !reflect.DeepEqual(current.Bindings, bindings) || bindings[0].LinkingKey() == bindings[1].LinkingKey() {
		t.Fatalf("distinct lifetimes = %+v, %v", current, err)
	}
}

func TestSurgicalWriterSemanticNoopPreservesFormatting(t *testing.T) {
	content := strings.Replace(bindingTestHeader, "radar-priority: normal", "radar-priority: 'normal' # user comment", 1)
	content += "radar-muted: TRUE # comment\nradar-source-refs: [{source: jira, kind: issue, id: ABC-123, work_item: TRUE}] # binding comment\nradar-completion-baseline: 'pending'\n---\nBody\r\n"
	current, err := parseNote(content)
	if err != nil {
		t.Fatal(err)
	}
	updated, _, err := updateNoteContent(current, map[string]string{
		"radar-priority": "normal", "radar-muted": "true",
		"radar-source-refs": encodeBindings(current.Bindings), "radar-completion-baseline": "pending",
	})
	if err != nil || updated != content {
		t.Fatalf("no-op normalized user formatting:\n%s, %v", updated, err)
	}
}

func TestUnknownNumericAndMergeFieldsRemainBytePreserved(t *testing.T) {
	unknown := "defaults: &defaults\n  color: blue\n<<: *defaults\n123: numeric key\n"
	content := bindingTestHeader + unknown + "---\nBody"
	current, err := parseNote(content)
	if err != nil {
		t.Fatal(err)
	}
	updated, _, err := updateNoteContent(current, map[string]string{"radar-muted": "true"})
	if err != nil || !strings.Contains(updated, unknown) || !strings.HasSuffix(updated, "---\nBody") {
		t.Fatalf("unknown fields changed:\n%s, %v", updated, err)
	}
}

func TestUserBlockScalarContainingDelimiterIsNotFrontmatterEnd(t *testing.T) {
	unknown := "user: |-\n  ---\n  radar-muted: false\n  ---\n"
	content := bindingTestHeader + unknown + "---\nUser body"
	current, err := parseNote(content)
	if err != nil {
		t.Fatal(err)
	}
	updated, _, err := updateNoteContent(current, map[string]string{"radar-muted": "true"})
	if err != nil || !strings.Contains(updated, unknown) || !strings.HasSuffix(updated, "---\nUser body") {
		t.Fatalf("user scalar changed:\n%s, %v", updated, err)
	}
}
