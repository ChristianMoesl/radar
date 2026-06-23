package github

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"radar.nvim/internal/linking"
	"radar.nvim/internal/protocol"
)

type notification struct {
	ID         string `json:"id"`
	Reason     string `json:"reason"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	Subject struct {
		Title string `json:"title"`
		Type  string `json:"type"`
		URL   string `json:"url"`
	} `json:"subject"`
}

type user struct {
	Login string `json:"login"`
}

type pullRequest struct {
	HTMLURL            string `json:"html_url"`
	State              string `json:"state"`
	Merged             bool   `json:"merged"`
	ClosedAt           string `json:"closed_at"`
	RequestedReviewers []user `json:"requested_reviewers"`
}

type searchPullRequest struct {
	Number        int                  `json:"number"`
	Title         string               `json:"title"`
	URL           string               `json:"url"`
	State         string               `json:"state"`
	Draft         bool                 `json:"isDraft"`
	ClosedAt      string               `json:"closedAt"`
	HeadRefName   string               `json:"headRefName"`
	Body          string               `json:"body"`
	Author        *user                `json:"author"`
	Comments      graphQLComments      `json:"comments"`
	Reviews       graphQLComments      `json:"reviews"`
	ReviewThreads graphQLReviewThreads `json:"reviewThreads"`
	Repository    struct {
		FullName      string `json:"fullName"`
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"repository"`
}

type graphQLComments struct {
	Nodes []graphQLComment `json:"nodes"`
}

type graphQLComment struct {
	Author    user   `json:"author"`
	CreatedAt string `json:"createdAt"`
}

type graphQLReviewThreads struct {
	Nodes []graphQLReviewThread `json:"nodes"`
}

type graphQLReviewThread struct {
	ID         string          `json:"id"`
	IsResolved bool            `json:"isResolved"`
	Comments   graphQLComments `json:"comments"`
}

type graphQLPullRequestsResponse struct {
	Data struct {
		ReviewRequested graphQLSearchResult `json:"reviewRequested"`
		Authored        graphQLSearchResult `json:"authored"`
		Participated    graphQLSearchResult `json:"participated"`
	} `json:"data"`
}

type graphQLSearchResult struct {
	Nodes []searchPullRequest `json:"nodes"`
}

const pullRequestsGraphQLQuery = `query($reviewQuery: String!, $authoredQuery: String!, $participatedQuery: String!) {
  reviewRequested: search(query: $reviewQuery, type: ISSUE, first: 50) {
    nodes {
      ... on PullRequest {
        number
        title
        url
        state
        isDraft
        headRefName
        body
        author { login }
        repository { nameWithOwner }
        reviewThreads(first: 25) { nodes { id isResolved comments(first: 25) { nodes { author { login } createdAt } } } }
      }
    }
  }
  authored: search(query: $authoredQuery, type: ISSUE, first: 50) {
    nodes {
      ... on PullRequest {
        number
        title
        url
        state
        isDraft
        headRefName
        body
        author { login }
        repository { nameWithOwner }
        comments(first: 50, orderBy: {field: UPDATED_AT, direction: DESC}) { nodes { author { login } createdAt } }
        reviews(first: 50) { nodes { author { login } createdAt } }
        reviewThreads(first: 25) { nodes { id isResolved comments(first: 25) { nodes { author { login } createdAt } } } }
      }
    }
  }
  participated: search(query: $participatedQuery, type: ISSUE, first: 50) {
    nodes {
      ... on PullRequest {
        number
        title
        url
        state
        isDraft
        headRefName
        body
        repository { nameWithOwner }
        reviewThreads(first: 25) { nodes { id isResolved comments(first: 25) { nodes { author { login } createdAt } } } }
      }
    }
  }
}`

func FetchPullRequests(ctx context.Context, previous []protocol.Task, logger *slog.Logger) ([]protocol.Task, []protocol.Task, []protocol.Task, error) {
	var response graphQLPullRequestsResponse
	args := []string{
		"api", "graphql",
		"-f", "query=" + pullRequestsGraphQLQuery,
		"-F", "reviewQuery=is:pr is:open review-requested:@me",
		"-F", "authoredQuery=is:pr is:open author:@me",
		"-F", "participatedQuery=is:pr is:open commenter:@me -author:@me",
	}
	if err := ghJSON(ctx, args, &response); err != nil {
		return nil, nil, nil, fmt.Errorf("fetch github pull requests: %w", err)
	}
	login, err := currentLogin(ctx)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("fetch github current user: %w", err)
	}

	previousActivity := activityStateFromPrevious(previous)
	reviewItems := reviewRequestTasks(response.Data.ReviewRequested.Nodes)
	authoredItems := authoredPullRequestTasks(response.Data.Authored.Nodes)
	activityItems := activityPullRequestItems(response.Data.Participated.Nodes, login, previousActivity)
	applyActivity(authoredItems, response.Data.Authored.Nodes, login, previousActivity, true)
	applyActivity(reviewItems, response.Data.ReviewRequested.Nodes, login, previousActivity, false)
	logger.Debug(
		"fetched github pull requests",
		"review_requested", len(reviewItems),
		"authored", len(authoredItems),
		"activity", len(activityItems),
	)
	return reviewItems, authoredItems, activityItems, nil
}

func FetchReviewRequests(ctx context.Context, logger *slog.Logger) ([]protocol.Task, error) {
	prs, err := fetchReviewRequestedPullRequests(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetch review requested pull requests: %w", err)
	}
	logger.Debug("fetched github review requested pull requests", "count", len(prs))

	items := reviewRequestTasks(prs)
	logger.Debug("github review requests classified", "count", len(items))
	return items, nil
}

func FetchAuthoredPullRequests(ctx context.Context, logger *slog.Logger) ([]protocol.Task, error) {
	prs, err := fetchAuthoredPullRequests(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetch authored pull requests: %w", err)
	}
	logger.Debug("fetched authored github pull requests", "count", len(prs))

	items := authoredPullRequestTasks(prs)
	logger.Debug("authored github pull requests classified", "count", len(items))
	return items, nil
}

func reviewRequestTasks(prs []searchPullRequest) []protocol.Task {
	items := make([]protocol.Task, 0, len(prs))
	for _, pr := range prs {
		repo := repoName(pr)
		item := protocol.Task{
			Kind:      "github_review_request",
			Title:     pr.Title,
			Repo:      repo,
			URL:       pr.URL,
			Attention: "attention",
			Reason:    "review requested",
			Metadata:  pullRequestMetadata(pr),
		}
		item.SourceRefs = []protocol.SourceRef{newGitHubPullRequestSourceRef(item, repo, pr.Number, "pull_request", pr.HeadRefName, pr.Body)}
		items = append(items, item)
	}
	return items
}

func authoredPullRequestTasks(prs []searchPullRequest) []protocol.Task {
	items := make([]protocol.Task, 0, len(prs))
	for _, pr := range prs {
		repo := repoName(pr)
		reason := "open PR"
		if pr.Draft {
			reason = "draft PR"
		}

		item := protocol.Task{
			Kind:      "github_own_pr",
			Title:     pr.Title,
			Repo:      repo,
			URL:       pr.URL,
			Attention: "in_progress",
			Reason:    reason,
			Metadata:  pullRequestMetadata(pr),
		}
		item.SourceRefs = []protocol.SourceRef{newGitHubPullRequestSourceRef(item, repo, pr.Number, "pull_request", pr.HeadRefName, pr.Body)}
		items = append(items, item)
	}
	return items
}

func PullRequestCollectionSummary(reviewCount int, authoredCount int, trackedCount int) string {
	return fmt.Sprintf("%d review requests, %d authored PRs, %d tracked PRs", reviewCount, authoredCount, trackedCount)
}

type pullRequestActivity struct {
	unresolvedReviewThreads int
	newGeneralComments      int
	latestGeneralCommentAt  string
}

type previousPullRequestActivity struct {
	generalCommentsAckAt string
}

func activityPullRequestItems(prs []searchPullRequest, login string, previous map[string]previousPullRequestActivity) []protocol.Task {
	items := make([]protocol.Task, 0)
	for _, pr := range prs {
		activity := detectActivity(pr, login, previous[prKey(repoName(pr), pr.Number)], false)
		if !activity.needsAttention() {
			continue
		}
		repo := repoName(pr)
		item := protocol.Task{
			Kind:      "github_pr_activity",
			Title:     pr.Title,
			Repo:      repo,
			URL:       pr.URL,
			Attention: "attention",
			Reason:    activity.reason(),
		}
		item.SourceRefs = []protocol.SourceRef{newGitHubPullRequestSourceRef(item, repo, pr.Number, "pull_request", pr.HeadRefName, pr.Body)}
		setActivityMetadata(&item.SourceRefs[0], activity, previous[prKey(repo, pr.Number)])
		items = append(items, item)
	}
	return items
}

func applyActivity(items []protocol.Task, prs []searchPullRequest, login string, previous map[string]previousPullRequestActivity, authored bool) {
	activityByKey := map[string]pullRequestActivity{}
	for _, pr := range prs {
		key := prKey(repoName(pr), pr.Number)
		activityByKey[key] = detectActivity(pr, login, previous[key], authored)
	}
	for i := range items {
		if i >= len(prs) || len(items[i].SourceRefs) == 0 {
			continue
		}
		repo := repoName(prs[i])
		number := prs[i].Number
		key := prKey(repo, number)
		activity := activityByKey[key]
		if !activity.needsAttention() {
			if previous[key].generalCommentsAckAt != "" {
				setActivityMetadata(&items[i].SourceRefs[0], activity, previous[key])
			}
			continue
		}
		if items[i].SourceRefs[0].Metadata == nil {
			items[i].SourceRefs[0].Metadata = map[string]string{}
		}
		items[i].SourceRefs[0].Metadata["base_reason"] = items[i].Reason
		items[i].Attention = "attention"
		items[i].Reason = activity.reason()
		items[i].SourceRefs[0].Status = items[i].Reason
		setActivityMetadata(&items[i].SourceRefs[0], activity, previous[key])
	}
}

func detectActivity(pr searchPullRequest, login string, previous previousPullRequestActivity, authored bool) pullRequestActivity {
	activity := pullRequestActivity{}
	for _, thread := range pr.ReviewThreads.Nodes {
		if thread.IsResolved || !relevantReviewThread(thread, login, authored) {
			continue
		}
		activity.unresolvedReviewThreads++
	}
	if authored {
		for _, comment := range append(pr.Comments.Nodes, pr.Reviews.Nodes...) {
			if strings.EqualFold(comment.Author.Login, login) || comment.CreatedAt == "" || comment.CreatedAt <= previous.generalCommentsAckAt {
				continue
			}
			activity.newGeneralComments++
			if comment.CreatedAt > activity.latestGeneralCommentAt {
				activity.latestGeneralCommentAt = comment.CreatedAt
			}
		}
	}
	return activity
}

func relevantReviewThread(thread graphQLReviewThread, login string, authored bool) bool {
	latestMine := ""
	latestOther := ""
	for _, comment := range thread.Comments.Nodes {
		if strings.EqualFold(comment.Author.Login, login) {
			if comment.CreatedAt > latestMine {
				latestMine = comment.CreatedAt
			}
		} else if comment.Author.Login != "" && comment.CreatedAt > latestOther {
			latestOther = comment.CreatedAt
		}
	}
	if authored {
		return latestOther != ""
	}
	return latestMine != "" && latestOther > latestMine
}

func (activity pullRequestActivity) needsAttention() bool {
	return activity.unresolvedReviewThreads > 0 || activity.newGeneralComments > 0
}

func (activity pullRequestActivity) reason() string {
	parts := make([]string, 0, 2)
	if activity.unresolvedReviewThreads > 0 {
		parts = append(parts, fmt.Sprintf("%d unresolved review thread(s)", activity.unresolvedReviewThreads))
	}
	if activity.newGeneralComments > 0 {
		parts = append(parts, fmt.Sprintf("%d new PR comment(s)", activity.newGeneralComments))
	}
	return strings.Join(parts, ", ")
}

func setActivityMetadata(sourceRef *protocol.SourceRef, activity pullRequestActivity, previous previousPullRequestActivity) {
	if sourceRef.Metadata == nil {
		sourceRef.Metadata = map[string]string{}
	}
	if activity.unresolvedReviewThreads > 0 {
		sourceRef.Metadata["unresolved_review_threads"] = strconv.Itoa(activity.unresolvedReviewThreads)
	}
	if activity.newGeneralComments > 0 {
		sourceRef.Metadata["new_general_comments"] = strconv.Itoa(activity.newGeneralComments)
		sourceRef.Metadata["latest_general_comment_at"] = activity.latestGeneralCommentAt
	}
}

func repoName(pr searchPullRequest) string {
	if pr.Repository.FullName != "" {
		return pr.Repository.FullName
	}
	return pr.Repository.NameWithOwner
}

func pullRequestMetadata(pr searchPullRequest) map[string]string {
	if pr.Author == nil || pr.Author.Login == "" {
		return nil
	}
	return map[string]string{"author": pr.Author.Login}
}

func activityStateFromPrevious(previous []protocol.Task) map[string]previousPullRequestActivity {
	state := map[string]previousPullRequestActivity{}
	for _, item := range previous {
		ack := ""
		if item.Metadata != nil {
			ack = item.Metadata["general_comments_ack_at"]
		}
		if ack == "" {
			continue
		}
		for _, sourceRef := range item.SourceRefs {
			repo, number, ok := parsePullRequestSourceRefID(sourceRef.ID)
			if !ok {
				continue
			}
			key := prKey(repo, number)
			if ack > state[key].generalCommentsAckAt {
				state[key] = previousPullRequestActivity{generalCommentsAckAt: ack}
			}
		}
	}
	return state
}

func prKey(repo string, number int) string {
	return fmt.Sprintf("%s:%d", repo, number)
}

func parsePullRequestSourceRefID(id string) (string, int, bool) {
	value, ok := strings.CutPrefix(id, "github:pr:")
	if !ok {
		return "", 0, false
	}
	idx := strings.LastIndex(value, ":")
	if idx <= 0 || idx == len(value)-1 {
		return "", 0, false
	}
	number, err := strconv.Atoi(value[idx+1:])
	if err != nil {
		return "", 0, false
	}
	return value[:idx], number, true
}

func ResolveDonePullRequests(ctx context.Context, previous []protocol.Task, active []protocol.Task, authoredComplete bool, logger *slog.Logger) []protocol.Task {
	if !authoredComplete {
		logger.Debug("skipping github done resolution; authored PR collection was incomplete")
		return keepTodaysDoneTasks(nil, previous)
	}
	if !EnsureCoreBudget(ctx, logger) {
		return keepTodaysDoneTasks(nil, previous)
	}

	activePullRequests := make(map[string]bool, len(active))
	for _, item := range active {
		if sourceRef, ok := githubPullRequestSourceRef(item); ok {
			activePullRequests[sourceRef.ID] = true
		}
	}

	items := keepTodaysDoneTasks(nil, previous)
	seenDone := doneTaskIDs(items)
	for _, item := range previous {
		if item.Attention == "done" {
			continue
		}

		sourceRef, ok := githubPullRequestSourceRef(item)
		if !ok || activePullRequests[sourceRef.ID] {
			continue
		}
		repo, number, ok := parsePullRequestSourceRefID(sourceRef.ID)
		if !ok {
			logger.Warn("could not parse github PR source ref id", "id", sourceRef.ID)
			continue
		}

		pr, err := fetchPullRequest(ctx, fmt.Sprintf("/repos/%s/pulls/%d", repo, number))
		if err != nil {
			logger.Warn("could not resolve previous github PR", "id", item.ID, "error", err)
			continue
		}
		if pr.State != "closed" || !isToday(pr.ClosedAt) {
			continue
		}

		reason := "closed today"
		if pr.Merged {
			reason = "merged today"
		}
		done := item
		done.Attention = "done"
		done.Reason = reason
		done.DoneAt = pr.ClosedAt
		done.SourceRefs = donePullRequestSourceRefs(item.SourceRefs, repo, number, reason)
		if id := doneTaskID(done); id != "" && seenDone[id] {
			continue
		}
		if id := doneTaskID(done); id != "" {
			seenDone[id] = true
		}
		items = append(items, done)
	}

	logger.Debug("resolved done github pull requests", "count", len(items))
	return items
}

func donePullRequestSourceRefs(sourceRefs []protocol.SourceRef, repo string, number int, reason string) []protocol.SourceRef {
	id := fmt.Sprintf("github:pr:%s:%d", repo, number)
	updated := make([]protocol.SourceRef, 0, len(sourceRefs)+1)
	seen := false
	for _, sourceRef := range sourceRefs {
		if sourceRef.ID == id {
			sourceRef = githubPullRequestRef(id, repo, number, sourceRef.Title, sourceRef.URL, reason, sourceRef.Branch)
			seen = true
		}
		updated = append(updated, sourceRef)
	}
	if !seen {
		updated = append(updated, githubPullRequestRef(id, repo, 0, "", "", reason, ""))
	}
	return updated
}

func keepTodaysDoneTasks(items []protocol.Task, previous []protocol.Task) []protocol.Task {
	seen := doneTaskIDs(items)
	for _, item := range previous {
		if item.Attention != "done" || !isToday(item.DoneAt) || !hasSource(item, "github") {
			continue
		}
		id := doneTaskID(item)
		if id != "" && seen[id] {
			continue
		}
		if id != "" {
			seen[id] = true
		}
		items = append(items, item)
	}
	return items
}

func doneTaskIDs(items []protocol.Task) map[string]bool {
	seen := map[string]bool{}
	for _, item := range items {
		if id := doneTaskID(item); id != "" {
			seen[id] = true
		}
	}
	return seen
}

func doneTaskID(item protocol.Task) string {
	for _, sourceRef := range item.SourceRefs {
		if sourceRef.Source == "github" && sourceRef.ID != "" {
			return sourceRef.ID
		}
	}
	return ""
}

func hasSource(item protocol.Task, source string) bool {
	for _, sourceRef := range item.SourceRefs {
		if sourceRef.Source == source {
			return true
		}
	}
	return false
}

func newGitHubPullRequestSourceRef(item protocol.Task, repo string, number int, kind string, branch string, body string) protocol.SourceRef {
	sourceRef := githubPullRequestRef(fmt.Sprintf("github:pr:%s:%d", repo, number), repo, number, item.Title, item.URL, item.Reason, branch)
	if kind != "" {
		sourceRef.Kind = kind
	}
	if body != "" {
		sourceRef.Metadata = map[string]string{"body": body}
	}
	return sourceRef
}

func githubPullRequestRef(id string, repo string, number int, title string, url string, status string, branch string) protocol.SourceRef {
	if id == "" && repo != "" && number != 0 {
		id = fmt.Sprintf("github:pr:%s:%d", repo, number)
	}
	return protocol.SourceRef{
		ID:           id,
		Source:       "github",
		Kind:         "pull_request",
		Title:        title,
		Repo:         repo,
		URL:          url,
		Branch:       branch,
		Status:       status,
		CanonicalKey: id,
		LinkingKeys:  linking.Keys(append(linking.TicketKeys(title, branch, repo, url), id, linking.BranchKey(repo, branch))...),
	}
}

func githubPullRequestSourceRef(task protocol.Task) (protocol.SourceRef, bool) {
	for _, sourceRef := range task.SourceRefs {
		if sourceRef.Source == "github" && sourceRef.Kind == "pull_request" {
			return sourceRef, true
		}
	}
	return protocol.SourceRef{}, false
}

func currentLogin(ctx context.Context) (string, error) {
	var u user
	if err := ghJSON(ctx, []string{"api", "user"}, &u); err != nil {
		return "", err
	}
	return u.Login, nil
}

func fetchNotifications(ctx context.Context) ([]notification, error) {
	var notifications []notification
	if err := ghJSON(ctx, []string{"api", "/notifications?all=true&per_page=100"}, &notifications); err != nil {
		return nil, err
	}
	return notifications, nil
}

func fetchReviewRequestedPullRequests(ctx context.Context) ([]searchPullRequest, error) {
	var prs []searchPullRequest
	args := []string{
		"search", "prs",
		"--review-requested", "@me",
		"--state", "open",
		"--limit", "100",
		"--json", "number,title,url,repository,isDraft,state,body,author",
	}
	if err := ghJSON(ctx, args, &prs); err != nil {
		return nil, err
	}
	return prs, nil
}

func fetchAuthoredPullRequests(ctx context.Context) ([]searchPullRequest, error) {
	var prs []searchPullRequest
	args := []string{
		"search", "prs",
		"--author", "@me",
		"--state", "open",
		"--limit", "100",
		"--json", "number,title,url,repository,isDraft,state,body,author",
	}
	if err := ghJSON(ctx, args, &prs); err != nil {
		return nil, err
	}
	return prs, nil
}

func fetchPullRequest(ctx context.Context, apiURL string) (pullRequest, error) {
	var pr pullRequest
	path, err := apiPath(apiURL)
	if err != nil {
		return pr, err
	}
	if err := ghJSON(ctx, []string{"api", path}, &pr); err != nil {
		return pr, err
	}
	return pr, nil
}

func isToday(value string) bool {
	if value == "" {
		return false
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return strings.HasPrefix(value, time.Now().Format("2006-01-02"))
	}
	now := time.Now()
	y1, m1, d1 := parsed.Local().Date()
	y2, m2, d2 := now.Local().Date()
	return y1 == y2 && m1 == m2 && d1 == d2
}

func reviewRequestedFrom(login string, pr pullRequest) bool {
	for _, reviewer := range pr.RequestedReviewers {
		if strings.EqualFold(reviewer.Login, login) {
			return true
		}
	}
	return false
}

func apiPath(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if parsed.Path == "" {
		return "", fmt.Errorf("missing path in url %q", raw)
	}
	return parsed.Path, nil
}

func ghJSON(ctx context.Context, args []string, target any) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "gh", args...)
	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			stderr := strings.TrimSpace(string(exitErr.Stderr))
			if strings.Contains(stderr, "API rate limit exceeded") || strings.Contains(stderr, "secondary rate limit") {
				until := time.Now().Add(15 * time.Minute)
				if len(args) > 0 && args[0] == "search" {
					PauseSearch(until)
				} else {
					PauseCore(until)
				}
			}
			return fmt.Errorf("gh %s failed: %s", strings.Join(args, " "), stderr)
		}
		return err
	}
	if err := json.Unmarshal(output, target); err != nil {
		return fmt.Errorf("decode gh output: %w", err)
	}
	return nil
}
