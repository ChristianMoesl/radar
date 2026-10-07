package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"radar/internal/integration/github/filters"
	"radar/internal/integration/github/settings"
	"radar/internal/protocol"
)

const repoCacheTTL = 24 * time.Hour

type repoCacheFile struct {
	Orgs map[string]repoCacheOrg `json:"orgs"`
}
type repoCacheOrg struct {
	FetchedAt string   `json:"fetched_at"`
	Repos     []string `json:"repos"`
}
type trackedPullRequestCacheFile struct {
	Targets map[string]trackedPullRequestCacheEntry `json:"targets"`
}
type trackedPullRequestCacheEntry struct {
	FetchedAt string              `json:"fetched_at"`
	PRs       []searchPullRequest `json:"prs"`
}
type pageInfo struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}
type graphQLErrors struct {
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

func (e graphQLErrors) err() error {
	if len(e.Errors) > 0 {
		return fmt.Errorf("github graphql: %s", e.Errors[0].Message)
	}
	return nil
}

// FetchTrackedPullRequests enumerates concrete repositories before fetching PRs.
// Repository connections have no search-result ceiling, and every page includes
// the branch/author facts needed for linking and policy. Tracking is additive;
// it never subscribes the viewer to otherwise irrelevant discussions.
func FetchTrackedPullRequests(ctx context.Context, scopes []settings.TrackingScope, logger *slog.Logger) ([]protocol.Task, error) {
	if len(scopes) == 0 {
		return nil, nil
	}
	var repoCache repoCacheFile
	var cache trackedPullRequestCacheFile
	_ = readGitHubCache("github-repos.json", &repoCache)
	_ = readGitHubCache("github-tracked-prs.json", &cache)
	if repoCache.Orgs == nil {
		repoCache.Orgs = map[string]repoCacheOrg{}
	}
	if cache.Targets == nil {
		cache.Targets = map[string]trackedPullRequestCacheEntry{}
	}
	repositories := map[string]bool{}
	owners := map[string][]string{}
	var failures []error
	repoCacheChanged := false
	for _, scope := range scopes {
		for _, pattern := range scope.Repos {
			pattern = strings.TrimSpace(pattern)
			if !strings.Contains(pattern, "*") {
				repositories[strings.ToLower(pattern)] = true
				continue
			}
			owner, _, _ := strings.Cut(pattern, "/")
			owner = strings.ToLower(owner)
			repos, found := owners[owner]
			if !found {
				var changed bool
				var err error
				repos, changed, err = cachedOwnerRepos(ctx, owner, &repoCache, logger)
				repoCacheChanged = repoCacheChanged || changed
				if err != nil {
					failures = append(failures, err)
				}
				owners[owner] = repos
			}
			for _, repo := range repos {
				if filters.MatchPattern(pattern, repo) {
					repositories[strings.ToLower(repo)] = true
				}
			}
		}
	}
	if repoCacheChanged {
		if err := writeGitHubCache("github-repos.json", repoCache); err != nil {
			logger.Warn("could not write github repository cache", "error", err)
		}
	}
	ordered := make([]string, 0, len(repositories))
	for repo := range repositories {
		ordered = append(ordered, repo)
	}
	sort.Strings(ordered)
	items := make([]protocol.Task, 0)
	cacheChanged := false
	for _, repo := range ordered {
		prs, changed, err := cachedRepositoryPullRequests(ctx, repo, &cache, logger)
		cacheChanged = cacheChanged || changed
		if err != nil {
			failures = append(failures, err)
		}
		for _, pr := range prs {
			if trackedByScopes(pr, scopes) {
				items = append(items, trackedPullRequestTask(pr))
			}
		}
	}
	if cacheChanged {
		if err := writeGitHubCache("github-tracked-prs.json", cache); err != nil {
			logger.Warn("could not write github tracked PR cache", "error", err)
		}
	}
	return items, errors.Join(failures...)
}

func trackedByScopes(pr searchPullRequest, scopes []settings.TrackingScope) bool {
	for _, scope := range scopes {
		repoMatches := false
		for _, pattern := range scope.Repos {
			repoMatches = repoMatches || filters.MatchPattern(pattern, repoName(pr))
		}
		if !repoMatches {
			continue
		}
		if len(scope.Authors) == 0 {
			return true
		}
		if pr.Author == nil {
			continue
		}
		for _, author := range scope.Authors {
			for _, alias := range githubActorAliases(*pr.Author) {
				if filters.MatchPattern(author, alias) {
					return true
				}
			}
		}
	}
	return false
}

const trackedRepositoriesQuery = `query RadarTrackedRepositories($owner: String!, $cursor: String) {
  repositoryOwner(login: $owner) {
    repositories(first: 100, after: $cursor) {
      nodes { nameWithOwner }
      pageInfo { hasNextPage endCursor }
    }
  }
}`

func cachedOwnerRepos(ctx context.Context, owner string, cache *repoCacheFile, logger *slog.Logger) ([]string, bool, error) {
	entry := cache.Orgs[owner]
	if fetched, err := time.Parse(time.RFC3339, entry.FetchedAt); err == nil && time.Since(fetched) < repoCacheTTL {
		return entry.Repos, false, nil
	}
	repos, err := fetchOwnerRepos(ctx, owner, logger)
	if err != nil {
		return entry.Repos, false, err
	}
	cache.Orgs[owner] = repoCacheOrg{FetchedAt: time.Now().UTC().Format(time.RFC3339), Repos: repos}
	return repos, true, nil
}

func fetchOwnerRepos(ctx context.Context, owner string, logger *slog.Logger) ([]string, error) {
	var repos []string
	cursor := ""
	seen := map[string]bool{}
	for {
		var response struct {
			graphQLErrors
			Data struct {
				Owner *struct {
					Repositories struct {
						Nodes []struct {
							Name string `json:"nameWithOwner"`
						} `json:"nodes"`
						Page *pageInfo `json:"pageInfo"`
					} `json:"repositories"`
				} `json:"repositoryOwner"`
			} `json:"data"`
		}
		if err := trackedGraphQL(ctx, trackedRepositoriesQuery, cursor, []string{"-f", "owner=" + owner}, &response, logger); err != nil {
			return nil, err
		}
		if err := response.err(); err != nil {
			return nil, err
		}
		if response.Data.Owner == nil {
			return nil, fmt.Errorf("github repository owner %s is unavailable", owner)
		}
		page := response.Data.Owner.Repositories
		if page.Page == nil || page.Nodes == nil {
			return nil, fmt.Errorf("incomplete repository listing for %s", owner)
		}
		for _, node := range page.Nodes {
			repoOwner, name, valid := strings.Cut(node.Name, "/")
			if !valid || repoOwner == "" || name == "" {
				return nil, fmt.Errorf("invalid repository identity in %s listing", owner)
			}
			if strings.EqualFold(repoOwner, owner) {
				repos = append(repos, node.Name)
			}
		}
		if !page.Page.HasNextPage {
			break
		}
		next := page.Page.EndCursor
		if next == "" || seen[next] {
			return nil, fmt.Errorf("incomplete github repository pagination for %s", owner)
		}
		seen[next], cursor = true, next
	}
	sort.Strings(repos)
	return repos, nil
}

const trackedPullRequestsQuery = `query RadarTrackedPullRequests($owner: String!, $name: String!, $cursor: String) {
  repository(owner: $owner, name: $name) {
    pullRequests(states: OPEN, first: 100, after: $cursor, orderBy: {field: CREATED_AT, direction: ASC}) {
      nodes {
        id number title url state isDraft headRefName body
        author { __typename login }
        repository { nameWithOwner }
      }
      pageInfo { hasNextPage endCursor }
    }
  }
}`

func cachedRepositoryPullRequests(ctx context.Context, repo string, cache *trackedPullRequestCacheFile, logger *slog.Logger) ([]searchPullRequest, bool, error) {
	if cache.Targets == nil {
		cache.Targets = map[string]trackedPullRequestCacheEntry{}
	}
	key := strings.ToLower(repo)
	prs, err := fetchRepositoryPullRequests(ctx, repo, logger)
	if err != nil {
		// Stale observations are useful, but never count as a complete refresh.
		return cache.Targets[key].PRs, false, fmt.Errorf("track %s: %w", repo, err)
	}
	cache.Targets[key] = trackedPullRequestCacheEntry{FetchedAt: time.Now().UTC().Format(time.RFC3339), PRs: prs}
	return prs, true, nil
}

func fetchRepositoryPullRequests(ctx context.Context, repo string, logger *slog.Logger) ([]searchPullRequest, error) {
	owner, name, _ := strings.Cut(repo, "/")
	var prs []searchPullRequest
	cursor := ""
	seen := map[string]bool{}
	seenPRs := map[int]bool{}
	for {
		var response struct {
			graphQLErrors
			Data struct {
				Repository *struct {
					PullRequests struct {
						Nodes []searchPullRequest `json:"nodes"`
						Page  *pageInfo           `json:"pageInfo"`
					} `json:"pullRequests"`
				} `json:"repository"`
			} `json:"data"`
		}
		if err := trackedGraphQL(ctx, trackedPullRequestsQuery, cursor, []string{"-f", "owner=" + owner, "-f", "name=" + name}, &response, logger); err != nil {
			return nil, err
		}
		if err := response.err(); err != nil {
			return nil, err
		}
		if response.Data.Repository == nil {
			return nil, fmt.Errorf("repository is missing or inaccessible")
		}
		page := response.Data.Repository.PullRequests
		if page.Page == nil || page.Nodes == nil {
			return nil, fmt.Errorf("incomplete pull request page for %s", repo)
		}
		for _, pr := range page.Nodes {
			if !strings.EqualFold(repoName(pr), repo) || pr.Number <= 0 || pr.HeadRefName == "" || pr.State != "OPEN" {
				return nil, fmt.Errorf("incomplete pull request identity/branch/state in %s", repo)
			}
			if !seenPRs[pr.Number] {
				prs = append(prs, pr)
				seenPRs[pr.Number] = true
			}
		}
		if !page.Page.HasNextPage {
			break
		}
		next := page.Page.EndCursor
		if next == "" || seen[next] {
			return nil, fmt.Errorf("incomplete github pull request pagination for %s", repo)
		}
		seen[next], cursor = true, next
	}
	return prs, nil
}

func trackedGraphQL(ctx context.Context, query, cursor string, variables []string, target any, logger *slog.Logger) error {
	if !EnsureGraphQLBudget(ctx, logger) {
		return fmt.Errorf("github graphql budget unavailable")
	}
	args := append([]string{"api", "graphql", "-f", "query=" + query}, variables...)
	if cursor != "" {
		args = append(args, "-f", "cursor="+cursor)
	}
	return ghJSON(ctx, args, target)
}

func readGitHubCache(name string, value any) error {
	base, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(base, "radar", name))
	if err != nil {
		return err
	}
	return json.Unmarshal(data, value)
}
func writeGitHubCache(name string, value any) error {
	base, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(base, "radar")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, name+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(append(data, '\n')); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(dir, name))
}

func trackedPullRequestTask(pr searchPullRequest) protocol.Task {
	repo := repoName(pr)
	reason := "tracked PR"
	if pr.Draft {
		reason = "tracked draft PR"
	}
	item := protocol.Task{Kind: "github_tracked_pr", Title: pr.Title, Repo: repo, URL: pr.URL,
		Attention: "in_progress", Reason: reason, Metadata: pullRequestMetadata(pr)}
	item.SourceRefs = []protocol.SourceRef{newGitHubPullRequestSourceRef(item, repo, pr.Number, "pull_request", pr.HeadRefName, pr.Body)}
	return item
}
