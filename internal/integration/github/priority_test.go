package github

import (
	"reflect"
	"testing"
	"time"

	"radar/internal/protocol"
)

func TestCurrentReviewRequestOccurrencesSurviveMergeButNotRealRerequests(t *testing.T) {
	now := time.Date(2026, 11, 3, 11, 0, 0, 0, time.UTC)
	pr := mergedReviewFixture(t, now)
	pr.State = "OPEN"
	note := protocol.SourceRef{ID: "note:one", Role: protocol.SourceRefRoleAuthoritative, Authored: true, Authority: protocol.SourceRefAuthorityPrimary, Signal: "low_priority", Metadata: map[string]string{"priority": "low"}}
	task := protocol.Task{SourceRefs: []protocol.SourceRef{note}}
	collect := func() protocol.SourceRef {
		t.Helper()
		var items []protocol.Task
		var err error
		if pr.State == "MERGED" {
			var item protocol.Task
			item, err = mergedReviewTask(pr, "me", nil, mergedReviewSettings().ReviewRequests, now)
			items = []protocol.Task{item}
		} else {
			items, err = reviewRequestTasks([]searchPullRequest{pr}, "me")
		}
		if err != nil {
			t.Fatal(err)
		}
		return observationsFromTasks(items)[0].Ref
	}
	check := func(want string) {
		t.Helper()
		task.SourceRefs = []protocol.SourceRef{task.SourceRefs[0], collect()}
		view, _ := protocol.ProjectAttention(task, nil)
		if view.Attention != want || view.Priority != "low" {
			t.Fatalf("review projection = %+v, want %s with retained Low", view, want)
		}
	}
	check("attention")
	task.SourceRefs[0].LowPriorityBaseline = protocol.CaptureLowPriorityBaseline(task)
	check("low_priority")
	pr.Title, pr.HeadRefOID = "Renamed", "a-new-head-is-not-a-review-request"
	check("low_priority")
	pr.State = "MERGED"
	check("low_priority") // Merge does not manufacture a new review obligation.
	pr.State = "OPEN"
	setLatestReview(t, &pr, "APPROVED", now.Add(-30*time.Minute))
	check("low_priority")
	pr.TimelineItems.Nodes[0].CreatedAt = now.Format(time.RFC3339)
	check("attention") // A later request on exactly the same PR resurfaces.
	check("attention")
	task.SourceRefs[0].LowPriorityBaseline = protocol.CaptureLowPriorityBaseline(task)
	check("low_priority")
	pr.TimelineItems.Nodes = nil
	if _, err := reviewRequestTasks([]searchPullRequest{pr}, "me"); err == nil {
		t.Fatal("incomplete history falsely established a resolved request")
	}
}

func TestActionOccurrenceKeysTrackRelevantRepliesNotPollingOrTitles(t *testing.T) {
	pr := searchPullRequest{Number: 7, Title: "Review", Comments: graphQLComments{Nodes: []graphQLComment{
		{Author: user{Login: "reviewer"}, CreatedAt: "2026-11-03T10:00:00Z"},
		{Author: user{Login: "me"}, CreatedAt: "2026-11-03T10:01:00Z"},
	}}, ReviewThreads: graphQLReviewThreads{Nodes: []graphQLReviewThread{{ID: "thread-one", Comments: graphQLComments{Nodes: []graphQLComment{
		{Author: user{Login: "reviewer"}, CreatedAt: "2026-11-03T10:00:00Z"},
	}}}}}}
	pr.Repository.NameWithOwner = "acme/app"
	keys := func() []string {
		activity := detectActivity(pr, "me", previousPullRequestActivity{}, nil, true)
		ref := protocol.SourceRef{}
		setActivityMetadata(&ref, activity, "in_progress", "open", false)
		return ref.ActionableSignals
	}
	first := keys()
	if len(first) != 2 {
		t.Fatalf("relevant thread/comment occurrences missing: %v", first)
	}
	pr.Title = "Changed title"
	pr.HeadRefOID = "another commit is not a reply"
	if !reflect.DeepEqual(first, keys()) {
		t.Fatal("presentation/work progress manufactured a new request")
	}
	pr.ReviewThreads.Nodes[0].Comments.Nodes = append(pr.ReviewThreads.Nodes[0].Comments.Nodes, graphQLComment{Author: user{Login: "reviewer"}, CreatedAt: "2026-11-03T10:02:00Z"})
	second := keys()
	if reflect.DeepEqual(first, second) || !reflect.DeepEqual(second, keys()) {
		t.Fatal("a new reply on the same source was missed, or repeated polling changed its identity")
	}
	pr.ReviewThreads.Nodes[0].IsResolved = true
	if remaining := keys(); len(remaining) != 1 || remaining[0] != first[1] {
		t.Fatalf("resolved thread kept breaking through: %v", remaining)
	}
}
