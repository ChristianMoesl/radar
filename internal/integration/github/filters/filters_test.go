package filters

import (
	"radar/internal/protocol"
	"reflect"
	"testing"
)

func pr(id, repo, author, signal string) protocol.SourceRef {
	return protocol.SourceRef{ID: id, Source: "github", Kind: "pull_request", Role: protocol.SourceRefRoleAuthoritative,
		Repo: repo, Signal: signal, Status: "PR feedback", Metadata: map[string]string{"author": author},
		Lifecycle: protocol.SourceRefLifecycleWorkItem, Authority: protocol.SourceRefAuthorityContributing}
}

func TestRulesMatchOnePRWithFirstMatchPrecedence(t *testing.T) {
	rules := []PullRequestRule{
		{Repos: []string{"acme/api"}, Authors: []string{"renovate[bot]"}, Action: "keep"},
		{Authors: []string{"renovate[bot]"}, Action: "mute"},
	}
	for _, test := range []struct {
		repo, author string
		want         protocol.ContributionAction
	}{
		{"ACME/API", "Renovate[bot]", "keep"},
		{"acme/other", "renovate[bot]", "mute"},
		{"acme/other", "human", "keep"},
	} {
		if got := ActionFor(pr("pr", test.repo, test.author, "attention"), rules); got != test.want {
			t.Fatalf("%+v: %s", test, got)
		}
	}
	refs := []protocol.SourceRef{pr("a", "acme/api", "alice", "in_progress"), pr("b", "acme/web", "renovate[bot]", "attention")}
	task := protocol.Task{Attention: "attention", SourceRefs: refs}
	got := Apply([]protocol.Task{task}, []PullRequestRule{{Repos: []string{"acme/api"}, Authors: []string{"renovate[bot]"}, Action: "mute"}})
	if len(got) != 1 || got[0].Attention != "attention" {
		t.Fatalf("matched across different PRs: %+v", got)
	}
}

func TestPolicyOnlyAffectsMatchingPRContribution(t *testing.T) {
	noisy := pr("pr", "acme/noisy", "bot", "attention")
	for _, action := range []protocol.ContributionAction{"mute", "deprioritize"} {
		for _, other := range []protocol.SourceRef{
			{ID: "note", Source: "obsidian", Kind: "task", Role: protocol.SourceRefRoleAuthoritative, Signal: "immediate", Status: "urgent note"},
			{ID: "jira", Source: "jira", Kind: "issue", Role: protocol.SourceRefRoleAuthoritative, Signal: "attention", Status: "Jira needs attention"},
			{ID: "worktree", Source: "git", Kind: "worktree", Role: protocol.SourceRefRoleAuthoritative, Repo: "acme/noisy", Signal: "in_progress", Status: "local work"},
			pr("other-pr", "acme/other", "human", "attention"),
		} {
			task := protocol.Task{Attention: "attention", SourceRefs: []protocol.SourceRef{noisy, other}}
			got := Apply([]protocol.Task{task}, []PullRequestRule{{Repos: []string{"acme/noisy"}, Action: action}})
			if len(got) != 1 || got[0].Attention != other.Signal || got[0].Reason != other.Status || got[0].AttentionSourceRefID != other.ID {
				t.Fatalf("%s affected %s: %+v", action, other.Source, got)
			}
			if !reflect.DeepEqual(got[0].SourceRefs, task.SourceRefs) {
				t.Fatal("policy mutated source truth")
			}
		}
	}
}

func TestLocalAndInformationalRefsDoNotMatchGitHubPolicy(t *testing.T) {
	local := protocol.SourceRef{ID: "local", Source: "git", Kind: "worktree", Role: protocol.SourceRefRoleAuthoritative, Repo: "acme/noisy", Signal: "in_progress"}
	rules := []PullRequestRule{{Repos: []string{"acme/noisy"}, Action: "mute"}}
	if got := Apply([]protocol.Task{{SourceRefs: []protocol.SourceRef{local}}}, rules); len(got) != 1 {
		t.Fatal("local-only task hidden")
	}
	local.Role = protocol.SourceRefRoleInformational
	if got := Apply([]protocol.Task{{SourceRefs: []protocol.SourceRef{local, pr("pr", "acme/noisy", "bot", "attention")}}}, rules); len(got) != 0 {
		t.Fatal("informational ref kept muted PR visible")
	}
}

func TestPRMuteHidesStandaloneActiveAndDoneButPreservesAuthoredMute(t *testing.T) {
	for _, signal := range []string{"attention", "done"} {
		task := protocol.Task{Attention: signal, Reason: "merged", SourceRefs: []protocol.SourceRef{pr("pr", "acme/app", "bot", signal)}}
		if got := Apply([]protocol.Task{task}, []PullRequestRule{{Authors: []string{"bot"}, Action: "mute"}}); len(got) != 0 {
			t.Fatalf("muted PR surfaced: %+v", got)
		}
		got := Apply([]protocol.Task{task}, []PullRequestRule{{Authors: []string{"bot"}, Action: "deprioritize"}})
		if len(got) != 1 {
			t.Fatal("deprioritization hid task")
		}
		if signal == "done" && (got[0].Attention != "done" || got[0].Reason != "merged") {
			t.Fatal("deprioritization reopened done task")
		}
	}
	task := protocol.Task{Muted: true, Attention: "attention", SourceRefs: []protocol.SourceRef{pr("pr", "acme/app", "bot", "attention"),
		{ID: "note", Source: "obsidian", Role: protocol.SourceRefRoleAuthoritative, Signal: "low_priority", Authored: true, Muted: true}}}
	got := Apply([]protocol.Task{task}, []PullRequestRule{{Authors: []string{"bot"}, Action: "mute"}})
	if len(got) != 1 || got[0].DisplayGroup() != "muted" {
		t.Fatalf("lost explicit task preference: %+v", got)
	}
}

func TestActivityRulesScopesExceptionsAndAliases(t *testing.T) {
	rules := []ActivityRule{
		{Repos: []string{"acme/important"}, Actors: []string{"review-bot[bot]"}, Action: "keep"},
		{Actors: []string{"review-bot[bot]"}, Action: "ignore"},
		{Repos: []string{"acme/archive-*"}, Action: "ignore"},
	}
	aliases := ActorAliases("review-bot", "Bot")
	if SuppressesActivity(rules, "acme/important", aliases) {
		t.Fatal("specific keep ignored")
	}
	if !SuppressesActivity(rules, "acme/other", aliases) {
		t.Fatal("confirmed bot alias did not match")
	}
	if !SuppressesActivity(rules, "acme/archive-one", []string{"human"}) {
		t.Fatal("repo-only activity rule did not match")
	}
	if SuppressesActivity(rules, "acme/other", ActorAliases("review-bot", "User")) {
		t.Fatal("human given bot alias")
	}
	ref := pr("pr", "acme/app", "review-bot", "attention")
	ref.Metadata["author_type"] = "Bot"
	if ActionFor(ref, []PullRequestRule{{Authors: []string{"review-bot[bot]"}, Action: "mute"}}) != "mute" {
		t.Fatal("PR author bot alias did not match")
	}
}

func TestWildcardMatch(t *testing.T) {
	for _, test := range []struct {
		pattern, value string
		want           bool
	}{
		{"org/*", "org/repo", true}, {"org/*-frontend", "org/app-frontend", true},
		{"*/repo", "org/repo", true}, {"org/*", "other/repo", false},
		{"org/*-frontend", "org/frontend-api", false}, {"ORG/*", "org/repo", true},
	} {
		if got := MatchPattern(test.pattern, test.value); got != test.want {
			t.Fatalf("%+v: %t", test, got)
		}
	}
}
