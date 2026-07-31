package tui

import (
	"strings"
	"testing"

	"radar/internal/protocol"
)

func TestInformationalJiraReferenceLabel(t *testing.T) {
	ref := protocol.SourceRef{ID: "jira:mention:4:RAD-7", Source: "jira", Kind: "issue", Role: protocol.SourceRefRoleInformational}
	if got := sourceRefLabel(ref); got != "Jira reference: jira:mention:4:RAD-7" {
		t.Fatalf("sourceRefLabel() = %q", got)
	}
}

func TestTaskInspectionShowsJiraReferenceRoleStatusAndMetadata(t *testing.T) {
	m := model{tasks: []protocol.Task{{Title: "Review RAD-7", SourceRefs: []protocol.SourceRef{{
		ID: "jira:mention:4:RAD-7", Source: "jira", Kind: "issue", Role: protocol.SourceRefRoleInformational,
		Status: "Open", Metadata: map[string]string{"key": "RAD-7", "issue_type": "Epic", "priority": "Medium", "status_category": "new"},
	}}}}}
	got := m.detailView(100)
	for _, want := range []string{"Jira reference:", "informational", "Open", "RAD-7", "Epic", "Medium", "new"} {
		if !strings.Contains(got, want) {
			t.Fatalf("task detail missing %q: %s", want, got)
		}
	}
}
