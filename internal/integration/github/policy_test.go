package github

import (
	"context"
	"strings"
	"testing"
	"time"

	"radar/internal/integration"
	"radar/internal/integration/github/filters"
	"radar/internal/protocol"
)

func TestIgnoredParticipationStillProducesLinkableAuthorFacts(t *testing.T) {
	pr := trackedFixture(7)
	pr.ReviewThreads.Nodes = []graphQLReviewThread{{Comments: graphQLComments{Nodes: []graphQLComment{
		{Author: user{Login: "me"}, CreatedAt: "2026-01-01T09:00:00Z"},
		{Author: user{Login: "review-bot", Type: "Bot"}, CreatedAt: "2026-01-01T10:00:00Z"},
	}}}}
	rules := []filters.ActivityRule{{Actors: []string{"review-bot[bot]"}, Action: "ignore"}}
	items := activityPullRequestItems([]searchPullRequest{pr}, "me", nil, rules)
	observed := observationsFromTasks(items)
	if len(observed) != 1 {
		t.Fatalf("ignored activity discarded PR: %+v", observed)
	}
	ref := observed[0].Ref
	if !ref.Presentation.Hidden || ref.Signal != "in_progress" || ref.Metadata["author"] != "renovate" || ref.Metadata["author_type"] != "Bot" || len(ref.LinkingKeys) == 0 {
		t.Fatalf("identity/lifecycle/author facts lost: %+v", ref)
	}
	participatedQuery := pullRequestsGraphQLQuery[strings.Index(pullRequestsGraphQLQuery, "participated:"):]
	if !strings.Contains(participatedQuery, "author { __typename login }") {
		t.Fatal("participated query omitted author facts")
	}
}

func TestExplicitTrackingKeepsAcknowledgedParticipationVisible(t *testing.T) {
	pr := trackedFixture(7)
	for _, relevant := range []bool{false, true} {
		if relevant {
			pr.ReviewThreads.Nodes = []graphQLReviewThread{{Comments: graphQLComments{Nodes: []graphQLComment{
				{Author: user{Login: "me"}, CreatedAt: "2026-01-01T09:00:00Z"},
				{Author: user{Login: "other"}, CreatedAt: "2026-01-01T10:00:00Z"},
			}}}}
		}
		items := activityPullRequestItems([]searchPullRequest{pr}, "me", nil, nil)
		merged := appendMissingPullRequests(items, []protocol.Task{trackedPullRequestTask(pr)})
		if len(merged) != 1 || merged[0].SourceRefs[0].Presentation.Hidden {
			t.Fatalf("tracking failed: %+v", merged)
		}
		if ack := merged[0].SourceRefs[0].Acknowledgement; ack != nil && (ack.HideWhenAcknowledged || ack.FallbackSignal != "in_progress") {
			t.Fatalf("acknowledgement would hide explicitly tracked PR: %+v", ack)
		}
	}
}

func TestCollectionFailurePreservesRawPreviousPRs(t *testing.T) {
	for _, failMain := range []bool{false, true} {
		t.Run(map[bool]string{false: "tracked", true: "main"}[failMain], func(t *testing.T) {
			trackingBudget(t)
			configureBoundGitHub(t, `{track: [{repos: [acme/app]}]}`)
			mainReply := `echo '{"data":{"viewer":{"login":"me"},"reviewRequested":{"nodes":[]},"authored":{"nodes":[]},"participated":{"nodes":[]}}}'`
			trackedReply := "exit 1"
			if failMain {
				mainReply = "exit 1"
				trackedReply = "echo '" + trackedPage(t, []searchPullRequest{trackedFixture(42)}, "") + "'"
			}
			installFakeGH(t, "#!/bin/sh\ncase \"$*\" in\n *reviewQuery*) "+mainReply+" ;;\n *RadarTrackedPullRequests*) "+trackedReply+" ;;\n *) exit 2 ;;\nesac\n")
			old := trackedPullRequestTask(trackedFixture(42))
			old.SourceRefs[0].Signal = "attention"
			old.SourceRefs[0].Metadata = old.Metadata
			result := NewSource().Collect(context.Background(), integration.CollectRequest{Previous: []protocol.Task{old}, Logger: testLogger()})
			if result.Complete || result.SourceStatus == nil || len(result.Observations) != 1 || result.Observations[0].Ref.ID != old.SourceRefs[0].ID || result.Observations[0].Ref.Metadata["author"] != "renovate" || result.Observations[0].Ref.Signal != "attention" {
				t.Fatalf("failed refresh lost source truth: %+v", result)
			}
		})
	}
}

func TestDoneResolutionPreservesAuthorPolicyAndHiddenPresentation(t *testing.T) {
	ref := trackedPullRequestTask(trackedFixture(42)).SourceRefs[0]
	ref.Metadata = map[string]string{"author": "renovate", "author_type": "Bot"}
	ref.Presentation.Hidden = true
	done := donePullRequestSourceRefs([]protocol.SourceRef{ref}, "acme/app", 42, "merged")
	if len(done) != 1 || done[0].Metadata["author"] != "renovate" || !done[0].Presentation.Hidden || done[0].Signal != "done" {
		t.Fatalf("terminal transition lost policy inputs: %+v", done)
	}
}

func TestActivityKeepDoesNotOverridePRPolicyOrCreateUnrelatedAttention(t *testing.T) {
	pr := trackedFixture(7)
	pr.Comments.Nodes = []graphQLComment{{Author: user{Login: "other"}, CreatedAt: "2026-01-01T10:00:00Z"}}
	activityRules := []filters.ActivityRule{{Actors: []string{"other"}, Action: "keep"}}
	if activity := detectActivity(pr, "me", previousPullRequestActivity{}, activityRules, false); activity.needsAttention() {
		t.Fatal("keep subscribed to unrelated discussion")
	}
	items := authoredPullRequestTasks([]searchPullRequest{pr})
	applyActivity(items, []searchPullRequest{pr}, "me", nil, activityRules, true)
	observed := observationsFromTasks(items)
	task := protocol.Task{Attention: string(observed[0].Signal), SourceRefs: []protocol.SourceRef{observed[0].Ref}}
	if got := filters.Apply([]protocol.Task{task}, []filters.PullRequestRule{{Authors: []string{"renovate[bot]"}, Action: "mute"}}); len(got) != 0 {
		t.Fatal("activity keep bypassed PR mute")
	}
}

func TestRetainedDonePRKeepsOwnAuthorNotAggregatedTaskAuthor(t *testing.T) {
	ref := trackedPullRequestTask(trackedFixture(42)).SourceRefs[0]
	ref.Signal = "done"
	ref.Metadata = map[string]string{"author": "renovate", "author_type": "Bot"}
	items := keepTodaysDoneTasks(nil, []protocol.Task{{Attention: "done", DoneAt: time.Now().UTC().Format(time.RFC3339),
		Metadata: map[string]string{"author": "unrelated"}, SourceRefs: []protocol.SourceRef{ref}}})
	observed := observationsFromTasks(items)
	if len(observed) != 1 || observed[0].Ref.Metadata["author"] != "renovate" {
		t.Fatalf("retained PR inherited unrelated author: %+v", observed)
	}
}
