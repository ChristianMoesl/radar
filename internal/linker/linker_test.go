package linker

import (
	"testing"

	"radar.nvim/internal/protocol"
)

func TestLinkMatchesTicketKeysCaseInsensitivelyInBranch(t *testing.T) {
	items := []protocol.Task{
		{
			Kind:      "github_own_pr",
			Title:     "Implement feature",
			Attention: "in_progress",
			SourceRefs: []protocol.SourceRef{
				{
					Source: "github",
					Kind:   "pull_request",
					Branch: "feature/dpscap-544-panel-deletion-navigation",
				},
			},
		},
	}
	source_refs := []protocol.SourceRef{
		{
			ID:     "jira:issue:DPSCAP-544",
			Source: "jira",
			Kind:   "issue",
			Title:  "DPSCAP-544 Fix navigation",
		},
	}

	linked := Link(Input{Tasks: items, SourceRefs: source_refs})

	if len(linked) != 1 {
		t.Fatalf("linked item count = %d, want 1", len(linked))
	}
	if len(linked[0].SourceRefs) != 2 {
		t.Fatalf("linked sourceRef count = %d, want 2: %+v", len(linked[0].SourceRefs), linked[0].SourceRefs)
	}
	if linked[0].SourceRefs[1].ID != "jira:issue:DPSCAP-544" {
		t.Fatalf("attached sourceRef = %q, want jira issue", linked[0].SourceRefs[1].ID)
	}
}

func TestLinkMatchesTmuxSessionToWorktreeByPath(t *testing.T) {
	items := []protocol.Task{
		{
			Kind:      "git_worktree",
			Title:     "feature/no-ticket",
			Attention: "in_progress",
			SourceRefs: []protocol.SourceRef{
				{
					ID:     "git:worktree:/home/me/repo",
					Source: "git",
					Kind:   "worktree",
					Path:   "/home/me/repo",
				},
			},
		},
	}
	sourceRefs := []protocol.SourceRef{
		{
			ID:     "tmux:session:$1",
			Source: "tmux",
			Kind:   "session",
			Title:  "scratch",
			Path:   "/home/me/repo/subdir",
		},
	}

	linked := Link(Input{Tasks: items, SourceRefs: sourceRefs})

	if len(linked) != 1 {
		t.Fatalf("linked item count = %d, want 1", len(linked))
	}
	if len(linked[0].SourceRefs) != 2 {
		t.Fatalf("linked sourceRef count = %d, want 2: %+v", len(linked[0].SourceRefs), linked[0].SourceRefs)
	}
	if linked[0].SourceRefs[1].ID != "tmux:session:$1" {
		t.Fatalf("attached sourceRef = %q, want tmux session", linked[0].SourceRefs[1].ID)
	}
}

func TestLinkGroupsStandaloneWorktreeAndTmuxSessionByPath(t *testing.T) {
	sourceRefs := []protocol.SourceRef{
		{
			ID:     "git:worktree:/home/me/repo",
			Source: "git",
			Kind:   "worktree",
			Title:  "feature/no-ticket",
			Branch: "feature/no-ticket",
			Path:   "/home/me/repo",
		},
		{
			ID:     "tmux:session:$1",
			Source: "tmux",
			Kind:   "session",
			Title:  "scratch",
			Path:   "/home/me/repo/subdir",
		},
	}

	linked := Link(Input{SourceRefs: sourceRefs})

	if len(linked) != 1 {
		t.Fatalf("linked item count = %d, want 1", len(linked))
	}
	if len(linked[0].SourceRefs) != 2 {
		t.Fatalf("linked sourceRef count = %d, want 2: %+v", len(linked[0].SourceRefs), linked[0].SourceRefs)
	}
}

func TestLinkExtractsTicketKeysFromAnyMetadataValue(t *testing.T) {
	items := []protocol.Task{
		{
			Kind:      "github_own_pr",
			Title:     "Implement feature",
			Attention: "in_progress",
			SourceRefs: []protocol.SourceRef{
				{
					Source:   "github",
					Kind:     "pull_request",
					Metadata: map[string]string{"custom_reference": "related to cap-123"},
				},
			},
		},
	}
	source_refs := []protocol.SourceRef{
		{
			ID:       "jira:issue:CAP-123",
			Source:   "jira",
			Kind:     "issue",
			Metadata: map[string]string{"key": "CAP-123"},
		},
	}

	linked := Link(Input{Tasks: items, SourceRefs: source_refs})

	if len(linked) != 1 {
		t.Fatalf("linked item count = %d, want 1", len(linked))
	}
	if len(linked[0].SourceRefs) != 2 {
		t.Fatalf("linked sourceRef count = %d, want 2: %+v", len(linked[0].SourceRefs), linked[0].SourceRefs)
	}
}
