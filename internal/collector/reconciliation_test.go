package collector

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"radar/internal/integration"
	"radar/internal/protocol"
)

func TestLocalSourcesComeFromSourceDeclarations(t *testing.T) {
	sources := []integration.Source{fakeSource{name: "remote"}, fakeLocalSource{name: "local-a"}, fakeLocalSource{name: "local-b"}}
	got := sourceNames(LocalSources(sources))
	want := []string{"local-a", "local-b"}
	if len(got) != len(want) {
		t.Fatalf("local sources = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("local sources = %+v, want %+v", got, want)
		}
	}
}

func sourceNames(sources []integration.Source) []string {
	names := make([]string, 0, len(sources))
	for _, source := range sources {
		names = append(names, source.Name())
	}
	return names
}

type fakeSource struct{ name string }

func (s fakeSource) Name() string { return s.name }
func (s fakeSource) Collect(context.Context, integration.CollectRequest) integration.CollectResult {
	return integration.CollectResult{}
}

type fakeLocalSource struct{ name string }

func (s fakeLocalSource) Name() string { return s.name }
func (s fakeLocalSource) Collect(context.Context, integration.CollectRequest) integration.CollectResult {
	return integration.CollectResult{}
}
func (s fakeLocalSource) Local() bool { return true }

func TestCollectReturnsRawTasksWithoutDisplayFilters(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	configPath := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "radar", "config.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`{"filters":{"mute_repos":["org/noisy"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	result := Collect(context.Background(), nil, slog.New(slog.NewTextHandler(io.Discard, nil)), []integration.Source{fakeObservedSource{}})
	if len(result.Tasks) != 1 || result.Tasks[0].Repo != "org/noisy" {
		t.Fatalf("collected tasks = %+v, want raw muted task to remain for storage", result.Tasks)
	}
}

type fakeObservedSource struct{}

func (fakeObservedSource) Name() string { return "fake" }
func (fakeObservedSource) Collect(context.Context, integration.CollectRequest) integration.CollectResult {
	return integration.CollectResult{Observations: []integration.Observation{{
		Ref:    protocol.SourceRef{ID: "github:pr:org/noisy:1", Source: "github", Kind: "pull_request", Repo: "org/noisy", Title: "Noisy PR"},
		Signal: integration.SignalAttention,
		Reason: "review requested",
	}}}
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
