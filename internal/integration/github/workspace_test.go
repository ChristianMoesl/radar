package github

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"radar/internal/protocol"
	"radar/internal/integration/workspace"
)

type pullRequestHeadResult struct {
	output string
	err    error
}

type pullRequestHeadRunner struct {
	results map[string]pullRequestHeadResult
	calls   []string
}

func (runner *pullRequestHeadRunner) LookPath(string) error { return nil }

func (runner *pullRequestHeadRunner) Run(_ context.Context, _ string, name string, args ...string) (string, error) {
	command := name + " " + strings.Join(args, " ")
	runner.calls = append(runner.calls, command)
	result, ok := runner.results[command]
	if !ok {
		return "", fmt.Errorf("unexpected command %q", command)
	}
	return result.output, result.err
}

func TestEnsurePullRequestHeadBranchCreatesLocalBranchForFork(t *testing.T) {
	notFound := errors.New("not found")
	runner := &pullRequestHeadRunner{results: map[string]pullRequestHeadResult{
		"git show-ref --verify --quiet refs/heads/feature":            {err: notFound},
		"git show-ref --verify --quiet refs/remotes/origin/feature":   {},
		"git fetch --no-tags origin refs/pull/7/head":                 {},
		"git rev-parse --verify FETCH_HEAD^{commit}":                  {output: "fork-commit\n"},
		"git rev-parse --verify refs/remotes/origin/feature^{commit}": {output: "origin-commit\n"},
		"git branch -- feature fork-commit":                           {},
	}}

	warning, err := ensurePullRequestHeadBranch(context.Background(), runner, "/repo", "feature", "7")
	if err != nil || warning != "" {
		t.Fatalf("ensurePullRequestHeadBranch() warning=%q error=%v", warning, err)
	}
	if got := runner.calls[len(runner.calls)-1]; got != "git branch -- feature fork-commit" {
		t.Fatalf("last command = %q, want local branch creation", got)
	}
}

func TestEnsurePullRequestHeadBranchUsesMatchingOriginBranch(t *testing.T) {
	notFound := errors.New("not found")
	runner := &pullRequestHeadRunner{results: map[string]pullRequestHeadResult{
		"git show-ref --verify --quiet refs/heads/feature":            {err: notFound},
		"git show-ref --verify --quiet refs/remotes/origin/feature":   {},
		"git fetch --no-tags origin refs/pull/7/head":                 {},
		"git rev-parse --verify FETCH_HEAD^{commit}":                  {output: "same-commit\n"},
		"git rev-parse --verify refs/remotes/origin/feature^{commit}": {output: "same-commit\n"},
	}}

	if warning, err := ensurePullRequestHeadBranch(context.Background(), runner, "/repo", "feature", "7"); err != nil || warning != "" {
		t.Fatalf("ensurePullRequestHeadBranch() warning=%q error=%v", warning, err)
	}
	for _, call := range runner.calls {
		if strings.HasPrefix(call, "git branch ") {
			t.Fatalf("matching origin branch unexpectedly created local branch: %q", call)
		}
	}
}

func TestEnsurePullRequestHeadBranchRejectsUnavailableHead(t *testing.T) {
	notFound := errors.New("not found")
	offline := errors.New("network is offline")
	runner := &pullRequestHeadRunner{results: map[string]pullRequestHeadResult{
		"git show-ref --verify --quiet refs/heads/feature":          {err: notFound},
		"git show-ref --verify --quiet refs/remotes/origin/feature": {err: notFound},
		"git fetch --no-tags origin refs/pull/7/head":               {err: offline},
	}}

	_, err := ensurePullRequestHeadBranch(context.Background(), runner, "/repo", "feature", "7")
	if err == nil || !strings.Contains(err.Error(), "unavailable on origin") {
		t.Fatalf("ensurePullRequestHeadBranch() error=%v, want unavailable branch error", err)
	}
}

func TestParsePullRequestRefKeepsRepositoryColons(t *testing.T) {
	repo, number, ok := parsePullRequestRef(protocol.SourceRef{ID: "github:pr:enterprise:owner/repo:7"})
	if !ok || repo != "enterprise:owner/repo" || number != 7 {
		t.Fatalf("parsePullRequestRef() = %q, %d, %t", repo, number, ok)
	}
}

var _ workspace.Runner = (*pullRequestHeadRunner)(nil)
