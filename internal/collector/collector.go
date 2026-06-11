package collector

import (
	"context"
	"log/slog"

	"radar.nvim/internal/filters"
	gitcollector "radar.nvim/internal/git"
	"radar.nvim/internal/github"
	"radar.nvim/internal/jira"
	"radar.nvim/internal/linker"
	"radar.nvim/internal/protocol"
)

type Ingested struct {
	Items                  []protocol.Item
	Entities               []protocol.Entity
	Services               []protocol.ServiceStatus
	GitHubAuthoredComplete bool
}

type Result struct {
	Items    []protocol.Item
	Services []protocol.ServiceStatus
}

func Collect(ctx context.Context, previous []protocol.Item, logger *slog.Logger) Result {
	ingested := Ingest(ctx, previous, logger)
	items := linker.Link(linker.Input{
		Items:    ingested.Items,
		Entities: ingested.Entities,
	})
	items = append(items, github.ResolveDonePullRequests(ctx, previous, items, ingested.GitHubAuthoredComplete, logger)...)
	return Result{Items: items, Services: ingested.Services}
}

func Ingest(ctx context.Context, previous []protocol.Item, logger *slog.Logger) Ingested {
	result := Ingested{
		Items:    make([]protocol.Item, 0),
		Entities: make([]protocol.Entity, 0),
		Services: make([]protocol.ServiceStatus, 0, 3),
	}

	githubStatus, githubAllowed := github.GraphQLServiceStatus(ctx, logger)
	if githubAllowed {
		filterCfg, filterErr := filters.Load()
		if filterErr != nil {
			logger.Warn("could not load filters for github rule collection", "error", filterErr)
		}
		reviewItems, authoredItems, activityItems, err := github.FetchPullRequests(ctx, previous, logger)
		if err != nil {
			logger.Warn("github pull request collection failed", "error", err)
			githubStatus.Status = "error"
			githubStatus.Detail = err.Error()
		} else {
			result.Items = append(result.Items, reviewItems...)
			result.Items = append(result.Items, authoredItems...)
			result.Items = append(result.Items, activityItems...)
			if filterErr == nil {
				trackedItems, err := github.FetchRulePullRequests(ctx, filterCfg, logger)
				if err != nil {
					logger.Warn("github rule pull request collection failed", "error", err)
				} else {
					result.Items = appendMissingPullRequests(result.Items, trackedItems)
				}
			}
			result.GitHubAuthoredComplete = true
			githubStatus.Detail = github.PullRequestCollectionSummary(len(reviewItems), len(authoredItems))
		}
	}
	result.Services = append(result.Services, githubStatus)

	jiraEntities, jiraStatus, err := jira.FetchAssignedIssues(ctx, logger)
	if err != nil {
		logger.Warn("jira issue collection failed", "error", err)
	}
	result.Services = append(result.Services, jiraStatus)
	result.Entities = append(result.Entities, jiraEntities...)

	gitEntities, gitStatus := gitcollector.FetchWorktrees(ctx, logger)
	result.Services = append(result.Services, gitStatus)
	result.Entities = append(result.Entities, gitEntities...)
	return result
}

func appendMissingPullRequests(items []protocol.Item, candidates []protocol.Item) []protocol.Item {
	seen := map[string]bool{}
	for _, item := range items {
		if item.URL != "" {
			seen[item.URL] = true
		}
	}
	for _, item := range candidates {
		if item.URL != "" && seen[item.URL] {
			continue
		}
		items = append(items, item)
		if item.URL != "" {
			seen[item.URL] = true
		}
	}
	return items
}
