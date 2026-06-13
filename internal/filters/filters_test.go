package filters

import (
	"testing"

	"radar.nvim/internal/protocol"
)

func TestApplyMutesRepos(t *testing.T) {
	items := []protocol.Task{
		{ID: 1, Repo: "org/noisy", Attention: "attention"},
		{ID: 2, Repo: "org/useful", Attention: "attention"},
	}

	got := Apply(items, Config{MuteRepos: []string{"ORG/NOISY"}})
	if len(got) != 1 || got[0].ID != 2 {
		t.Fatalf("expected only useful item, got %#v", got)
	}
}

func TestApplyDeprioritizesRepos(t *testing.T) {
	items := []protocol.Task{{ID: 1, Repo: "org/noisy", Attention: "attention", Reason: "review requested"}}

	got := Apply(items, Config{DeprioritizeRepos: []string{"org/noisy"}})
	if len(got) != 1 {
		t.Fatalf("expected one item, got %d", len(got))
	}
	if got[0].Attention != "low_priority" {
		t.Fatalf("expected low_priority, got %q", got[0].Attention)
	}
	if got[0].Reason != "low priority: review requested" {
		t.Fatalf("unexpected reason %q", got[0].Reason)
	}
}

func TestApplyMatchesUsers(t *testing.T) {
	items := []protocol.Task{
		{ID: 1, Attention: "attention", Metadata: map[string]string{"author": "dependabot[bot]"}},
		{ID: 2, Attention: "attention", Metadata: map[string]string{"author": "person"}},
	}

	got := Apply(items, Config{MuteUsers: []string{"dependabot[bot]"}})
	if len(got) != 1 || got[0].ID != 2 {
		t.Fatalf("expected only person item, got %#v", got)
	}
}

func TestApplyMatchesWildcardRepos(t *testing.T) {
	items := []protocol.Task{
		{ID: 1, Repo: "org/api-backend", Attention: "attention"},
		{ID: 2, Repo: "org/app", Attention: "attention"},
	}

	got := Apply(items, Config{DeprioritizeRepos: []string{"org/api-*"}})
	if got[0].Attention != "low_priority" {
		t.Fatalf("expected wildcard repo to be low priority, got %#v", got[0])
	}
	if got[1].Attention != "attention" {
		t.Fatalf("expected non-matching repo to remain attention, got %#v", got[1])
	}
}

func TestApplyRuleRequiresRepoAndUser(t *testing.T) {
	items := []protocol.Task{
		{ID: 1, Repo: "org/important", Attention: "attention", Metadata: map[string]string{"author": "renovate[bot]"}},
		{ID: 2, Repo: "org/other", Attention: "attention", Metadata: map[string]string{"author": "renovate[bot]"}},
		{ID: 3, Repo: "org/important", Attention: "attention", Metadata: map[string]string{"author": "person"}},
	}

	got := Apply(items, Config{Rules: []Rule{{Repos: []string{"org/important"}, Users: []string{"renovate[bot]"}, Action: "deprioritize"}}})
	if got[0].Attention != "low_priority" {
		t.Fatalf("expected matching repo+user item to be low priority, got %#v", got[0])
	}
	if got[1].Attention != "attention" || got[2].Attention != "attention" {
		t.Fatalf("expected partial matches to remain attention, got %#v", got)
	}
}

func TestApplyLastMatchingRuleWins(t *testing.T) {
	items := []protocol.Task{{ID: 1, Repo: "org/important", Attention: "attention", Metadata: map[string]string{"author": "renovate[bot]"}}}
	cfg := Config{
		MuteUsers: []string{"renovate[bot]"},
		Rules: []Rule{
			{Repos: []string{"org/*"}, Users: []string{"renovate[bot]"}, Action: "deprioritize"},
			{Repos: []string{"org/important"}, Users: []string{"renovate[bot]"}, Action: "keep"},
		},
	}

	got := Apply(items, cfg)
	if len(got) != 1 {
		t.Fatalf("expected keep rule to unmute item, got %#v", got)
	}
	if got[0].Attention != "attention" {
		t.Fatalf("expected keep rule to preserve attention, got %#v", got[0])
	}
}

func TestWildcardMatch(t *testing.T) {
	cases := []struct {
		pattern string
		value   string
		want    bool
	}{
		{"org/*", "org/repo", true},
		{"org/*-frontend", "org/app-frontend", true},
		{"*/repo", "org/repo", true},
		{"org/*", "other/repo", false},
		{"org/*-frontend", "org/frontend-api", false},
		{"ORG/*", "org/repo", true},
	}
	for _, tc := range cases {
		if got := wildcardMatch(tc.pattern, tc.value); got != tc.want {
			t.Fatalf("wildcardMatch(%q, %q) = %v, want %v", tc.pattern, tc.value, got, tc.want)
		}
	}
}

func TestSummaryIncludesLowPriority(t *testing.T) {
	summary := Summary([]protocol.Task{
		{Attention: "attention"},
		{Attention: "low_priority"},
	})
	if summary.Attention != 1 || summary.LowPriority != 1 {
		t.Fatalf("unexpected summary %#v", summary)
	}
}
