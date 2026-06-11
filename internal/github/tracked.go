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

func FetchRulePullRequests(ctx context.Context, cfg filters.Config, logger *slog.Logger) ([]protocol.Item, error) {
	targets := trackingTargets(ctx, cfg, logger)
	if len(targets) == 0 {
		return nil, nil
	}

	items := make([]protocol.Item, 0)
	seen := map[string]bool{}
	for _, target := range targets {
		if !EnsureSearchBudget(ctx, logger) {
			break
		}
		prs, err := searchPullRequestsByRepoAndAuthor(ctx, target.repo, target.user)
		if err != nil {
			return items, err
		}
		for _, pr := range prs {
			if !target.matches(pr) {
				continue
			}
			key := fmt.Sprintf("%s:%d", repoName(pr), pr.Number)
			if seen[key] {
				continue
			}
			seen[key] = true
			items = append(items, trackedPullRequestItem(pr))
		}
	}
	logger.Debug("fetched github rule pull requests", "count", len(items), "targets", len(targets))
	return items, nil
}

type trackingTarget struct {
	repo        string
	repoPattern string
	user        string
}

func (target trackingTarget) matches(pr searchPullRequest) bool {
	return filters.MatchPattern(target.repo, repoName(pr)) && pr.Author != nil && filters.MatchPattern(target.user, pr.Author.Login)
}

func trackingTargets(ctx context.Context, cfg filters.Config, logger *slog.Logger) []trackingTarget {
	targets := make([]trackingTarget, 0)
	seen := map[string]bool{}
	for _, rule := range cfg.Rules {
		action := strings.ToLower(strings.TrimSpace(rule.Action))
		if action == "" || action == "mute" || len(rule.Repos) == 0 || len(rule.Users) == 0 {
			continue
		}
		for _, user := range exactUsers(rule.Users) {
			for _, repoPattern := range rule.Repos {
				for _, repo := range expandRepoPatternBestEffort(ctx, repoPattern, logger) {
					key := strings.ToLower(repo + "\x00" + user)
					if seen[key] {
						continue
					}
					seen[key] = true
					targets = append(targets, trackingTarget{repo: repo, repoPattern: repoPattern, user: user})
				}
			}
		}
	}
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].repo == targets[j].repo {
			return targets[i].user < targets[j].user
		}
		return targets[i].repo < targets[j].repo
	})
	return targets
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

func searchPullRequestsByRepoAndAuthor(ctx context.Context, repo string, author string) ([]searchPullRequest, error) {
	var prs []searchPullRequest
	args := []string{
		"search", "prs",
		"--repo", repo,
		"--author", author,
		"--state", "open",
		"--limit", "100",
		"--json", "number,title,url,repository,isDraft,state,body,author",
	}
	if err := ghJSON(ctx, args, &prs); err != nil {
		return nil, err
	}
	return prs, nil
}

func trackedPullRequestItem(pr searchPullRequest) protocol.Item {
	repo := repoName(pr)
	reason := "tracked PR"
	if pr.Draft {
		reason = "tracked draft PR"
	}
	item := protocol.Item{
		ID:        fmt.Sprintf("github:tracked_pr:%s:%d", repo, pr.Number),
		Kind:      "github_tracked_pr",
		Title:     pr.Title,
		Repo:      repo,
		URL:       pr.URL,
		Attention: "in_progress",
		Reason:    reason,
		Metadata:  pullRequestMetadata(pr),
	}
	item.Entities = []protocol.Entity{githubEntity(item, "pull_request", pr.HeadRefName, pr.Body)}
	return item
}
