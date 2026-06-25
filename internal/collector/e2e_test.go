package collector

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"radar/internal/app"
	"radar/internal/protocol"
	"radar/internal/state"
)

func TestCollectEndToEndIngestsLinksAndMarksGitHubPRDone(t *testing.T) {
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	tmp := t.TempDir()

	setupIsolatedEnvironment(t, tmp)
	setupFakeGitHubCLI(t, tmp)
	jiraServer := setupFakeJira(t)
	setupGitWorktree(t, ctx, tmp)

	t.Setenv("RADAR_JIRA_API_BASE_URL", jiraServer.URL)
	t.Setenv("RADAR_JIRA_BASE_URL", "https://jira.example.test")
	t.Setenv("RADAR_JIRA_EMAIL", "radar@example.test")
	t.Setenv("RADAR_JIRA_API_TOKEN", "token")

	store, err := state.NewStore(logger)
	if err != nil {
		t.Fatal(err)
	}

	integrations := app.DefaultIntegrationSet()
	first := Collect(ctx, nil, logger, integrations.Sources)
	store.SetTasks(first.Tasks)
	firstTasks := store.Tasks()
	active := taskBySourceRef(firstTasks, "github:pr:acme/app:7")
	if active == nil {
		t.Fatalf("first collect did not ingest GitHub PR; tasks=%+v", firstTasks)
	}
	if active.Kind != "github_own_pr" || active.Attention != "in_progress" {
		t.Fatalf("github task = %s/%s, want github_own_pr/in_progress", active.Kind, active.Attention)
	}
	assertHasSourceRef(t, *active, "github:pr:acme/app:7")
	assertHasSourceRef(t, *active, "jira:issue:RAD-123")
	assertHasSourceRefPrefix(t, *active, "git:worktree:")

	if sourceStatus(first.Sources, "jira").SourceRefCount != 1 {
		t.Fatalf("jira source status = %+v, want one source ref", sourceStatus(first.Sources, "jira"))
	}
	if sourceStatus(first.Sources, "git").SourceRefCount < 1 {
		t.Fatalf("git source status = %+v, want at least one source ref", sourceStatus(first.Sources, "git"))
	}

	if err := os.WriteFile(filepath.Join(tmp, "gh-mode"), []byte("closed"), 0o600); err != nil {
		t.Fatal(err)
	}
	second := Collect(ctx, firstTasks, logger, integrations.Sources)
	store.SetTasks(second.Tasks)
	secondTasks := store.Tasks()
	done := taskBySourceRef(secondTasks, "github:pr:acme/app:7")
	if done == nil {
		t.Fatalf("second collect did not keep closed GitHub PR; tasks=%+v", secondTasks)
	}
	if done.Kind != "github_own_pr" || done.Attention != "done" || done.Reason != "merged today" {
		t.Fatalf("done task = %s/%s/%s, want github_own_pr/done/merged today", done.Kind, done.Attention, done.Reason)
	}
	assertHasSourceRef(t, *done, "jira:issue:RAD-123")
	assertHasSourceRefPrefix(t, *done, "git:worktree:")
}

func setupIsolatedEnvironment(t *testing.T, tmp string) {
	t.Helper()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(tmp, "state"))
	t.Setenv("TMUX", "")
}

func setupFakeGitHubCLI(t *testing.T, tmp string) {
	t.Helper()
	bin := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	modeFile := filepath.Join(tmp, "gh-mode")
	if err := os.WriteFile(modeFile, []byte("open"), 0o600); err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf(`#!/bin/sh
set -eu
mode=$(cat %q)
if [ "$1" = "api" ] && [ "$2" = "rate_limit" ]; then
  cat <<'JSON'
{"resources":{"core":{"limit":5000,"remaining":4999,"reset":4102444800},"search":{"limit":30,"remaining":30,"reset":4102444800},"graphql":{"limit":5000,"remaining":4999,"reset":4102444800}}}
JSON
  exit 0
fi
if [ "$1" = "api" ] && [ "$2" = "user" ]; then
  printf '{"login":"octo"}\n'
  exit 0
fi
if [ "$1" = "api" ] && [ "$2" = "graphql" ]; then
  if [ "$mode" = "open" ]; then
    cat <<'JSON'
{"data":{"reviewRequested":{"nodes":[]},"authored":{"nodes":[{"number":7,"title":"RAD-123 Ship linked work","url":"https://github.com/acme/app/pull/7","state":"OPEN","isDraft":false,"headRefName":"feature/RAD-123-linked-work","body":"Implements RAD-123","author":{"login":"octo"},"repository":{"nameWithOwner":"acme/app"},"comments":{"nodes":[]},"reviews":{"nodes":[]},"reviewThreads":{"nodes":[]}}]},"participated":{"nodes":[]}}}
JSON
  else
    cat <<'JSON'
{"data":{"reviewRequested":{"nodes":[]},"authored":{"nodes":[]},"participated":{"nodes":[]}}}
JSON
  fi
  exit 0
fi
if [ "$1" = "api" ] && [ "$2" = "/repos/acme/app/pulls/7" ]; then
  printf '{"html_url":"https://github.com/acme/app/pull/7","state":"closed","merged":true,"closed_at":"%%s","requested_reviewers":[]}\n' "$(date -u +%%Y-%%m-%%dT12:00:00Z)"
  exit 0
fi
echo "unexpected gh args: $*" >&2
exit 1
`, modeFile)
	path := filepath.Join(bin, "gh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func setupFakeJira(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search/jql" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"issues":[{"id":"10001","key":"RAD-123","fields":{"summary":"Ship linked work","issuetype":{"name":"Story"},"priority":{"name":"High"},"status":{"name":"In Progress","statusCategory":{"key":"indeterminate","name":"In Progress"}}}}]}`))
	}))
	t.Cleanup(server.Close)
	return server
}

func setupGitWorktree(t *testing.T, ctx context.Context, tmp string) {
	t.Helper()
	repo := filepath.Join(tmp, "repo")
	wt := filepath.Join(tmp, "repo-rad-123")
	runGit(t, ctx, tmp, "init", repo)
	runGit(t, ctx, repo, "config", "user.email", "radar@example.test")
	runGit(t, ctx, repo, "config", "user.name", "Radar Test")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, ctx, repo, "add", "README.md")
	runGit(t, ctx, repo, "commit", "-m", "initial")
	runGit(t, ctx, repo, "worktree", "add", "-b", "feature/RAD-123-linked-work", wt)
	t.Setenv("RADAR_GIT_REPOS", repo)
}

func runGit(t *testing.T, ctx context.Context, dir string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, output)
	}
}

func taskBySourceRef(tasks []protocol.Task, id string) *protocol.Task {
	for i := range tasks {
		for _, ref := range tasks[i].SourceRefs {
			if ref.ID == id {
				return &tasks[i]
			}
		}
	}
	return nil
}

func assertHasSourceRef(t *testing.T, task protocol.Task, id string) {
	t.Helper()
	for _, ref := range task.SourceRefs {
		if ref.ID == id {
			return
		}
	}
	t.Fatalf("task %q missing source ref %q; refs=%+v", task.Title, id, task.SourceRefs)
}

func assertHasSourceRefPrefix(t *testing.T, task protocol.Task, prefix string) {
	t.Helper()
	for _, ref := range task.SourceRefs {
		if strings.HasPrefix(ref.ID, prefix) {
			return
		}
	}
	t.Fatalf("task %q missing source ref prefix %q; refs=%+v", task.Title, prefix, task.SourceRefs)
}

func sourceStatus(statuses []protocol.SourceStatus, name string) protocol.SourceStatus {
	for _, status := range statuses {
		if status.Name == name {
			return status
		}
	}
	return protocol.SourceStatus{}
}
