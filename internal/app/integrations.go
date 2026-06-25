package app

import (
	"radar/internal/git"
	"radar/internal/github"
	"radar/internal/integration"
	"radar/internal/jira"
	"radar/internal/sbx"
	"radar/internal/tmux"
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
		DeleteProviders: []integration.DeleteProvider{
			gitSource,
			tmuxSource,
			sbxSource,
		},
	}
}
