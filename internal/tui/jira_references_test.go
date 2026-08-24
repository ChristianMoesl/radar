package tui

import (
	"strings"
	"testing"

	"radar/internal/protocol"
)

func TestInformationalJiraReferenceUsesStableIDAsLabel(t *testing.T) {
	ref := protocol.SourceRef{ID: "jira:mention:4:XYZ-7", Source: "jira", SourceLabel: "Jira", Kind: "issue", Role: protocol.SourceRefRoleInformational}
	if got := sourceRefLabel(ref); got != "jira:mention:4:XYZ-7" {
		t.Fatalf("sourceRefLabel() = %q", got)
	}
}

func TestInformationalJiraReferenceRemainsOpenable(t *testing.T) {
	url := "https://jira.example.test/browse/XYZ-7"
	links := taskLinks(protocol.Task{SourceRefs: []protocol.SourceRef{{
		ID: "jira:mention:4:XYZ-7", Source: "jira", Kind: "issue", Role: protocol.SourceRefRoleInformational, URL: url,
	}}})
	if len(links) != 1 || links[0].URL != url {
		t.Fatalf("links = %+v, want informational Jira URL", links)
	}
}

func TestTaskInspectionShowsJiraReferenceRoleStatusAndMetadata(t *testing.T) {
	m := model{tasks: []protocol.Task{{Title: "Review XYZ-7", SourceRefs: []protocol.SourceRef{{
		ID: "jira:mention:4:XYZ-7", Source: "jira", SourceLabel: "Jira", Kind: "issue", Role: protocol.SourceRefRoleInformational,
		Status: "Open", Metadata: map[string]string{"key": "XYZ-7", "issue_type": "Epic", "priority": "Medium", "status_category": "new"},
	}}}}}
	got := m.detailView(100)
	for _, want := range []string{"jira:mention:4:XYZ-7", "informational", "Open", "XYZ-7", "Epic", "Medium", "new"} {
		if !strings.Contains(got, want) {
			t.Fatalf("task detail missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, "Jira reference:") {
		t.Fatalf("task detail includes redundant Jira reference label: %s", got)
	}
}
