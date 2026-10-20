package app

import (
	"radar/internal/integration"
	"radar/internal/integration/agent"
	"radar/internal/integration/datadog"
	"radar/internal/integration/git"
	"radar/internal/integration/github"
	"radar/internal/integration/jira"
	"radar/internal/integration/obsidian"
	"radar/internal/integration/sbx"
	"radar/internal/integration/terraform"
	"radar/internal/integration/tmux"
	workspace "radar/internal/integration/workspace"
)

func DefaultIntegrations() integration.Registry {
	githubSource := github.NewSource()
	noteAuthor := obsidian.NewSource()
	workspaces := workspace.NewSource(noteAuthor)
	return integration.NewRegistry(
		noteAuthor,
		githubSource,
		terraform.NewSource(githubSource),
		jira.NewSource(),
		datadog.NewSource(),
		workspaces,
		git.NewSource(),
		tmux.NewSource(),
		sbx.NewSource(),
		agent.NewSource(workspaces),
	)
}
