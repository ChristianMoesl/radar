package collector

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"radar/internal/protocol"
)

func TestApplyTaskFiltersRemovesMutedTasksBeforeSaving(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	configPath := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "radar", "config.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`{"filters":{"mute_repos":["org/noisy"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	tasks := []protocol.Task{
		{ID: 1, Repo: "org/noisy", Attention: "attention"},
		{ID: 2, Repo: "org/useful", Attention: "attention"},
	}

	got := applyTaskFilters(tasks, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if len(got) != 1 || got[0].ID != 2 {
		t.Fatalf("filtered tasks = %+v, want only useful task", got)
	}
}

func TestDeduplicateReconciledTasksKeepsOneTaskPerGitHubPullRequest(t *testing.T) {
	tasks := []protocol.Task{
		{
			Title:     "Ship panel details",
			Attention: "done",
			SourceRefs: []protocol.SourceRef{
				{ID: "github:pr:acme/app:7", Source: "github", Kind: "pull_request"},
				{ID: "git:worktree:/repo/feature", Source: "git", Kind: "worktree"},
			},
		},
		{
			Title:     "Ship panel details",
			Attention: "done",
			SourceRefs: []protocol.SourceRef{
				{ID: "github:pr:acme/app:7", Source: "github", Kind: "pull_request"},
				{ID: "jira:issue:CAP-7", Source: "jira", Kind: "issue"},
			},
		},
	}

	got := deduplicateReconciledTasks(tasks)
	if len(got) != 1 {
		t.Fatalf("deduplicated tasks = %d, want 1: %+v", len(got), got)
	}
	assertSourceRef(t, got[0], "github:pr:acme/app:7")
	assertSourceRef(t, got[0], "git:worktree:/repo/feature")
	assertSourceRef(t, got[0], "jira:issue:CAP-7")
}

func TestDeduplicateReconciledTasksKeepsDifferentGitHubPullRequestsOnSameIssue(t *testing.T) {
	tasks := []protocol.Task{
		{Title: "first", SourceRefs: []protocol.SourceRef{{ID: "github:pr:acme/app:7", Source: "github", Kind: "pull_request"}, {ID: "jira:issue:CAP-7", Source: "jira", Kind: "issue"}}},
		{Title: "second", SourceRefs: []protocol.SourceRef{{ID: "github:pr:acme/app:8", Source: "github", Kind: "pull_request"}, {ID: "jira:issue:CAP-7", Source: "jira", Kind: "issue"}}},
	}

	got := deduplicateReconciledTasks(tasks)
	if len(got) != 2 {
		t.Fatalf("deduplicated tasks = %d, want 2: %+v", len(got), got)
	}
}

func assertSourceRef(t *testing.T, task protocol.Task, id string) {
	t.Helper()
	for _, sourceRef := range task.SourceRefs {
		if sourceRef.ID == id {
			return
		}
	}
	t.Fatalf("task missing source ref %q: %+v", id, task.SourceRefs)
}
