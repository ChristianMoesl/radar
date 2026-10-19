package github

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"radar/internal/integration/github/identity"
	"radar/internal/linking"
	"radar/internal/protocol"
)

const pullRequestCommitsQuery = `query($owner: String!, $name: String!, $number: Int!, $cursor: String) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      number url headRefName headRefOid
      commits(first: 100, after: $cursor) {
        totalCount nodes { commit { oid message } }
        pageInfo { hasNextPage endCursor }
      }
    }
  }
}`

type commitPageResponse struct {
	Data struct {
		Repository *struct {
			PullRequest *struct {
				Number                       int
				URL, HeadRefName, HeadRefOID string
				Commits                      *struct {
					TotalCount *int
					Nodes      []*struct {
						Commit *struct {
							OID     string
							Message *string
						}
					}
					PageInfo *struct {
						HasNextPage *bool
						EndCursor   string
					}
				}
			}
		}
	}
	Errors []struct{ Message string }
}

// Only the PR's own commits are inspected. No global text search, Jira graph
// lookup, or count-based matching participates in association discovery.
func fetchCommitMarks(ctx context.Context, ref protocol.SourceRef, marks linking.MarkMatcher, logger *slog.Logger) ([]string, error) {
	repo, number, ok := parsePullRequestRef(ref)
	parts := strings.Split(repo, "/")
	if !ok || len(parts) != 2 || ref.Metadata["head_oid"] == "" {
		return nil, fmt.Errorf("%s has incomplete commit identity", ref.ID)
	}
	variables := []string{"-f", "owner=" + parts[0], "-f", "name=" + parts[1], "-F", "number=" + strconv.Itoa(number)}
	var values []string
	cursor := ""
	seenCursors, seenCommits := map[string]bool{}, map[string]bool{}
	total := -1
	for {
		var response commitPageResponse
		if err := trackedGraphQL(ctx, pullRequestCommitsQuery, cursor, variables, &response, logger); err != nil {
			return nil, err
		}
		if len(response.Errors) != 0 || response.Data.Repository == nil || response.Data.Repository.PullRequest == nil {
			return nil, fmt.Errorf("%s commit lookup unavailable", ref.ID)
		}
		pr := response.Data.Repository.PullRequest
		urlRepo, urlNumber, validURL := identity.ParsePullRequestURL(pr.URL)
		if !validURL || !strings.EqualFold(urlRepo, repo) || urlNumber != number || pr.Number != number ||
			pr.HeadRefName != ref.Branch || pr.HeadRefOID != ref.Metadata["head_oid"] {
			return nil, fmt.Errorf("%s changed during commit lookup; retry collection", ref.ID)
		}
		page := pr.Commits
		if page == nil || page.TotalCount == nil || *page.TotalCount < 0 || page.Nodes == nil || page.PageInfo == nil || page.PageInfo.HasNextPage == nil {
			return nil, fmt.Errorf("%s has incomplete commit pagination", ref.ID)
		}
		if total != -1 && total != *page.TotalCount {
			return nil, fmt.Errorf("%s commit count changed", ref.ID)
		}
		total = *page.TotalCount
		for _, node := range page.Nodes {
			if node == nil || node.Commit == nil || node.Commit.OID == "" || node.Commit.Message == nil || seenCommits[node.Commit.OID] {
				return nil, fmt.Errorf("%s has missing or duplicate commits", ref.ID)
			}
			seenCommits[node.Commit.OID] = true
			values = append(values, marks.Values(*node.Commit.Message)...)
		}
		if len(seenCommits) > total {
			return nil, fmt.Errorf("%s commit count exceeded", ref.ID)
		}
		if !*page.PageInfo.HasNextPage {
			if len(seenCommits) != total {
				return nil, fmt.Errorf("%s commit list truncated", ref.ID)
			}
			return linking.Keys(values...), nil
		}
		next := page.PageInfo.EndCursor
		if len(page.Nodes) == 0 || len(seenCommits) >= total || next == "" || seenCursors[next] {
			return nil, fmt.Errorf("%s commit cursor incomplete", ref.ID)
		}
		seenCursors[next], cursor = true, next
	}
}

// A single branch mark owns a PR, including stacked PRs mentioning their base
// issue elsewhere. Ambiguous branches require an explicit authored binding.
func applyLinkingMarks(tasks []protocol.Task, marks linking.MarkMatcher) {
	for i := range tasks {
		for j := range tasks[i].SourceRefs {
			ref := &tasks[i].SourceRefs[j]
			if ref.Source != "github" || ref.Kind != "pull_request" {
				continue
			}
			var keys []string
			branchMarks := marks.Keys(ref.Branch)
			for _, key := range ref.LinkingKeys {
				if linking.IsMarkKey(key) || (len(branchMarks) > 1 && strings.HasPrefix(key, "branch:")) {
					continue
				}
				keys = append(keys, key)
			}
			switch len(branchMarks) {
			case 0:
				keys = append(keys, marks.Keys(ref.Title, ref.Metadata["body"], ref.Metadata["commit_marks"])...)
			case 1:
				keys = append(keys, branchMarks...)
			}
			ref.LinkingKeys = linking.Keys(keys...)
		}
	}
}

// Commit enrichment has a bounded wall time and concurrency. Failed requests
// retain the last complete mark set; partial commit pages never replace it.
func collectLinkingMarks(ctx context.Context, tasks, previous []protocol.Task, marks linking.MarkMatcher, logger *slog.Logger) error {
	applyLinkingMarks(tasks, marks)
	if !marks.Enabled() {
		return nil
	}
	prior := map[string]protocol.SourceRef{}
	for _, task := range previous {
		for _, ref := range task.SourceRefs {
			if ref.Source == "github" {
				prior[ref.ID] = ref
			}
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	var mu sync.Mutex
	failures := 0
	failed := map[*protocol.SourceRef]bool{}
	jobs := make(chan *protocol.SourceRef)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ref := range jobs {
				values, err := fetchCommitMarks(ctx, *ref, marks, logger)
				if err == nil {
					ref.Metadata = cloneMetadata(ref.Metadata)
					if ref.Metadata == nil {
						ref.Metadata = map[string]string{}
					}
					ref.Metadata["commit_marks"] = strings.Join(values, "\n")
					continue
				}
				if logger != nil {
					logger.Warn("github commit linking failed", "id", ref.ID, "error", err)
				}
				mu.Lock()
				failures++
				failed[ref] = true
				mu.Unlock()
				// Keep no partial references from this attempt, including new title
				// or body keys. Identity and other current PR facts remain usable.
				var keys []string
				for _, key := range ref.LinkingKeys {
					if !linking.IsMarkKey(key) {
						keys = append(keys, key)
					}
				}
				for _, key := range prior[ref.ID].LinkingKeys {
					if value, ok := linking.MarkValue(key); ok && marks.FirstValue(value) == value {
						keys = append(keys, key)
					}
				}
				ref.LinkingKeys = linking.Keys(keys...)
			}
		}()
	}
	for i := range tasks {
		for j := range tasks[i].SourceRefs {
			ref := &tasks[i].SourceRefs[j]
			if ref.Source == "github" && ref.Kind == "pull_request" && len(marks.Values(ref.Branch)) == 0 {
				jobs <- ref
			}
		}
	}
	close(jobs)
	wg.Wait()
	for i := range tasks {
		for j := range tasks[i].SourceRefs {
			ref := &tasks[i].SourceRefs[j]
			if failed[ref] {
				continue
			}
			one := []protocol.Task{{SourceRefs: []protocol.SourceRef{*ref}}}
			applyLinkingMarks(one, marks)
			*ref = one[0].SourceRefs[0]
		}
	}
	if failures > 0 {
		return fmt.Errorf("%d PR commit lookups failed", failures)
	}
	return nil
}
