package app

import (
	"radar/internal/integration"
	"radar/internal/integration/datadog"
	"radar/internal/integration/git"
	"radar/internal/integration/github"
	"radar/internal/integration/jira"
	"radar/internal/integration/obsidian"
	"radar/internal/integration/sbx"
	"radar/internal/integration/tmux"
	workspace "radar/internal/integration/workspace"
)

func DefaultIntegrations() integration.Registry {
	githubSource := github.NewSource()
	noteAuthor := obsidian.NewSource()
	return integration.NewRegistry(
		noteAuthor,
		githubSource,
		jira.NewSource(githubSource),
		datadog.NewSource(),
		workspace.NewSource(noteAuthor),
		git.NewSource(),
		tmux.NewSource(),
		sbx.NewSource(),
	)
}
