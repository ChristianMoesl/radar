package github

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"radar/internal/integration"
	"radar/internal/integration/contracttest"
	"radar/internal/linking"
	"radar/internal/protocol"
)

func TestResolveBindingsReadsOpenAndTerminalPRsOutsideActiveSearch(t *testing.T) {
	for _, test := range []struct {
		state, signal, reason string
	}{
		{"OPEN", "in_progress", "tracked PR"},
		{"CLOSED", "done", "closed"},
		{"MERGED", "done", "merged"},
	} {
		t.Run(test.state, func(t *testing.T) {
			configureBoundGitHub(t, `{}`)
			pr := boundPRFixture()
			pr["state"] = test.state
			marker := fakeBoundGH(t, boundGHResponse(pr), false)
			result := NewSource().ResolveBindings(context.Background(), boundGitHubRequest())
			if !result.Complete || len(result.Observations) != 1 {
				t.Fatalf("result = %+v", result)
			}
			got := result.Observations[0]
			contracttest.AssertValidSourceRefs(t, "github", []protocol.SourceRef{got.Ref})
			if got.Ref.ID != "github:pr:acme/app:7" || string(got.Signal) != test.signal || got.Reason != test.reason || got.Ref.Title != "ABC-7 Bound work" || got.Ref.Branch != "ABC-7-work" {
				t.Fatalf("observation = %+v", got)
			}
			if got.Ref.Metadata["author"] != "someone" || !slices.Contains(got.Ref.LinkingKeys, "mark:ABC-7") || slices.Contains(got.Ref.LinkingKeys, "mark:ABC-99") {
				t.Fatalf("metadata/linking = %+v", got.Ref)
			}
			if test.signal == "done" && got.Ref.Metadata["completed_at"] != "2020-01-01T10:00:00Z" {
				t.Fatalf("cold-cache completion timestamp = %+v", got.Ref.Metadata)
			}
			if data, err := os.ReadFile(marker); err != nil || string(data) != "lookup\n" {
				t.Fatalf("lookups = %q, %v", data, err)
			}
		})
	}
}

func TestResolveBindingsPreservesAuthoredIdentityAcrossRepositoryCapitalization(t *testing.T) {
	configureBoundGitHub(t, `{}`)
	pr := boundPRFixture()
	pr["repository"] = map[string]any{"nameWithOwner": "Acme/App"}
	pr["url"] = "https://github.com/Acme/App/pull/7"
	fakeBoundGH(t, boundGHResponse(pr), false)
	result := NewSource().ResolveBindings(context.Background(), boundGitHubRequest())
	if !result.Complete || len(result.Observations) != 1 {
		t.Fatalf("result = %+v", result)
	}
	ref := result.Observations[0].Ref
	if ref.ID != "github:pr:acme/app:7" || ref.CanonicalKey != ref.ID || ref.EntityID != ref.ID || !slices.Contains(ref.LinkingKeys, ref.ID) || ref.Repo != "Acme/App" {
		t.Fatalf("authored identity detached from API facts: %+v", ref)
	}
}

func TestResolveBindingsPreservesReviewAndActivityClassificationAndFilters(t *testing.T) {
	for _, test := range []struct {
		name, filters, author, signal string
		review                        bool
	}{
		{"current review request", `{}`, "someone", "attention", true},
		{"authored activity", `{}`, "me", "attention", false},
		{"muted bot activity", `{"mute_users":["review-bot[bot]"]}`, "me", "in_progress", false},
		{"deprioritized bot activity", `{"deprioritize_users":["review-bot[bot]"]}`, "me", "in_progress", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			configureBoundGitHub(t, `{"filters":`+test.filters+`}`)
			pr := boundPRFixture()
			pr["author"] = map[string]any{"login": test.author}
			pr["comments"] = map[string]any{"nodes": []any{map[string]any{"author": map[string]any{"__typename": "Bot", "login": "review-bot"}, "createdAt": "2026-01-01T10:00:00Z"}}}
			if test.review {
				pr["reviewRequests"] = map[string]any{"nodes": []any{map[string]any{"requestedReviewer": map[string]any{"login": "me"}}}}
			}
			fakeBoundGH(t, boundGHResponse(pr), false)
			result := NewSource().ResolveBindings(context.Background(), boundGitHubRequest())
			if !result.Complete || len(result.Observations) != 1 || string(result.Observations[0].Signal) != test.signal {
				t.Fatalf("result = %+v", result)
			}
		})
	}
}

func TestResolveBindingsKeepsRepositoryAndAuthorTaskFiltersEffective(t *testing.T) {
	configureBoundGitHub(t, `{"filters":{"mute_repos":["acme/app"],"deprioritize_users":["someone"]}}`)
	fakeBoundGH(t, boundGHResponse(boundPRFixture()), false)
	result := NewSource().ResolveBindings(context.Background(), boundGitHubRequest())
	if !result.Complete || len(result.Observations) != 1 {
		t.Fatalf("result = %+v", result)
	}
	ref := result.Observations[0].Ref
	tasks := []protocol.Task{{Attention: ref.Signal, Repo: ref.Repo, SourceRefs: []protocol.SourceRef{ref}}}
	if filtered := NewSource().FilterTasks(tasks, testLogger()); len(filtered) != 0 {
		t.Fatalf("bound PR bypassed repo filter: %+v", filtered)
	}
	configureBoundGitHub(t, `{"filters":{"deprioritize_users":["someone"]}}`)
	if filtered := NewSource().FilterTasks(tasks, testLogger()); len(filtered) != 1 || filtered[0].Attention != "low_priority" {
		t.Fatalf("bound PR bypassed author filter: %+v", filtered)
	}
}

func TestResolveBindingsSkipsFreshAndReusesHistoricalTerminalRefs(t *testing.T) {
	configureBoundGitHub(t, `{}`)
	marker := fakeBoundGH(t, nil, true)
	ref := githubPullRequestRef("github:pr:acme/app:7", "acme/app", 7, "Original", "https://github.com/acme/app/pull/7", "merged", "work")
	ref.Signal = "done"
	ref.Metadata = map[string]string{"completed_at": "2020-01-01T10:00:00Z"}
	req := boundGitHubRequest()
	req.Previous = []protocol.Task{{Attention: "in_progress", SourceRefs: []protocol.SourceRef{ref}}}
	result := NewSource().ResolveBindings(context.Background(), req)
	if !result.Complete || len(result.Observations) != 1 || !reflect.DeepEqual(result.Observations[0].Ref, ref) {
		t.Fatalf("cached terminal result = %+v", result)
	}
	ref.Signal = "in_progress"
	req.Result.Observations = []integration.Observation{{Ref: ref, Signal: integration.SignalInProgress}}
	result = NewSource().ResolveBindings(context.Background(), req)
	if !result.Complete || len(result.Observations) != 0 {
		t.Fatalf("fresh result = %+v", result)
	}
	req.Result.Complete = false // An independent rule-search failure is not stale main-search data.
	result = NewSource().ResolveBindings(context.Background(), req)
	if !result.Complete || len(result.Observations) != 0 {
		t.Fatalf("fresh reopened PR lost to cached terminal ref: %+v", result)
	}
	if _, err := os.Stat(marker + ".requests"); !os.IsNotExist(err) {
		t.Fatal("fresh/terminal binding made a remote request")
	}
}

func TestResolveBindingsDoesNotUseTaskDoneAsPRCompletionEvidence(t *testing.T) {
	configureBoundGitHub(t, `{}`)
	fakeBoundGH(t, boundGHResponse(boundPRFixture()), false)
	req := boundGitHubRequest()
	ref := githubPullRequestRef(req.Bindings[0].ID, "acme/app", 7, "Not complete", "https://github.com/acme/app/pull/7", "open PR", "work")
	ref.Signal = "in_progress"
	req.Previous = []protocol.Task{{Attention: "done", SourceRefs: []protocol.SourceRef{ref}}}
	result := NewSource().ResolveBindings(context.Background(), req)
	if !result.Complete || len(result.Observations) != 1 || result.Observations[0].Signal != integration.SignalInProgress {
		t.Fatalf("result = %+v", result)
	}
}

func TestResolveBindingsRejectsMissingFailedAndInvalidPRResponses(t *testing.T) {
	for _, name := range []string{"missing", "error", "graphql error", "wrong identity", "wrong repository", "wrong URL", "wrong state"} {
		t.Run(name, func(t *testing.T) {
			configureBoundGitHub(t, `{}`)
			pr := boundPRFixture()
			var response map[string]any
			switch name {
			case "missing":
				response = boundGHResponse(nil)
			case "graphql error":
				response = map[string]any{"errors": []any{map[string]any{"message": "lookup unavailable"}}}
			case "wrong identity":
				pr["number"] = 8
				response = boundGHResponse(pr)
			case "wrong repository":
				pr["repository"] = map[string]any{"nameWithOwner": "other/app"}
				response = boundGHResponse(pr)
			case "wrong URL":
				pr["url"] = "https://github.com/acme/other/pull/7"
				response = boundGHResponse(pr)
			case "wrong state":
				pr["state"] = "UNKNOWN"
				response = boundGHResponse(pr)
			default:
				response = boundGHResponse(pr)
			}
			fakeBoundGH(t, response, name == "error")
			result := NewSource().ResolveBindings(context.Background(), boundGitHubRequest())
			if result.Complete || len(result.Observations) != 0 || result.SourceStatus == nil || result.SourceStatus.Status != "error" {
				t.Fatalf("result = %+v", result)
			}
		})
	}
}

func TestResolveBindingsValidatesPRBindingBeforeRequests(t *testing.T) {
	configureBoundGitHub(t, `{}`)
	marker := fakeBoundGH(t, nil, true)
	for _, id := range []string{"github:pr:acme/app:0", "github:pr:acme/app:-1", "github:pr:acme/app:07", "github:pr:acme/app:2147483648", "github:pr:acme/app:7?x", "github:pr:acme/../app:7", "github:pr:../app:7", "github:pr:acme/app%2fother:7", "github:pr:enterprise:acme/app:7", "jira:issue:ABC-7"} {
		req := boundGitHubRequest()
		req.Bindings[0].ID = id
		if got := NewSource().ResolveBindings(context.Background(), req); got.Complete || len(got.Observations) != 0 {
			t.Fatalf("invalid %q accepted: %+v", id, got)
		}
	}
	for _, field := range []string{"source", "kind", "key", "work_item"} {
		req := boundGitHubRequest()
		switch field {
		case "source":
			req.Bindings[0].Source = "jira"
		case "kind":
			req.Bindings[0].Kind = "issue"
		case "key":
			req.Bindings[0].Key = "lifetime"
		case "work_item":
			req.Bindings[0].WorkItem = false
		}
		if got := NewSource().ResolveBindings(context.Background(), req); got.Complete || len(got.Observations) != 0 {
			t.Fatalf("invalid %s accepted: %+v", field, got)
		}
	}
	if _, err := os.Stat(marker + ".requests"); !os.IsNotExist(err) {
		t.Fatal("invalid binding made a remote request")
	}
}

func TestResolveBindingsDeduplicatesPRsAndHonorsDisabledOrPausedSource(t *testing.T) {
	configureBoundGitHub(t, `{}`)
	marker := fakeBoundGH(t, boundGHResponse(boundPRFixture()), false)
	req := boundGitHubRequest()
	req.Bindings = append(req.Bindings, req.Bindings[0])
	result := NewSource().ResolveBindings(context.Background(), req)
	if !result.Complete || len(result.Observations) != 1 {
		t.Fatalf("duplicate bindings = %+v", result)
	}
	if data, _ := os.ReadFile(marker); string(data) != "lookup\n" {
		t.Fatalf("duplicate lookups = %q", data)
	}
	configureBoundGitHub(t, `{enabled: false}`)
	if got := NewSource().ResolveBindings(context.Background(), req); got.Complete || len(got.Observations) != 0 || got.SourceStatus.Status != "disabled" {
		t.Fatalf("disabled = %+v", got)
	}
	configureBoundGitHub(t, `{}`)
	rateState.mu.Lock()
	rateState.response.Resources.GraphQL.Remaining = 0
	rateState.mu.Unlock()
	if got := NewSource().ResolveBindings(context.Background(), req); got.Complete || len(got.Observations) != 0 {
		t.Fatalf("paused = %+v", got)
	}
	if data, _ := os.ReadFile(marker); string(data) != "lookup\n" {
		t.Fatalf("disabled/paused lookup = %q", data)
	}
}

func boundGitHubRequest() integration.BindingRequest {
	return integration.BindingRequest{Bindings: []protocol.SourceBinding{{Source: "github", Kind: "pull_request", ID: "github:pr:acme/app:7", WorkItem: true}}, Result: integration.CollectResult{Complete: true}, LinkingMarks: linking.NewMarkMatcher([]string{"ABC"}), Logger: testLogger()}
}

func configureBoundGitHub(t *testing.T, githubYAML string) {
	t.Helper()
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	path := filepath.Join(configHome, "radar", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{linking_mark_prefixes: ["ABC"],github: `+githubYAML+`}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

func boundPRFixture() map[string]any {
	return map[string]any{"number": 7, "title": "ABC-7 Bound work", "url": "https://github.com/acme/app/pull/7", "state": "OPEN", "headRefName": "ABC-7-work", "body": "Informational ABC-99", "closedAt": "2020-01-01T10:00:00Z", "author": map[string]any{"login": "someone"}, "repository": map[string]any{"nameWithOwner": "acme/app"}}
}

func boundGHResponse(pr map[string]any) map[string]any {
	var value any
	if pr != nil {
		value = pr
	}
	return map[string]any{"data": map[string]any{"viewer": map[string]any{"login": "me"}, "repository": map[string]any{"pullRequest": value}}}
}

func fakeBoundGH(t *testing.T, response map[string]any, fail bool) string {
	t.Helper()
	resetRateStateForTest(t)
	marker := filepath.Join(t.TempDir(), "lookup")
	data, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	lookup := "cat <<'JSON'\n" + string(data) + "\nJSON\n"
	if fail {
		lookup = "echo 'lookup failed' >&2; exit 1\n"
	}
	installFakeGH(t, `#!/bin/sh
echo request >> "`+marker+`.requests"
case "$*" in
  "api rate_limit") echo '{"resources":{"graphql":{"limit":5000,"remaining":5000,"reset":4102444800}}}' ;;
  *"api graphql"*"owner=acme"*"name=app"*"number=7"*)
    echo lookup >> "`+marker+`"
    `+lookup+` ;;
  *) echo "unexpected gh args: $*" >&2; exit 1 ;;
esac
`)
	return marker
}
