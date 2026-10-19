package github

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"radar/internal/integration/github/settings"
	"radar/internal/protocol"
)

// Current requests identify the obligation; request events distinguish a fresh
// re-request from a retained request that a later submitted review already met.
const mergedReviewFields = `
 mergedAt
 reviewRequests(first: 100) {
  nodes { requestedReviewer { __typename ... on User { login } ... on Team { id } } }
  pageInfo { hasNextPage endCursor }
 }
 viewerLatestReview { state submittedAt }
 timelineItems(last: 100, itemTypes: [REVIEW_REQUESTED_EVENT]) {
  nodes { ... on ReviewRequestedEvent {
   createdAt requestedReviewer { __typename ... on User { login } ... on Team { id } }
  } }
  pageInfo { hasPreviousPage }
 }`

type reviewRequestFacts struct {
	MergedAt       string `json:"mergedAt"`
	ReviewRequests struct {
		Nodes []struct {
			RequestedReviewer user `json:"requestedReviewer"`
		} `json:"nodes"`
		Page *pageInfo `json:"pageInfo"`
	} `json:"reviewRequests"`
	ViewerLatestReview *struct {
		State       string `json:"state"`
		SubmittedAt string `json:"submittedAt"`
	} `json:"viewerLatestReview"`
	TimelineItems struct {
		Nodes []struct {
			CreatedAt         string `json:"createdAt"`
			RequestedReviewer user   `json:"requestedReviewer"`
		} `json:"nodes"`
		Page *struct {
			HasPreviousPage bool `json:"hasPreviousPage"`
		} `json:"pageInfo"`
	} `json:"timelineItems"`
}

const reviewPageQuery = `query RadarReviewRequests($reviewQuery: String!, $cursor: String) {
 viewer { login }
 reviewRequested: search(query: $reviewQuery, type: ISSUE, first: 50, after: $cursor) {
  issueCount pageInfo { hasNextPage endCursor }
  nodes { ... on PullRequest {
   id number title url state isDraft headRefName headRefOid body
   author { __typename login } repository { nameWithOwner }
   reviewThreads(first: 25) { nodes { id isResolved comments(first: 25) { nodes { author { __typename login } createdAt } } } }
  } }
 }
}`

const mergedReviewQuery = `query RadarMergedReviewRequests($reviewQuery: String!, $cursor: String) {
 viewer { login }
 reviewRequested: search(query: $reviewQuery, type: ISSUE, first: 50, after: $cursor) {
  issueCount pageInfo { hasNextPage endCursor }
  nodes { ... on PullRequest {
   id number title url state isDraft closedAt headRefName headRefOid body
   author { __typename login } repository { nameWithOwner }
   ` + mergedReviewFields + `
  } }
 }
}`

// Page review searches separately from authored/participated discovery. An
// incomplete page or GitHub's 1,000-result ceiling must never imply completion.
func remainingReviewPages(ctx context.Context, query, search string, first graphQLSearchResult, logger *slog.Logger) ([]searchPullRequest, error) {
	prs := append([]searchPullRequest(nil), first.Nodes...)
	page := first
	seen := map[string]bool{}
	for {
		if page.Page == nil || page.Nodes == nil || page.IssueCount > 1000 {
			return nil, fmt.Errorf("incomplete github review search (maximum 1000 results per scope)")
		}
		if !page.Page.HasNextPage {
			return prs, nil
		}
		cursor := page.Page.EndCursor
		if cursor == "" || seen[cursor] {
			return nil, fmt.Errorf("incomplete github review pagination")
		}
		seen[cursor] = true
		var response graphQLPullRequestsResponse
		if err := trackedGraphQL(ctx, query, cursor, []string{"-f", "reviewQuery=" + search}, &response, logger); err != nil {
			return nil, err
		}
		if err := response.err(); err != nil {
			return nil, err
		}
		page = response.Data.ReviewRequested
		prs = append(prs, page.Nodes...)
	}
}

func FetchMergedReviewRequests(ctx context.Context, previous []protocol.Task, cfg settings.ReviewRequestsConfig, logger *slog.Logger) ([]protocol.Task, error) {
	// Scope searches to concrete owners, then match repository patterns locally.
	// This avoids fetching unrelated owners' historical review requests.
	owners := map[string]bool{}
	for _, pattern := range cfg.IncludeMergedPullRequests {
		owner, _, _ := strings.Cut(strings.TrimSpace(pattern), "/")
		owners[strings.ToLower(owner)] = true
	}
	ordered := make([]string, 0, len(owners))
	for owner := range owners {
		ordered = append(ordered, owner)
	}
	sort.Strings(ordered)
	now := time.Now()
	var tasks []protocol.Task
	var failures []error
	seen := map[string]bool{}
	teams := map[string]bool{}
	for _, owner := range ordered {
		search := "is:pr is:merged review-requested:@me user:" + owner + " merged:>=" + cfg.Cutoff(now).UTC().Format("2006-01-02")
		var response graphQLPullRequestsResponse
		if err := trackedGraphQL(ctx, mergedReviewQuery, "", []string{"-f", "reviewQuery=" + search}, &response, logger); err != nil {
			failures = append(failures, err)
			continue
		}
		if err := response.err(); err != nil {
			failures = append(failures, err)
			continue
		}
		login := strings.TrimSpace(response.Data.Viewer.Login)
		if login == "" {
			failures = append(failures, fmt.Errorf("merged review search returned empty viewer"))
			continue
		}
		prs, err := remainingReviewPages(ctx, mergedReviewQuery, search, response.Data.ReviewRequested, logger)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		for _, pr := range prs {
			if !cfg.IncludesMerged(repoName(pr)) {
				continue
			}
			if pr.State != "MERGED" || pr.Number <= 0 || pr.Title == "" || pr.URL == "" {
				failures = append(failures, fmt.Errorf("invalid merged review identity/state"))
				continue
			}
			if err := prepareMergedReview(ctx, &pr, login, cfg, now, teams, logger); err != nil {
				failures = append(failures, err)
				continue
			}
			task, err := mergedReviewTask(pr, login, previous, cfg, now)
			if err != nil {
				failures = append(failures, err)
				continue
			}
			if !seen[task.SourceRefs[0].ID] {
				tasks = append(tasks, task)
				seen[task.SourceRefs[0].ID] = true
			}
		}
	}
	return tasks, errors.Join(failures...)
}

func reviewerKey(reviewer user, login string) string {
	if reviewer.Login != "" && strings.EqualFold(reviewer.Login, login) {
		return "user:" + strings.ToLower(login)
	}
	if reviewer.Type == "Team" && reviewer.ViewerIsAMember {
		return "team:" + reviewer.NodeID
	}
	return ""
}

func outstandingMergedReview(pr searchPullRequest, login string) (bool, error) {
	if pr.ReviewRequests.Page == nil || pr.ReviewRequests.Page.HasNextPage || pr.ReviewRequests.Nodes == nil {
		return false, fmt.Errorf("incomplete review requests for %s#%d", repoName(pr), pr.Number)
	}
	for _, request := range pr.ReviewRequests.Nodes {
		key := reviewerKey(request.RequestedReviewer, login)
		if key == "" {
			continue
		}
		review := pr.ViewerLatestReview
		if review == nil || review.State == "PENDING" || review.SubmittedAt == "" {
			return true, nil
		}
		latestRequest := ""
		for _, event := range pr.TimelineItems.Nodes {
			if reviewerKey(event.RequestedReviewer, login) == key && event.CreatedAt > latestRequest {
				latestRequest = event.CreatedAt
			}
		}
		if latestRequest == "" {
			// Do not mistake a truncated history for proof that an obligation ended.
			return false, fmt.Errorf("missing current review request history for %s#%d", repoName(pr), pr.Number)
		}
		if review.SubmittedAt < latestRequest {
			return true, nil
		}
	}
	return false, nil
}

func mergedReviewTask(pr searchPullRequest, login string, previous []protocol.Task, cfg settings.ReviewRequestsConfig, now time.Time) (protocol.Task, error) {
	mergedAt, err := time.Parse(time.RFC3339, pr.MergedAt)
	if err != nil {
		return protocol.Task{}, fmt.Errorf("invalid merge timestamp for %s#%d", repoName(pr), pr.Number)
	}
	task := terminalPullRequestTask(pr)
	ref := &task.SourceRefs[0]
	if ref.Metadata == nil {
		ref.Metadata = map[string]string{}
	}
	ref.Metadata["pr_state"], ref.Metadata["merged_at"] = "MERGED", pr.MergedAt
	ref.Metadata["completed_at"] = pr.MergedAt
	pending := false
	if mergedAt.After(cfg.Cutoff(now)) && !(pr.Author != nil && strings.EqualFold(pr.Author.Login, login)) {
		pending, err = outstandingMergedReview(pr, login)
		if err != nil {
			return protocol.Task{}, err
		}
	}
	if pending {
		task.Kind, task.Attention, task.Reason = "github_review_request", "attention", "merged — review requested"
		task.DoneAt = ""
		task.Metadata = pullRequestMetadata(pr)
		ref.Status = task.Reason
		ref.Metadata["merged_review_pending"] = "true"
		delete(ref.Metadata, "completed_at")
	} else {
		// Review completion/expiry starts a new cleanup grace period, not one
		// backdated to the merge. Reuse an already-confirmed completion timestamp.
		for _, old := range previous {
			for _, oldRef := range old.SourceRefs {
				if !strings.EqualFold(oldRef.ID, ref.ID) {
					continue
				}
				if oldRef.Signal == "done" && oldRef.Metadata["completed_at"] != "" {
					ref.Metadata["completed_at"] = oldRef.Metadata["completed_at"]
				} else if oldRef.Metadata["merged_review_pending"] == "true" {
					ref.Metadata["completed_at"] = now.UTC().Format(time.RFC3339)
				}
			}
		}
		if pr.ViewerLatestReview != nil && pr.ViewerLatestReview.SubmittedAt > ref.Metadata["completed_at"] {
			ref.Metadata["completed_at"] = pr.ViewerLatestReview.SubmittedAt
		}
		task.DoneAt = ref.Metadata["completed_at"]
	}
	return task, nil
}

func cachedTerminalReviewUsable(ref protocol.SourceRef, cfg settings.ReviewRequestsConfig, now time.Time) bool {
	if !cfg.IncludesMerged(ref.Repo) {
		return true
	}
	if ref.Metadata["pr_state"] == "CLOSED" {
		return true
	}
	mergedAt, err := time.Parse(time.RFC3339, ref.Metadata["merged_at"])
	return err == nil && !mergedAt.After(cfg.Cutoff(now))
}

func terminalPullRequestTask(pr searchPullRequest) protocol.Task {
	reason := "closed"
	if pr.State == "MERGED" {
		reason = "merged"
	}
	if isToday(pr.ClosedAt) {
		reason += " today"
	}
	task := trackedPullRequestTask(pr)
	task.Attention, task.Reason = "done", reason
	ref := &task.SourceRefs[0]
	ref.Status = reason
	if ref.Metadata == nil {
		ref.Metadata = map[string]string{}
	}
	ref.Metadata["pr_state"] = pr.State
	if _, err := time.Parse(time.RFC3339, pr.ClosedAt); err == nil {
		ref.Metadata["completed_at"] = pr.ClosedAt
	}
	task.DoneAt = ref.Metadata["completed_at"]
	return task
}

// Team membership is not exposed as a viewer flag on GitHub's Team type.
// Resolve only teams that actually requested a review, reusing answers within
// the collection instead of listing every member of every repository team.
func prepareMergedReview(ctx context.Context, pr *searchPullRequest, login string, cfg settings.ReviewRequestsConfig, now time.Time, teams map[string]bool, logger *slog.Logger) error {
	mergedAt, err := time.Parse(time.RFC3339, pr.MergedAt)
	if pr.State != "MERGED" || !cfg.IncludesMerged(repoName(*pr)) || err != nil || !mergedAt.After(cfg.Cutoff(now)) {
		return nil
	}
	if pr.Author != nil && strings.EqualFold(pr.Author.Login, login) {
		return nil
	}
	// An actionable direct request is sufficient; unrelated team membership
	// lookups must not block it or require unnecessary permissions.
	if pending, _ := outstandingMergedReview(*pr, login); pending {
		return nil
	}
	if teams == nil {
		teams = map[string]bool{}
	}
	for i := range pr.ReviewRequests.Nodes {
		reviewer := &pr.ReviewRequests.Nodes[i].RequestedReviewer
		if reviewer.Type != "Team" {
			continue
		}
		member, found := teams[reviewer.NodeID]
		if !found {
			if reviewer.NodeID == "" {
				return fmt.Errorf("review request missing team identity")
			}
			var response struct {
				graphQLErrors
				Data struct {
					Node *struct {
						Members struct {
							Nodes []user    `json:"nodes"`
							Page  *pageInfo `json:"pageInfo"`
						} `json:"members"`
					} `json:"node"`
				} `json:"data"`
			}
			query := `query RadarReviewTeamMember($team: ID!, $login: String!) {
    node(id: $team) { ... on Team { members(first: 100, query: $login) {
     nodes { login } pageInfo { hasNextPage endCursor }
    } } }
   }`
			if err := trackedGraphQL(ctx, query, "", []string{"-f", "team=" + reviewer.NodeID, "-f", "login=" + login}, &response, logger); err != nil {
				return err
			}
			if err := response.err(); err != nil {
				return err
			}
			if response.Data.Node == nil || response.Data.Node.Members.Page == nil || response.Data.Node.Members.Nodes == nil {
				return fmt.Errorf("incomplete review team membership")
			}
			for _, user := range response.Data.Node.Members.Nodes {
				member = member || strings.EqualFold(user.Login, login)
			}
			if !member && response.Data.Node.Members.Page.HasNextPage {
				return fmt.Errorf("incomplete review team membership search")
			}
			teams[reviewer.NodeID] = member
		}
		reviewer.ViewerIsAMember = member
	}
	for i := range pr.TimelineItems.Nodes {
		reviewer := &pr.TimelineItems.Nodes[i].RequestedReviewer
		reviewer.ViewerIsAMember = teams[reviewer.NodeID]
	}
	return nil
}
