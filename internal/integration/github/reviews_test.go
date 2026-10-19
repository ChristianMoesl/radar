package github

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"radar/internal/integration"
	"radar/internal/integration/github/filters"
	"radar/internal/integration/github/settings"
	"radar/internal/protocol"
	"radar/internal/state"
	"radar/internal/workspacegc"
)

func mergedReviewFixture(t *testing.T, now time.Time) searchPullRequest {
	t.Helper()
	data := boundPRFixture()
	data["state"] = "MERGED"
	data["mergedAt"] = now.Add(-24 * time.Hour).UTC().Format(time.RFC3339)
	data["closedAt"] = data["mergedAt"]
	data["reviewRequests"] = map[string]any{"nodes": []any{map[string]any{"requestedReviewer": map[string]any{"__typename": "User", "login": "me"}}}, "pageInfo": map[string]any{"hasNextPage": false}}
	data["timelineItems"] = map[string]any{"nodes": []any{map[string]any{"createdAt": now.Add(-time.Hour).UTC().Format(time.RFC3339), "requestedReviewer": map[string]any{"__typename": "User", "login": "me"}}}, "pageInfo": map[string]any{"hasPreviousPage": false}}
	bytes, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	var pr searchPullRequest
	if err := json.Unmarshal(bytes, &pr); err != nil {
		t.Fatal(err)
	}
	return pr
}

func mergedReviewSettings() settings.Config {
	cfg := settings.Default()
	cfg.ReviewRequests.IncludeMergedPullRequests = []string{"ACME/*"}
	return cfg
}

func setLatestReview(t *testing.T, pr *searchPullRequest, state string, submitted time.Time) {
	t.Helper()
	data := fmt.Sprintf(`{"viewerLatestReview":{"state":%q,"submittedAt":%q}}`, state, submitted.UTC().Format(time.RFC3339))
	if err := json.Unmarshal([]byte(data), pr); err != nil {
		t.Fatal(err)
	}
}

func TestMergedReviewClassification(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name   string
		change func(*searchPullRequest, *settings.Config)
		signal string
		fail   bool
	}{
		{"opted-in outstanding request", nil, "attention", false},
		{"disabled by default", func(_ *searchPullRequest, c *settings.Config) { c.ReviewRequests.IncludeMergedPullRequests = nil }, "done", false},
		{"other repository", func(_ *searchPullRequest, c *settings.Config) {
			c.ReviewRequests.IncludeMergedPullRequests = []string{"other/*"}
		}, "done", false},
		{"expired", func(p *searchPullRequest, _ *settings.Config) {
			p.MergedAt = now.Add(-31 * 24 * time.Hour).Format(time.RFC3339)
		}, "done", false},
		{"exact boundary expires", func(p *searchPullRequest, _ *settings.Config) {
			p.MergedAt = now.Add(-30 * 24 * time.Hour).Format(time.RFC3339)
		}, "done", false},
		{"custom age", func(p *searchPullRequest, c *settings.Config) {
			c.ReviewRequests.MergedPullRequestMaxAgeDays = 90
			p.MergedAt = now.Add(-60 * 24 * time.Hour).Format(time.RFC3339)
		}, "attention", false},
		{"withdrawn", func(p *searchPullRequest, _ *settings.Config) { p.ReviewRequests.Nodes = p.ReviewRequests.Nodes[:0] }, "done", false},
		{"someone else requested", func(p *searchPullRequest, _ *settings.Config) {
			p.ReviewRequests.Nodes[0].RequestedReviewer.Login = "other"
		}, "done", false},
		{"older approval does not satisfy re-request", func(p *searchPullRequest, _ *settings.Config) {
			setLatestReview(t, p, "APPROVED", now.Add(-2*time.Hour))
		}, "attention", false},
		{"new approval satisfies retained request", func(p *searchPullRequest, _ *settings.Config) {
			setLatestReview(t, p, "APPROVED", now.Add(-30*time.Minute))
		}, "done", false},
		{"submitted comments satisfy request", func(p *searchPullRequest, _ *settings.Config) {
			setLatestReview(t, p, "COMMENTED", now.Add(-30*time.Minute))
		}, "done", false},
		{"pending draft is not submitted", func(p *searchPullRequest, _ *settings.Config) {
			setLatestReview(t, p, "PENDING", now.Add(-30*time.Minute))
		}, "attention", false},
		{"own PR team request not actionable", func(p *searchPullRequest, _ *settings.Config) { p.Author.Login = "me" }, "done", false},
		{"closed unmerged remains done", func(p *searchPullRequest, _ *settings.Config) { p.State = "CLOSED" }, "done", false},
		{"invalid merge timestamp", func(p *searchPullRequest, _ *settings.Config) { p.MergedAt = "invalid" }, "", true},
		{"missing request page", func(p *searchPullRequest, _ *settings.Config) { p.ReviewRequests.Page = nil }, "", true},
		{"incomplete requests", func(p *searchPullRequest, _ *settings.Config) { p.ReviewRequests.Page.HasNextPage = true }, "", true},
		{"cannot infer satisfaction from missing history", func(p *searchPullRequest, _ *settings.Config) {
			setLatestReview(t, p, "APPROVED", now)
			p.TimelineItems.Nodes = nil
		}, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pr, cfg := mergedReviewFixture(t, now), mergedReviewSettings()
			if tc.change != nil {
				tc.change(&pr, &cfg)
			}
			task, err := boundPullRequestTask(boundPullRequest{pr}, "me", nil, cfg, now)
			if tc.fail {
				if err == nil {
					t.Fatal("incomplete facts accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if task.Attention != tc.signal {
				t.Fatalf("task=%+v, want %s", task, tc.signal)
			}
			if tc.signal == "attention" {
				ref := task.SourceRefs[0]
				if task.Reason != "merged — review requested" || ref.Metadata["pr_state"] != "MERGED" || ref.Metadata["merged_review_pending"] != "true" || ref.Metadata["completed_at"] != "" {
					t.Fatalf("lost remote state/obligation: %+v", task)
				}
				task.AcknowledgementCursor = now.Format(time.RFC3339)
				ref.Signal = "attention"
				task.SourceRefs[0] = ref
				got, _ := protocol.ProjectAttention(task, nil)
				if got.Attention != "attention" {
					t.Fatal("acknowledgement dismissed review")
				}
				muted := filters.Apply([]protocol.Task{task}, []filters.PullRequestRule{{Repos: []string{"acme/*"}, Action: protocol.ContributionMute}})
				if len(muted) != 0 {
					t.Fatal("merged review bypassed PR mute")
				}
				low := filters.Apply([]protocol.Task{task}, []filters.PullRequestRule{{Repos: []string{"acme/*"}, Action: protocol.ContributionDeprioritize}})
				if len(low) != 1 || low[0].Attention != "low_priority" {
					t.Fatal("merged review bypassed deprioritization")
				}
			}
		})
	}
}

func TestMergedTeamReviewMembershipAndReRequest(t *testing.T) {
	now := time.Now()
	pr := mergedReviewFixture(t, now)
	pr.ReviewRequests.Nodes[0].RequestedReviewer = user{Type: "Team", NodeID: "team-1"}
	pr.TimelineItems.Nodes[0].RequestedReviewer = user{Type: "Team", NodeID: "team-1"}
	marker := filepath.Join(t.TempDir(), "calls")
	trackingBudget(t)
	installFakeGH(t, `#!/bin/sh
case "$*" in
 *RadarReviewTeamMember*"team=team-1"*"login=me"*)
 echo call >> '`+marker+`'
 echo '{"data":{"node":{"members":{"nodes":[{"login":"me"}],"pageInfo":{"hasNextPage":false}}}}}' ;;
 *) echo "unexpected: $*" >&2; exit 1 ;;
esac`)
	cache := map[string]bool{}
	for i := 0; i < 2; i++ {
		if err := prepareMergedReview(context.Background(), &pr, "me", mergedReviewSettings().ReviewRequests, now, cache, testLogger()); err != nil {
			t.Fatal(err)
		}
	}
	if data, _ := os.ReadFile(marker); string(data) != "call\n" {
		t.Fatalf("membership lookups=%q", data)
	}
	setLatestReview(t, &pr, "APPROVED", now.Add(-2*time.Hour))
	if pending, err := outstandingMergedReview(pr, "me"); err != nil || !pending {
		t.Fatalf("team re-request=%v, %v", pending, err)
	}
	setLatestReview(t, &pr, "APPROVED", now)
	if pending, err := outstandingMergedReview(pr, "me"); err != nil || pending {
		t.Fatalf("satisfied team request=%v, %v", pending, err)
	}
	pr.ReviewRequests.Nodes[0].RequestedReviewer.ViewerIsAMember = false
	if pending, err := outstandingMergedReview(pr, "me"); err != nil || pending {
		t.Fatalf("unrelated team=%v, %v", pending, err)
	}
}

func reviewSearchPage(t *testing.T, prs []searchPullRequest, cursor string) string {
	t.Helper()
	page := map[string]any{"hasNextPage": cursor != "", "endCursor": cursor}
	data, err := json.Marshal(map[string]any{"data": map[string]any{"viewer": map[string]any{"login": "me"}, "reviewRequested": map[string]any{"nodes": prs, "pageInfo": page}}})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestMergedReviewDiscoveryPaginatesScopesDeduplicatesAndChecksExactAge(t *testing.T) {
	trackingBudget(t)
	now := time.Now()
	first, second, old, unrelated := mergedReviewFixture(t, now), mergedReviewFixture(t, now), mergedReviewFixture(t, now), mergedReviewFixture(t, now)
	second.Number = 8
	second.URL = "https://github.com/acme/app/pull/8"
	old.Number = 9
	old.URL = "https://github.com/acme/app/pull/9"
	old.MergedAt = now.Add(-31 * 24 * time.Hour).Format(time.RFC3339)
	unrelated.Repository.NameWithOwner = "acme/unrelated"
	marker := filepath.Join(t.TempDir(), "calls")
	installFakeGH(t, `#!/bin/sh
case "$*" in
 *RadarMergedReviewRequests*"user:acme"*"cursor=next"*) echo '`+reviewSearchPage(t, []searchPullRequest{first, second, old, unrelated}, "")+`' ;;
 *RadarMergedReviewRequests*"user:acme"*)
 echo scope >> '`+marker+`'
 echo '`+reviewSearchPage(t, []searchPullRequest{first}, "next")+`' ;;
 *) echo "unexpected: $*" >&2; exit 1 ;;
esac`)
	cfg := mergedReviewSettings().ReviewRequests
	cfg.IncludeMergedPullRequests = []string{"acme/app", "acme/app*"}
	tasks, err := FetchMergedReviewRequests(context.Background(), nil, cfg, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 3 || tasks[0].Attention != "attention" || tasks[1].Attention != "attention" || tasks[2].Attention != "done" {
		t.Fatalf("tasks=%+v", tasks)
	}
	if data, _ := os.ReadFile(marker); string(data) != "scope\n" {
		t.Fatalf("overlapping scopes repeated requests: %q", data)
	}
}

func TestReviewSearchRejectsIncompletePagesAndSearchCeiling(t *testing.T) {
	for _, test := range []struct{ name, reply string }{
		{"failed page", "exit 1"},
		{"graphql partial", `echo '{"errors":[{"message":"unavailable"}],"data":{"reviewRequested":{"nodes":[],"pageInfo":{"hasNextPage":false}}}}'`},
		{"missing page", `echo '{"data":{"reviewRequested":{"nodes":[]}}}'`},
		{"repeated cursor", `echo '{"data":{"reviewRequested":{"nodes":[],"pageInfo":{"hasNextPage":true,"endCursor":"next"}}}}'`},
		{"ceiling", `echo '{"data":{"reviewRequested":{"nodes":[],"issueCount":1001,"pageInfo":{"hasNextPage":false}}}}'`},
	} {
		t.Run(test.name, func(t *testing.T) {
			trackingBudget(t)
			installFakeGH(t, "#!/bin/sh\n"+test.reply+"\n")
			first := graphQLSearchResult{Nodes: []searchPullRequest{}, Page: &pageInfo{HasNextPage: true, EndCursor: "next"}}
			if _, err := remainingReviewPages(context.Background(), mergedReviewQuery, "scope", first, testLogger()); err == nil {
				t.Fatal("incomplete search accepted")
			}
		})
	}
}

func TestOpenReviewPaginationPreservesPersonalCollection(t *testing.T) {
	trackingBudget(t)
	pr := mergedReviewFixture(t, time.Now())
	pr.State = "OPEN"
	first := reviewSearchPage(t, []searchPullRequest{pr}, "next")
	second := pr
	second.Number = 8
	second.URL = "https://github.com/acme/app/pull/8"
	installFakeGH(t, `#!/bin/sh
case "$*" in
 *RadarReviewRequests*"cursor=next"*) echo '`+reviewSearchPage(t, []searchPullRequest{second}, "")+`' ;;
 *"authoredQuery=is:pr is:open author:@me"*) echo '`+first+`' ;;
 *) exit 1 ;;
esac`)
	reviews, _, _, err := FetchPullRequests(context.Background(), nil, nil, testLogger())
	if err != nil || len(reviews) != 2 {
		t.Fatalf("reviews=%+v, %v", reviews, err)
	}
}

func TestMergedReviewBindingRefreshesCachedDoneAndExpiresActiveReview(t *testing.T) {
	now := time.Now()
	cfg := mergedReviewSettings()
	configureBoundGitHub(t, `{review_requests: {include_merged_pull_requests: [acme/app]}}`)
	pr := mergedReviewFixture(t, now)
	bytes, _ := json.Marshal(pr)
	var value map[string]any
	if err := json.Unmarshal(bytes, &value); err != nil {
		t.Fatal(err)
	}
	fakeBoundGH(t, boundGHResponse(value), false)
	old := terminalPullRequestTask(pr)
	old.SourceRefs[0].Signal = "done"
	old.SourceRefs[0].Metadata["merged_at"] = pr.MergedAt
	req := boundGitHubRequest()
	req.Previous = []protocol.Task{old}
	result := NewSource().ResolveBindings(context.Background(), req)
	if !result.Complete || len(result.Observations) != 1 || result.Observations[0].Signal != integration.SignalAttention {
		t.Fatalf("cached merge hid new request: %+v", result)
	}
	active := protocol.Task{Attention: "attention", SourceRefs: []protocol.SourceRef{result.Observations[0].Ref}}
	// At the configured boundary the request is still outstanding, but the review expires.
	pr.MergedAt = now.Add(-31 * 24 * time.Hour).Format(time.RFC3339)
	bytes, _ = json.Marshal(pr)
	_ = json.Unmarshal(bytes, &value)
	fakeBoundGH(t, boundGHResponse(value), false)
	req.Previous = []protocol.Task{active}
	result = NewSource().ResolveBindings(context.Background(), req)
	if !result.Complete || len(result.Observations) != 1 || result.Observations[0].Signal != integration.SignalDone {
		t.Fatalf("review did not expire: %+v", result)
	}
	ref := result.Observations[0].Ref
	completed, err := time.Parse(time.RFC3339, ref.Metadata["completed_at"])
	if err != nil || completed.Before(now.Add(-time.Second)) {
		t.Fatalf("expiry backdated cleanup: %+v", ref)
	}
	if cachedTerminalReviewUsable(old.SourceRefs[0], cfg.ReviewRequests, now) {
		t.Fatal("eligible terminal review skipped fresh lookup")
	}
	if !cachedTerminalReviewUsable(ref, cfg.ReviewRequests, now) {
		t.Fatal("expired terminal review unnecessarily refetched")
	}
	cfg.ReviewRequests.MergedPullRequestMaxAgeDays = 90
	if cachedTerminalReviewUsable(ref, cfg.ReviewRequests, now) {
		t.Fatal("larger cutoff failed to reconsider cached expiry")
	}
}

func TestMergedReviewReconciliationKeepsRequestActiveUntilSatisfiedOrExpired(t *testing.T) {
	for _, tc := range []struct {
		name             string
		withdraw, expire bool
		signal           string
	}{
		{"still requested", false, false, "attention"},
		{"withdrawn", true, false, "done"},
		{"expired", false, true, "done"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now()
			pr := mergedReviewFixture(t, now)
			cfg := mergedReviewSettings()
			old, err := mergedReviewTask(pr, "me", nil, cfg.ReviewRequests, now)
			if err != nil {
				t.Fatal(err)
			}
			old.SourceRefs[0].Signal = "attention"
			if tc.withdraw {
				pr.ReviewRequests.Nodes = pr.ReviewRequests.Nodes[:0]
			}
			if tc.expire {
				pr.MergedAt = now.Add(-31 * 24 * time.Hour).Format(time.RFC3339)
			}
			bytes, _ := json.Marshal(pr)
			var value map[string]any
			_ = json.Unmarshal(bytes, &value)
			fakeBoundGH(t, boundGHResponse(value), false)
			tasks := ResolveDonePullRequests(context.Background(), []protocol.Task{old}, nil, true, cfg.ReviewRequests, testLogger())
			if len(tasks) != 1 || tasks[0].Attention != tc.signal {
				t.Fatalf("tasks=%+v", tasks)
			}
		})
	}
}

func TestMergedReviewPartialCollectionRetainsOutstandingFacts(t *testing.T) {
	trackingBudget(t)
	configureBoundGitHub(t, `{review_requests: {include_merged_pull_requests: [acme/app]}}`)
	old, err := mergedReviewTask(mergedReviewFixture(t, time.Now()), "me", nil, mergedReviewSettings().ReviewRequests, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	old.SourceRefs[0].Signal = "attention"
	installFakeGH(t, `#!/bin/sh
case "$*" in
 *RadarMergedReviewRequests*) echo '{"errors":[{"message":"unavailable"}]}' ;;
 *authoredQuery*) echo '{"data":{"viewer":{"login":"me"},"reviewRequested":{"nodes":[],"pageInfo":{"hasNextPage":false}},"authored":{"nodes":[]},"participated":{"nodes":[]}}}' ;;
 *) exit 1 ;;
esac`)
	result := NewSource().Collect(context.Background(), integration.CollectRequest{Previous: []protocol.Task{old}, Logger: testLogger()})
	if result.Complete || result.SourceStatus == nil || len(result.Observations) != 1 || result.Observations[0].Ref.Signal != "attention" {
		t.Fatalf("partial collection lost review: %+v", result)
	}
}

func TestFreshReviewWinsOverCachedDoneEvenDuringPartialCollection(t *testing.T) {
	today := time.Now().Format(time.RFC3339)
	old := protocol.Task{Attention: "done", DoneAt: today, SourceRefs: []protocol.SourceRef{{ID: "github:pr:acme/app:7", Source: "github", Kind: "pull_request", Signal: "done"}}}
	active := old
	active.Attention = "attention"
	for _, complete := range []bool{false, true} {
		if got := ResolveDonePullRequests(context.Background(), []protocol.Task{old}, []protocol.Task{active}, complete, mergedReviewSettings().ReviewRequests, testLogger()); len(got) != 0 {
			t.Fatalf("cached done overwrote fresh request: %+v", got)
		}
	}
}

func TestMergedReviewContributionControlsLifecycleAndCleanup(t *testing.T) {
	t.Setenv("RADAR_STATE", filepath.Join(t.TempDir(), "state.json"))
	now := time.Now()
	pr := mergedReviewFixture(t, now)
	cfg := mergedReviewSettings().ReviewRequests
	task, err := mergedReviewTask(pr, "me", nil, cfg, now)
	if err != nil {
		t.Fatal(err)
	}
	task.SourceRefs[0].Signal = task.Attention
	workspace := protocol.SourceRef{ID: "workspace:test", Source: "workspace", Kind: "workspace", Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleWorkspace, Authority: protocol.SourceRefAuthorityNone, ProvidesWorkspace: true, WorkspaceID: "test", Path: "/workspaces/test", LinkingKeys: []string{task.SourceRefs[0].ID}}
	task.SourceRefs = append(task.SourceRefs, workspace)
	store, err := state.NewStore(testLogger())
	if err != nil {
		t.Fatal(err)
	}
	store.SetTasks([]protocol.Task{task})
	if got := store.Tasks(); len(got) != 1 || got[0].Attention != "attention" {
		t.Fatalf("merged review completed task: %+v", got)
	}
	if plan, err := workspacegc.BuildPlan(store, now, workspacegc.Options{WorkspaceRoot: "/workspaces", IgnoreRetention: true}); err != nil || len(plan.Candidates) != 0 {
		t.Fatalf("pending review allowed cleanup: %+v, %v", plan, err)
	}
	pr.ReviewRequests.Nodes = pr.ReviewRequests.Nodes[:0]
	done, err := mergedReviewTask(pr, "me", []protocol.Task{task}, cfg, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	done.SourceRefs[0].Signal = "done"
	done.SourceRefs = append(done.SourceRefs, workspace)
	store.SetTasks([]protocol.Task{done})
	if plan, err := workspacegc.BuildPlan(store, now.Add(time.Hour), workspacegc.Options{WorkspaceRoot: "/workspaces", IgnoreRetention: true}); err != nil || len(plan.Candidates) != 1 {
		t.Fatalf("completed review blocked cleanup: %+v, %v", plan, err)
	}
}

func TestMergedReviewCompletionTimestampSurvivesRefreshAndRepositoryCase(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	pr := mergedReviewFixture(t, now)
	cfg := mergedReviewSettings().ReviewRequests
	active, err := mergedReviewTask(pr, "me", nil, cfg, now)
	if err != nil {
		t.Fatal(err)
	}
	active.SourceRefs[0].Signal = "attention"
	pr.ReviewRequests.Nodes = pr.ReviewRequests.Nodes[:0]
	pr.Repository.NameWithOwner = "Acme/App"
	pr.URL = "https://github.com/Acme/App/pull/7"
	done, err := mergedReviewTask(pr, "me", []protocol.Task{active}, cfg, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	want := now.Add(time.Minute).Format(time.RFC3339)
	if done.SourceRefs[0].Metadata["completed_at"] != want {
		t.Fatalf("case change backdated cleanup: %+v", done)
	}
	done.SourceRefs[0].Signal = "done"
	again, err := mergedReviewTask(pr, "me", []protocol.Task{done}, cfg, now.Add(time.Hour))
	if err != nil || again.SourceRefs[0].Metadata["completed_at"] != want {
		t.Fatalf("refresh moved completion: %+v, %v", again, err)
	}
}

func TestDirectMergedReviewDoesNotRequireUnrelatedTeamMembership(t *testing.T) {
	now := time.Now()
	pr := mergedReviewFixture(t, now)
	team := pr.ReviewRequests.Nodes[0]
	team.RequestedReviewer = user{Type: "Team", NodeID: "unrelated"}
	pr.ReviewRequests.Nodes = append(pr.ReviewRequests.Nodes, team)
	installFakeGH(t, "#!/bin/sh\necho 'membership lookup must not be needed' >&2; exit 1\n")
	if err := prepareMergedReview(context.Background(), &pr, "me", mergedReviewSettings().ReviewRequests, now, nil, testLogger()); err != nil {
		t.Fatal(err)
	}
	if pending, err := outstandingMergedReview(pr, "me"); err != nil || !pending {
		t.Fatalf("direct request lost: %v, %v", pending, err)
	}
}
