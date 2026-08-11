package github

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"radar/internal/filters"
	"radar/internal/integration/contracttest"
	"radar/internal/protocol"
)

func TestFetchPullRequestsClassifiesGraphQLResults(t *testing.T) {
	installFakeGH(t, `#!/bin/sh
case "$*" in
  *"api graphql"*)
    cat <<'JSON'
{
  "data": {
    "viewer": { "login": "me" },
    "reviewRequested": {
      "nodes": [
        {
          "number": 12,
          "title": "Review me",
          "url": "https://github.com/acme/widgets/pull/12",
          "state": "OPEN",
          "isDraft": false,
          "headRefName": "abc-12-review-me",
          "body": "refs: ABC-12",
          "repository": { "nameWithOwner": "acme/widgets" }
        }
      ]
    },
    "authored": {
      "nodes": [
        {
          "number": 34,
          "title": "My draft",
          "url": "https://github.com/acme/app/pull/34",
          "state": "OPEN",
          "isDraft": true,
          "headRefName": "ABC-34-my-draft",
          "body": "refs: ABC-34",
          "repository": { "nameWithOwner": "acme/app" },
          "reviews": { "nodes": [{ "author": { "__typename": "Bot", "login": "gemini-code-assist" }, "createdAt": "2026-06-11T10:00:00Z" }] },
          "reviewThreads": { "nodes": [{ "isResolved": false, "comments": { "nodes": [{ "author": { "__typename": "Bot", "login": "gemini-code-assist" }, "createdAt": "2026-06-11T10:00:00Z" }] } }] }
        }
      ]
    }
  }
}
JSON
    ;;
  *)
    echo "unexpected gh args: $*" >&2
    exit 1
    ;;
esac
`)

	cfg := filters.Config{MuteUsers: []string{"gemini-code-assist[bot]"}}
	reviewItems, authoredItems, activityItems, err := FetchPullRequests(context.Background(), nil, cfg, testLogger())
	if err != nil {
		t.Fatalf("FetchPullRequests() error = %v", err)
	}

	if len(activityItems) != 0 {
		t.Fatalf("activity item count = %d, want 0", len(activityItems))
	}
	if len(reviewItems) != 1 {
		t.Fatalf("review item count = %d, want 1", len(reviewItems))
	}
	assertItem(t, reviewItems[0], protocol.Task{
		Kind:      "github_review_request",
		Title:     "Review me",
		Repo:      "acme/widgets",
		URL:       "https://github.com/acme/widgets/pull/12",
		Attention: "attention",
		Reason:    "review requested",
	})
	assertSourceRef(t, reviewItems[0].SourceRefs, "github:pr:acme/widgets:12", "review requested", "abc-12-review-me", "refs: ABC-12")

	if len(authoredItems) != 1 {
		t.Fatalf("authored item count = %d, want 1", len(authoredItems))
	}
	assertItem(t, authoredItems[0], protocol.Task{
		Kind:      "github_own_pr",
		Title:     "My draft",
		Repo:      "acme/app",
		URL:       "https://github.com/acme/app/pull/34",
		Attention: "in_progress",
		Reason:    "draft PR",
	})
	assertSourceRef(t, authoredItems[0].SourceRefs, "github:pr:acme/app:34", "draft PR", "ABC-34-my-draft", "refs: ABC-34")
}

func TestDetectActivityTracksReviewThreadsAndGeneralComments(t *testing.T) {
	pr := searchPullRequest{
		Comments: graphQLComments{Nodes: []graphQLComment{
			{Author: user{Login: "someone"}, CreatedAt: "2026-06-11T10:00:00Z"},
			{Author: user{Login: "me"}, CreatedAt: "2026-06-11T09:00:00Z"},
			{Author: user{Login: "someone"}, CreatedAt: "2026-06-11T08:00:00Z"},
		}},
		ReviewThreads: graphQLReviewThreads{Nodes: []graphQLReviewThread{
			{IsResolved: false, Comments: graphQLComments{Nodes: []graphQLComment{
				{Author: user{Login: "me"}, CreatedAt: "2026-06-11T09:00:00Z"},
				{Author: user{Login: "someone"}, CreatedAt: "2026-06-11T10:00:00Z"},
			}}},
			{IsResolved: true, Comments: graphQLComments{Nodes: []graphQLComment{
				{Author: user{Login: "me"}, CreatedAt: "2026-06-11T09:00:00Z"},
				{Author: user{Login: "someone"}, CreatedAt: "2026-06-11T10:00:00Z"},
			}}},
		}},
	}

	activity := detectActivity(pr, "me", previousPullRequestActivity{generalCommentsAckAt: "2026-06-11T09:30:00Z"}, filters.Config{}, true)
	if activity.unresolvedReviewThreads != 1 || activity.newGeneralComments != 1 || activity.latestGeneralCommentAt != "2026-06-11T10:00:00Z" {
		t.Fatalf("activity = %+v, want one unresolved thread and one new general comment", activity)
	}

	activity = detectActivity(pr, "me", previousPullRequestActivity{}, filters.Config{}, false)
	if activity.unresolvedReviewThreads != 1 || activity.newGeneralComments != 0 {
		t.Fatalf("participated activity = %+v, want one unresolved participated thread only", activity)
	}
}

func TestRelevantReviewThreadOnlyNeedsAttentionWhenAnotherHumanRespondedLast(t *testing.T) {
	tests := []struct {
		name     string
		authored bool
		comments []graphQLComment
		want     bool
	}{
		{
			name:     "authored PR with initial reviewer comment",
			authored: true,
			comments: []graphQLComment{{Author: user{Login: "reviewer"}, CreatedAt: "2026-06-11T09:00:00Z"}},
			want:     true,
		},
		{
			name:     "authored PR awaiting author response",
			authored: true,
			comments: []graphQLComment{
				{Author: user{Login: "me"}, CreatedAt: "2026-06-11T09:00:00Z"},
				{Author: user{Login: "reviewer"}, CreatedAt: "2026-06-11T10:00:00Z"},
			},
			want: true,
		},
		{
			name:     "authored PR awaiting reviewer response",
			authored: true,
			comments: []graphQLComment{
				{Author: user{Login: "reviewer"}, CreatedAt: "2026-06-11T09:00:00Z"},
				{Author: user{Login: "me"}, CreatedAt: "2026-06-11T10:00:00Z"},
			},
			want: false,
		},
		{
			name:     "participated PR requires prior participation",
			authored: false,
			comments: []graphQLComment{{Author: user{Login: "reviewer"}, CreatedAt: "2026-06-11T09:00:00Z"}},
			want:     false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			thread := graphQLReviewThread{Comments: graphQLComments{Nodes: test.comments}}
			if got := relevantReviewThread(thread, "me", "acme/app", filters.Config{}, test.authored); got != test.want {
				t.Fatalf("relevantReviewThread() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestDetectActivityFiltersConfiguredBotAliases(t *testing.T) {
	pr := searchPullRequest{
		Repository: struct {
			FullName      string `json:"fullName"`
			NameWithOwner string `json:"nameWithOwner"`
		}{NameWithOwner: "acme/app"},
		Reviews: graphQLComments{Nodes: []graphQLComment{
			{Author: user{Type: "Bot", Login: "gemini-code-assist"}, CreatedAt: "2026-06-11T10:00:00Z"},
		}},
		ReviewThreads: graphQLReviewThreads{Nodes: []graphQLReviewThread{
			{IsResolved: false, Comments: graphQLComments{Nodes: []graphQLComment{
				{Author: user{Login: "me"}, CreatedAt: "2026-06-11T09:00:00Z"},
				{Author: user{Type: "Bot", Login: "gemini-code-assist"}, CreatedAt: "2026-06-11T10:00:00Z"},
			}}},
		}},
	}
	cfg := filters.Config{MuteUsers: []string{"gemini-code-assist[bot]"}}

	activity := detectActivity(pr, "me", previousPullRequestActivity{}, cfg, true)
	if activity.needsAttention() {
		t.Fatalf("activity = %+v, want configured bot alias filtered", activity)
	}
}

func TestGitHubActorAliasesNormalizesBotsForConfiguration(t *testing.T) {
	tests := []struct {
		name  string
		actor user
		want  []string
	}{
		{name: "graphql bot", actor: user{Type: "Bot", Login: "gemini-code-assist"}, want: []string{"gemini-code-assist", "gemini-code-assist[bot]"}},
		{name: "suffix-only bot", actor: user{Login: "renovate[bot]"}, want: []string{"renovate[bot]", "renovate"}},
		{name: "human", actor: user{Type: "User", Login: "reviewer"}, want: []string{"reviewer"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := githubActorAliases(test.actor); !slices.Equal(got, test.want) {
				t.Fatalf("githubActorAliases() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestDetectActivityKeepsUnconfiguredBotComments(t *testing.T) {
	pr := searchPullRequest{
		Reviews: graphQLComments{Nodes: []graphQLComment{
			{Author: user{Type: "Bot", Login: "review-bot"}, CreatedAt: "2026-06-11T10:00:00Z"},
		}},
	}

	activity := detectActivity(pr, "me", previousPullRequestActivity{}, filters.Config{}, true)
	if activity.newGeneralComments != 1 {
		t.Fatalf("activity = %+v, want unconfigured bot activity retained", activity)
	}
}

func TestResolveDonePullRequestsSkipsRemoteFetchWhenAuthoredIncomplete(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "called")
	installFakeGH(t, `#!/bin/sh
touch "`+marker+`"
echo "gh should not be called" >&2
exit 1
`)

	today := time.Now().Format(time.RFC3339)
	yesterday := time.Now().Add(-24 * time.Hour).Format(time.RFC3339)
	previous := []protocol.Task{
		{ID: 1, Kind: "github_own_pr", Attention: "in_progress", Title: "Still unknown", SourceRefs: []protocol.SourceRef{{ID: "github:pr:acme/app:34", Source: "github", Kind: "pull_request"}}},
		{ID: 2, Kind: "github_own_pr", Attention: "done", DoneAt: today, Title: "Done today", SourceRefs: []protocol.SourceRef{{ID: "github:pr:acme/app:33", Source: "github", Kind: "pull_request"}}},
		{ID: 3, Kind: "github_own_pr", Attention: "done", DoneAt: yesterday, Title: "Done yesterday", SourceRefs: []protocol.SourceRef{{ID: "github:pr:acme/app:32", Source: "github", Kind: "pull_request"}}},
	}

	items := ResolveDonePullRequests(context.Background(), previous, nil, false, testLogger())
	if len(items) != 1 {
		t.Fatalf("resolved items = %d, want 1", len(items))
	}
	if items[0].ID != 2 {
		t.Fatalf("resolved item ID = %d, want today's done item", items[0].ID)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("gh was called even though authored collection was incomplete")
	}
}

func TestResolveDonePullRequestsDoesNotRefetchDonePRs(t *testing.T) {
	resetRateStateForTest(t)
	marker := filepath.Join(t.TempDir(), "called")
	installFakeGH(t, `#!/bin/sh
touch "`+marker+`"
echo "gh should not be called for done PRs" >&2
exit 1
`)

	today := time.Now().Format(time.RFC3339)
	yesterday := time.Now().Add(-24 * time.Hour).Format(time.RFC3339)
	previous := []protocol.Task{
		{ID: 1, Kind: "github_own_pr", Attention: "done", DoneAt: today, SourceRefs: []protocol.SourceRef{{ID: "github:pr:acme/app:33", Source: "github", Kind: "pull_request", Signal: "done"}}},
		{ID: 2, Kind: "github_own_pr", Attention: "done", DoneAt: yesterday, SourceRefs: []protocol.SourceRef{{ID: "github:pr:acme/app:32", Source: "github", Kind: "pull_request", Signal: "done"}}},
		{ID: 3, Kind: "jira_issue", Attention: "in_progress", SourceRefs: []protocol.SourceRef{
			{ID: "jira:issue:ABC-31", Source: "jira", Kind: "issue", Signal: "in_progress"},
			{ID: "github:pr:acme/app:31", Source: "github", Kind: "pull_request", Signal: "done"},
		}},
	}

	items := ResolveDonePullRequests(context.Background(), previous, nil, true, testLogger())
	if len(items) != 1 || items[0].ID != 1 {
		t.Fatalf("resolved items = %+v, want only today's cached done PR", items)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("gh was called for an already-done PR")
	}
}

func TestResolveDonePullRequestsMarksPRMergedBeforeTodayDone(t *testing.T) {
	resetRateStateForTest(t)
	closedAt := time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339)
	installFakeGH(t, `#!/bin/sh
case "$*" in
  "api rate_limit")
    echo '{"resources":{"core":{"limit":5000,"remaining":5000,"reset":4102444800},"search":{"limit":30,"remaining":30,"reset":4102444800},"graphql":{"limit":5000,"remaining":5000,"reset":4102444800}}}'
    ;;
  "api /repos/acme/app/pulls/34")
    echo '{"state":"closed","merged":true,"closed_at":"`+closedAt+`"}'
    ;;
  *)
    echo "unexpected gh args: $*" >&2
    exit 1
    ;;
esac
`)
	previous := []protocol.Task{{
		ID:        1,
		Kind:      "github_own_pr",
		Attention: "in_progress",
		Title:     "Merged while Radar was offline",
		SourceRefs: []protocol.SourceRef{{
			ID: "github:pr:acme/app:34", Source: "github", Kind: "pull_request", Status: "open PR",
		}},
	}}

	items := ResolveDonePullRequests(context.Background(), previous, nil, true, testLogger())
	if len(items) != 1 {
		t.Fatalf("resolved items = %d, want 1", len(items))
	}
	if items[0].Attention != "done" || items[0].Reason != "merged" || items[0].DoneAt != closedAt {
		t.Fatalf("resolved item = %+v, want done/merged at %s", items[0], closedAt)
	}
	if got := items[0].SourceRefs[0]; got.Signal != "done" || got.Status != "merged" {
		t.Fatalf("resolved source ref = %+v, want done/merged", got)
	}
}

func TestResolveDonePullRequestsChecksEveryPRInGroupedTask(t *testing.T) {
	resetRateStateForTest(t)
	closedAt := time.Now().UTC().Format(time.RFC3339)
	installFakeGH(t, `#!/bin/sh
case "$*" in
  "api rate_limit")
    echo '{"resources":{"core":{"limit":5000,"remaining":5000,"reset":4102444800},"search":{"limit":30,"remaining":30,"reset":4102444800},"graphql":{"limit":5000,"remaining":5000,"reset":4102444800}}}'
    ;;
  "api /repos/acme/app/pulls/34")
    echo '{"state":"closed","merged":true,"closed_at":"`+closedAt+`"}'
    ;;
  *)
    echo "unexpected gh args: $*" >&2
    exit 1
    ;;
esac
`)
	previous := []protocol.Task{{
		ID: 1, Attention: "attention", Title: "Grouped work",
		SourceRefs: []protocol.SourceRef{
			{ID: "github:pr:acme/app:33", Source: "github", Kind: "pull_request", Signal: "attention"},
			{ID: "github:pr:acme/app:34", Source: "github", Kind: "pull_request", Signal: "attention"},
		},
	}}
	active := []protocol.Task{{SourceRefs: []protocol.SourceRef{{
		ID: "github:pr:acme/app:33", Source: "github", Kind: "pull_request",
	}}}}

	items := ResolveDonePullRequests(context.Background(), previous, active, true, testLogger())
	if len(items) != 1 || items[0].SourceRefs[0].ID != "github:pr:acme/app:34" {
		t.Fatalf("resolved items = %+v, want grouped PR 34 marked done", items)
	}
	if items[0].Attention != "done" || items[0].SourceRefs[0].Signal != "done" {
		t.Fatalf("resolved item = %+v, want done signals", items[0])
	}
}

func TestResolveDonePullRequestsDeduplicatesDonePRRefs(t *testing.T) {
	today := time.Now().Format(time.RFC3339)
	previous := []protocol.Task{
		{ID: 1, Kind: "github_own_pr", Attention: "done", DoneAt: today, SourceRefs: []protocol.SourceRef{{ID: "github:pr:acme/app:33", Source: "github", Kind: "pull_request"}}},
		{ID: 1, Kind: "github_own_pr", Attention: "done", DoneAt: today, SourceRefs: []protocol.SourceRef{{ID: "github:pr:acme/app:33", Source: "github", Kind: "pull_request"}}},
	}

	items := ResolveDonePullRequests(context.Background(), previous, nil, false, testLogger())
	if len(items) != 1 {
		t.Fatalf("resolved items = %d, want 1", len(items))
	}
	if items[0].SourceRefs[0].ID != "github:pr:acme/app:33" {
		t.Fatalf("source ref = %+v", items[0].SourceRefs[0])
	}
}

func TestDonePullRequestSourceRefsOnlyMarksCanonicalGitHubPRRefDone(t *testing.T) {
	sourceRefs := donePullRequestSourceRefs([]protocol.SourceRef{
		{ID: "github:pr:acme/app:42", Source: "github", Kind: "pull_request", Status: "open PR"},
		{ID: "jira:issue:ABC-42", Source: "jira", Kind: "issue"},
	}, "acme/app", 42, "merged today")

	if len(sourceRefs) != 1 {
		t.Fatalf("source refs = %d, want only canonical GitHub ref: %+v", len(sourceRefs), sourceRefs)
	}
	if sourceRefs[0].ID != "github:pr:acme/app:42" || sourceRefs[0].Status != "merged today" || sourceRefs[0].Signal != "done" {
		t.Fatalf("github source ref = %+v, want canonical PR ref marked done", sourceRefs[0])
	}
}

func TestParsePullRequestSourceRefID(t *testing.T) {
	tests := []struct {
		name       string
		id         string
		wantRepo   string
		wantNumber int
		wantOK     bool
	}{
		{name: "valid", id: "github:pr:acme/app:42", wantRepo: "acme/app", wantNumber: 42, wantOK: true},
		{name: "repo with colon", id: "github:pr:enterprise:acme/app:42", wantRepo: "enterprise:acme/app", wantNumber: 42, wantOK: true},
		{name: "wrong prefix", id: "notgithub-issue-acme/app-42"},
		{name: "missing number", id: "github:pr:acme/app:"},
		{name: "non numeric", id: "github:pr:acme/app:nope"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotRepo, gotNumber, gotOK := parsePullRequestSourceRefID(tt.id)
			if gotOK != tt.wantOK || gotRepo != tt.wantRepo || gotNumber != tt.wantNumber {
				t.Fatalf("parsePullRequestSourceRefID(%q) = (%q, %d, %v), want (%q, %d, %v)", tt.id, gotRepo, gotNumber, gotOK, tt.wantRepo, tt.wantNumber, tt.wantOK)
			}
		})
	}
}

func TestAPPath(t *testing.T) {
	path, err := apiPath("https://api.github.com/repos/acme/app/pulls/42")
	if err != nil {
		t.Fatalf("apiPath() error = %v", err)
	}
	if path != "/repos/acme/app/pulls/42" {
		t.Fatalf("apiPath() = %q", path)
	}

	if _, err := apiPath("https://api.github.com"); err == nil {
		t.Fatalf("apiPath() error = nil, want error for URL without path")
	}
}

func TestReviewRequestedFromIsCaseInsensitive(t *testing.T) {
	pr := pullRequest{RequestedReviewers: []user{{Login: "ChristianMoesl"}}}
	if !reviewRequestedFrom("christianmoesl", pr) {
		t.Fatalf("reviewRequestedFrom() = false, want true")
	}
	if reviewRequestedFrom("someoneelse", pr) {
		t.Fatalf("reviewRequestedFrom() = true, want false")
	}
}

func installFakeGH(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "gh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func assertItem(t *testing.T, got protocol.Task, want protocol.Task) {
	t.Helper()
	if got.ID != want.ID || got.Kind != want.Kind || got.Title != want.Title || got.Repo != want.Repo || got.URL != want.URL || got.Attention != want.Attention || got.Reason != want.Reason {
		t.Fatalf("item = %+v, want matching %+v", got, want)
	}
}

func assertSourceRef(t *testing.T, source_refs []protocol.SourceRef, wantID string, wantStatus string, wantBranch string, wantBody string) {
	t.Helper()
	if len(source_refs) != 1 {
		t.Fatalf("sourceRef count = %d, want 1", len(source_refs))
	}
	sourceRef := source_refs[0]
	contracttest.AssertValidSourceRefs(t, "github", source_refs)
	if sourceRef.ID != wantID || sourceRef.Source != "github" || sourceRef.Kind != "pull_request" || sourceRef.Status != wantStatus || sourceRef.Branch != wantBranch || sourceRef.Metadata["body"] != wantBody {
		t.Fatalf("sourceRef = %+v, want github pull_request %q status %q branch %q body %q", sourceRef, wantID, wantStatus, wantBranch, wantBody)
	}
	if sourceRef.CanonicalKey != wantID || !slices.Contains(sourceRef.LinkingKeys, wantID) {
		t.Fatalf("sourceRef linking = canonical %q keys %+v, want canonical and linking key %q", sourceRef.CanonicalKey, sourceRef.LinkingKeys, wantID)
	}
}
