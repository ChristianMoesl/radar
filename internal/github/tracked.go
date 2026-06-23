package github

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"radar.nvim/internal/filters"
	"radar.nvim/internal/protocol"
)

const repoCacheTTL = 24 * time.Hour
const trackedPullRequestCacheTTL = 30 * time.Minute

type repoListEntry struct {
	NameWithOwner string `json:"nameWithOwner"`
}

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

func FetchRulePullRequests(ctx context.Context, cfg filters.Config, logger *slog.Logger) ([]protocol.Task, error) {
	targets := trackingTargets(cfg, logger)
	if len(targets) == 0 {
		return nil, nil
	}

	items := make([]protocol.Task, 0)
	seen := map[string]bool{}
	cache, _ := readTrackedPullRequestCache()
	cacheChanged := false
	for _, group := range groupedTrackingTargets(targets) {
		prs, changed, err := cachedSearchPullRequestsByOwnerAndAuthor(ctx, group.owner, group.user, &cache, logger)
		if changed {
			cacheChanged = true
		}
		if err != nil {
			logger.Warn("github tracked pull request search failed", "owner", group.owner, "user", group.user, "error", err)
			break
		}
		for _, pr := range prs {
			if !group.matches(pr) {
				continue
			}
			key := fmt.Sprintf("%s:%d", repoName(pr), pr.Number)
			if seen[key] {
				continue
			}
			seen[key] = true
			items = append(items, trackedPullRequestTask(pr))
		}
	}
	if cacheChanged {
		if err := writeTrackedPullRequestCache(cache); err != nil {
			logger.Warn("could not write github tracked pull request cache", "error", err)
		}
	}
	logger.Debug("fetched github rule pull requests", "count", len(items), "targets", len(targets))
	return items, nil
}

type trackingTarget struct {
	owner       string
	repoPattern string
	user        string
}

type trackingTargetGroup struct {
	owner   string
	user    string
	targets []trackingTarget
}

func (group trackingTargetGroup) matches(pr searchPullRequest) bool {
	if pr.Author == nil || !filters.MatchPattern(group.user, pr.Author.Login) {
		return false
	}
	for _, target := range group.targets {
		if filters.MatchPattern(target.repoPattern, repoName(pr)) {
			return true
		}
	}
	return false
}

func trackingTargets(cfg filters.Config, logger *slog.Logger) []trackingTarget {
	targets := make([]trackingTarget, 0)
	seen := map[string]bool{}
	for _, rule := range cfg.Rules {
		action := strings.ToLower(strings.TrimSpace(rule.Action))
		if action == "" || action == "mute" || len(rule.Repos) == 0 || len(rule.Users) == 0 {
			continue
		}
		for _, user := range exactUsers(rule.Users) {
			for _, repoPattern := range rule.Repos {
				repoPattern = strings.TrimSpace(repoPattern)
				owner, ok := ownerFromRepoPattern(repoPattern)
				if !ok {
					logger.Debug("skipping github rule repo pattern; owner wildcard is not supported", "pattern", repoPattern)
					continue
				}
				key := strings.ToLower(owner + "\x00" + repoPattern + "\x00" + user)
				if seen[key] {
					continue
				}
				seen[key] = true
				targets = append(targets, trackingTarget{owner: owner, repoPattern: repoPattern, user: user})
			}
		}
	}
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].owner == targets[j].owner {
			if targets[i].user == targets[j].user {
				return targets[i].repoPattern < targets[j].repoPattern
			}
			return targets[i].user < targets[j].user
		}
		return targets[i].owner < targets[j].owner
	})
	return targets
}

func groupedTrackingTargets(targets []trackingTarget) []trackingTargetGroup {
	byKey := map[string]int{}
	groups := make([]trackingTargetGroup, 0)
	for _, target := range targets {
		key := strings.ToLower(target.owner + "\x00" + target.user)
		index, ok := byKey[key]
		if !ok {
			index = len(groups)
			byKey[key] = index
			groups = append(groups, trackingTargetGroup{owner: target.owner, user: target.user})
		}
		groups[index].targets = append(groups[index].targets, target)
	}
	return groups
}

func ownerFromRepoPattern(pattern string) (string, bool) {
	owner, repo, ok := strings.Cut(pattern, "/")
	if !ok || owner == "" || repo == "" || strings.Contains(owner, "*") {
		return "", false
	}
	return owner, true
}

func exactUsers(users []string) []string {
	exact := make([]string, 0, len(users))
	seen := map[string]bool{}
	for _, user := range users {
		user = strings.TrimSpace(user)
		if user == "" || strings.Contains(user, "*") {
			continue
		}
		key := strings.ToLower(user)
		if seen[key] {
			continue
		}
		seen[key] = true
		exact = append(exact, user)
	}
	return exact
}

func expandRepoPatternBestEffort(ctx context.Context, pattern string, logger *slog.Logger) []string {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return nil
	}
	if !strings.Contains(pattern, "*") {
		return []string{pattern}
	}
	owner, _, ok := strings.Cut(pattern, "/")
	if !ok || owner == "" || strings.Contains(owner, "*") {
		logger.Debug("skipping github rule repo pattern; owner wildcard is not supported", "pattern", pattern)
		return nil
	}
	repos, err := cachedOrgRepos(ctx, owner, logger)
	if err != nil {
		logger.Warn("could not expand github repo pattern", "pattern", pattern, "error", err)
		return nil
	}
	matches := make([]string, 0)
	for _, repo := range repos {
		if filters.MatchPattern(pattern, repo) {
			matches = append(matches, repo)
		}
	}
	return matches
}

func cachedOrgRepos(ctx context.Context, org string, logger *slog.Logger) ([]string, error) {
	cache, _ := readRepoCache()
	if cache.Orgs == nil {
		cache.Orgs = map[string]repoCacheOrg{}
	}
	key := strings.ToLower(org)
	if entry, ok := cache.Orgs[key]; ok && !repoCacheExpired(entry) {
		return entry.Repos, nil
	}

	var repos []repoListEntry
	if err := ghJSON(ctx, []string{"repo", "list", org, "--limit", "1000", "--json", "nameWithOwner"}, &repos); err != nil {
		return nil, err
	}
	values := make([]string, 0, len(repos))
	for _, repo := range repos {
		if repo.NameWithOwner != "" {
			values = append(values, repo.NameWithOwner)
		}
	}
	sort.Strings(values)
	cache.Orgs[key] = repoCacheOrg{FetchedAt: time.Now().UTC().Format(time.RFC3339), Repos: values}
	if err := writeRepoCache(cache); err != nil {
		logger.Warn("could not write github repo cache", "error", err)
	}
	return values, nil
}

func repoCacheExpired(entry repoCacheOrg) bool {
	fetchedAt, err := time.Parse(time.RFC3339, entry.FetchedAt)
	return err != nil || time.Since(fetchedAt) > repoCacheTTL
}

func repoCachePath() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "radar", "github-repos.json"), nil
}

func readRepoCache() (repoCacheFile, error) {
	path, err := repoCachePath()
	if err != nil {
		return repoCacheFile{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return repoCacheFile{}, err
	}
	var cache repoCacheFile
	if err := json.Unmarshal(data, &cache); err != nil {
		return repoCacheFile{}, err
	}
	return cache, nil
}

func writeRepoCache(cache repoCacheFile) error {
	path, err := repoCachePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

func cachedSearchPullRequestsByOwnerAndAuthor(ctx context.Context, owner string, author string, cache *trackedPullRequestCacheFile, logger *slog.Logger) ([]searchPullRequest, bool, error) {
	if cache.Targets == nil {
		cache.Targets = map[string]trackedPullRequestCacheEntry{}
	}
	key := trackedPullRequestCacheKey(owner, author)
	entry, hasEntry := cache.Targets[key]
	if hasEntry && !trackedPullRequestCacheExpired(entry) {
		return entry.PRs, false, nil
	}

	if !EnsureSearchBudget(ctx, logger) {
		if hasEntry {
			logger.Debug("using stale github tracked pull request cache", "owner", owner, "user", author)
			return entry.PRs, false, nil
		}
		return nil, false, nil
	}

	prs, err := searchPullRequestsByOwnerAndAuthor(ctx, owner, author)
	if err != nil {
		if hasEntry {
			logger.Debug("using stale github tracked pull request cache after search failure", "owner", owner, "user", author, "error", err)
			return entry.PRs, false, nil
		}
		return nil, false, err
	}
	cache.Targets[key] = trackedPullRequestCacheEntry{FetchedAt: time.Now().UTC().Format(time.RFC3339), PRs: prs}
	return prs, true, nil
}

func trackedPullRequestCacheKey(owner string, author string) string {
	return strings.ToLower(owner + "\x00" + author)
}

func trackedPullRequestCacheExpired(entry trackedPullRequestCacheEntry) bool {
	fetchedAt, err := time.Parse(time.RFC3339, entry.FetchedAt)
	return err != nil || time.Since(fetchedAt) > trackedPullRequestCacheTTL
}

func trackedPullRequestCachePath() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "radar", "github-tracked-prs.json"), nil
}

func readTrackedPullRequestCache() (trackedPullRequestCacheFile, error) {
	path, err := trackedPullRequestCachePath()
	if err != nil {
		return trackedPullRequestCacheFile{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return trackedPullRequestCacheFile{}, err
	}
	var cache trackedPullRequestCacheFile
	if err := json.Unmarshal(data, &cache); err != nil {
		return trackedPullRequestCacheFile{}, err
	}
	return cache, nil
}

func writeTrackedPullRequestCache(cache trackedPullRequestCacheFile) error {
	path, err := trackedPullRequestCachePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

func searchPullRequestsByOwnerAndAuthor(ctx context.Context, owner string, author string) ([]searchPullRequest, error) {
	var prs []searchPullRequest
	args := []string{
		"search", "prs",
		"--owner", owner,
		"--author", author,
		"--state", "open",
		"--limit", "100",
		"--json", "number,title,url,repository,isDraft,state,body,author,headRefName",
	}
	if err := ghJSON(ctx, args, &prs); err != nil {
		return nil, err
	}
	return prs, nil
}

func trackedPullRequestTask(pr searchPullRequest) protocol.Task {
	repo := repoName(pr)
	reason := "tracked PR"
	if pr.Draft {
		reason = "tracked draft PR"
	}
	item := protocol.Task{
		Kind:      "github_tracked_pr",
		Title:     pr.Title,
		Repo:      repo,
		URL:       pr.URL,
		Attention: "in_progress",
		Reason:    reason,
		Metadata:  pullRequestMetadata(pr),
	}
	item.SourceRefs = []protocol.SourceRef{newGitHubPullRequestSourceRef(item, repo, pr.Number, "pull_request", pr.HeadRefName, pr.Body)}
	return item
}
