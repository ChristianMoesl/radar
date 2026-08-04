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
)

func DefaultIntegrations() integration.Registry {
	return integration.NewRegistry(
		obsidian.NewSource(),
		github.NewSource(),
		jira.NewSource(),
		datadog.NewSource(),
		git.NewSource(),
		tmux.NewSource(),
		sbx.NewSource(),
	)
}
