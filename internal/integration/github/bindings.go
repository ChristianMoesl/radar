package github

import (
	"context"
	"fmt"
	"strings"
	"time"

	"radar/internal/config"
	"radar/internal/integration"
	"radar/internal/integration/github/filters"
	"radar/internal/integration/github/identity"
	"radar/internal/linking"
	"radar/internal/protocol"
)

// Bound PRs are read independently of assigned/review/activity searches. Include
// the same activity facts as ordinary discovery so binding does not bypass actor
// filters or turn an old review request into a current attention signal.
const boundPullRequestQuery = `query($owner: String!, $name: String!, $number: Int!) {
  viewer { login }
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      number title url state isDraft closedAt headRefName body
      author { login }
      repository { nameWithOwner }
      reviewRequests(first: 50) { nodes { requestedReviewer { ... on User { login } } } }
      comments(first: 50, orderBy: {field: UPDATED_AT, direction: DESC}) { nodes { author { __typename login } createdAt } }
      reviews(first: 50) { nodes { author { __typename login } createdAt } }
      reviewThreads(first: 25) { nodes { id isResolved comments(first: 25) { nodes { author { __typename login } createdAt } } } }
    }
  }
}`

type boundPullRequest struct {
	searchPullRequest
	ReviewRequests struct {
		Nodes []struct {
			RequestedReviewer user `json:"requestedReviewer"`
		} `json:"nodes"`
	} `json:"reviewRequests"`
}

type boundPullRequestResponse struct {
	Data struct {
		Viewer     user `json:"viewer"`
		Repository *struct {
			PullRequest *boundPullRequest `json:"pullRequest"`
		} `json:"repository"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

func (Source) ResolveBindings(ctx context.Context, req integration.BindingRequest) integration.CollectResult {
	result := integration.CollectResult{Complete: true}
	if len(req.Bindings) == 0 {
		return result
	}
	cfg, err := config.Load()
	if err != nil {
		bindingFailure(&result, req, err)
		return result
	}
	if availability := integration.OptionalStatus("github", cfg.GitHub.Enabled, ""); !availability.CanRun {
		return integration.CollectResult{SourceStatus: &availability.Status}
	}

	seen := map[string]bool{}
	for _, binding := range req.Bindings {
		repo, number, ok := boundPullRequestIdentity(binding)
		if !ok {
			bindingFailure(&result, req, fmt.Errorf("invalid pull request binding %q", binding.ID))
			continue
		}
		if seen[binding.ID] {
			continue
		}
		seen[binding.ID] = true
		// Main-search observations are actual API results even when an
		// independent rule search fails. A freshly reopened PR must always win
		// over an older cached terminal fact.
		if observedBoundPullRequest(req.Result.Observations, binding.ID) {
			continue
		}
		if ref, ok := cachedBoundPullRequest(req.Previous, binding.ID); ok {
			tasks := []protocol.Task{{Attention: "done", Reason: ref.Status, SourceRefs: []protocol.SourceRef{ref}}}
			applyLinkingMarks(tasks, req.LinkingMarks)
			result.Observations = append(result.Observations, observationsFromTasks(tasks)...)
			continue
		}
		if !EnsureGraphQLBudget(ctx, req.Logger) {
			bindingFailure(&result, req, fmt.Errorf("pull request %s lookup budget unavailable", binding.ID))
			continue
		}
		pr, login, err := fetchBoundPullRequest(ctx, repo, number)
		if err != nil {
			bindingFailure(&result, req, fmt.Errorf("resolve %s: %w", binding.ID, err))
			continue
		}
		task := boundPullRequestTask(pr, login, req.Previous, cfg.GitHub.Filters)
		// GitHub repository names are case-insensitive. Keep the authored identity
		// when the API returns a different capitalization of the same repository.
		task.SourceRefs[0].ID = binding.ID
		task.SourceRefs[0].EntityID = binding.ID
		task.SourceRefs[0].CanonicalKey = binding.ID
		task.SourceRefs[0].LinkingKeys = linking.Keys(append([]string{binding.ID}, task.SourceRefs[0].LinkingKeys...)...)
		tasks := []protocol.Task{task}
		applyLinkingMarks(tasks, req.LinkingMarks)
		result.Observations = append(result.Observations, observationsFromTasks(tasks)...)
	}
	return result
}

func boundPullRequestIdentity(binding protocol.SourceBinding) (string, int, bool) {
	if binding.Source != "github" || binding.Kind != "pull_request" || binding.Key != "" || !binding.WorkItem {
		return "", 0, false
	}
	repo, number, ok := parsePullRequestSourceRefID(binding.ID)
	parts := strings.Split(repo, "/")
	if !ok || number <= 0 || number > 1<<31-1 || len(parts) != 2 || identity.PullRequestKey(repo, number) != binding.ID {
		return "", 0, false
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", 0, false
		}
		for _, r := range part {
			if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_' && r != '.' {
				return "", 0, false
			}
		}
	}
	return repo, number, true
}

func observedBoundPullRequest(observations []integration.Observation, id string) bool {
	for _, observation := range observations {
		ref := observation.Ref
		if ref.ID == id && boundPullRequestRef(ref) {
			return true
		}
	}
	return false
}

func boundPullRequestRef(ref protocol.SourceRef) bool {
	return ref.Source == "github" && ref.Kind == "pull_request" && ref.Role == protocol.SourceRefRoleAuthoritative &&
		ref.Lifecycle == protocol.SourceRefLifecycleWorkItem && ref.Authority == protocol.SourceRefAuthorityContributing
}

func cachedBoundPullRequest(tasks []protocol.Task, id string) (protocol.SourceRef, bool) {
	for _, task := range tasks {
		for _, ref := range task.SourceRefs {
			if ref.ID == id && boundPullRequestRef(ref) && ref.Signal == string(integration.SignalDone) {
				return ref, true
			}
		}
	}
	return protocol.SourceRef{}, false
}

func fetchBoundPullRequest(ctx context.Context, repo string, number int) (boundPullRequest, string, error) {
	owner, name, _ := strings.Cut(repo, "/")
	var response boundPullRequestResponse
	args := []string{"api", "graphql", "-f", "query=" + boundPullRequestQuery, "-f", "owner=" + owner, "-f", "name=" + name, "-F", fmt.Sprintf("number=%d", number)}
	if err := ghJSON(ctx, args, &response); err != nil {
		return boundPullRequest{}, "", err
	}
	if len(response.Errors) > 0 {
		return boundPullRequest{}, "", fmt.Errorf("pull request lookup failed: %s", response.Errors[0].Message)
	}
	if response.Data.Repository == nil || response.Data.Repository.PullRequest == nil {
		return boundPullRequest{}, "", fmt.Errorf("pull request not found or inaccessible")
	}
	pr := *response.Data.Repository.PullRequest
	urlRepo, urlNumber, validURL := identity.ParsePullRequestURL(pr.URL)
	state := strings.ToUpper(pr.State)
	if pr.Number != number || !strings.EqualFold(repoName(pr.searchPullRequest), repo) || !validURL || !strings.EqualFold(urlRepo, repo) || urlNumber != number ||
		strings.TrimSpace(pr.Title) == "" || (state != "OPEN" && state != "CLOSED" && state != "MERGED") {
		return boundPullRequest{}, "", fmt.Errorf("pull request response has invalid identity or state")
	}
	login := strings.TrimSpace(response.Data.Viewer.Login)
	if login == "" {
		return boundPullRequest{}, "", fmt.Errorf("pull request lookup returned empty viewer login")
	}
	pr.State = state
	return pr, login, nil
}

func boundPullRequestTask(pr boundPullRequest, login string, previous []protocol.Task, cfg filters.Config) protocol.Task {
	value := pr.searchPullRequest
	if pr.State != "OPEN" {
		reason := "closed"
		if pr.State == "MERGED" {
			reason = "merged"
		}
		if isToday(pr.ClosedAt) {
			reason += " today"
		}
		task := trackedPullRequestTask(value)
		task.Attention, task.Reason = "done", reason
		task.SourceRefs[0].Status = reason
		if _, err := time.Parse(time.RFC3339, pr.ClosedAt); err == nil {
			if task.Metadata == nil {
				task.Metadata = map[string]string{}
			}
			task.Metadata["completed_at"] = pr.ClosedAt
		}
		return task
	}

	authored := pr.Author != nil && strings.EqualFold(pr.Author.Login, login)
	items := []protocol.Task{trackedPullRequestTask(value)}
	if authored {
		items = authoredPullRequestTasks([]searchPullRequest{value})
	} else {
		for _, request := range pr.ReviewRequests.Nodes {
			if strings.EqualFold(request.RequestedReviewer.Login, login) {
				items = reviewRequestTasks([]searchPullRequest{value})
				break
			}
		}
	}
	applyActivity(items, []searchPullRequest{value}, login, activityStateFromPrevious(previous), cfg, authored)
	return items[0]
}

func bindingFailure(result *integration.CollectResult, req integration.BindingRequest, err error) {
	result.Complete = false
	status := protocol.SourceStatus{Name: "github", Status: "error", Detail: "bound pull request resolution failed: " + err.Error()}
	result.SourceStatus = &status
	if req.Logger != nil {
		req.Logger.Warn("github bound pull request resolution failed", "error", err)
	}
}

var _ integration.BoundSourceResolver = Source{}
