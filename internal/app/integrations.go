package app

import (
	"radar/internal/integration"
	"radar/internal/integration/git"
	"radar/internal/integration/github"
	"radar/internal/integration/jira"
	"radar/internal/integration/sbx"
	"radar/internal/integration/tmux"
)

func DefaultIntegrationSet() integration.Set {
	gitSource := git.NewSource()
	tmuxSource := tmux.NewSource()
	sbxSource := sbx.NewSource()

	return integration.Set{
		Sources: []integration.Source{
			github.NewSource(),
			jira.NewSource(),
			gitSource,
			tmuxSource,
			sbxSource,
		},
		Workspace:       gitSource,
		Multiplexer:     tmuxSource,
		ActionProviders: []integration.ActionProvider{sbxSource},
		CleanupProviders: []integration.CleanupProvider{
			tmuxSource,
			sbxSource,
			gitSource,
		},
	}
}
